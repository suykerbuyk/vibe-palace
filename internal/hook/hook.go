// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/enrichment"
	"github.com/suykerbuyk/vibe-palace/internal/memory"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Payload carries the fields extracted from the Claude Code hook
// invocation (stdin JSON).
type Payload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
}

// RunOptions carries configuration that is resolved once per vp
// process (vault root, project slug, binary version).
type RunOptions struct {
	VaultRoot   string
	ProjectSlug string
	VPVersion   string
	ClaimDir    string // override for testing; default: <CWD>/.vibe-palace
}

// Result reports what the hook run produced.
type Result struct {
	Event            string         `json:"event"`
	SkippedNoProject bool           `json:"skipped_no_project"`
	ArchiveSkipped   bool           `json:"archive_skipped"`
	ArchivePath      string         `json:"archive_path,omitempty"`
	SessionNoteID    string         `json:"session_note_id,omitempty"`
	ClaimedSkip      bool           `json:"claimed_skip"`
	MemoryHarvest    *memory.Result `json:"memory_harvest,omitempty"`
	Error            string         `json:"error,omitempty"`

	// Failures lists the capture stages this run lost. On the hook path a loss
	// is reported here and in vp.log — never as a non-zero exit. The hook has no
	// agent and no human in the loop, and its only hard-failure primitive is exit
	// code 2, which Claude Code reads as a BLOCKING error: the hook is installed
	// on Stop, which fires once per assistant turn, so a deterministically-failing
	// capture would block the first turn of every session and feed its own error
	// back into the model. A loop. Loudness here is the durable log, not the exit.
	Failures []capture.CaptureFailure `json:"failures,omitempty"`
}

// enrichDrainBudget bounds how many queued enrichment jobs a single SessionEnd
// hook run will drain, so a large backlog (e.g. after an LLM outage) cannot
// stall Claude Code session teardown. Remaining jobs drain on later sessions.
const enrichDrainBudget = 5

// ValidEvents are the Claude Code hook events we handle.
var ValidEvents = map[string]bool{
	"SessionEnd": true,
	"Stop":       true,
	"PreCompact": true,
}

