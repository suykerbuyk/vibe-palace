// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"fmt"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

const codexReceiptSummary = "Codex transcript archived from the SessionEnd event; Codex provides no semantic summary in this event."

// WriteCodexArchiveReceipt writes only a deterministic auto-capture receipt and
// closes the archive links. It intentionally does not enter WriteSession's
// enrichment, friction, or summary-queue pipeline.
func WriteCodexArchiveReceipt(vault *storage.Vault, project, sessionID, manifestPath string) (storage.SessionRef, error) {
	if vault == nil || project == "" || sessionID == "" || manifestPath == "" {
		return storage.SessionRef{}, fmt.Errorf("Codex archive receipt requires vault, project, session id and manifest path")
	}
	archiveRel := archive.VaultRelPath(vault.Root, manifestPath)
	now := time.Now()
	meta := storage.SessionMeta{
		Date: vault.CalendarDay(now), SessionKey: "codex-session-end:" + sessionID,
		SessionKeySource: storage.KeySourceCaller,
		ArchiveSessionID: sessionID, ArchiveSessionIDSource: storage.ArchiveIDSourceCodexSessionEnd,
		Host: storage.HostCodex, HostSource: storage.HostSourceCodexHookEvent,
		Title: "Codex transcript archive receipt", Summary: codexReceiptSummary,
		Tag: storage.TagAutoCapture, Archive: archiveRel,
	}
	body := "Codex supplied a transcript file at SessionEnd. The archive preserves its exact source bytes; no semantic summary was supplied by Codex."
	ref, _, err := vault.TryUpsertSessionByKey(project, meta.SessionKey, meta, body, mergeCaptureMeta)
	if err != nil {
		return storage.SessionRef{}, err
	}
	links, err := vault.TryLinkArchiveToSessions(project, sessionID, archiveRel)
	if err != nil {
		return ref, err
	}
	// The receipt is a stub. Only a canonical, non-stub note may be written into
	// the manifest's reverse link; never make the archive point back at its own
	// receipt when that is the only note available.
	if links.Found && !links.CanonicalIsStub {
		if err := archive.TryLinkSessionNote(vault.Root, manifestPath, links.Canonical.NotePath); err != nil {
			return ref, err
		}
	}
	return ref, nil
}
