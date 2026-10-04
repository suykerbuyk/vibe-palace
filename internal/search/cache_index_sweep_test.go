// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
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
	}, mustWriter(t, cache, tx))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("the orphan's store was touched during another project's commit: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := reapProject(t, eng, "proj"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("the engine path did not reap the orphan's store: %v", err)
	}
}

// The engine's index sweep runs before the engine takes any commit lock, never
// while it holds one: the sweep takes each gone project's commit lock, and no
// process may hold two. The observer sees every commit lock taken and
// released; the gone project's must be taken while no other is held. Each
// entry point that takes a commit lock through lockProject (a lazy search's
// Rebuild, IndexDrawers, RemoveDrawer) is driven on a fresh engine, so each is
// the engine's first lock and runs the sweep.
func TestEngineSweepRunsBeforeTheCommitLock(t *testing.T) {
	for _, entry := range []string{"search", "rebuild", "index-drawers", "remove-drawer"} {
		t.Run(entry, func(t *testing.T) {
			eng, v := testEngine(t)
			ctx := context.Background()
			addDrawer(t, v, "proj", "wing", "room", "a drawer", "facts")
			ensureProjectDir(t, v, "proj") // the sweep removes nothing while Projects/ is empty
			orphan, _ := v.IndexDir("gone")
			if err := os.MkdirAll(orphan, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(orphan, "chunks.jsonl"), []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			var mu sync.Mutex
			held := map[string]bool{}
			var sweptWhile []string
			sawGone := false
			restore := indexstore.ObserveCommitLocks(func(project string, ev indexstore.CommitLockEvent) {
				mu.Lock()
				defer mu.Unlock()
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

			var err error
			switch entry {
			case "search":
				_, err = eng.Search(ctx, "drawer", SearchFilters{Project: "proj"})
			case "rebuild":
				_, err = eng.Rebuild(ctx, "proj")
			case "index-drawers":
				err = eng.IndexDrawers(ctx, []DrawerInput{{Project: "proj", Wing: "wing", Room: "room",
					Drawer: storage.Drawer{ID: "d1", Content: "x"}, Vec: []float32{1, 0, 0}}})
			case "remove-drawer":
				err = eng.RemoveDrawer("proj", "d1")
			}
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !sawGone {
				t.Fatal("the engine's index sweep never took the gone project's commit lock")
			}
			if len(sweptWhile) > 0 {
				t.Fatalf("the index sweep took gone's commit lock while the engine held %v: two commit locks at once", sweptWhile)
			}
		})
	}
}
