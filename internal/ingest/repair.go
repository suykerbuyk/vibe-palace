// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The repair pass (Scope 4, "The repair pass"; ADR-014 decision 7), run once
// per project per RunHeld after its pending archives, within the same budget:
//
//   - missing vectors of chunks a ledgered source owns (an archive or an
//     import batch): re-embedded outside the lock and written under it; each
//     archive or batch whose vectors it repairs counts one against the
//     budget. When no miss remains it clears the missing-vector stale reason,
//     never the fingerprint reason. The proof is Engine.RepairScan, which
//     holds the project's commit lock for a full store scan: searches still
//     read meanwhile, but skip their own completeness writes (they take the
//     commit lock with a timeout).
//   - a ledgered archive owning fewer chunks than its ledger record counts is
//     re-ingested through IngestArchive (Repair), one budget unit;
//   - an import batch with a shortfall is reported in vp.log: only re-running
//     the import restores it.

// repairProject runs the repair pass for project p.
func repairProject(ctx context.Context, d Deps, o RunOptions, p string, entries []*archive.Entry, st *runState, res *RunResult) error {
	snap, err := indexstore.ReadStore(d.Vault, p)
	if err != nil {
		return err
	}
	l := snap.Ledger()
	if !l.Exists() {
		return nil
	}
	if err := repairVectors(ctx, d, o, p, snap, st); err != nil {
		return err
	}
	for _, e := range entries {
		sha, sid := e.Manifest.SourceSHA256, e.Manifest.SessionID
		rec, ok := l.Session(sid)
		if sha == "" || !ok || rec.State != indexstore.StateLive || rec.SHA != sha || st.attempted[sha] {
			continue
		}
		if snap.CountChunks(indexstore.ArchiveOwner(sha)) >= rec.ChunkCount {
			continue
		}
		if !st.admit(d, o.Explicit) {
			return nil
		}
		slog.Info("ingest: re-ingesting an archive short of chunks", "project", p, "archive", e.ArchivePath,
			"owned", snap.CountChunks(indexstore.ArchiveOwner(sha)), "recorded", rec.ChunkCount)
		_, err := IngestArchive(ctx, d, p, e, IngestOptions{Embed: true, Checkpoint: o.Checkpoint, Repair: true})
		st.admitted++
		st.attempted[sha] = true
		switch {
		case err == nil:
			res.Repaired++
		case errors.Is(err, ErrCheckpoint):
			st.stopped = "checkpoint"
			return nil
		case errors.Is(err, indexstore.ErrProjectGone), errors.Is(err, ErrFingerprintStale):
			return nil
		default:
			slog.Warn("ingest: repair of an archive failed", "project", p, "archive", e.ArchivePath, "error", err)
		}
	}
	for _, id := range l.BatchIDs() {
		b, _ := l.Batch(id)
		if have := snap.CountChunks(indexstore.BatchOwner(id)); have < b.ChunkCount {
			slog.Warn("ingest: an import batch owns fewer chunks than its ledger record; only re-running the mempalace import from its export file restores them",
				"project", p, "batch", id, "owned", have, "recorded", b.ChunkCount)
		}
	}
	return nil
}

// repairVectors re-embeds the missing vectors of chunks a ledgered archive or
// batch owns, and clears the missing-vector reason once none is left.
func repairVectors(ctx context.Context, d Deps, o RunOptions, p string, snap *indexstore.Store, st *runState) error {
	byOwner := map[string]map[string]string{} // owner key -> chunk id -> content
	var owners []string
	for _, c := range snap.Chunks(true) {
		if c.SourceType == storage.SourceTypeDecision || c.Selected == nil || c.Selected.Kind == indexstore.OwnerNote {
			continue
		}
		_, hit, err := d.Engine.CachedVector(p, c.ID)
		if err != nil {
			return err
		}
		if hit {
			continue
		}
		k := c.Selected.Kind + ":" + c.Selected.SHA + c.Selected.ID
		if byOwner[k] == nil {
			byOwner[k] = map[string]string{}
			owners = append(owners, k)
		}
		byOwner[k][c.ID] = c.Content
	}
	slices.Sort(owners)
	for _, k := range owners {
		if !st.admit(d, o.Explicit) {
			break
		}
		st.admitted++
		ids := make([]string, 0, len(byOwner[k]))
		texts := make([]string, 0, len(byOwner[k]))
		for id, content := range byOwner[k] {
			ids = append(ids, id)
			texts = append(texts, content)
		}
		vecs, err := embedTexts(ctx, d, ids, texts)
		if err != nil {
			slog.Warn("ingest: repair could not embed missing vectors", "project", p, "owner", k, "error", err)
			continue
		}
		if err := putVectors(ctx, d, p, vecs); err != nil {
			if errors.Is(err, indexstore.ErrProjectGone) || errors.Is(err, ErrFingerprintStale) {
				return nil
			}
			return err
		}
	}
	// The repair scan, not this pass's bookkeeping, decides: it clears the
	// reason only when no loaded chunk misses a vector.
	return clearMissingVectors(ctx, d, p)
}

