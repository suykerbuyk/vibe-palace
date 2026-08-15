// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"context"
	"encoding/json"
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

// Payload carries the fields extracted from a hook invocation's stdin JSON.
// Two clients emit it in two different wire formats, and UnmarshalJSON below
// reconciles them: Claude Code sends snake_case keys (session_id) with
// PascalCase event values ("SessionEnd"); Grok sends camelCase keys
// (sessionId) with snake_case event values ("session_end"). The struct itself
// is the canonical, post-normalization shape — snake_case keys, PascalCase
// event — so every consumer below (the ValidEvents gate, the
// HookEventName == "SessionEnd" branches) sees one format regardless of client.
type Payload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`

	// Dialect names which of those two wire formats this payload actually
	// arrived in — the discriminator UnmarshalJSON used to compute and throw
	// away. It is set ONLY by UnmarshalJSON, so a Payload built as a struct
	// literal carries DialectUnknown, which is correct: nothing was observed.
	Dialect string `json:"-"`
}

// Wire dialects a hook payload can arrive in. These are HOST EVIDENCE, and the
// strongest kind this path has: the dialect is a property of the bytes the host
// itself wrote to our stdin, so unlike an environment variable it cannot be
// inherited by an unrelated process launched from inside a host's session. That
// distinction is the whole reason it is recorded — see the Host resolution in
// Run below.
const (
	DialectClaude  = "claude"
	DialectGrok    = "grok"
	DialectUnknown = ""
)

// detectDialect reports which wire format supplied this payload's fields.
//
// It answers on the SPELLING of the keys, not their values: Claude Code sends
// snake_case (session_id), Grok sends camelCase (sessionId), and no client sends
// both. A payload mixing the two is not a client vp knows — it is a synthetic or
// malformed invocation — and returns DialectUnknown rather than picking a winner,
// because the point of this signal is that it identifies a host and a guess
// identifies nothing. `cwd` is spelled identically by both and contributes no
// evidence, so it is not consulted.
func detectDialect(snake, camel []string) string {
	snakeSeen, camelSeen := false, false
	for _, v := range snake {
		if v != "" {
			snakeSeen = true
		}
	}
	for _, v := range camel {
		if v != "" {
			camelSeen = true
		}
	}
	switch {
	case snakeSeen && !camelSeen:
		return DialectClaude
	case camelSeen && !snakeSeen:
		return DialectGrok
	default:
		return DialectUnknown
	}
}

// UnmarshalJSON accepts BOTH the Claude Code (snake_case key) and Grok
// (camelCase key) hook wire formats and normalizes to the canonical Payload.
//
// Go's encoding/json key matching is case-insensitive but not
// separator-insensitive, so `sessionId` never binds to a `session_id` tag — the
// underscore differs. That single gap is why a Grok hook payload used to land
// with only `cwd` populated and fail at "session_id is required": Grok sends a
// COMPLETE payload, just under different keys. We therefore read every field
// under both spellings and take the first non-empty, then canonicalize the
// event value. A genuinely empty payload still yields an empty SessionID and is
// still rejected by Run — this reconciles formats, it does not swallow blanks.
func (p *Payload) UnmarshalJSON(data []byte) error {
	var raw struct {
		SessionID           string `json:"session_id"`
		SessionIDCamel      string `json:"sessionId"`
		TranscriptPath      string `json:"transcript_path"`
		TranscriptPathCamel string `json:"transcriptPath"`
		CWD                 string `json:"cwd"`
		HookEventName       string `json:"hook_event_name"`
		HookEventNameCamel  string `json:"hookEventName"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.SessionID = firstNonEmpty(raw.SessionID, raw.SessionIDCamel)
	p.TranscriptPath = firstNonEmpty(raw.TranscriptPath, raw.TranscriptPathCamel)
	p.CWD = raw.CWD
	p.HookEventName = canonicalHookEvent(firstNonEmpty(raw.HookEventName, raw.HookEventNameCamel))
	// Keep WHICH spelling matched. This used to be computed implicitly by the
	// firstNonEmpty calls above and discarded on the next line — transport-observed
	// evidence about the client, thrown away by the one function in the tree that
	// could see it.
	p.Dialect = detectDialect(
		[]string{raw.SessionID, raw.TranscriptPath, raw.HookEventName},
		[]string{raw.SessionIDCamel, raw.TranscriptPathCamel, raw.HookEventNameCamel},
	)
	return nil
}

