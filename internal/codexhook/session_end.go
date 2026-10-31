// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package codexhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

const sessionEndArchiveBudget = 2600 * time.Millisecond

type SessionEndEvent struct {
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
	Reason         string `json:"reason"`
	HookEventName  string `json:"hook_event_name"`
}

// HandleSessionEnd is the JSON protocol boundary. Unlike SessionStart, a
// missing or unusable transcript is a hard failure: no archive is claimed.
func HandleSessionEnd(in io.Reader, out io.Writer) error {
	var event SessionEndEvent
	if err := json.NewDecoder(in).Decode(&event); err != nil {
		return fmt.Errorf("decode Codex SessionEnd event: %w", err)
	}
	if event.HookEventName != "" && event.HookEventName != "SessionEnd" {
		return fmt.Errorf("unexpected Codex hook event %q", event.HookEventName)
	}
	if event.CWD == "" {
		return fmt.Errorf("Codex SessionEnd event has no cwd")
	}
	if project.DetectSignal(event.CWD) != project.SignalVibeConfig {
		return fmt.Errorf("Codex SessionEnd cwd has no Vibe-Palace project marker")
	}
	marker, err := project.LocateMarker(event.CWD)
	if err != nil {
		return fmt.Errorf("read Vibe-Palace project marker: %w", err)
	}
	if marker.Path == "" || marker.Name == "" || marker.NameErr != nil {
		return fmt.Errorf("Codex SessionEnd requires a valid [project].name marker")
	}
	vaultRoot, _, err := storage.ResolveVaultPath(event.CWD)
	if err != nil {
		return fmt.Errorf("resolve Vibe-Palace vault: %w", err)
	}
	return ProcessSessionEnd(context.Background(), event, storage.NewVault(vaultRoot), marker.Name, out)
}

// ProcessSessionEnd archives Codex's supplied file path. Its split deadline
// keeps archive work bounded and leaves the remainder for the typed receipt.
func ProcessSessionEnd(parent context.Context, event SessionEndEvent, vault *storage.Vault, projectSlug string, out io.Writer) error {
	if parent == nil {
		parent = context.Background()
	}
	if event.SessionID == "" || event.TranscriptPath == "" || vault == nil || projectSlug == "" {
		return fmt.Errorf("SessionEnd requires session_id, transcript_path, project marker, and vault")
	}
	info, err := os.Stat(event.TranscriptPath)
	if err != nil {
		return fmt.Errorf("Codex transcript unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("Codex transcript unavailable: expected a usable non-empty regular file")
	}
	ctx, cancel := context.WithTimeout(parent, sessionEndArchiveBudget)
	defer cancel()
	created, err := archive.Create(archive.CreateOptions{
		Context: ctx, Adapter: archive.CodexRawAdapterName, SessionID: event.SessionID,
		SourcePath: event.TranscriptPath, VaultRoot: vault.Root, ProjectSlug: projectSlug,
		LockPosture: archive.LockNonBlocking, MaxArchiveBytes: archive.DefaultMaxArchiveBytes,
	})
	if err != nil {
		return fmt.Errorf("archive Codex transcript: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("archive created; note/link incomplete: receipt budget exhausted: %w", err)
	}
	if _, err := capture.WriteCodexArchiveReceipt(vault, projectSlug, event.SessionID, created.ManifestPath); err != nil {
		return fmt.Errorf("archive created; note/link incomplete: %w", err)
	}
	if _, err := fmt.Fprintf(out, "archived: %s\n", created.ManifestPath); err != nil {
		return fmt.Errorf("write SessionEnd result: %w", err)
	}
	return nil
}
