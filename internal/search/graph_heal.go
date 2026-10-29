// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// HealOutcome is what one HealGraph did. It mirrors ingest.HealResult field
// for field; internal/search cannot import internal/ingest (coverage.go), so
// the ingest-side adapter (ingest.NewEngineHealer) maps this to that package's
// result type.
type HealOutcome struct {
	Rebuilt bool   // the graph was rebuilt from cached vectors
	Deleted bool   // a corrupt hnsw.idx, or a below-L mismatch, was removed
	Reason  string // why, for vp.log
}

// healGatherHook, when a test sets it, runs the instant HealGraph captures its
// gather-time generation witness (builtGen), before HealGraph takes any lock.
// It proves the witness was read at the lock-free gather rather than under the
// commit lock (the B1/B3 guard depends on that ordering).
var healGatherHook func(project string)

// HealGraph is the graph-fingerprint self-heal (epic
// host-local-hnsw-index-rebuilt-from-vault-artifacts, Scope 8). The ingester
// and the explicit rebuild call it once per project with the index RUN LOCK
// already held; it is never on the search path. On a graph-fingerprint
// mismatch it rebuilds the HNSW graph from CACHED VECTORS ONLY (no embedding)
// and re-saves; a corrupt-header file is deleted; a mismatch whose expected id
// count is below the HNSW floor is deleted; a matching or absent file is a
// no-op.
//
// It takes no embedder (it cannot embed) and never writes a fingerprint stale
// reason: a graph mismatch is not stale. The build runs OFF ALL LOCKS; the
// only concurrency guard is a generation witness (builtGen, read at the
// lock-free gather, compared to the store counter under the commit lock),
// which skips the write when any writer committed in between.
//
// vaultRoot must equal the engine's bound vault root; the method heals
// e.vault, so a caller that named a different tree is a programming error.
func (e *Engine) HealGraph(ctx context.Context, vaultRoot, project string) (HealOutcome, error) {
	if vaultRoot != e.vault.Root {
		return HealOutcome{}, fmt.Errorf("search: heal vault %q != engine vault %q", vaultRoot, e.vault.Root)
	}

	// 1. Read the graph file lock-free, and classify its HEADER FIRST — before
	// the dims probe, so a corrupt-header file is deleted even when the cache is
	// empty.
	data, err := indexstore.ReadGraph(e.vault, project)
	if err != nil {
		return HealOutcome{}, fmt.Errorf("heal: read graph of %s: %w", project, err)
	}
	if data == nil {
		return HealOutcome{Reason: "no graph"}, nil // never convert a no-graph project
	}
	fp, ok := readGraphHeader(data)
	if !ok {
		// A corrupt header is always deletable and must go even if the store
		// advanced, so the delete is UNCONDITIONAL (no gen witness).
		if derr := e.healDelete(ctx, project); derr != nil {
			return HealOutcome{}, derr
		}
		return HealOutcome{Deleted: true, Reason: "corrupt header"}, nil
	}

	// 2. Dims from the cache (never the embedder, which would load ONNX).
	dims, haveDims, err := e.cachedDims(project)
	if err != nil {
		return HealOutcome{}, fmt.Errorf("heal: cached dims of %s: %w", project, err)
	}
	if !haveDims {
		// The header parsed, so no corrupt file is left behind; without a cached
		// vector there is nothing to rebuild from and no way to form the
		// configured fingerprint. Leave the file for the next rebuild.
		return HealOutcome{Reason: "dims unknown (empty cache)"}, nil
	}

	// 3. Match → "current" no-op. EfSearch stays in the fingerprint (ADR-014).
	params := provisionalHNSWParams
	if fp == computeFingerprint(dims, params.M, params.EfSearch) {
		return HealOutcome{Reason: "current"}, nil
	}

	// 4. Mismatch. Capture the gen witness at the lock-free gather, then gather
	// the cached corpus and build off every lock.
	builtGen, err := indexstore.ReadGeneration(e.vault, project)
	if err != nil {
		return HealOutcome{}, fmt.Errorf("heal: read generation of %s: %w", project, err)
	}
	if healGatherHook != nil {
		healGatherHook(project)
	}

	ids, vecs, nExpected, localMisses, err := e.gatherCachedCorpus(ctx, project)
	if err != nil {
		return HealOutcome{}, err
	}

	// 5. Below the HNSW floor by EXPECTED id count (not the cache-hit count):
	// delete, mirroring selectKind's mismatch-below-L row, behind the gen
	// witness.
	if nExpected < hnswSelectL {
		return e.healBelowL(ctx, project, builtGen)
	}

	// 6. Build a fresh graph from the cache hits only (misses left out, never
	// embedded) and save it, gen witness first.
	keep, err := dedupBatch(vecs, ids, dims)
	if err != nil {
		return HealOutcome{}, fmt.Errorf("heal: dedup corpus of %s: %w", project, err)
	}
	keepVecs := make([][]float32, len(keep))
	keepIDs := make([]string, len(keep))
	for i, idx := range keep {
		keepVecs[i] = vecs[idx]
		keepIDs[i] = ids[idx]
	}
	h := newHNSWIndex(dims, params)
	if err := h.Build(ctx, keepVecs, keepIDs); err != nil {
		return HealOutcome{}, fmt.Errorf("heal: build graph of %s: %w", project, err)
	}
	return e.healSave(ctx, project, h, builtGen, localMisses)
}

