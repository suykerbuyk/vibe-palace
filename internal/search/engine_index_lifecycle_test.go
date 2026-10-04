// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// heldHNSWIndex returns an hnswIndex whose tombstone rebuild has started and is
// held at gate, just after its snapshot.
func heldHNSWIndex(t *testing.T, gate *rebuildGate) (*hnswIndex, *hnswRebuild) {
	t.Helper()
	idx, _ := filledIndex(t, embeddingDims, 200, rebuildTestParams(gate), rand.New(rand.NewSource(31)))
	for i := 0; currentRebuild(idx) == nil; i++ {
		idx.Delete(fmt.Sprintf("v%d", i))
	}
	b := currentRebuild(idx)
	waitOrDump(t, gate.snapshotted, "the rebuild's snapshot")
	return idx, b
}

// TestEngineRebuildClosesTheReplacedIndexOffLock: when Rebuild replaces a
// project's index, on the ordinary path and on the empty-corpus path that
// drops it, the old index is closed (its held rebuild cancelled and exited,
// never swapped) and closed AFTER e.mu is released: Close waits for the
// rebuild goroutine, and holding e.mu there would stall every search.
func TestEngineRebuildClosesTheReplacedIndexOffLock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content bool
	}{{"replaced", true}, {"dropped-empty-corpus", false}} {
		t.Run(tc.name, func(t *testing.T) {
			eng, v := testEngine(t)
			mkProject(t, v, "proj")
			if tc.content {
				addDrawer(t, v, "proj", "wing-a", "room-1", "some content", "facts")
			}
			gate := newRebuildGate()
			old, b := heldHNSWIndex(t, gate)
			eng.mu.Lock()
			eng.indexes["proj"] = old
			eng.mu.Unlock()

			hookRan, lockFree := false, false
			eng.beforeIndexClose = func() {
				hookRan = true
				if eng.mu.TryLock() {
					lockFree = true
					eng.mu.Unlock()
				}
			}
			done := goDone(func() {
				if _, err := eng.Rebuild(context.Background(), "proj"); err != nil {
					t.Errorf("Rebuild: %v", err)
				}
			})
			waitOrDump(t, done, "Rebuild")

			if !hookRan {
				t.Fatal("the replaced index was never closed")
			}
			if !lockFree {
				t.Error("the replaced index was closed while e.mu was held")
			}
			select {
			case <-b.done:
			default:
				t.Error("Rebuild returned before the replaced index's rebuild goroutine exited")
			}
			if got := old.rebuildsRunning.Load(); got != 0 {
				t.Errorf("replaced index: rebuilds running = %d, want 0", got)
			}
			if got := old.swaps.Load(); got != 0 {
				t.Errorf("replaced index: swaps = %d, want 0", got)
			}
		})
	}
}

// TestEngineCloseClosesEveryIndex: Engine.Close cancels each index's background
// rebuild and waits for it before closing the embedder.
func TestEngineCloseClosesEveryIndex(t *testing.T) {
	eng, _ := testEngine(t)
	gate := newRebuildGate()
	idx, b := heldHNSWIndex(t, gate)
	eng.mu.Lock()
	eng.indexes["proj"] = idx
	eng.mu.Unlock()

	waitOrDump(t, goDone(func() { _ = eng.Close() }), "Engine.Close")
	select {
	case <-b.done:
	default:
		t.Error("Engine.Close returned before the index's rebuild goroutine exited")
	}
	if got := idx.swaps.Load(); got != 0 {
		t.Errorf("swaps = %d, want 0", got)
	}
}

