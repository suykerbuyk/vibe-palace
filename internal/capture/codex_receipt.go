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
	// Back-link the manifest at whatever TryLinkArchiveToSessions chose as
	// canonical — INCLUDING this receipt, when it is the session's only note.
	//
	// 🔴 THIS REVERSES THE RULE THAT STOOD HERE, deliberately and on an operator
	// ruling (task codex-back-link-withheld-by-a-guard-only-one-call-site-applies).
	// The old rule read `links.Found && !links.CanonicalIsStub` and withheld the
	// link whenever the canonical note was an auto-capture stub. The receipt
	// written twelve lines above IS tagged auto-capture, and at Codex SessionEnd
	// it is the only note that exists — so the condition was ALWAYS true, the
	// back-link was NEVER written, and every Codex session that never gained a
	// richer note stranded its own transcript permanently. It was silent too:
	// the guard sat BEFORE the call, so nothing failed and nothing logged.
	//
	// An archive naming its own receipt is not the loss the old comment feared.
	// The receipt is the only record of that session, so naming it beats naming
	// nothing, and the stub is never PREFERRED: storage.preferCanonical still
	// promotes a real note over a stub the moment one exists, which is why the
	// four preference tests over this path keep passing unchanged.
	//
	// The guard's field is gone, not merely unread (storage.ArchiveLinkResult).
	// Consulting it at every back-link call site or at none is the invariant;
	// one of each is what produced the task above.
	if links.Found {
		if err := archive.TryLinkSessionNote(vault.Root, manifestPath, links.Canonical.NotePath); err != nil {
			return ref, err
		}
	}
	return ref, nil
}