// cachedDims reports the embedding dimensionality from one of the project's
// cached vectors, WITHOUT loading the embedder (a .vec is exactly len(vec)*4
// little-endian float32 bytes, one vector per file — cache.putVector). No
// cached vector → (0, false, nil). If two .vec entries disagree on size it
// returns an error rather than trusting the first ReadDir entry (directory
// order is unspecified).
func (e *Engine) cachedDims(project string) (int, bool, error) {
	dir, err := e.cache.dir(project)
	if err != nil {
		return 0, false, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	dims, have := 0, false
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".vec") {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			return 0, false, err
		}
		size := info.Size()
		if size <= 0 || size%4 != 0 {
			continue // a corrupt .vec is a miss (cachedVector treats it so); not a dims source
		}
		d := int(size / 4)
		switch {
		case !have:
			dims, have = d, true
		case d != dims:
			return 0, false, fmt.Errorf("heal: embed cache of %s has .vec files of differing size (%d vs %d bytes)", project, dims*4, size)
		}
	}
	return dims, have, nil
}

// gatherCachedCorpus walks the same five corpus sources as rebuild — the
// host-local store, the glide-path tracked drawers, iterations, note bodies,
// and commit-log — READ-ONLY and WITHOUT embedding, and WITHOUT regenerating decision
// chunks (that writes under the commit lock; the heal re-saves only the
// graph). It returns the cache HITS as equal-length ids/vecs (so dedupBatch
// never sees a nil vector), the count of EXPECTED ids across all sources
// (after the drawer content-hash dedup; this governs the below-L decision),
// and the count of local-tier chunks whose vector missed the cache (which sets
// the missing-vector stale reason).
func (e *Engine) gatherCachedCorpus(ctx context.Context, project string) (ids []string, vecs [][]float32, nExpected, localMisses int, err error) {
	st, err := readStoreFn(e.vault, project)
	if err != nil {
		return nil, nil, 0, 0, fmt.Errorf("heal: read index store of %s: %w", project, err)
	}

	// First source: the host-local store (decision + local-tier chunks). A
	// decision miss is left out like --no-embed; a local-tier miss is counted
	// so it sets the missing-vector stale reason.
	storeIDs := map[string]bool{}
	for _, c := range st.Chunks(true) {
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, 0, 0, cerr
		}
		storeIDs[c.ID] = true
		nExpected++
		vec, verr := e.cachedVector(project, c.ID)
		if verr != nil {
			return nil, nil, 0, 0, verr
		}
		if vec == nil {
			if c.SourceType != storage.SourceTypeDecision {
				localMisses++
			}
			continue
		}
		ids = append(ids, c.ID)
		vecs = append(vecs, vec)
	}

	// Second source: tracked drawers (the glide path), only while the vault
	// carries no migration marker. A drawer whose content is already a store
	// chunk is deduped on the content hash (index.ChunkID), exactly as rebuild.
	migrated, merr := storage.VaultMigrated(e.vault.Root)
	if merr != nil {
		return nil, nil, 0, 0, fmt.Errorf("heal: migration marker of %s: %w", project, merr)
	}
	if !migrated {
		wings, werr := e.vault.ListWings(project)
		if werr != nil {
			return nil, nil, 0, 0, fmt.Errorf("heal: list wings of %s: %w", project, werr)
		}
		for _, wing := range wings {
			rooms, rerr := e.vault.ListRooms(project, wing)
			if rerr != nil {
				return nil, nil, 0, 0, fmt.Errorf("heal: list rooms of %s/%s: %w", project, wing, rerr)
			}
			for _, room := range rooms {
				drawers, derr := e.vault.ListDrawers(project, wing, room)
				if derr != nil {
					slog.Warn("heal: list drawers failed", "project", project, "wing", wing, "room", room, "err", derr)
					continue
				}
				for _, d := range drawers {
					if cerr := ctx.Err(); cerr != nil {
						return nil, nil, 0, 0, cerr
					}
					if storeIDs[index.ChunkID(d.Content)] {
						continue // deduped: the store's record wins; not a distinct expected id
					}
					nExpected++
					vec, verr := e.cachedVector(project, d.ID)
					if verr != nil {
						return nil, nil, 0, 0, verr
					}
					if vec == nil {
						continue
					}
					ids = append(ids, d.ID)
					vecs = append(vecs, vec)
				}
			}
		}
	}

	// Third source: iterations.md. Fourth source: session note bodies. Fifth
	// source: commit-log.md. All read from the vault, not the store; a cache
	// miss is left out.
	for _, src := range []struct {
		name    string
		collect func(*storage.Vault, string) ([]string, []string, []drawerMeta, error)
	}{
		{"iteration", collectIterationCorpus},
		{"note", collectNoteCorpus},
		{"commit", collectCommitLogCorpus},
	} {
		cids, _, _, cerr := src.collect(e.vault, project)
		if cerr != nil {
			return nil, nil, 0, 0, fmt.Errorf("heal: %s corpus of %s: %w", src.name, project, cerr)
		}
		for _, id := range cids {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, nil, 0, 0, ctxErr
			}
			nExpected++
			vec, verr := e.cachedVector(project, id)
			if verr != nil {
				return nil, nil, 0, 0, verr
			}
			if vec == nil {
				continue
			}
			ids = append(ids, id)
			vecs = append(vecs, vec)
		}
	}

	return ids, vecs, nExpected, localMisses, nil
}

