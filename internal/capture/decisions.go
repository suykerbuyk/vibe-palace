// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// decisionWriteTimeout bounds how long a decision write waits for the project's
// index commit lock before it skips, as a search waits (ADR-014 decision 7:
// note-time writers never stall behind an ingest commit). A var so a test can
// make the wait zero. A context deadline bounds it further, which is what keeps
// the hook's write inside its remaining time.
var decisionWriteTimeout = 5 * time.Second

// fileDecisionDrawers writes a session note's decisions to the host-local chunk
// store (decision-chunks-in-the-host-local-store), replacing the note's whole
// decision set under the project's index commit lock. It no longer touches the
// tracked tree: decision chunks live in palace/.local/index/<p>/chunks.jsonl,
// owned by the note, dated from the ledger.
//
// It is one commit step under the leaf commit lock, taken with a timeout. On a
// timeout (or a vanished project) the write is skipped with a vp.log warning and
// nil is returned: the next notes-tier build or `vp index rebuild` restores the
// note's decision chunks, so a busy lock never costs a captured session. Only
// the chunk text and owners are written here; vectors for decision chunks are
// embedded later by the search path's notes tier, outside the lock.
//
// noteStem is the note's file stem under Projects/<p>/sessions/ (its owner key);
// archiveSessionID is the note's host session id, used only to date the chunks
// from the ledger. It returns the number of decision chunks the note carries.
func fileDecisionDrawers(ctx context.Context, vault *storage.Vault, project, noteStem, archiveSessionID, noteDate string, decisions []string) (int, error) {
	ix, err := palace.ProjectIndexing(vault, project)
	if err != nil {
		return 0, err
	}

	tx, err := indexstore.Lock(ctx, vault, project, decisionWriteTimeout)
	if err != nil {
		if errors.Is(err, vaultlock.ErrLockWaitTimeout) || errors.Is(err, indexstore.ErrProjectGone) {
			slog.Warn("decision chunks: index commit lock unavailable; skipping, the next notes-tier build will restore them",
				"project", project, "note", noteStem, "err", err)
			return 0, nil
		}
		return 0, err
	}
	defer func() { _ = tx.Release() }()

	if err := search.WriteNoteDecisions(tx, ix.Recipe, project, noteStem, archiveSessionID, noteDate, decisions); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	n := 0
	for _, d := range decisions {
		if len(d) > 0 {
			n++
		}
	}
	return n, nil
}
