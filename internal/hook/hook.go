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
}

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

	// 6. Check claim sentinel (idempotency). The claim gates ONLY the rich
	// session capture below — archive and harvest above already ran.
	if IsClaimed(claimDir, payload.SessionID) {
		res.ClaimedSkip = true
		return res, nil
	}

	// 7. Build auto summary from git log.
	summary := AutoSummary(payload.CWD)

	// 8. Read transcript for friction analysis (best-effort).
	transcript := ""
	if payload.TranscriptPath != "" {
		if data, err := os.ReadFile(payload.TranscriptPath); err == nil {
			transcript = string(data)
		} else {
			slog.Warn("hook: could not read transcript for friction analysis", "err", err)
		}
	}

	// 9. Open vault and capture session.
	vault := storage.NewVault(opts.VaultRoot)
	sessionResult, err := capture.WriteSession(ctx, vault, nil, capture.SessionParams{
		Project:          opts.ProjectSlug,
		Summary:          summary,
		Tag:              "auto-capture",
		Transcript:       transcript,
		ArchiveSessionID: payload.SessionID,
		NeedsIndexing:    true,
		CWD:              payload.CWD,
	})
	if err != nil {
		return nil, fmt.Errorf("capture session: %w", err)
	}

	res.SessionNoteID = sessionResult.SessionID

	// Write claim sentinel so re-runs of the hook skip this session.
	if err := WriteClaim(claimDir, payload.SessionID, sessionResult.SessionID); err != nil {
		slog.Warn("hook: could not write claim sentinel", "err", err)
	}

	return res, nil
}
