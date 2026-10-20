// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// --- Unit C helpers ---------------------------------------------------------

// catchupIdx builds an hnswIndex holding ids/vecs under production params, for
// the direct catchUpGraph tests.
func catchupIdx(t *testing.T, ids []string, vecs [][]float32) *hnswIndex {
	t.Helper()
	h := newHNSWIndex(384, provisionalHNSWParams)
	if err := h.Build(context.Background(), vecs, ids); err != nil {
		t.Fatal(err)
	}
	return h
}

func idPositions(ids []string) []int {
	keep := make([]int, len(ids))
	for i := range ids {
		keep[i] = i
	}
	return keep
}

func emptyMetas(n int) []drawerMeta {
	m := make([]drawerMeta, n)
	for i := range m {
		m[i] = drawerMeta{Content: fmt.Sprintf("c%d", i)}
	}
	return m
}

func liveIDSet(h *hnswIndex) map[string]bool {
	s := map[string]bool{}
	for _, id := range h.liveIDs() {
		s[id] = true
	}
	return s
}

func catchingUpFlag(h *hnswIndex) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.catchingUp
}

// --- 1-3. catchUpGraph (direct) ---------------------------------------------

// Test 1: catch-up inserts new ids and tombstones removed ones.
// Mutant: skip the Delete loop (c stays live) / skip the Insert loop (d absent).
func TestCatchUpAddsNewTombstonesRemoved(t *testing.T) {
	e := newSelEngine(t, testVault(t))
	ids := []string{"a", "b", "c"}
	vecs := [][]float32{oneHotVec(384, 1), oneHotVec(384, 2), oneHotVec(384, 3)}
	h := catchupIdx(t, ids, vecs)

	// Now the corpus is {a, b, d}: c removed, d added.
	ids2 := []string{"a", "b", "d"}
	vecs2 := [][]float32{oneHotVec(384, 1), oneHotVec(384, 2), oneHotVec(384, 4)}
	changed, err := e.catchUpGraph(h, idPositions(ids2), ids2, vecs2, emptyMetas(3))
	if err != nil {
		t.Fatal(err)
	}

	if !changed {
		t.Fatal("catch-up with an add and a remove reported no change")
	}
	got := liveIDSet(h)
	for _, want := range []string{"a", "b", "d"} {
		if !got[want] {
			t.Fatalf("id %q not live after catch-up; live=%v", want, got)
		}
	}
	if got["c"] {
		t.Fatalf("removed id c still live after catch-up; live=%v", got)
	}
}

// Test 2 (B2/S2): a SAME id whose vector changed must upsert (not be an empty
// delta). Mutant: a set-difference delta that only inserts ids absent from live
// → the changed id keeps its OLD vector and libraryAdds does not advance.
func TestCatchUpChangedVectorUpserts(t *testing.T) {
	e := newSelEngine(t, testVault(t))
	ids := []string{"a", "b", "c"}
	vecs := [][]float32{oneHotVec(384, 1), oneHotVec(384, 2), oneHotVec(384, 3)}
	h := catchupIdx(t, ids, vecs)
	before := h.libraryAdds.Load()

	// Same ids, but b's vector changed.
	newB := oneHotVec(384, 42)
	vecs2 := [][]float32{oneHotVec(384, 1), newB, oneHotVec(384, 3)}
	changed, err := e.catchUpGraph(h, idPositions(ids), ids, vecs2, emptyMetas(3))
	if err != nil {
		t.Fatal(err)
	}

	if !changed {
		t.Fatal("a changed vector was treated as no change (set-difference delta?)")
	}
	if delta := h.libraryAdds.Load() - before; delta != 1 {
		t.Fatalf("libraryAdds advanced by %d, want 1 (only b upserted; a and c no-op)", delta)
	}
	h.mu.RLock()
	gotB := h.st.live["b"].vec
	h.mu.RUnlock()
	if !equalVectors(gotB, newB) {
		t.Fatal("b still holds its old vector; the upsert was skipped (stale result)")
	}
}