// Run executes the hook pipeline. Ordering (claim-decoupling): validate →
// opt-in gate (.vibe-palace.toml signal) → resolve claimDir → archive (ALWAYS,
// non-fatal) → memory harvest (SessionEnd only, non-fatal) → claim-gated session
// capture (auto-summary, transcript read, WriteSession, WriteClaim). Archive and
// harvest run regardless of claim state — both are idempotent housekeeping; only
// the rich session capture is gated by the claim sentinel. The opt-in gate runs
// before everything so a non-project CWD scaffolds nothing and claims nothing.
func Run(ctx context.Context, payload Payload, opts RunOptions) (*Result, error) {
	// 1. Validate required fields.
	if payload.SessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if payload.CWD == "" {
		return nil, fmt.Errorf("cwd is required")
	}
	if !ValidEvents[payload.HookEventName] {
		return nil, fmt.Errorf("unsupported hook event: %q", payload.HookEventName)
	}

	res := &Result{Event: payload.HookEventName}

	// 2. Opt-in gate. Auto-capture only into directories carrying an explicit
	// .vibe-palace.toml signal (cwd or a parent). Without it the CWD is not a
	// vp-managed project — e.g. the vault root (itself a git repo) or an
	// un-init'd code repo — and capturing would scaffold a stray
	// Projects/<basename>/ tree and write a claim sentinel into a non-project dir.
	if project.DetectSignal(payload.CWD) != project.SignalVibeConfig {
		res.SkippedNoProject = true
		slog.Info("hook: skipping capture — no .vibe-palace.toml project signal", "cwd", payload.CWD)
		return res, nil
	}

	// 3. Resolve claim directory.
	claimDir := opts.ClaimDir
	if claimDir == "" {
		claimDir = filepath.Join(payload.CWD, ".vibe-palace")
	}

	// 4. Archive transcript at the "preserve now" events — SessionEnd (the
	// complete transcript) and PreCompact (before compaction discards history) —
	// but NOT on Stop. Stop fires once per assistant turn, and archive's dedup
	// keys on the transcript content hash, which grows every turn; archiving on
	// every Stop would re-hash + re-compress the whole (growing) transcript and
	// leak a manifest .bak per turn. Archive runs BEFORE the claim gate (non-fatal)
	// so a session already claimed by an MCP vp_capture_session still gets its
	// transcript ledger — archive's own dedup keeps that from duplicating.
	if payload.HookEventName == "SessionEnd" || payload.HookEventName == "PreCompact" {
		archiveResult, archiveErr := archive.Create(archive.CreateOptions{
			Adapter:     archive.ClaudeCodeAdapterName,
			SessionID:   payload.SessionID,
			SourcePath:  payload.TranscriptPath,
			SourceCWD:   payload.CWD,
			VaultRoot:   opts.VaultRoot,
			ProjectSlug: opts.ProjectSlug,
			VPVersion:   opts.VPVersion,
		})
		if archiveErr != nil {
			slog.Warn("hook: archive failed (non-fatal)", "err", archiveErr)
			res.Error = archiveErr.Error()
		} else {
			res.ArchivePath = archiveResult.ArchivePath
			res.ArchiveSkipped = archiveResult.Skipped
		}
	}

	// 5. Memory harvest — SessionEnd only, and only when a transcript path is
	// known (the native memory dir is resolved from it). Runs before the claim
	// gate so it happens once at SessionEnd regardless of claim state; it is
	// idempotent housekeeping (a drained native dir is simply missing/empty next
	// time). Stop and PreCompact never harvest. Failures are non-fatal.
	if payload.HookEventName == "SessionEnd" {
		if payload.TranscriptPath == "" {
			slog.Debug("hook: skipping memory harvest (no transcript path to locate native dir)")
		} else {
			nativeDir := memory.NativeDirFromTranscript(payload.TranscriptPath)
			hr, herr := memory.Harvest(memory.Options{
				VaultRoot: opts.VaultRoot,
				Project:   opts.ProjectSlug,
				NativeDir: nativeDir,
				DryRun:    false,
				Push:      true,
			})
			if herr != nil {
				slog.Warn("hook: memory harvest failed (non-fatal)", "err", herr)
			} else {
				res.MemoryHarvest = hr
			}
		}
	}

	// 6. Open the vault and resolve the LLM enricher from project config (both
	// non-fatal). Built before the claim gate so the SessionEnd drain below runs
	// even for sessions already captured (claimed) via vp_capture_session, and
	// reused for this run's capture.
	vault := storage.NewVault(opts.VaultRoot)
	var enricher *enrichment.Enricher
	if cfg, cfgErr := vault.LoadConfig(opts.ProjectSlug); cfgErr != nil {
		slog.Debug("hook: load config for enrichment failed (non-fatal)", "err", cfgErr)
	} else {
		e, eerr := capture.NewEnricherFromConfig(cfg.Enrichment, opts.VaultRoot)
		if eerr != nil {
			slog.Warn("hook: enrichment disabled", "err", eerr)
		}
		enricher = e
	}

	// 7. Drain previously-queued enrichment jobs at SessionEnd, BEFORE this run's
	// capture and BEFORE the claim gate. Draining before capture means a job
	// enqueued by THIS run's own inline miss waits for the next session rather
	// than being retried immediately against the just-failed endpoint; running
	// before the claim gate means MCP-captured (claimed) sessions still drain the
	// backlog. Bounded so a large backlog cannot stall session teardown. A nil
	// enricher (enrichment disabled) makes this a no-op.
	if payload.HookEventName == "SessionEnd" {
		if n, derr := capture.DrainEnrichmentQueue(ctx, vault, payload.CWD, enricher, enrichDrainBudget); derr != nil {
			slog.Warn("hook: enrichment drain failed (non-fatal)", "err", derr)
		} else if n > 0 {
			slog.Info("hook: drained enrichment queue", "count", n)
		}
	}

	// 8. Check claim sentinel (idempotency). The claim gates ONLY the rich
	// session capture below — archive, harvest, and drain above already ran.
	if IsClaimed(claimDir, payload.SessionID) {
		res.ClaimedSkip = true
		return res, nil
	}

	// 7. Build auto summary from git log.
	summary := AutoSummary(payload.CWD)

	// 8. Read transcript for friction analysis (best-effort), and extract the
	// model from the JSONL transcript (also best-effort — never fail capture).
	transcript := ""
	model := ""
	if payload.TranscriptPath != "" {
		if data, err := os.ReadFile(payload.TranscriptPath); err == nil {
			transcript = string(data)
		} else {
			slog.Warn("hook: could not read transcript for friction analysis", "err", err)
		}
		if _, m, err := archive.InspectClaudeJSONL(payload.TranscriptPath); err == nil {
			model = m
		} else {
			slog.Warn("hook: could not extract model from transcript", "err", err)
		}
	}

	// 9. Capture the session, reusing the vault + enricher built above.
	sessionResult, err := capture.WriteSession(ctx, vault, nil, capture.SessionParams{
		Project:          opts.ProjectSlug,
		Summary:          summary,
		Tag:              "auto-capture",
		Model:            model,
		Transcript:       transcript,
		ArchiveSessionID: payload.SessionID,
		NeedsIndexing:    true,
		CWD:              payload.CWD,
		Enricher:         enricher,
	})
	if err != nil {
		// The note did not land — capture's one fatal error. This is the total
		// loss, and it is precisely the failure that used to leave NO trace
		// anywhere: it was surfaced by returning an error that cmd_hook printed
		// with a bare fmt.Fprintf to stderr and turned into exit 2. It never
		// touched slog, so it never reached vp.log, while the failures that did
		// not matter (archive, harvest, drain) were all durably logged. Report it
		// where someone can actually see it, and do NOT fail the hook.
		slog.Error("hook: session capture failed; no note was written",
			"err", err, "project", opts.ProjectSlug, "event", payload.HookEventName)
		res.Error = fmt.Sprintf("capture session: %v", err)
		return res, nil
	}

	res.SessionNoteID = sessionResult.SessionID
	res.Failures = sessionResult.Failures

	// Write the claim sentinel on the SUCCESS path, even when a peripheral stage
	// was lost. The claim asserts "this session has a note", which is still true
	// when the archive back-link or the friction score went missing. Gating it on
	// a clean run would mean every subsequent hook event re-captures the same
	// session — a duplicate note per turn, forever, over a missing link.
	if err := WriteClaim(claimDir, payload.SessionID, sessionResult.SessionID); err != nil {
		slog.Warn("hook: could not write claim sentinel", "err", err)
		res.Failures = append(res.Failures, capture.CaptureFailure{
			Stage: capture.StageClaimSentinel,
			Err:   err.Error(),
		})
	}

	return res, nil
}
