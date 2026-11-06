// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"log/slog"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
)

// DropTranscriptTier runs the one-time archive-owner purge over project's
// host-local store: every archive-owned chunk, KG record and ledger session
// row goes, and the imported batches, the note-owned decision chunks and the
// baseline set stay — SCOPED TO THIS OPERATION: step 3's discardOnMismatch
// runs before it on the same rebuild, and a chunks-recipe mismatch there
// still removes the imported batches wholesale through
// Tx.Discard(DiscardChunks). See indexstore.Tx.DropTranscriptTier.
// It is `vp index rebuild <project> --drop-transcripts`,
// and it is the only thing that takes an already-built host's store back under
// the chunk-store size cliff after the transcript tier is excluded.
//
// It is this package's entry point rather than internal/ingest's because the
// vector reap needs the engine's live set (liveSet), which only the engine can
// compute. The ordering, the preservation rules and the interruption
// semantics all belong to indexstore.Tx.DropTranscriptTier; this holds the
// project's mutex and its index commit lock around that call and commits, the
// same shape every other engine-side destructive step uses.
func (e *Engine) DropTranscriptTier(ctx context.Context, project string) (indexstore.TranscriptPurge, error) {
	var res indexstore.TranscriptPurge
	pl, err := e.lockProject(ctx, project, indexstore.NoTimeout)
	if err != nil {
		return res, err
	}
	defer pl.release()
	tx, err := pl.Tx(ctx, indexstore.NoTimeout)
	if err != nil {
		return res, err
	}
	res, err = tx.DropTranscriptTier(func() (map[string]bool, error) { return e.liveSet(project) })
	if err != nil {
		return res, err
	}
	// The counter moved destructively (beginDestructive's pre-bump, the
	// explicit post-ledger bump, and Commit's own), so nothing is remembered
	// here: every remembered generation this engine held is now stale, which
	// is what makes the next search rebuild rather than serve a graph and an
	// id set that still describe the transcripts just removed.
	if _, _, err := pl.finishTx(); err != nil {
		return res, err
	}
	slog.Info("vp index rebuild: transcript tier purged",
		"project", project, "ledger_lines_dropped", res.LedgerLinesDropped,
		"baseline_added", res.BaselineAdded, "chunks_dropped", res.ChunksDropped,
		"kg_dropped", res.KGDropped, "vectors_reaped", res.VectorsReaped)
	return res, nil
}