// firstNonEmpty returns the first non-empty string in order.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// canonicalHookEvent maps the wire event name onto the PascalCase form the rest
// of this package gates on (ValidEvents, the SessionEnd/PreCompact branches).
// Claude already sends PascalCase, so those pass through the default arm
// unchanged; Grok sends snake_case ("session_end"), so those are translated.
// Only the three events vp actually handles are mapped — an unhandled event
// keeps its wire spelling and is correctly rejected by the ValidEvents gate.
func canonicalHookEvent(s string) string {
	switch s {
	case "session_end":
		return "SessionEnd"
	case "stop":
		return "Stop"
	case "pre_compact":
		return "PreCompact"
	default:
		return s
	}
}

// resolveHookHost settles host attribution for one hook capture from the two
// signals this path has, and keeps them in separate fields because they answer
// separate questions.
//
// HOST comes from the wire dialect ONLY. The MCP path derives its host from the
// initialize handshake; the hook has no handshake, and the dialect is its nearest
// equivalent — evidence the transport itself carried. A Claude-shaped payload is
// written by Claude Code, a Grok-shaped payload by Grok, and neither client can
// produce the other's spelling.
//
// 🔴 The env var deliberately does NOT decide the host. It used to (2026-08-05 to
// 2026-08-15), and that was wrong for the reason this project had already settled
// one session earlier for HERDR_ENV: environment inheritance is TRANSITIVE, so a
// grok pane, a wrapper script, or any agent launched from inside a Claude Code
// session inherits CLAUDE_CODE_ENTRYPOINT=cli and would have recorded
// host_source: derived — a confidently wrong attribution wearing a provenance
// that says it was measured. The dialect cannot leak that way, which is exactly
// why it is the one allowed to name the host.
//
// ENTRYPOINT still carries the env var, under a name that claims only what was
// measured (see storage.SessionMeta.Entrypoint). It is written on every capture,
// EntrypointUnknown included, so an explicit "we looked and found nothing" stays
// distinguishable from a note whose writer never looked.
//
// No branch invents a value: an indeterminate dialect yields the explicit unknown
// pair, per ADR-006.
func resolveHookHost(dialect, envEntrypoint string) (host, hostSource, entrypoint string) {
	host, hostSource = storage.HostUnknown, storage.HostSourceUnknown
	switch dialect {
	case DialectClaude:
		host, hostSource = storage.HostClaudeCode, storage.HostSourceDerived
	case DialectGrok:
		host, hostSource = storage.HostGrok, storage.HostSourceDerived
	}

	entrypoint = storage.EntrypointUnknown
	if envEntrypoint != "" {
		entrypoint = envEntrypoint
	}
	return host, hostSource, entrypoint
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
	Event            string `json:"event"`
	SkippedNoProject bool   `json:"skipped_no_project"`
	ArchiveSkipped   bool   `json:"archive_skipped"`
	ArchivePath      string `json:"archive_path,omitempty"`
	SessionNoteID    string `json:"session_note_id,omitempty"`
	// LinkedNotes counts the session notes whose archive: field this run pointed at
	// the transcript. Zero is the steady state once a session is linked (the link is
	// idempotent and rewrites nothing), so this reports work DONE, not health.
	LinkedNotes   int            `json:"linked_notes,omitempty"`
	ClaimedSkip   bool           `json:"claimed_skip"`
	MemoryHarvest *memory.Result `json:"memory_harvest,omitempty"`
	Error         string         `json:"error,omitempty"`

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
	//
	// archiveManifestPath carries the manifest OUT of this block so step 7b below can
	// close the note <-> transcript loop against it. It is set even when the archive
	// was DEDUPED (Skipped): CreateResult.ManifestPath names the written OR
	// pre-existing manifest, and a re-run must still be able to link.
	var archiveManifestPath string
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
			archiveManifestPath = archiveResult.ManifestPath
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

	// 7b. CLOSE THE NOTE <-> TRANSCRIPT LOOP. This runs BEFORE the claim gate on
	// purpose: the gate is the entire reason the link was never closed.
	//
	// Capture can only link a note to an archive that already exists, and it almost
	// never does at capture time — the auto-capture note is written at the first
	// Stop (~90 s in) and the agent's wrap note lands mid-session, while the archive
	// is not created until SessionEnd. The claim written by that first Stop then
	// short-circuits capture forever, so the archive that finally appeared here was
	// linked to nothing. Measured: 105 of 417 manifests vault-wide stranded, and
	// every agent-written note divorced from its transcript (implementation 0/61).
	//
	// Linking from the ARCHIVE side, after archive.Create, is the "future hook run
	// can call LinkSessionNote" recovery path that capture/session.go's comment has
	// promised since the day it was written and that NOTHING ever built.
	//
	// Non-fatal in every branch: a failure to link must never cost the session its
	// note or its transcript — both are already durable by this point — but it is
	// never silent either.
	if archiveManifestPath != "" {
		archiveRel := archive.VaultRelPath(opts.VaultRoot, archiveManifestPath)
		link, linkErr := vault.LinkArchiveToSessions(opts.ProjectSlug, payload.SessionID, archiveRel)
		switch {
		case linkErr != nil:
			slog.Warn("hook: could not link session notes to transcript (non-fatal)",
				"err", linkErr, "archive_session_id", payload.SessionID, "archive", archiveRel)
		case !link.Found:
			// No note carries this session id. Expected and fine: a session captured
			// before ArchiveSessionID was recorded, or one whose only capture is still
			// to come at step 9 below (which links inline, as it always has). Not a
			// warning — there is nothing wrong and nothing an operator would do.
			slog.Debug("hook: no session note to link yet", "archive_session_id", payload.SessionID)
		default:
			// Back-link (transcript -> note). Points at the note a human would want to
			// land on, never the auto-capture stub when a real one exists.
			if err := archive.LinkSessionNote(opts.VaultRoot, archiveManifestPath, link.Canonical.NotePath); err != nil {
				slog.Warn("hook: archive manifest back-link failed; transcript is reachable from the note but not the reverse (non-fatal)",
					"err", err, "manifest", archiveManifestPath, "note_path", link.Canonical.NotePath)
			}
			res.LinkedNotes = len(link.Updated)
			slog.Info("hook: linked session notes to transcript",
				"updated", len(link.Updated), "canonical", link.Canonical.NotePath, "archive", archiveRel)
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
	host, hostSource, entrypoint := resolveHookHost(payload.Dialect, os.Getenv("CLAUDE_CODE_ENTRYPOINT"))
	sessionResult, err := capture.WriteSession(ctx, vault, nil, capture.SessionParams{
		Project:          opts.ProjectSlug,
		Summary:          summary,
		Tag:              storage.TagAutoCapture,
		Model:            model,
		Transcript:       transcript,
		ArchiveSessionID: payload.SessionID,
		NeedsIndexing:    true,
		CWD:              payload.CWD,
		Enricher:         enricher,
		Host:             host,
		HostSource:       hostSource,
		Entrypoint:       entrypoint,
		// The hook PUSHES its key: the harness session ID it was handed. The hook
		// captures a given session at most once, so this makes a re-run idempotent
		// on its own terms rather than depending on the claim sentinel — which is a
		// host-local file, and therefore gone the moment .vibe-palace/ is cleaned.
		// Belt and braces: the claim still short-circuits before we get here.
		SessionKey: payload.SessionID,
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
