// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
)

// The index sweep never runs inside the embed cache's own sweep. A cache's first
// operation can be a Put made under a project's index commit lock (Tx.putVectors);
// had the index pass run there, it would take a SECOND commit lock (the orphan's)
// while the first is held, and remove the orphan's store mid-commit. Here the
// first operation of a fresh cache is exactly such a Put, with an orphan store
// present: the store survives the commit, and the engine path reaps it later,
// before it takes any commit lock.
func TestIndexSweepIsNotInsideTheEmbedCacheSweep(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(v.Root, "Projects", "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	orphan, _ := v.IndexDir("gone")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "chunks.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cache := NewEmbedCache(v) // its first operation is the Put below
	tx, err := indexstore.Lock(ctx, v, "proj", indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	tx.UseRecipe(index.ChunkRecipe{IndexerVersion: index.IndexerVersion})
	if _, err := tx.EnsureLedger(nil); err != nil {
		t.Fatal(err)
	}
	id := index.ChunkID("a batch chunk")
	err = tx.CommitBatch(indexstore.BatchCommit{
		BatchID:  "batch-1",
		StartDay: "2026-05-13",
		Chunks:   []indexstore.OwnedChunk{{Chunk: indexstore.Chunk{ID: id, Content: "a batch chunk"}}},
		Vectors:  map[string][]float32{id: {1, 0, 0}},
	}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("the orphan's store was touched during another project's commit: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.reap(ctx, "proj"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("the engine path did not reap the orphan's store: %v", err)
	}
}

// The engine's index sweep runs before reap takes the project's commit lock,
// never while it holds it: the sweep takes each gone project's commit lock, and
// no process may hold two. The observer sees every commit lock taken and
// released; the gone project's must be taken while no other is held.
func TestEngineSweepRunsBeforeTheCommitLock(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(v.Root, "Projects", "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	orphan, _ := v.IndexDir("gone")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "chunks.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	held := map[string]bool{}
	var sweptWhile []string
	sawGone := false
	restore := indexstore.ObserveCommitLocks(func(project string, ev indexstore.CommitLockEvent) {
		switch ev {
		case indexstore.CommitAcquired:
			if project == "gone" {
				sawGone = true
				for p := range held {
					sweptWhile = append(sweptWhile, p)
				}
			}
			held[project] = true
		case indexstore.CommitReleased:
			delete(held, project)
		}
	})
	defer restore()

	if _, err := eng.reap(ctx, "proj"); err != nil {
		t.Fatal(err)
	}
	if !sawGone {
		t.Fatal("the engine's index sweep never took the gone project's commit lock")
	}
	if len(sweptWhile) > 0 {
		t.Fatalf("the index sweep took gone's commit lock while the engine held %v: two commit locks at once", sweptWhile)
	}
}