// Test 3: an unchanged corpus is a true no-op (every Insert equal, no removed).
// Mutant: report changed even when nothing mutated (a needless save).
func TestCatchUpEmptyDeltaNoChange(t *testing.T) {
	e := newSelEngine(t, testVault(t))
	ids := []string{"a", "b", "c"}
	vecs := [][]float32{oneHotVec(384, 1), oneHotVec(384, 2), oneHotVec(384, 3)}
	h := catchupIdx(t, ids, vecs)
	before := h.libraryAdds.Load()

	changed, err := e.catchUpGraph(h, idPositions(ids), ids, vecs, emptyMetas(3))
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("an identical corpus reported a change")
	}
	if delta := h.libraryAdds.Load() - before; delta != 0 {
		t.Fatalf("libraryAdds advanced by %d on an identical corpus, want 0", delta)
	}
	if tombs := hnswTombs(h); tombs != 0 {
		t.Fatalf("tombstones = %d on an identical corpus, want 0", tombs)
	}
}

// --- 4. B1: a large-drift catch-up persists ---------------------------------

// A loaded graph of 80 synthetic ids whose corpus is now 3 real drawers →
// catch-up tombstones ~80 (over MinTombstones and the 0.20 ratio). The save must
// PERSIST (suppression keeps h.rebuild == nil), and the file re-loads WITHOUT the
// synthetic ids. Mutant: leave maybeRebuildLocked unsuppressed → a background
// rebuild starts → saveGraph returns skipStale → the file still carries them.
func TestLargeDriftCatchUpPersists(t *testing.T) {
	withThresholds(t, 1, 1)
	v := testVault(t)
	const project = "bigdrift"
	addDrawers(t, v, project, 3) // the real corpus

	// Pre-save a graph of 80 synthetic ids with the production fingerprint.
	synth := buildHNSW(t, 384, 80, provisionalHNSWParams)
	saveBuilt(t, v, project, synth)
	// Age the file so the catch-up save is not debounced.
	old := nowFn
	nowFn = func() time.Time { return time.Now().Add(graphSaveDebounce + time.Second) }
	t.Cleanup(func() { nowFn = old })

	e := newSelEngine(t, v)
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	data, err := indexstore.ReadGraph(v, project)
	if err != nil || data == nil {
		t.Fatalf("graph not persisted after a large-drift catch-up: data=%v err=%v (skipStale?)", data != nil, err)
	}
	idx, res, err := newHNSWIndexFromDisk(v, project, 384, provisionalHNSWParams)
	if err != nil || res != loaded {
		t.Fatalf("saved graph not re-loadable: res=%v err=%v", res, err)
	}
	for _, id := range idx.liveIDs() {
		if len(id) >= 3 && id[:3] == "id-" {
			t.Fatalf("a synthetic id %q survived in the persisted graph (stale save)", id)
		}
	}
	if idx.Len() != 3 {
		t.Fatalf("persisted graph Len = %d, want 3 (the real corpus)", idx.Len())
	}
}

// Test 4b (B1, deterministic): beginCatchUp suppresses maybeRebuildLocked, so a
// catch-up whose churn crosses the tombstone threshold starts NO background
// rebuild while it runs — which is what keeps h.rebuild == nil so the subsequent
// saveGraph does not skipStale. Mutant: drop the catchingUp guard in
// maybeRebuildLocked → a rebuild starts during catch-up (rebuildsStarted > 0).
func TestCatchUpSuppressesRebuild(t *testing.T) {
	e := newSelEngine(t, testVault(t))
	h := buildHNSW(t, 384, 80, provisionalHNSWParams) // 80 live
	ids := []string{"r1", "r2", "r3"}                 // corpus shrank to 3: ~80 tombstones
	vecs := [][]float32{oneHotVec(384, 1), oneHotVec(384, 2), oneHotVec(384, 3)}

	h.beginCatchUp()
	if _, err := e.catchUpGraph(h, idPositions(ids), ids, vecs, emptyMetas(3)); err != nil {
		t.Fatal(err)
	}
	if got := h.rebuildsStarted.Load(); got != 0 {
		t.Fatalf("a background rebuild started during catch-up (rebuildsStarted=%d); saveGraph would skipStale", got)
	}
	h.finishCatchUp() // re-arm; a post-save rebuild from here on is fine
}

