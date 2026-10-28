// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// --- Unit D: kind-swap dual-write conversion test harness --------------------
//
// Every test runs the converter SYNCHRONOUSLY on the test goroutine via
// convertSync, so a step-hook that injects a concurrent capture / rebuild /
// store commit produces a deterministic interleaving. Oracles assert on the
// PUBLISHED index's Search / liveIDs or on the graph file directly — never on a
// restarted engine, which would catch a loaded graph up and mask the bug (B5).

// convertSync marks project in-flight and runs one conversion to completion on
// the caller's goroutine (runConvert defers Done + clearConverting).
func convertSync(e *Engine, project string) {
	e.convertMu.Lock()
	e.converting[project] = struct{}{}
	e.convertMu.Unlock()
	e.convertWG.Add(1)
	e.runConvert(project)
}

// withConvertBounds lowers the converter's own catch-up bounds for one test and
// restores them. Distinct from the wrapper's catchUpRounds/finalReplayMax.
func withConvertBounds(t *testing.T, finalMax, rounds int) {
	t.Helper()
	of, or := convertFinalReplayMax, convertCatchUpRounds
	convertFinalReplayMax, convertCatchUpRounds = finalMax, rounds
	t.Cleanup(func() { convertFinalReplayMax, convertCatchUpRounds = of, or })
}

// clearConvertHooks nils every converter step-hook on cleanup.
func clearConvertHooks(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		convertAfterBuild = nil
		convertEachRound = nil
		convertBeforeFinal = nil
		convertBeforeGate = nil
		convertBeforeSwap = nil
		convertBeforeSave = nil
		convertAttempt = nil
	})
}

// seedBruteProject writes n tracked drawers and builds the project on the lazy
// (modeSearch, converts=false) path, so the installed index is a *bruteIndex of
// n ids — the converter's starting point. It returns the engine.
func seedBruteProject(t *testing.T, v *storage.Vault, e *Engine, project string, n int) {
	t.Helper()
	addDrawers(t, v, project, n)
	if _, err := e.Rebuild(context.Background(), project); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if _, ok := installedIndex(t, e, project).(*bruteIndex); !ok {
		t.Fatalf("setup: want *bruteIndex, got %T", installedIndex(t, e, project))
	}
}

