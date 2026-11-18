// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// U3 (task append-only-tx-kill-leaves-append-unannounced): a process killed
// after an append-only Tx's first write and before its finishing bump leaves a
// stale "a" marker; the next search must notice, rebuild, and serve the append
// with NO new writer, while a LIVE append costs a search nothing.

// warmHNSW builds a project the engine serves from an HNSW graph, with
// hnsw.idx freshly saved (so graphSaveDebounce is ACTIVE: nowFn is NOT
// overridden), and an engine whose loaded counter is current. It returns the
// build counter the tests read through buildErrHook.
func warmHNSW(t *testing.T, project string) (*Engine, *storage.Vault, *atomic.Int64) {
	t.Helper()
	withThresholds(t, 2, 1)
	v := testVault(t)
	addDrawers(t, v, project, 3)
	eng := newSelEngine(t, v)
	ctx := context.Background()
	if _, err := eng.RebuildExplicit(ctx, project); err != nil {
		t.Fatal(err)
	}
	if _, ok := installedIndex(t, eng, project).(*hnswIndex); !ok {
		t.Fatalf("fixture: want an HNSW index, got %T", installedIndex(t, eng, project))
	}
	if mt, present, err := graphFileModTime(v, project); err != nil || !present || time.Since(mt) >= graphSaveDebounce {
		t.Fatalf("fixture: the graph must be freshly saved so the debounce is active (present=%v age=%v err=%v)", present, time.Since(mt), err)
	}
	var builds atomic.Int64
	buildErrHook = func(string, error) { builds.Add(1) }
	t.Cleanup(func() { buildErrHook = nil })
	for i := 0; i < 2; i++ { // settle: the warm-up search may rebuild once
		if err := eng.ensureIndex(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	builds.Store(0)
	if err := eng.ensureIndex(ctx, project); err != nil || builds.Load() != 0 {
		t.Fatalf("fixture: a settled engine rebuilt (builds=%d err=%v)", builds.Load(), err)
	}
	return eng, v, &builds
}

func found(t *testing.T, eng *Engine, project string, d storage.Drawer) bool {
	t.Helper()
	res, err := eng.Search(context.Background(), d.Content, SearchFilters{Project: project, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.DrawerID == d.ID {
			return true
		}
	}
	return false
}

func graphHolds(t *testing.T, v *storage.Vault, project, id string) bool {
	t.Helper()
	idx, res, err := newHNSWIndexFromDisk(v, project, 384, provisionalHNSWParams)
	if err != nil || res != loaded || idx == nil {
		t.Fatalf("on-disk graph unreadable: res=%v err=%v", res, err)
	}
	defer idx.Close()
	return liveIDSet(idx)[id]
}

// S2'-A: a killed append on a warm HNSW project, debounce ACTIVE. With no
// writer: search 1 returns the item; searches 2..30, across the repair-induced
// clean counter, return it EVERY time; the durable graph holds it; at most two
// builds, then none; and a fresh engine, after the save, returns it too.
//
// Red before the change: the counter never moved, so the engine fast-returns
// and the item is never served.
// Mutations: drop the Stale->rebuild condition (the old disk graph is
// reinstalled at search 2); drop the debounce bypass (the disk graph lacks it,
// and the fresh engine misses it).
func TestAKilledAppendSurvivesTheDebouncedGraphAndTheRepairInducedGen(t *testing.T) {
	const project = "killhnsw"
	eng, v, builds := warmHNSW(t, project)
	x := addDrawer(t, v, project, "wing", "room", "killed append sentinel quux zorp", "facts")
	killAppendHelper(t, eng, v, project, x)

	for i := 1; i <= 30; i++ {
		if !found(t, eng, project, x) {
			t.Fatalf("search %d did not return the killed append's drawer (builds so far %d)", i, builds.Load())
		}
	}
	if n := builds.Load(); n > 2 {
		t.Errorf("a killed append cost %d builds, want at most two (the stale one and the post-repair one)", n)
	}
	if !graphHolds(t, v, project, x.ID) {
		t.Error("the durable graph does not hold the appended drawer: the stale-started rebuild's save was debounced away")
	}
	settled := builds.Load()
	for i := 0; i < 5; i++ {
		_ = found(t, eng, project, x)
	}
	if builds.Load() != settled {
		t.Errorf("searches kept rebuilding after the repair settled (%d -> %d)", settled, builds.Load())
	}
	fresh := newSelEngine(t, v)
	if !found(t, fresh, project, x) {
		t.Error("a fresh engine after the save does not serve the appended drawer")
	}
}

// S2b: the same, on a project served by a brute index (no graph to catch up).
func TestAKilledAppendIsServedFromABruteIndexWithoutAnyNewWriter(t *testing.T) {
	const project = "killbrute"
	v := testVault(t)
	addDrawers(t, v, project, 3)
	eng := newSelEngine(t, v)
	ctx := context.Background()
	if _, err := eng.Rebuild(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := eng.ensureIndex(ctx, project); err != nil {
		t.Fatal(err)
	}
	x := addDrawer(t, v, project, "wing", "room", "killed brute sentinel plugh", "facts")
	killAppendHelper(t, eng, v, project, x)
	for i := 0; i < 5; i++ {
		if !found(t, eng, project, x) {
			t.Fatalf("search %d did not serve the killed append", i+1)
		}
	}
}

// S1: a search overlapping a LIVE append keeps its fast path: ZERO rebuilds
// (counted through buildErrHook) and it never waits on the commit lock the
// appender holds. After the append commits, the next search reloads once, as
// before. Passes before the change; red under any design that fails a reader
// closed on an append marker.
//
// Mutation: make the append marker return ErrWriteInFlight, or make the probe
// always report the writer dead.
func TestASearchOverlappingALiveAppendKeepsTheFastPath(t *testing.T) {
	const project = "liveappend"
	eng, v, builds := warmHNSW(t, project)
	ctx := context.Background()

	warmGen, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := indexstore.Lock(ctx, v, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	w, err := eng.CacheWriter(tx)
	if err != nil {
		_ = tx.Release()
		t.Fatal(err)
	}
	if err := tx.PutVectors(w, map[string][]float32{"live-orphan": mockVec(t, "live append vector")}); err != nil {
		_ = tx.Release()
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		done := make(chan error, 1)
		go func() { done <- eng.ensureIndex(ctx, project) }()
		select {
		case err := <-done:
			if err != nil {
				_ = tx.Release()
				t.Fatalf("ensureIndex during a live append: %v", err)
			}
		case <-time.After(2 * time.Second):
			_ = tx.Release()
			t.Fatal("ensureIndex blocked during a live append: it left the fast path and waited on the commit lock")
		}
	}
	if n := builds.Load(); n != 0 {
		_ = tx.Release()
		t.Fatalf("%d rebuilds during a live append, want 0", n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := eng.ensureIndex(ctx, project); err != nil {
		t.Fatal(err)
	}
	// The finishing bump announces the append: the next search leaves the fast
	// path (a witnessed load or a build) and remembers the new counter.
	cur, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		t.Fatal(err)
	}
	eng.buildMu.Lock()
	remembered := eng.loaded[project]
	eng.buildMu.Unlock()
	if cur == warmGen || remembered != cur {
		t.Errorf("after the append committed: counter %+v (was %+v), remembered %+v; want the engine to have reloaded to the new counter", cur, warmGen, remembered)
	}
}

// E1: the stale observation survives a repair that lands BETWEEN ensureIndex's
// read and rebuildAndRemember's own read. The seam repairs the marker (a
// Lock) after ensureIndex saw Stale; rebuildAndRemember then reads a clean
// counter, so only the carried bit can make the rebuild bypass the graph-save
// debounce. The graph mtime is inside the debounce window. Both the current
// engine and a fresh engine must serve the appended drawer.
//
// Red before the change: the hook never fires (the counter never was Stale).
// Mutation: derive the stale bit from the second read only.
func TestTheStaleObservationSurvivesARepairBetweenTheTwoReads(t *testing.T) {
	const project = "e1hnsw"
	eng, v, _ := warmHNSW(t, project)
	x := addDrawer(t, v, project, "wing", "room", "e1 sentinel wibble frotz", "facts")
	killAppendHelper(t, eng, v, project, x)

	var hookRan, sawStale atomic.Bool
	afterEnsureReadHook = func(p string, g indexstore.Gen) {
		hookRan.Store(true)
		if !g.Stale {
			return
		}
		sawStale.Store(true)
		tx, err := indexstore.Lock(context.Background(), v, p, indexstore.NoTimeout) // repairs the marker
		if err != nil {
			t.Errorf("repairing Lock: %v", err)
			return
		}
		_ = tx.Release()
	}
	t.Cleanup(func() { afterEnsureReadHook = nil })

	if !found(t, eng, project, x) {
		t.Fatal("the current engine did not serve the killed append")
	}
	if !hookRan.Load() || !sawStale.Load() {
		t.Fatalf("the seam did not see a Stale counter (ran=%v stale=%v)", hookRan.Load(), sawStale.Load())
	}
	if !graphHolds(t, v, project, x.ID) {
		t.Error("the durable graph lacks the appended drawer: the rebuild lost the stale observation and the debounce skipped its save")
	}
	if !found(t, newSelEngine(t, v), project, x) {
		t.Error("a fresh engine does not serve the appended drawer")
	}
}

// P3 (C3): the probe holds the commit lock while a ZERO-timeout capture insert
// (indexDrawers with timeout 0) attempts it. The insert is skipped, and the
// retry path is insertSkipped: the project is marked out of date (loaded
// dropped, invalidated incremented) so the NEXT search rebuilds from the
// tracked drawers: the drawer is found, not lost.
//
// Red before the change: no probe runs, so the hook never fires.
// Mutation: insertSkipped no longer marks the project out of date.
func TestASkippedCaptureInsertIsRetriedByTheNextSearch(t *testing.T) {
	const project = "probecapture"
	v := testVault(t)
	addDrawers(t, v, project, 3)
	eng := newSelEngine(t, v)
	ctx := context.Background()
	if _, err := eng.Rebuild(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := eng.ensureIndex(ctx, project); err != nil {
		t.Fatal(err)
	}
	stale := addDrawer(t, v, project, "wing", "room", "stale marker carrier drawer", "facts")
	killAppendHelper(t, eng, v, project, stale)
	late := addDrawer(t, v, project, "wing", "room", "late capture drawer nusquam", "facts")

	var insertErr error
	var hookRan bool
	var invalidated uint64
	var loadedAfter bool
	defer indexstore.SetProbeHeldHook(func() {
		hookRan = true
		insertErr = eng.indexDrawers(ctx, []DrawerInput{{Project: project, Wing: "wing", Room: "room", Drawer: late, Vec: mockVec(t, late.Content)}}, 0)
		eng.buildMu.Lock()
		invalidated = eng.invalidated[project]
		_, loadedAfter = eng.loaded[project]
		eng.buildMu.Unlock()
	})()

	if !found(t, eng, project, stale) {
		t.Fatal("the search did not serve the stale marker's drawer")
	}
	if !hookRan {
		t.Fatal("the probe never ran its held hook")
	}
	if insertErr != nil {
		t.Fatalf("a zero-timeout insert on a non-migrated vault returned %v, want nil (skipped, retried by the next search)", insertErr)
	}
	if invalidated == 0 || loadedAfter {
		t.Fatalf("insertSkipped did not mark the project out of date (invalidated=%d loaded=%v)", invalidated, loadedAfter)
	}
	if !found(t, eng, project, late) {
		t.Fatal("the capture insert the probe made skip was LOST: the next search did not serve it")
	}
}