// N-CR1: a failed catch-up Insert fails the rebuild — the project is NOT
// installed or remembered with a partial graph. Mutant: swallow the error and
// return changed → the rebuild succeeds and remembers a partial graph.
func TestCatchUpInsertErrorFailsRebuild(t *testing.T) {
	withThresholds(t, 1, 1)
	v := testVault(t)
	const project = "catchuperr"
	addDrawers(t, v, project, 3)
	e := newSelEngine(t, v)
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	// Drift so the next rebuild does a catch-up, and force its Insert to fail.
	addDrawer(t, v, project, "wing", "room", "a drift whose insert will fail", "facts")
	catchUpErrHook = func() error { return errors.New("injected catch-up insert failure") }
	t.Cleanup(func() { catchUpErrHook = nil })

	e2 := newSelEngine(t, v)
	if _, err := e2.Rebuild(context.Background(), project); err == nil {
		t.Fatal("a failed catch-up insert must fail the rebuild")
	}
	if installedIndex(t, e2, project) != nil {
		t.Fatal("a failed catch-up left an index installed")
	}
	e2.buildMu.Lock()
	_, remembered := e2.loaded[project]
	e2.buildMu.Unlock()
	if remembered {
		t.Fatal("a failed catch-up was remembered as current (e.loaded set)")
	}
}

// --- 5-7, 10. debounce ------------------------------------------------------

func graphMtime(t *testing.T, v *storage.Vault, project string) time.Time {
	t.Helper()
	mt, present, err := graphFileModTime(v, project)
	if err != nil || !present {
		t.Fatalf("graphFileModTime(%s) present=%v err=%v", project, present, err)
	}
	return mt
}

// Test 5 (N2): a save within the window is skipped — the file is not rewritten,
// the in-memory index stays current, and the skip holds the 7a shape (loaded gen
// unchanged, a second search does not rebuild). Mutant: remove the debounce
// check (the file is rewritten) / step the chain on skipDebounce.
func TestDebounceSkipsTooSoonSave(t *testing.T) {
	withThresholds(t, 1, 1)
	v := testVault(t)
	const project = "debskip"
	addDrawers(t, v, project, 3)
	e := newSelEngine(t, v)
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	mt0 := graphMtime(t, v, project)

	// Drift, then rebuild within the window (default nowFn ≈ real now).
	d4 := addDrawer(t, v, project, "wing", "room", "a fourth drawer for debounce", "facts")
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	if mt := graphMtime(t, v, project); !mt.Equal(mt0) {
		t.Fatalf("the within-window save was not debounced: mtime moved %v -> %v", mt0, mt)
	}
	// In-memory index is current with the drift despite the skipped save.
	h := installedIndex(t, e, project).(*hnswIndex)
	if !liveIDSet(h)[d4.ID] {
		t.Fatal("the in-memory index lost the drift across a debounced save")
	}
	// 7a shape for the skip path: current, and a second search does not rebuild.
	g, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		t.Fatal(err)
	}
	e.buildMu.Lock()
	lg, ok := e.loaded[project]
	e.buildMu.Unlock()
	if !ok || lg != g {
		t.Fatalf("debounced skip did not hold the chain: loaded=%v want %v", lg, g)
	}
	before := installedIndex(t, e, project)
	if _, err := e.Search(context.Background(), "drawer", SearchFilters{Project: project}); err != nil {
		t.Fatal(err)
	}
	if installedIndex(t, e, project) != before {
		t.Fatal("a second search rebuilt after a debounced save")
	}
}

// Test 6: past the window the save fires — the on-disk graph is rewritten and
// re-loads WITH the drifted id. Mutant: the window never elapses (always skip) →
// the on-disk graph stays stale (the drifted id is absent).
func TestDebounceFiresAfterWindow(t *testing.T) {
	withThresholds(t, 1, 1)
	v := testVault(t)
	const project = "debfire"
	addDrawers(t, v, project, 3)
	e := newSelEngine(t, v)
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	old := nowFn
	nowFn = func() time.Time { return time.Now().Add(graphSaveDebounce + time.Second) }
	t.Cleanup(func() { nowFn = old })

	d4 := addDrawer(t, v, project, "wing", "room", "a fourth drawer past the window", "facts")
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	idx, res, err := newHNSWIndexFromDisk(v, project, 384, provisionalHNSWParams)
	if err != nil || res != loaded {
		t.Fatalf("graph not re-loadable after the post-window save: res=%v err=%v", res, err)
	}
	if !liveIDSet(idx)[d4.ID] {
		t.Fatal("the post-window save did not fire: the on-disk graph is missing the drifted id")
	}
}