// healSave writes the rebuilt graph under the commit lock, guarded by the
// generation witness: if the store counter advanced since builtGen was read at
// the gather, a writer (a capture's PutVectors, a raw commit) landed in the
// window and the rebuilt graph may omit its write, so the save is skipped and
// the next run retries. On a local-tier miss it appends the missing-vector
// stale reason (append-only, never through writeCompletenessLocked, which
// would also write a fingerprint reason and overwrite LocalMisses).
func (e *Engine) healSave(ctx context.Context, project string, h *hnswIndex, builtGen indexstore.Gen, localMisses int) (HealOutcome, error) {
	pl, err := e.lockProject(ctx, project, searchLockTimeout)
	if err != nil {
		return HealOutcome{}, fmt.Errorf("heal: lock project %s: %w", project, err)
	}
	defer pl.release()
	tx, err := pl.Tx(ctx, searchLockTimeout)
	if err != nil {
		return HealOutcome{}, fmt.Errorf("heal: commit lock of %s: %w", project, err)
	}
	if tx.Generation() != builtGen {
		return HealOutcome{Reason: "store advanced under heal; next run retries"}, nil
	}

	if localMisses > 0 {
		if err := markMissingVectorsLocked(tx); err != nil {
			return HealOutcome{}, fmt.Errorf("heal: mark missing vectors of %s: %w", project, err)
		}
	}

	data, w := h.snapshot()
	builtAt := tx.Generation()
	out, serr := saveGraph(tx, e.vault, project, h, data, w, builtAt)
	if serr != nil {
		return HealOutcome{}, fmt.Errorf("heal: save graph of %s: %w", project, serr)
	}
	if _, _, cerr := pl.finishTx(); cerr != nil {
		return HealOutcome{}, fmt.Errorf("heal: commit of %s: %w", project, cerr)
	}
	if out != written {
		slog.Info("heal: graph save skipped", "project", project, "outcome", out.String())
		return HealOutcome{Reason: "graph save skipped: " + out.String()}, nil
	}
	slog.Info("heal: graph rebuilt from cached vectors", "project", project, "nodes", h.Len(), "local_misses", localMisses)
	return HealOutcome{Rebuilt: true, Reason: "fingerprint mismatch"}, nil
}