// putCacheVec writes vec to project's embed cache under id, through a raw commit
// lock Tx (TestLockDiscipline ignores _test.go). It bumps the store generation.
func putCacheVec(t *testing.T, e *Engine, v *storage.Vault, project, id string, vec []float32) {
	t.Helper()
	tx, err := indexstore.Lock(context.Background(), v, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	w, err := e.cache.Writer(tx)
	if err != nil {
		t.Fatalf("Writer: %v", err)
	}
	if err := tx.PutVectors(w, map[string][]float32{id: vec}); err != nil {
		t.Fatalf("PutVectors: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

// captureDrawer inserts one drawer (id, content, vec) through the capture path,
// landing it in the live brute index, the embed cache and the metadata map.
func captureDrawer(t *testing.T, e *Engine, project, id, content string, vec []float32) {
	t.Helper()
	d := storage.Drawer{ID: id, Content: content, SourceType: "session", FiledAt: "2026-04-07T10:00:00Z"}
	in := DrawerInput{Project: project, Wing: "wing", Room: "room", Drawer: d, Vec: vec}
	if err := e.IndexDrawers(context.Background(), []DrawerInput{in}); err != nil {
		t.Fatalf("IndexDrawers(%s): %v", id, err)
	}
}

// publishedHNSW returns the installed index as *hnswIndex, failing if the swap
// did not happen.
func publishedHNSW(t *testing.T, e *Engine, project string) *hnswIndex {
	t.Helper()
	idx := installedIndex(t, e, project)
	h, ok := idx.(*hnswIndex)
	if !ok {
		t.Fatalf("want published *hnswIndex, got %T", idx)
	}
	return h
}

// nearestID returns the id of the single nearest neighbour of query in idx.
func nearestID(t *testing.T, idx VectorIndex, query []float32) string {
	t.Helper()
	res, err := idx.Search(query, 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("Search returned no results")
	}
	return res[0].ID
}

// idxHas reports whether idx returns id among the k nearest of query.
func idxHas(t *testing.T, idx VectorIndex, query []float32, id string, k int) bool {
	t.Helper()
	res, err := idx.Search(query, k)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, r := range res {
		if r.ID == id {
			return true
		}
	}
	return false
}

// --- T1: a mid-conversion capture is in the published graph; no write lost ---
//
// A capture of new drawer X lands in b0 during the off-lock window
// (convertBeforeFinal). convertBeforeSave then asserts, AFTER the swap and
// BEFORE the save (the crash point), that the PUBLISHED graph already holds X —
// so even a crash before the file is written loses no write. Revert that makes
// it FAIL: make convertReconcileFull/Bounded skip the catch-up (publish the
// Step-2 snapshot) — X is then absent and the gate abandons (no swap), or if the
// gate is also removed, convertBeforeSave sees X missing. Either way T1 fails.
func TestConvertT1MidConversionCaptureNotLost(t *testing.T) {
	withThresholds(t, 4, 2)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "t1"
	e := newSelEngine(t, v)
	seedBruteProject(t, v, e, project, 5)

	const xID = "convX01"
	xVec := oneHotVec(384, 50)
	convertBeforeFinal = func() {
		captureDrawer(t, e, project, xID, "x content", xVec)
	}
	var sawXInPublished bool
	var swappedBeforeSave bool
	convertBeforeSave = func() {
		// After the swap, before the save: the published graph must already hold X.
		idx := installedIndex(t, e, project)
		h, ok := idx.(*hnswIndex)
		if !ok {
			return
		}
		swappedBeforeSave = true
		sawXInPublished = idxHas(t, h, xVec, xID, 5)
	}

	convertSync(e, project)

	if !swappedBeforeSave {
		t.Fatal("convertBeforeSave never observed a published *hnswIndex: the swap did not happen (X was lost at the gate)")
	}
	if !sawXInPublished {
		t.Fatal("the published graph did not hold the mid-conversion capture X: a write was lost")
	}
	// And the final published index is HNSW holding X.
	if got := nearestID(t, publishedHNSW(t, e, project), xVec); got != xID {
		t.Fatalf("post-swap nearest of X's vector = %q, want %q", got, xID)
	}
}

// --- T2: a concurrent Insert during the build is in the post-switch HNSW -----
//
// X is inserted into b0 during Step 3 (convertAfterBuild). After the conversion
// the published HNSW must rank X first for its own vector. Revert: drop the
// Step-4/5 reconcile — X is absent and the gate abandons, so there is no
// published *hnswIndex and the test fails.
func TestConvertT2ConcurrentInsertInPostSwitch(t *testing.T) {
	withThresholds(t, 4, 2)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "t2"
	e := newSelEngine(t, v)
	seedBruteProject(t, v, e, project, 5)

	const xID = "convX02"
	xVec := oneHotVec(384, 51)
	convertAfterBuild = func() {
		captureDrawer(t, e, project, xID, "x2 content", xVec)
	}

	convertSync(e, project)

	h := publishedHNSW(t, e, project)
	if got := nearestID(t, h, xVec); got != xID {
		t.Fatalf("nearest of X's vector in published HNSW = %q, want %q (X not caught up)", got, xID)
	}
	// Note: this test drives the converter through convertAfterBuild, NOT the
	// runRebuild-only hooks (afterSnapshot / beforeCatchUpRound, which are
	// hnswParams fields) — those never fire on the converter path.
}

// --- T3: a Delete and an upsert racing the conversion ------------------------
//
// During the build, Y is upserted with a CHANGED vector and Z is deleted. The
// published HNSW must rank Y FIRST for Y's NEW vector (not merely contain Y —
// the stale vector still returns Y), and must never return Z. Reverts: (a) only
// Insert adds in the off-lock reconcile (skip the upsert) — Y keeps its old
// vector; (b) drop the removed-id Delete — Z reappears.
func TestConvertT3DeleteAndUpsertRace(t *testing.T) {
	withThresholds(t, 4, 2)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "t3"
	e := newSelEngine(t, v)
	addDrawers(t, v, project, 3)
	dY := addDrawer(t, v, project, "wing", "room", "yankee content to upsert", "facts")
	dZ := addDrawer(t, v, project, "wing", "room", "zulu content to delete", "facts")
	if _, err := e.Rebuild(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, ok := installedIndex(t, e, project).(*bruteIndex); !ok {
		t.Fatalf("setup: want brute, got %T", installedIndex(t, e, project))
	}

	yNew := oneHotVec(384, 60)
	convertAfterBuild = func() {
		// Upsert Y's vector in both the live brute index and the cache, so the
		// converter reads the NEW vector; delete Z from the brute index + metadata.
		b0 := installedIndex(t, e, project).(*bruteIndex)
		if err := b0.Insert(dY.ID, yNew); err != nil {
			t.Fatalf("b0.Insert(Y): %v", err)
		}
		putCacheVec(t, e, v, project, dY.ID, yNew)
		e.evictMemory(project, dZ.ID)
	}

	convertSync(e, project)

	// Assert directly on the published graph's stored state (not search ranking,
	// which one-hot ties make ambiguous): Y must hold the NEW vector exactly, and
	// Z must be gone.
	h := publishedHNSW(t, e, project)
	h.mu.RLock()
	yEnt, yLive := h.st.live[dY.ID]
	_, zLive := h.st.live[dZ.ID]
	h.mu.RUnlock()
	if !yLive {
		t.Fatal("upserted drawer Y is missing from the published HNSW")
	}
	if !equalVectors(yEnt.vec, yNew) {
		t.Fatalf("Y holds the stale vector in the published HNSW (upsert not applied)")
	}
	if zLive {
		t.Fatal("deleted drawer Z is still live in the published HNSW")
	}
}

// --- T4: the completeness gate blocks a poisoned switch ----------------------
//
// convertBeforeGate corrupts the detached graph (tombstones a want id) AFTER the
// reconcile and BEFORE the gate, simulating a reconcile that did not fully apply.
// The set-equality gate (h.liveIDs == b0's want) must then ABANDON: no swap, b0
// still served, no graph file. Revert: remove the gate — the incomplete graph is
// published and the test (which requires the brute index to still be installed)
// fails. This pins the gate as the child-16 proof over b0's set.
func TestConvertT4CompletenessGateBlocksPoisonedSwitch(t *testing.T) {
	withThresholds(t, 4, 2)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "t4"
	e := newSelEngine(t, v)
	seedBruteProject(t, v, e, project, 5)
	b0 := installedIndex(t, e, project).(*bruteIndex)

	convertBeforeGate = func(h *hnswIndex) {
		// Drop one live id so h no longer equals b0's want: a reconcile bug the gate
		// must catch.
		ids := h.liveIDs()
		if len(ids) == 0 {
			t.Fatal("h is empty before the gate")
		}
		h.Delete(ids[0])
	}

	convertSync(e, project)

	// The gate must have abandoned: the brute index is still installed, no file.
	if installedIndex(t, e, project) != b0 {
		t.Fatalf("gate did not abandon: installed index is %T, not the original b0", installedIndex(t, e, project))
	}
	if hasGraphFile(t, v, project) {
		t.Fatal("gate did not abandon: a graph file was written for an incomplete graph")
	}
}

// --- T5: abandon on outpaced writes, using the CONVERTER's own bound ---------
//
// convertEachRound lands more than convertFinalReplayMax fresh captures into b0
// every off-lock round. Because each round's want snapshot is read BEFORE that
// round's flood, the surplus surfaces at the under-mutex final reconcile, where
// the converter's own bound (convertFinalReplayMax in convertReconcileBounded)
// trips and the conversion ABANDONS rather than publish a graph more than the
// bound behind b0. Assert it abandoned (brute still served, no file, backoff
// recorded). Revert: delete the converter's final bound (the `> bound` check) —
// the conversion then reconciles unbounded and swaps, and the test fails. The
// wrapper's own catchUpRounds/finalReplayMax (runRebuild) are untouched here.
func TestConvertT5AbandonOnOutpacedWrites(t *testing.T) {
	withThresholds(t, 4, 2)
	withConvertBounds(t, 8, 3) // small bound + few rounds so the test is fast
	clearConvertHooks(t)
	v := testVault(t)
	const project = "t5"
	e := newSelEngine(t, v)
	seedBruteProject(t, v, e, project, 5)
	b0 := installedIndex(t, e, project).(*bruteIndex)

	seq := 0
	convertEachRound = func(round int) {
		// Land convertFinalReplayMax+1 fresh captures this round, so the set diff
		// to b0 stays above the bound and the converter never converges.
		for i := 0; i <= convertFinalReplayMax; i++ {
			seq++
			id := fmt.Sprintf("t5flood-%d", seq)
			captureDrawer(t, e, project, id, "flood "+id, oneHotVec(384, 100+seq%200))
		}
	}

	convertSync(e, project)

	if installedIndex(t, e, project) != b0 {
		t.Fatalf("converter did not abandon under outpacing writes: installed %T, not b0", installedIndex(t, e, project))
	}
	if hasGraphFile(t, v, project) {
		t.Fatal("converter wrote a graph file despite never converging")
	}
	e.convertMu.Lock()
	bo := e.convertBackoff[project]
	e.convertMu.Unlock()
	if bo.abandons == 0 {
		t.Fatal("an abandoned conversion recorded no backoff")
	}
}

// --- S1: a cache-miss abandon records the write-counted backoff --------------
//
// A want id whose cached .vec is missing makes convertReconcileFull return false.
// That return must call convertAbandon (record the backoff) so a persistently
// missing vector does not thrash a conversion on every search. Revert that makes
// it FAIL: drop the convertAbandon on the cache-miss return — no backoff recorded.
func TestConvertS1CacheMissAbandonBacksOff(t *testing.T) {
	withThresholds(t, 4, 2)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "s1"
	e := newSelEngine(t, v)
	seedBruteProject(t, v, e, project, 5)

	const xID = "s1missX"
	convertAfterBuild = func() {
		// Capture X (into b0, metadata and cache), then delete its .vec so the
		// reconcile hits a cache miss on a want id.
		captureDrawer(t, e, project, xID, "x content s1", oneHotVec(384, 70))
		dir, err := v.EmbedCacheDir(project)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, xID+".vec")); err != nil {
			t.Fatalf("remove X .vec: %v", err)
		}
	}

	convertSync(e, project)

	e.convertMu.Lock()
	bo := e.convertBackoff[project]
	e.convertMu.Unlock()
	if bo.abandons == 0 {
		t.Fatal("a cache-miss abandon recorded no backoff (next search would thrash)")
	}
	if hasGraphFile(t, v, project) {
		t.Fatal("a cache-miss abandon still wrote a graph file")
	}
}

// --- T6: a modeSearch brute rebuild during the window forces an abandon ------
//
// convertBeforeFinal runs a lazy rebuild that swaps b0 -> b1 (a new brute index,
// converts=false). The Step-5 identity check (e.indexes[project] == b0) must
// then abandon: the installed index stays b1, never the converter's h, and no
// file is written. Revert: drop the identity check — h is swapped over b1,
// losing b1's content, and the test (which requires a *bruteIndex installed)
// fails.
func TestConvertT6IdentityChangeAbandons(t *testing.T) {
	withThresholds(t, 4, 2)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "t6"
	e := newSelEngine(t, v)
	seedBruteProject(t, v, e, project, 5)
	b0 := installedIndex(t, e, project)

	convertBeforeFinal = func() {
		// A concurrent lazy rebuild (modeSearch) installs a NEW brute index.
		e.buildMu.Lock()
		delete(e.loaded, project) // force ensureIndex/Rebuild to actually rebuild
		e.buildMu.Unlock()
		if _, err := e.Rebuild(context.Background(), project); err != nil {
			t.Fatalf("concurrent rebuild: %v", err)
		}
	}

	convertSync(e, project)

	idx := installedIndex(t, e, project)
	if _, ok := idx.(*bruteIndex); !ok {
		t.Fatalf("identity change not honoured: installed %T, want a *bruteIndex (b1)", idx)
	}
	if idx == b0 {
		t.Fatal("expected the concurrent rebuild's b1, got the original b0")
	}
	if hasGraphFile(t, v, project) {
		t.Fatal("converter published a graph over a replaced index")
	}
}

// --- T7: the generation witness skips the file on a concurrent store commit --
//
// convertBeforeSave (after the swap, before the save) bumps the store generation
// with a raw commit. The save must then skip (tx.Generation() != builtGen), and
// e.loaded must be dropped DIRECTLY (never via remember), so the next search
// rebuilds; no file is written for this generation. Revert: drop the
// tx.Generation()==builtGen guard — saveGraph writes (the epoch is unchanged by a
// gen-only commit), a file appears, and the test fails.
func TestConvertT7GenWitnessSkipsFile(t *testing.T) {
	withThresholds(t, 4, 2)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "t7"
	e := newSelEngine(t, v)
	seedBruteProject(t, v, e, project, 5)

	convertBeforeSave = func() {
		// Another writer commits a gen-only vector write between builtGen and the
		// save: the epoch is unchanged, so only the converter's builtGen witness
		// can catch it.
		putCacheVec(t, e, v, project, "t7genbump", oneHotVec(384, 199))
	}

	convertSync(e, project)

	// The swap happened in memory...
	if _, ok := installedIndex(t, e, project).(*hnswIndex); !ok {
		t.Fatalf("expected the in-memory swap to *hnswIndex, got %T", installedIndex(t, e, project))
	}
	// ...but the file was NOT written at the stale generation...
	if hasGraphFile(t, v, project) {
		t.Fatal("gen-witness miss still wrote hnsw.idx: the save was not skipped")
	}
	// ...and e.loaded was dropped directly (not remembered as current).
	e.buildMu.Lock()
	_, remembered := e.loaded[project]
	e.buildMu.Unlock()
	if remembered {
		t.Fatal("a skipped save left the project remembered as current (should delete e.loaded directly)")
	}
}

// --- S3: the converter is gated by the constructor option --------------------
//
// Without WithBackgroundConverter the trigger is a no-op; with it, an eligible
// brute project is marked in-flight (and a goroutine launched). This proves the
// goroutine runs only in an engine that bootstrap() built, never in a CLI engine.
func TestConvertTriggerGatedByOption(t *testing.T) {
	withThresholds(t, 4, 2)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "gate"

	// Disabled engine: the trigger must not start a conversion.
	off := newSelEngine(t, v)
	if off.convertEnabled {
		t.Fatal("newSelEngine must not enable the converter")
	}
	seedBruteProject(t, v, off, project, 5)
	off.maybeStartConvert(project)
	off.convertMu.Lock()
	_, running := off.converting[project]
	off.convertMu.Unlock()
	if running {
		t.Fatal("a converter without WithBackgroundConverter started a conversion")
	}

	// Enabled engine: the option is set.
	on := NewEngine(embedder.NewMock(384), v, selCfg(), WithBackgroundConverter())
	t.Cleanup(func() { _ = on.Close() })
	if !on.convertEnabled {
		t.Fatal("WithBackgroundConverter did not enable the converter")
	}
}

// TestConvertTriggerRunsAndCloses exercises the REAL async path: a search on an
// enabled engine launches the converter goroutine, which converts the project to
// HNSW; and Close cancels/waits for it cleanly. (The T1-T7 tests drive runConvert
// synchronously for determinism; this one proves the trigger + goroutine +
// Close lifecycle.)
func TestConvertTriggerRunsAndCloses(t *testing.T) {
	withThresholds(t, 4, 2)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "trig"
	e := NewEngine(embedder.NewMock(384), v, selCfg(), WithBackgroundConverter())
	addDrawers(t, v, project, 5)
	if _, err := e.Rebuild(context.Background(), project); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// A search touching the brute-over-threshold project triggers the converter.
	_ = e.ensureIndex(context.Background(), project)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := installedIndex(t, e, project).(*hnswIndex); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the background converter did not run / did not publish an HNSW index")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// --- U3 §4.1: the converter holds the run lock, taken before any commit lock ---
//
// ADR-016 decision 3: runConvert takes the (host,vault) index run lock at the TOP,
// before the Step-2 lockProject, and holds it across the save's commit-lock Tx.
// This pins (a) no commit lock is acquired before the convert's RunAcquired — a
// REAL oracle, never "first event in the slice": ObserveCommitLocks is
// process-global and the lockProject sweep's ReapGoneProjects can emit
// CommitAcquired, so we assert on the ordering relative to RunAcquired — and
// (b) RunAcquired still holds when convertSave takes its Tx (driven by the
// convertBeforeSave hook). Deleting the acquire makes RunAcquired/RunReleased
// vanish and this test goes RED.
func TestConvertHoldsRunLockBeforeAnyCommitLock(t *testing.T) {
	withThresholds(t, 4, 2)
	withConvertBounds(t, 64, 8)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "u3own"
	e := newSelEngine(t, v)
	seedBruteProject(t, v, e, project, 5)

	// Record run- and commit-lock events in one ordered log, installed AFTER the
	// seed's Rebuild (which takes commit locks, no run lock) so only the convert's
	// events are captured.
	var mu sync.Mutex
	var log []string
	add := func(s string) { mu.Lock(); log = append(log, s); mu.Unlock() }
	restoreRun := indexstore.ObserveRunLocks(func(ev indexstore.RunLockEvent) { add("run:" + string(ev)) })
	defer restoreRun()
	restoreCommit := indexstore.ObserveCommitLocks(func(_ string, ev indexstore.CommitLockEvent) { add("commit:" + string(ev)) })
	defer restoreCommit()
	convertBeforeSave = func() { add("mark:beforeSave") }

	convertSync(e, project)

	mu.Lock()
	defer mu.Unlock()
	idxOf := func(tag string) int {
		for i, s := range log {
			if s == tag {
				return i
			}
		}
		return -1
	}
	try := idxOf("run:" + string(indexstore.RunTry))
	acq := idxOf("run:" + string(indexstore.RunAcquired))
	rel := idxOf("run:" + string(indexstore.RunReleased))
	if try < 0 || acq < 0 || rel < 0 {
		t.Fatalf("missing run-lock events (acquire not taken?): log=%v", log)
	}
	if !(try < acq && acq < rel) {
		t.Fatalf("run-lock events out of order: try=%d acq=%d rel=%d log=%v", try, acq, rel, log)
	}
	// (a) No commit lock is acquired before the run lock — the documented order.
	for i := 0; i < acq; i++ {
		if log[i] == "commit:"+string(indexstore.CommitAcquired) {
			t.Fatalf("a commit lock was acquired before the run lock (acquire taken too late): log=%v", log)
		}
	}
	// (b) The run lock still holds when convertSave is about to take its Tx: the
	// beforeSave mark is after RunAcquired and before RunReleased, and the save's
	// commit acquire falls in that window.
	mark := idxOf("mark:beforeSave")
	if !(acq < mark && mark < rel) {
		t.Fatalf("convertBeforeSave did not fall inside the run-lock hold: acq=%d mark=%d rel=%d log=%v", acq, mark, rel, log)
	}
	saveCommit := -1
	for i := mark; i < rel; i++ {
		if log[i] == "commit:"+string(indexstore.CommitAcquired) {
			saveCommit = i
			break
		}
	}
	if saveCommit < 0 {
		t.Fatalf("the save's commit lock was not taken while the run lock was held: log=%v", log)
	}
	if _, ok := installedIndex(t, e, project).(*hnswIndex); !ok {
		t.Fatalf("convert did not publish an HNSW index; log=%v", log)
	}
}

// --- U3 §4.2: a convert declines when the vault run lock is already held --------
//
// The PRIMARY oracle is "stays *bruteIndex" — the only observable a broken
// proceed would flip. The event-stream and inbox checks are TRIPWIRES (F4):
// RunLockEvent carries no caller identity, and the kernel forbids the test's own
// second same-process acquire, so the stream alone cannot distinguish a broken
// proceed. (The real decline+retry oracle is TestConvertDeclineReleasesThenRetries.)
func TestConvertDeclinesWhenRunLockHeld(t *testing.T) {
	withThresholds(t, 4, 2)
	withConvertBounds(t, 64, 8)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "u3decl"
	e := newSelEngine(t, v)
	seedBruteProject(t, v, e, project, 5)

	// A different run holds the one vault-wide run lock (project "" needs no index
	// dir; the lock file is the same regardless of the holder's named project).
	rl, ok, err := indexstore.TryRunLock(v, indexstore.KindRebuild, "")
	if err != nil || !ok {
		t.Fatalf("test could not pre-acquire the run lock: ok=%v err=%v", ok, err)
	}
	defer func() { _ = rl.Release() }()

	// Observe only the converter's events (installed AFTER the pre-acquire).
	var mu sync.Mutex
	var sawTry, sawAcq bool
	restore := indexstore.ObserveRunLocks(func(ev indexstore.RunLockEvent) {
		mu.Lock()
		switch ev {
		case indexstore.RunTry:
			sawTry = true
		case indexstore.RunAcquired:
			sawAcq = true
		}
		mu.Unlock()
	})
	defer restore()

	convertSync(e, project)

	// PRIMARY oracle.
	if _, isHNSW := installedIndex(t, e, project).(*hnswIndex); isHNSW {
		t.Fatal("the converter swapped to HNSW despite the run lock being held by another run")
	}
	// Tripwires.
	mu.Lock()
	gotTry, gotAcq := sawTry, sawAcq
	mu.Unlock()
	if !gotTry {
		t.Error("tripwire: the converter never tried the run lock")
	}
	if gotAcq {
		t.Error("tripwire: the converter acquired the run lock though it was held")
	}
	if wanted, werr := indexstore.ReadRebuildWanted(v, project); werr != nil {
		t.Fatalf("ReadRebuildWanted: %v", werr)
	} else if wanted {
		t.Error("tripwire: the decline wrote a rebuild-corpus marker (it must not — decision 3)")
	}
	// F1: the decline records NO write-counted backoff.
	e.convertMu.Lock()
	abandons := e.convertBackoff[project].abandons
	e.convertMu.Unlock()
	if abandons != 0 {
		t.Errorf("the decline recorded a backoff (abandons=%d); decision 3 is re-fire on the next trigger", abandons)
	}
}

// --- U3 §4.3: Close waits out an in-flight convert's Build; the run lock is free
//
//	afterward --------------------------------------------------------------
//
// U3 holds the run lock across h.Build (uncancellable here; threading ctx into
// Build is U5, out of scope). Close must therefore WAIT on convertWG while a
// converter is mid-build, and the run lock must be released by the time Close
// returns (held.Release runs before convertWG.Done, LIFO). This asserts the WAIT,
// not interruption.
func TestConvertCloseWaitsForBuildThenRunLockFree(t *testing.T) {
	withThresholds(t, 4, 2)
	withConvertBounds(t, 64, 8)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "u3close"
	e := NewEngine(embedder.NewMock(384), v, selCfg(), WithBackgroundConverter())

	// F8 precondition: maybeStartConvert is a DEFER in ensureIndex and no-ops
	// unless the project is an installed *bruteIndex over threshold. Build it so,
	// or the trigger silently no-ops and inBuild never arrives.
	seedBruteProject(t, v, e, project, 5)

	inBuild := make(chan struct{})
	release := make(chan struct{})
	var relOnce sync.Once
	closeRelease := func() { relOnce.Do(func() { close(release) }) }
	t.Cleanup(closeRelease) // always let a parked converter drain

	var buildOnce sync.Once
	convertAfterBuild = func() {
		buildOnce.Do(func() { close(inBuild) })
		<-release // park the converter while it holds the run lock
	}

	var closeOnce sync.Once
	var closeErr error
	closeEngine := func() { closeOnce.Do(func() { closeErr = e.Close() }) }
	t.Cleanup(closeEngine)

	// Trigger the async converter goroutine.
	_ = e.ensureIndex(context.Background(), project)

	select {
	case <-inBuild:
	case <-time.After(5 * time.Second):
		t.Fatal("the converter never reached convertAfterBuild: the trigger no-oped (not brute / over threshold?)")
	}

	// The run lock is held now: a concurrent try must fail.
	if rl, ok, _ := indexstore.TryRunLock(v, indexstore.KindRebuild, ""); ok {
		_ = rl.Release() // release the leaked hold before failing — a held lock poisons later run-lock tests
		t.Fatal("the run lock was NOT held while the converter was mid-build")
	}

	// Close must BLOCK on convertWG while the converter is parked.
	closed := make(chan struct{})
	go func() { closeEngine(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned while the converter was still inside Build")
	case <-time.After(200 * time.Millisecond):
	}

	closeRelease() // let the converter return
	select {
	case <-closed:
		if closeErr != nil {
			t.Fatalf("Close: %v", closeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the converter was released")
	}

	// After Close, the run lock must be free (held.Release ran before convertWG.Done).
	rl, ok, err := indexstore.TryRunLock(v, indexstore.KindRebuild, "")
	if err != nil {
		t.Fatalf("post-Close TryRunLock: %v", err)
	}
	if !ok {
		t.Fatal("the run lock outlived the converter (held.Release leaked)")
	}
	_ = rl.Release()
}

// --- U3 §4.4: a decline leaves NO durable suppression; the next trigger retries -
//
// F1 oracle. convertAbandon keys its write-counted backoff on THIS project's Gen,
// but the run lock is vault-wide — a convert of A that lost the lock to a build of
// B must NOT be suppressed until A is itself rewritten. So the decline records no
// backoff, and once the foreign holder is gone the next maybeStartConvert (the gate
// that consults the backoff) converts. Reverting the fix to convertAbandon-on-
// decline makes the abandons==0 assertion RED (and would gate out the retry since
// project's Gen never advanced).
func TestConvertDeclineReleasesThenRetries(t *testing.T) {
	withThresholds(t, 4, 2)
	withConvertBounds(t, 64, 8)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "u3retry"
	e := NewEngine(embedder.NewMock(384), v, selCfg(), WithBackgroundConverter())
	t.Cleanup(func() { _ = e.Close() })
	e.indexRunCooldown = 0 // isolate the decline behaviour from the cooldown gate
	seedBruteProject(t, v, e, project, 5)

	// Count every runConvert entry, so the holder-liveness bound (one re-fire per
	// holder, not one per search) is assertable.
	var attempts int64
	convertAttempt = func(string) { atomic.AddInt64(&attempts, 1) }

	// Hold the vault-wide run lock for a different run. Releasing it later does NOT
	// advance `project`'s Gen — the whole point of F1.
	rl, ok, err := indexstore.TryRunLock(v, indexstore.KindRebuild, "")
	if err != nil || !ok {
		t.Fatalf("pre-acquire: ok=%v err=%v", ok, err)
	}
	released := false
	defer func() {
		if !released {
			_ = rl.Release()
		}
	}()

	// Decline deterministically (synchronous): no swap, and F1's hard oracle — no
	// backoff recorded. This first decline is the ONE attempt that sets the mark.
	convertSync(e, project)
	if _, isHNSW := installedIndex(t, e, project).(*hnswIndex); isHNSW {
		t.Fatal("declined convert swapped anyway")
	}
	if got := atomic.LoadInt64(&attempts); got != 1 {
		t.Fatalf("first decline: attempts=%d, want 1", got)
	}
	e.convertMu.Lock()
	abandons := e.convertBackoff[project].abandons
	_, marked := e.convertDecline[project]
	e.convertMu.Unlock()
	if abandons != 0 {
		t.Fatalf("decline recorded a backoff (abandons=%d); with the vault-wide lock this suppresses the convert until THIS project's Gen advances, which a foreign release never does (decision 3 is re-fire on the next trigger)", abandons)
	}
	if !marked {
		t.Fatal("decline did not record a holder-liveness mark")
	}

	// PRE-RELEASE no-respawn: while the foreign holder is live and inside the
	// re-check interval, repeated triggers must NOT re-fire runConvert. Poll across
	// a window so a wrongly-spawned goroutine has time to tick the counter (a single
	// sample races the `go runConvert`).
	noRespawn := time.Now().Add(2 * time.Second)
	for time.Now().Before(noRespawn) {
		e.maybeStartConvert(project)
		if got := atomic.LoadInt64(&attempts); got != 1 {
			t.Fatalf("pre-release: runConvert re-fired while the holder was live (attempts=%d, want 1) — the decline mark did not bound the re-fire", got)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Foreign holder gone; project's Gen is UNCHANGED (nothing was written to it).
	if rerr := rl.Release(); rerr != nil {
		t.Fatalf("release foreign holder: %v", rerr)
	}
	released = true

	// The next NATURAL trigger (maybeStartConvert — the gate) must retry and now
	// convert: ReadHolder is ErrHolderUnknown (record gone), the fast path clears
	// the mark and re-fires even inside the interval.
	e.maybeStartConvert(project)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, isHNSW := installedIndex(t, e, project).(*hnswIndex); isHNSW {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("convert did not retry after the foreign holder released (decline left durable suppression?)")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&attempts); got != 2 {
		t.Fatalf("after release: attempts=%d, want exactly 2 (one decline + one successful re-fire)", got)
	}
}

// TestConvertDeclineDeadHolderRefires pins the dead-pid fast path: a decline mark
// naming a holder whose pid is dead (a crash leftover — flock gone, stale record)
// must clear and re-fire at once, inside the interval. The planted record is a
// PARSEABLE Holder (pid>0, kind set) so ReadHolder returns it (a Kind-less file is
// ErrHolderUnknown and would prove the wrong row); the pid is one the test killed
// and reaped, never a magic number (which can flake red on a shared host).
func TestConvertDeclineDeadHolderRefires(t *testing.T) {
	withThresholds(t, 4, 2)
	withConvertBounds(t, 64, 8)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "u3dead"
	e := NewEngine(embedder.NewMock(384), v, selCfg(), WithBackgroundConverter())
	t.Cleanup(func() { _ = e.Close() })
	e.indexRunCooldown = 0
	seedBruteProject(t, v, e, project, 5)

	// A pid the test started, killed, and REAPED: provably dead.
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn a helper process: %v", err)
	}
	deadPID := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_ = cmd.Wait() // reap, so the pid is not merely signalled but gone
	if pidSignalAlive(deadPID) {
		t.Skipf("killed pid %d still reports alive (reused?)", deadPID)
	}

	// Plant a parseable, OS-lock-free stale holder record naming the dead pid.
	if err := os.MkdirAll(v.IndexLocksDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rec, err := json.Marshal(indexstore.Holder{PID: deadPID, Kind: indexstore.KindRebuild, StartTime: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v.IndexRunHolderPath(), append(rec, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	// Police the plant: it must parse as a valid Holder (pid+kind), so this test
	// exercises the dead-pid row and not the ErrHolderUnknown row. A future edit
	// that drops Kind would otherwise false-green through ErrHolderUnknown.
	if h, herr := indexstore.ReadHolder(v); herr != nil || h.PID != deadPID || h.Kind != indexstore.KindRebuild {
		t.Fatalf("planted holder did not parse as a valid record (ReadHolder=%+v, err=%v) — the dead-pid branch is not actually exercised", h, herr)
	}

	// Arm a decline mark against the dead holder, lastTry = now so ONLY the
	// dead-holder fast path (not the interval backstop) can clear it.
	e.convertMu.Lock()
	e.convertDecline[project] = declineMark{pid: deadPID, lastTry: time.Now()}
	e.convertMu.Unlock()

	var attempts int64
	convertAttempt = func(string) { atomic.AddInt64(&attempts, 1) }

	e.maybeStartConvert(project)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, isHNSW := installedIndex(t, e, project).(*hnswIndex); isHNSW {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("convert did not re-fire against a dead holder (attempts=%d)", atomic.LoadInt64(&attempts))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&attempts); got != 1 {
		t.Fatalf("dead-holder re-fire: attempts=%d, want 1", got)
	}
}

// TestConvertDeclineIntervalRefiresWhileLive pins the operator-decided bounded
// backstop: while the marked holder is genuinely live, the convert still re-fires
// ONE attempt once declineRecheckInterval has elapsed (so a reused pid or an
// unreadable record cannot suppress forever), then re-arms — the next immediate
// trigger does NOT re-fire. The live holder is this process (TryRunLock writes
// os.Getpid()); the elapsed interval is injected via lastTry.
func TestConvertDeclineIntervalRefiresWhileLive(t *testing.T) {
	withThresholds(t, 4, 2)
	withConvertBounds(t, 64, 8)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "u3interval"
	e := NewEngine(embedder.NewMock(384), v, selCfg(), WithBackgroundConverter())
	t.Cleanup(func() { _ = e.Close() })
	e.indexRunCooldown = 0
	seedBruteProject(t, v, e, project, 5)

	var attempts int64
	convertAttempt = func(string) { atomic.AddInt64(&attempts, 1) }

	// A LIVE foreign holder (this process holds the vault lock).
	rl, ok, err := indexstore.TryRunLock(v, indexstore.KindRebuild, "")
	if err != nil || !ok {
		t.Fatalf("pre-acquire: ok=%v err=%v", ok, err)
	}
	defer func() { _ = rl.Release() }()

	// Arm a mark whose interval has ELAPSED, against the live self-holder.
	e.convertMu.Lock()
	e.convertDecline[project] = declineMark{pid: os.Getpid(), lastTry: time.Now().Add(-2 * declineRecheckInterval)}
	e.convertMu.Unlock()

	// Interval elapsed ⇒ exactly one re-fire even though the holder is live.
	e.maybeStartConvert(project)
	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&attempts) < 1 {
		if time.Now().After(deadline) {
			t.Fatal("interval backstop did not re-fire against a still-live holder")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Wait for the goroutine to finish (converting cleared ⇒ it re-declined and
	// re-marked with a fresh lastTry).
	for {
		e.convertMu.Lock()
		_, running := e.converting[project]
		e.convertMu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("convert goroutine did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, isHNSW := installedIndex(t, e, project).(*hnswIndex); isHNSW {
		t.Fatal("convert swapped while the lock was held by a live holder")
	}

	// Re-armed: a fresh mark (lastTry ≈ now) + live holder ⇒ skip, no new attempt.
	stay := time.Now().Add(2 * time.Second)
	for time.Now().Before(stay) {
		e.maybeStartConvert(project)
		if got := atomic.LoadInt64(&attempts); got != 1 {
			t.Fatalf("interval re-arm failed: a fresh mark re-fired while the holder was live (attempts=%d, want 1)", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// --- U3 §4.4 (F2): the converter owns a KindRebuild holder record during the hold
//
// ADR-016 decision 6: a convert runs as KindRebuild, so TryRunLock writes a
// KindRebuild holder record on acquire. This test pins that KIND (and the holder's
// project) during the hold, so a telemetry/liveness reader sees a rebuild-kind run,
// never a lifecycle one. It is NOT an ordering oracle: the sweep's runLockHeld probe
// emits RunTry only when a KindLifecycle holder AND a rename-pending record already
// exist (lifecycle.go:314,:198) — neither of which this test plants — and TryRunLock
// overwrites the holder to KindRebuild regardless of where the acquire sits, so this
// test cannot catch a move of the acquire below lockProject. §4.1
// TestConvertHoldsRunLockBeforeAnyCommitLock is the ordering oracle (no commit lock
// acquired before RunAcquired); do NOT delete it believing this test covers it.
func TestConvertHolderRecordIsRebuildKindDuringHold(t *testing.T) {
	withThresholds(t, 4, 2)
	withConvertBounds(t, 64, 8)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "u3holder"
	e := newSelEngine(t, v)
	seedBruteProject(t, v, e, project, 5)

	var mu sync.Mutex
	var firstEvent indexstore.RunLockEvent
	var seen int
	restore := indexstore.ObserveRunLocks(func(ev indexstore.RunLockEvent) {
		mu.Lock()
		if seen == 0 {
			firstEvent = ev
		}
		seen++
		mu.Unlock()
	})
	defer restore()

	var holderKind indexstore.RunKind
	var holderProject string
	var holderErr error
	convertAfterBuild = func() {
		h, herr := indexstore.ReadHolder(v)
		holderKind, holderProject, holderErr = h.Kind, h.Project, herr
	}

	convertSync(e, project)

	if holderErr != nil {
		t.Fatalf("ReadHolder during the convert: %v", holderErr)
	}
	if holderKind != indexstore.KindRebuild {
		t.Fatalf("holder kind during the convert = %q, want %q (the converter must own a rebuild-kind holder record, never read as a lifecycle run)", holderKind, indexstore.KindRebuild)
	}
	if holderProject != project {
		t.Fatalf("holder project during the convert = %q, want %q", holderProject, project)
	}
	mu.Lock()
	fe := firstEvent
	mu.Unlock()
	if fe != indexstore.RunTry {
		t.Fatalf("first run-lock event = %q, want %q (on a quiet vault the converter's own RunTry is first; a sanity check, NOT an ordering guard — §4.1 is the ordering oracle)", fe, indexstore.RunTry)
	}
}