// Test 7: two engines on one vault do not both save within the window — the
// second reads the first's fresh mtime under the commit lock and skips. Mutant:
// no debounce → the second engine rewrites the file.
func TestDebounceCrossProcessNoDoubleSave(t *testing.T) {
	withThresholds(t, 1, 1)
	v := testVault(t)
	const project = "debxproc"
	addDrawers(t, v, project, 3)

	e1 := newSelEngine(t, v)
	if _, err := e1.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	mt0 := graphMtime(t, v, project)

	// A second process drifts and rebuilds within the window.
	addDrawer(t, v, project, "wing", "room", "cross-process fourth drawer", "facts")
	e2 := newSelEngine(t, v)
	if _, err := e2.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if mt := graphMtime(t, v, project); !mt.Equal(mt0) {
		t.Fatalf("engine2 double-saved within the window: mtime moved %v -> %v", mt0, mt)
	}
}

// Test 10: a debounced save loses nothing — a restart warm-starts the slightly
// stale file and catch-up reconciles it to the drifted corpus.
func TestSkippedSaveSurvivesRestart(t *testing.T) {
	withThresholds(t, 1, 1)
	v := testVault(t)
	const project = "debrestart"
	addDrawers(t, v, project, 3)
	e := newSelEngine(t, v)
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	// Drift + rebuild within the window → the save is debounced (stale file).
	d4 := addDrawer(t, v, project, "wing", "room", "drawer added before a restart", "facts")
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}

	// Restart: a fresh engine warm-starts the stale file and catches up.
	e2 := newSelEngine(t, v)
	if _, err := e2.Rebuild(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	h := installedIndex(t, e2, project).(*hnswIndex)
	if !liveIDSet(h)[d4.ID] {
		t.Fatal("the drifted id is absent after a restart; the skipped save lost it")
	}
}

// --- 8-9. R1: finishCatchUp (deferred) always clears the flag ---------------

// deleteStorm tombstones the first n live ids of h to cross MinTombstones and
// the 0.20 ratio, so maybeRebuildLocked would start a rebuild — if the catching-up
// flag has been cleared.
func deleteStorm(h *hnswIndex, n int) {
	live := h.liveIDs()
	for i := 0; i < n && i < len(live); i++ {
		h.Delete(live[i])
	}
}

// Test 8 (R1-a): after a drift catch-up that armed a save, the flag is cleared,
// so a later tombstone storm can start a background rebuild. Mutant: finishCatchUp
// does not clear the flag (or is not run) → catchingUp stays set → no rebuild.
func TestCatchUpClearsFlagThenRebuildStarts(t *testing.T) {
	withThresholds(t, 1, 1)
	v := testVault(t)
	const project = "r1a"
	addDrawers(t, v, project, 80)
	e := newSelEngine(t, v)
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	addDrawer(t, v, project, "wing", "room", "the drift drawer", "facts") // drift → catch-up saves
	e2 := newSelEngine(t, v)
	if _, err := e2.Rebuild(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	h := installedIndex(t, e2, project).(*hnswIndex)
	if catchingUpFlag(h) {
		t.Fatal("catchingUp still set after rebuild returned (finishCatchUp did not run/clear)")
	}
	started := h.rebuildsStarted.Load()
	deleteStorm(h, 64)
	if h.rebuildsStarted.Load() <= started {
		t.Fatal("no rebuild started after a tombstone storm; the catching-up flag was not cleared")
	}
}

// Test 9 (R1-b): a no-change catch-up takes commitGraphAction's early return
// (action graphNone, no save), and the DEFERRED finishCatchUp still clears the
// flag. Mutant: make finishCatchUp a conditional statement reached only when a
// save was armed → the no-change path leaks the flag → no later rebuild.
func TestCatchUpNoChangeStillClearsFlag(t *testing.T) {
	withThresholds(t, 1, 1)
	v := testVault(t)
	const project = "r1b"
	addDrawers(t, v, project, 80)
	e := newSelEngine(t, v)
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	// No drift: the catch-up is a pure no-op (changed=false, action graphNone).
	e2 := newSelEngine(t, v)
	if _, err := e2.Rebuild(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	h := installedIndex(t, e2, project).(*hnswIndex)
	if catchingUpFlag(h) {
		t.Fatal("catchingUp still set after a no-change catch-up (finishCatchUp was not deferred)")
	}
	if h.libraryAdds.Load() != 0 {
		t.Fatalf("a no-change catch-up did g.Adds: libraryAdds=%d, want 0", h.libraryAdds.Load())
	}
	started := h.rebuildsStarted.Load()
	deleteStorm(h, 64)
	if h.rebuildsStarted.Load() <= started {
		t.Fatal("no rebuild started; the no-change path leaked the catching-up flag")
	}
}