// healDelete removes hnsw.idx under the commit lock. Used for a corrupt header
// (unconditional) — see healBelowL for the gen-guarded below-L delete.
func (e *Engine) healDelete(ctx context.Context, project string) error {
	pl, err := e.lockProject(ctx, project, searchLockTimeout)
	if err != nil {
		return fmt.Errorf("heal: lock project %s: %w", project, err)
	}
	defer pl.release()
	tx, err := pl.Tx(ctx, searchLockTimeout)
	if err != nil {
		return fmt.Errorf("heal: commit lock of %s: %w", project, err)
	}
	if _, derr := deleteGraph(tx); derr != nil {
		return fmt.Errorf("heal: delete graph of %s: %w", project, derr)
	}
	if _, _, cerr := pl.finishTx(); cerr != nil {
		return fmt.Errorf("heal: commit of %s: %w", project, cerr)
	}
	return nil
}

// healBelowL deletes a mismatched file whose expected id count is below the
// HNSW floor, behind the gen witness: a concurrent commit may have pushed the
// corpus across L, so on an advance the delete is skipped (the file survives,
// so Deleted is false) and the next run retries.
func (e *Engine) healBelowL(ctx context.Context, project string, builtGen indexstore.Gen) (HealOutcome, error) {
	pl, err := e.lockProject(ctx, project, searchLockTimeout)
	if err != nil {
		return HealOutcome{}, fmt.Errorf("heal: lock project %s: %w", project, err)
	}
	defer pl.release()
	tx, err := pl.Tx(ctx, searchLockTimeout)
	if err != nil {
		return HealOutcome{}, fmt.Errorf("heal: commit lock of %s: %w", project, err)
	}
	if tx.Generation() != builtGen {
		return HealOutcome{Reason: "store advanced under heal; next run retries"}, nil
	}
	if _, derr := deleteGraph(tx); derr != nil {
		return HealOutcome{}, fmt.Errorf("heal: delete graph of %s: %w", project, derr)
	}
	if _, _, cerr := pl.finishTx(); cerr != nil {
		return HealOutcome{}, fmt.Errorf("heal: commit of %s: %w", project, cerr)
	}
	return HealOutcome{Deleted: true, Reason: "below L"}, nil
}

// markMissingVectorsLocked appends the missing-vector stale reason to the
// project's completeness record under the held commit lock, and only that
// reason: it never writes a fingerprint reason and never overwrites Tiers or
// LocalMisses (unlike writeCompletenessLocked). A graph mismatch is not stale;
// the stale reason here is the VECTOR miss the heal rediscovered.
func markMissingVectorsLocked(tx *indexstore.Tx) error {
	rec, err := tx.Completeness()
	if err != nil {
		return err
	}
	want := indexstore.StaleReason{Kind: indexstore.StaleMissingVectors}
	if containsReason(rec.Stale, want) {
		return nil
	}
	rec.Stale = append(rec.Stale, want)
	_, err = tx.WriteCompleteness(rec)
	return err
}