// embedTexts embeds texts in batches, keyed by ids.
func embedTexts(ctx context.Context, d Deps, ids, texts []string) (map[string][]float32, error) {
	if d.Embedder == nil {
		return nil, errors.New("ingest: vectors to repair but no embedder")
	}
	vecs := map[string][]float32{}
	for start := 0; start < len(ids); start += embedBatchSize {
		end := min(start+embedBatchSize, len(ids))
		got, err := d.Embedder.EmbedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		if len(got) != end-start {
			return nil, errors.New("ingest: the embedder returned the wrong number of vectors")
		}
		for i, id := range ids[start:end] {
			vecs[id] = got[i]
		}
	}
	return vecs, nil
}

// putVectors writes repaired vectors under the commit lock, through the
// engine's regime-checked writer.
func putVectors(ctx context.Context, d Deps, p string, vecs map[string][]float32) error {
	tx, err := indexstore.Lock(ctx, d.Vault, p, indexstore.NoTimeout)
	if err != nil {
		return err
	}
	defer tx.Release()
	w, err := d.Engine.CacheWriter(tx)
	if err != nil {
		if errors.Is(err, search.ErrEmbedRegimeMismatch) {
			return ErrFingerprintStale
		}
		return err
	}
	if err := tx.PutVectors(w, vecs); err != nil {
		return err
	}
	return tx.Commit()
}

// clearMissingVectors clears the missing-vector stale reason when the
// project's completeness record carries it and a repair scan under the lock
// finds no miss left. The fingerprint reason is never cleared here.
func clearMissingVectors(ctx context.Context, d Deps, p string) error {
	rec, err := indexstore.ReadCompleteness(d.Vault, p)
	if err != nil {
		return err
	}
	has := false
	for _, r := range rec.Stale {
		if r.Kind == indexstore.StaleMissingVectors {
			has = true
		}
	}
	if !has {
		return nil
	}
	tx, err := indexstore.Lock(ctx, d.Vault, p, indexstore.NoTimeout)
	if err != nil {
		if errors.Is(err, indexstore.ErrProjectGone) {
			return nil
		}
		return err
	}
	defer tx.Release()
	proof, err := d.Engine.RepairScan(tx)
	if err != nil {
		slog.Info("ingest: missing vectors remain; the stale reason stays", "project", p, "error", err)
		return nil
	}
	if err := search.ClearMissingVectors(tx, proof); err != nil {
		return err
	}
	return tx.Commit()
}

// healGraph calls the graph-heal seam for a project the run visited (Scope 4,
// "The graph-fingerprint self-heal"), with the run lock held. It embeds
// nothing and counts nothing against the budget; a graph mismatch is never
// stale.
func healGraph(ctx context.Context, d Deps, p string) {
	if d.GraphHealer == nil {
		return
	}
	r, err := d.GraphHealer.HealGraph(ctx, d.Vault.Root, p)
	if err != nil {
		slog.Warn("ingest: graph heal failed", "project", p, "error", err)
		return
	}
	if r.Rebuilt || r.Deleted {
		slog.Info("ingest: graph healed", "project", p, "rebuilt", r.Rebuilt, "deleted", r.Deleted, "reason", r.Reason)
	}
}