// TestEngineRebuildRunsOffLock: RemoveDrawer holds e.mu while it deletes, and
// the delete that crosses the tombstone threshold starts the rebuild. While that
// rebuild is held, RemoveDrawer returns, and searches on the same project and
// on another project all return; only then is the rebuild released. A rebuild
// run under e.mu or under the index's own lock fails at the liveness bound.
func TestEngineRebuildRunsOffLock(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	mkProject(t, v, "p1")
	mkProject(t, v, "p2")

	gate := newRebuildGate()
	params := rebuildTestParams(gate)
	held := newHNSWIndex(embeddingDims, params)
	rng := rand.New(rand.NewSource(32))
	vecs := make([][]float32, 300)
	ids := make([]string, len(vecs))
	for i := range vecs {
		vecs[i] = randomUnitVector(rng, embeddingDims)
		ids[i] = fmt.Sprintf("p1-%d", i)
	}
	if err := held.Build(vecs, ids); err != nil {
		t.Fatal(err)
	}
	other := newBruteIndex(embeddingDims)
	if err := other.Insert("p2-0", randomUnitVector(rng, embeddingDims)); err != nil {
		t.Fatal(err)
	}
	eng.mu.Lock()
	eng.indexes["p1"] = held
	eng.indexes["p2"] = other
	eng.mu.Unlock()

	removed := goDone(func() {
		for i := 0; currentRebuild(held) == nil && i < len(ids); i++ {
			if err := eng.RemoveDrawer("p1", ids[i]); err != nil {
				t.Errorf("RemoveDrawer: %v", err)
				return
			}
		}
	})
	waitOrDump(t, removed, "RemoveDrawer across the threshold")
	b := currentRebuild(held)
	if b == nil {
		t.Fatal("no rebuild started")
	}
	waitOrDump(t, gate.snapshotted, "the rebuild's snapshot")

	for _, project := range []string{"p1", "p2"} {
		searched := goDone(func() {
			if _, err := eng.SearchReady(ctx, "anything", SearchFilters{Project: project}); err != nil {
				t.Errorf("SearchReady(%s): %v", project, err)
			}
		})
		waitOrDump(t, searched, "a search on "+project+" during the rebuild")
	}
	sameIndex := goDone(func() {
		if _, err := held.Search(vecs[299], 5); err != nil {
			t.Errorf("Search on the rebuilding index: %v", err)
		}
	})
	waitOrDump(t, sameIndex, "a search on the rebuilding index")

	close(gate.release)
	waitOrDump(t, b.done, "the released rebuild")
	if got := held.swaps.Load(); got != 1 {
		t.Errorf("swaps = %d, want 1", got)
	}
}

// TestEngineSearchHugeLimit: an absurd limit (vp search -n) neither overflows
// the candidate count into an error nor sizes an allocation by it.
func TestEngineSearchHugeLimit(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	addDrawer(t, v, "proj", "wing-a", "room-1", "first document", "facts")
	addDrawer(t, v, "proj", "wing-a", "room-1", "second document", "facts")
	if _, err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}
	res, err := eng.Search(ctx, "document", SearchFilters{Project: "proj", Limit: math.MaxInt})
	if err != nil {
		t.Fatalf("Search with Limit = MaxInt: %v", err)
	}
	if len(res) != 2 {
		t.Errorf("got %d results, want 2", len(res))
	}
}

// TestEngineCloseClearsTheBuiltMemo: once Close has dropped every index, no
// project may still read as built, or a later lazy search would skip the build
// and find no index.
func TestEngineCloseClearsTheBuiltMemo(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	addDrawer(t, v, "proj", "wing-a", "room-1", "some content", "facts")
	if _, err := eng.Search(ctx, "content", SearchFilters{Project: "proj"}); err != nil {
		t.Fatal(err)
	}
	eng.buildMu.Lock()
	_, builtBefore := eng.loaded["proj"]
	eng.buildMu.Unlock()
	if !builtBefore {
		t.Fatal("the lazy search did not mark proj built; the test proves nothing")
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	eng.buildMu.Lock()
	defer eng.buildMu.Unlock()
	if len(eng.loaded) != 0 {
		t.Errorf("loaded = %v after Close, want empty: the index map is empty", eng.loaded)
	}
}
