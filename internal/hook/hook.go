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
	Event          string `json:"event"`
	ArchiveSkipped bool   `json:"archive_skipped"`
	ArchivePath    string `json:"archive_path,omitempty"`
	SessionNoteID  string `json:"session_note_id,omitempty"`
	ClaimedSkip    bool   `json:"claimed_skip"`
	Error          string `json:"error,omitempty"`
}

// ValidEvents are the Claude Code hook events we handle.
var ValidEvents = map[string]bool{
	"SessionEnd": true,
	"Stop":       true,
	"PreCompact": true,
}

// Run executes the hook pipeline: validate, claim-check, archive,
// auto-summary, and session capture.
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

	// 2. Resolve claim directory.
	claimDir := opts.ClaimDir
	if claimDir == "" {
		claimDir = filepath.Join(payload.CWD, ".vibe-palace")
	}

	// 3. Check claim sentinel (idempotency).
	if IsClaimed(claimDir, payload.SessionID) {
		res.ClaimedSkip = true
		return res, nil
	}

	// 4. Archive transcript (non-fatal on failure).
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

	// 5. Build auto summary from git log.
	summary := AutoSummary(payload.CWD)

	// 6. Read transcript for friction analysis (best-effort).
	transcript := ""
	if payload.TranscriptPath != "" {
		if data, err := os.ReadFile(payload.TranscriptPath); err == nil {
			transcript = string(data)
		} else {
			slog.Warn("hook: could not read transcript for friction analysis", "err", err)
		}
	}

	// 7. Open vault and capture session.
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

