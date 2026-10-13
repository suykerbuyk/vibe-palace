// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// --- Unit B selection / warm-start / off-e.mu test harness -------------------

// withThresholds lowers the selection band for the duration of one test so a
// small corpus can straddle it (the production constants are 3000/2700). It
// restores them on cleanup.
func withThresholds(t *testing.T, tHi, lLo int) {
	t.Helper()
	ot, ol := hnswSelectT, hnswSelectL
	hnswSelectT, hnswSelectL = tHi, lLo
	t.Cleanup(func() { hnswSelectT, hnswSelectL = ot, ol })
}

func selCfg() storage.Config { return storage.Config{SearchDefaultLimit: 10} }

// newSelEngine builds an engine on vault v with a deterministic mock embedder,
// so a second engine on the same vault reads the same cached vectors.
func newSelEngine(t *testing.T, v *storage.Vault) *Engine {
	t.Helper()
	e := NewEngine(embedder.NewMock(384), v, selCfg())
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func addDrawers(t *testing.T, v *storage.Vault, project string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		addDrawer(t, v, project, "wing", "room", fmt.Sprintf("drawer content number %d about topic alpha", i), "facts")
	}
}

func installedIndex(t *testing.T, e *Engine, project string) VectorIndex {
	t.Helper()
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.indexes[project]
}

func hasGraphFile(t *testing.T, v *storage.Vault, project string) bool {
	t.Helper()
	data, err := indexstore.ReadGraph(v, project)
	if err != nil {
		t.Fatalf("ReadGraph: %v", err)
	}
	return data != nil
}

// writeOrphanVec drops a .vec for an id not in the corpus and not live, which a
// later rebuild's reap removes (beginDestructive).
func writeOrphanVec(t *testing.T, v *storage.Vault, project string) {
	t.Helper()
	dir, err := v.EmbedCacheDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orphanbogusid.vec"), make([]byte, 384*4), 0o644); err != nil {
		t.Fatal(err)
	}
}

func oneHotVec(dims, seed int) []float32 {
	v := make([]float32, dims)
	v[seed%dims] = 1 // one-hot: usable (norm 1), distinct per seed
	return v
}

func hnswTombs(h *hnswIndex) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.st.g.Len() - len(h.st.live)
}

// zeroingEmbedder forces one sentinel content to an all-zeros (unusable) vector,
// so dedupBatch drops it and n (= len(keep)) is one below the distinct-id count.
type zeroingEmbedder struct {
	inner embedder.Embedder
	zero  string
}

func (z *zeroingEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == z.zero {
		d, _ := z.inner.Dimensions()
		return make([]float32, d), nil
	}
	return z.inner.Embed(ctx, text)
}

func (z *zeroingEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := z.Embed(ctx, t)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (z *zeroingEmbedder) Dimensions() (int, error) { return z.inner.Dimensions() }
func (z *zeroingEmbedder) Close() error             { return z.inner.Close() }

// --- 1. selectKind table (pure) ---------------------------------------------

// Test 1: the Scope-1 table. Mutants: (a) noGraph ignores converts (search-path
// conversion); (b) mismatch&n>=L -> HNSW+save (clobbers the heal file); (c) >=
// becomes > at T/L.
func TestSelectKindTable(t *testing.T) {
	withThresholds(t, 3000, 2700)
	cases := []struct {
		res      loadResult
		n        int
		converts bool
		wantKind indexKind
		wantAct  graphAction
	}{
		{loaded, 2700, false, kindHNSW, graphNone},
		{loaded, 2699, false, kindBrute, graphDelete},
		{mismatch, 2700, false, kindBrute, graphNone},
		{mismatch, 2699, false, kindBrute, graphDelete},
		{corrupt, 5000, false, kindBrute, graphDelete},
		{corrupt, 0, false, kindBrute, graphDelete},
		{noGraph, 3000, true, kindHNSW, graphSave},
		{noGraph, 3000, false, kindBrute, graphNone},
		{noGraph, 2999, true, kindBrute, graphNone},
	}
	for _, c := range cases {
		k, a := selectKind(c.res, c.n, c.converts)
		if k != c.wantKind || a != c.wantAct {
			t.Errorf("selectKind(%v, %d, converts=%v) = (%v, %v), want (%v, %v)",
				c.res, c.n, c.converts, k, a, c.wantKind, c.wantAct)
		}
	}
}

// Test 2: the constants match the task. Mutant: L=3000.
func TestSelectionConstantsMatchTask(t *testing.T) {
	if hnswSelectT != 3000 {
		t.Errorf("hnswSelectT = %d, want 3000", hnswSelectT)
	}
	if hnswSelectL != 2700 {
		t.Errorf("hnswSelectL = %d, want 2700", hnswSelectL)
	}
	if !(hnswSelectL < hnswSelectT) {
		t.Errorf("want L (%d) < T (%d) for a hysteresis band", hnswSelectL, hnswSelectT)
	}
}

// Test 3: n is the post-dedup keep-set length, not len(ids). One drawer embeds
// to a zero (unusable) vector, so keep = distinct-1. With T=4, keep=3 selects
// brute; the mutant n=len(ids)=4 would select HNSW.
func TestNIsKeepSetLength(t *testing.T) {
	withThresholds(t, 4, 1)
	v := testVault(t)
	const project = "nkeep"
	const sentinel = "ZERO-VECTOR-SENTINEL-CONTENT"
	addDrawer(t, v, project, "wing", "room", sentinel, "facts")
	for i := 0; i < 3; i++ {
		addDrawer(t, v, project, "wing", "room", fmt.Sprintf("usable content %d", i), "facts")
	}
	e := NewEngine(&zeroingEmbedder{inner: embedder.NewMock(384), zero: sentinel}, v, selCfg())
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, ok := installedIndex(t, e, project).(*bruteIndex); !ok {
		t.Fatalf("keep=3 < T=4 must select brute; got %T (n used len(ids)=4?)", installedIndex(t, e, project))
	}
}

// --- 4-12. engine selection / warm-start ------------------------------------

// Test 4 (B2): the lazy search path never converts a project with no graph.
// Mutant: selectKind ignores converts.
func TestSearchPathNoGraphStaysBrute(t *testing.T) {
	withThresholds(t, 2, 1)
	v := testVault(t)
	const project = "searchbrute"
	addDrawers(t, v, project, 3)
	e := newSelEngine(t, v)
	if _, err := e.Rebuild(context.Background(), project); err != nil { // modeSearch, converts=false
		t.Fatal(err)
	}
	if _, ok := installedIndex(t, e, project).(*bruteIndex); !ok {
		t.Fatalf("search path must not convert; got %T", installedIndex(t, e, project))
	}
	if hasGraphFile(t, v, project) {
		t.Fatal("search path wrote a graph file")
	}
}

// Test 5 (B2): the explicit rebuild above T builds HNSW and saves a re-loadable
// file. Mutant: skip graphSave.
func TestExplicitRebuildAboveTBuildsAndSaves(t *testing.T) {
	withThresholds(t, 2, 1)
	v := testVault(t)
	const project = "explicitsave"
	addDrawers(t, v, project, 3)
	e := newSelEngine(t, v)
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, ok := installedIndex(t, e, project).(*hnswIndex); !ok {
		t.Fatalf("explicit rebuild above T must build HNSW; got %T", installedIndex(t, e, project))
	}
	if !hasGraphFile(t, v, project) {
		t.Fatal("explicit rebuild above T did not save hnsw.idx")
	}
	idx, res, err := newHNSWIndexFromDisk(v, project, 384, provisionalHNSWParams)
	if err != nil || res != loaded || idx == nil {
		t.Fatalf("saved graph not re-loadable: res=%v err=%v", res, err)
	}
}

// Test 6: a warm start Loads (Imports) and performs no g.Add. Mutant: always
// Build.
func TestWarmStartLoadsSkipsBuild(t *testing.T) {
	withThresholds(t, 2, 1)
	v := testVault(t)
	const project = "warmskip"
	addDrawers(t, v, project, 3)
	e1 := newSelEngine(t, v)
	if _, err := e1.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if !hasGraphFile(t, v, project) {
		t.Fatal("precondition: the first rebuild saved no graph")
	}
	e2 := newSelEngine(t, v) // fresh engine, same vault
	if _, err := e2.Rebuild(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	h, ok := installedIndex(t, e2, project).(*hnswIndex)
	if !ok {
		t.Fatalf("warm start did not install HNSW; got %T", installedIndex(t, e2, project))
	}
	if got := h.libraryAdds.Load(); got != 0 {
		t.Fatalf("libraryAdds = %d after a warm start, want 0 (Load Imports, never Adds)", got)
	}
}

// Test 7 (B1/R1): warm start survives the save on a no-reap build (the next
// search does not rebuild), and a reaping build falls back to a rebuild. Mutant:
// the v2 shape breaks the gen chain (represented by chain.broken after the save)
// so case (a) also rebuilds.
func TestWarmStartSecondSearchDoesNotRebuild(t *testing.T) {
	withThresholds(t, 2, 1)

	t.Run("no_orphan_warm_start_holds", func(t *testing.T) {
		v := testVault(t)
		const project = "b1a"
		addDrawers(t, v, project, 3)
		e := newSelEngine(t, v)
		if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
			t.Fatal(err)
		}
		g, err := indexstore.ReadGeneration(v, project)
		if err != nil {
			t.Fatal(err)
		}
		e.buildMu.Lock()
		lg, ok := e.loaded[project]
		e.buildMu.Unlock()
		if !ok || lg != g {
			t.Fatalf("after build+save, loaded=%v (ok=%v) want ReadGeneration=%v — the chain broke (v2 reaping-Tx shape)", lg, ok, g)
		}
		before := installedIndex(t, e, project)
		if _, err := e.Search(context.Background(), "topic alpha", SearchFilters{Project: project}); err != nil {
			t.Fatal(err)
		}
		if installedIndex(t, e, project) != before {
			t.Fatal("the second search rebuilt the project (index replaced); warm start did not hold")
		}
	})

	t.Run("orphan_reap_falls_back_to_rebuild", func(t *testing.T) {
		v := testVault(t)
		const project = "b1b"
		addDrawers(t, v, project, 3)
		e := newSelEngine(t, v)
		if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
			t.Fatal(err)
		}
		writeOrphanVec(t, v, project) // the next rebuild's reap fires (beginDestructive)
		if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
			t.Fatal(err)
		}
		g, err := indexstore.ReadGeneration(v, project)
		if err != nil {
			t.Fatal(err)
		}
		e.buildMu.Lock()
		lg := e.loaded[project]
		e.buildMu.Unlock()
		if lg == g {
			t.Fatalf("a reaping rebuild breaks the chain, so the project is not left current: loaded=%v == gen=%v", lg, g)
		}
	})
}

// Test 8 (B2): a fingerprint mismatch serves brute and the file is left
// byte-identical for Unit E's heal. Mutant: treat mismatch as loaded and save.
func TestMismatchAboveLKeepsFileServesBrute(t *testing.T) {
	withThresholds(t, 2, 1)
	v := testVault(t)
	const project = "mismatch"
	addDrawers(t, v, project, 3)
	mm := buildHNSW(t, 384, 3, smallParams(50)) // M=8 != production M=32 -> fingerprint differs
	saved := saveBuilt(t, v, project, mm)
	e := newSelEngine(t, v)
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, ok := installedIndex(t, e, project).(*bruteIndex); !ok {
		t.Fatalf("a fingerprint mismatch must serve brute; got %T", installedIndex(t, e, project))
	}
	cur, err := indexstore.ReadGraph(v, project)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cur, saved) {
		t.Fatal("the mismatch file was modified; the Unit E heal must see the original bytes")
	}
}

// Test 9 (B2): a corrupt file is deleted, then the project is brute. Mutant: no
// corrupt-delete branch.
func TestCorruptFileDeletedThenBrute(t *testing.T) {
	withThresholds(t, 10, 2)
	v := testVault(t)
	const project = "corrupt"
	addDrawers(t, v, project, 3)
	writeGraphFile(t, v, project, []byte("this is not a valid hnsw envelope"))
	e := newSelEngine(t, v)
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, ok := installedIndex(t, e, project).(*bruteIndex); !ok {
		t.Fatalf("a corrupt file must fall to brute; got %T", installedIndex(t, e, project))
	}
	if hasGraphFile(t, v, project) {
		t.Fatal("the corrupt file was not deleted")
	}
}

// Test 10: a valid graph whose project is now below L is deleted and the index
// falls to brute. Mutant: drop the loaded&n<L delete.
func TestWarmStartBelowLDeletesFallsBrute(t *testing.T) {
	ot, ol := hnswSelectT, hnswSelectL
	t.Cleanup(func() { hnswSelectT, hnswSelectL = ot, ol })
	v := testVault(t)
	const project = "belowl"
	addDrawers(t, v, project, 3)
	e := newSelEngine(t, v)

	hnswSelectT, hnswSelectL = 1, 1 // n=3 >= T -> HNSW + save
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if !hasGraphFile(t, v, project) {
		t.Fatal("precondition: no HNSW graph saved")
	}

	hnswSelectT, hnswSelectL = 6, 5 // same corpus (3) is now below L=5
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if _, ok := installedIndex(t, e, project).(*bruteIndex); !ok {
		t.Fatalf("below L must fall to brute; got %T", installedIndex(t, e, project))
	}
	if hasGraphFile(t, v, project) {
		t.Fatal("the below-L graph file was not deleted")
	}
}

// Test 11 (S3): when the corpus id set drifts from the on-disk graph, the warm
// start declines and a fresh build serves the new id. Mutant: sameIDSet always
// true (serves the stale graph).
func TestWarmStartIDSetDriftRebuilds(t *testing.T) {
	withThresholds(t, 2, 1)
	v := testVault(t)
	const project = "drift"
	addDrawers(t, v, project, 3)
	e := newSelEngine(t, v)
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	d4 := addDrawer(t, v, project, "wing", "room", "a brand new fourth drawer content", "facts")

	e2 := newSelEngine(t, v)
	if _, err := e2.Rebuild(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	h, ok := installedIndex(t, e2, project).(*hnswIndex)
	if !ok {
		t.Fatalf("want HNSW; got %T", installedIndex(t, e2, project))
	}
	if h.Len() != 4 {
		t.Fatalf("index Len = %d, want 4 (rebuilt from the current corpus, not the stale 3-node graph)", h.Len())
	}
	found := false
	for _, id := range h.liveIDs() {
		if id == d4.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the new drawer %s is absent; the warm start served the stale graph", d4.ID)
	}
}

// Test 12 (B2): an I/O fault reading the file falls back to brute without
// failing the rebuild and without deleting. Mutant: return the error / delete.
func TestIOErrorFallsBackToBrute(t *testing.T) {
	withThresholds(t, 10, 2)
	v := testVault(t)
	const project = "ioerr"
	addDrawers(t, v, project, 3)
	gp := graphFilePath(t, v, project)
	if err := os.MkdirAll(gp, 0o755); err != nil { // a directory where the file should be -> ReadFile errors
		t.Fatal(err)
	}
	e := newSelEngine(t, v)
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatalf("an I/O fault must not fail the rebuild: %v", err)
	}
	if _, ok := installedIndex(t, e, project).(*bruteIndex); !ok {
		t.Fatalf("an I/O fault must fall to brute; got %T", installedIndex(t, e, project))
	}
	if fi, err := os.Stat(gp); err != nil || !fi.IsDir() {
		t.Fatalf("the I/O-fault path must not delete or modify the file: %v", err)
	}
}

// --- 13-15. off-e.mu capture publish ----------------------------------------

func warmHNSWProject(t *testing.T, project string) (*Engine, *storage.Vault, *hnswIndex) {
	t.Helper()
	v := testVault(t)
	addDrawers(t, v, project, 2)
	e := newSelEngine(t, v)
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	h, ok := installedIndex(t, e, project).(*hnswIndex)
	if !ok {
		t.Fatalf("precondition: want a warm *hnswIndex, got %T", installedIndex(t, e, project))
	}
	return e, v, h
}

// Test 13 (S3/B3): the capture insert runs with e.mu released. A hook inside
// Insert (under h.mu) can take e.mu.TryRLock. Mutant: wrap the insert under e.mu.
func TestCaptureInsertReleasesEMuAcrossAdd(t *testing.T) {
	withThresholds(t, 1, 1)
	e, _, _ := warmHNSWProject(t, "offmu")

	var ran, gotLock bool
	insertHook = func() {
		ran = true
		if e.mu.TryRLock() {
			gotLock = true
			e.mu.RUnlock()
		}
	}
	t.Cleanup(func() { insertHook = nil })

	d := storage.Drawer{ID: "newcap01", Content: "a fresh capture into the warm hnsw index", SourceType: "session", FiledAt: "2026-04-07T10:00:00Z"}
	if err := e.IndexDrawers(context.Background(), []DrawerInput{{Project: "offmu", Wing: "wing", Room: "room", Drawer: d, Vec: oneHotVec(384, 7)}}); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("the insert hook never fired; the capture path did not Insert")
	}
	if !gotLock {
		t.Fatal("e.mu was held across the g.Add; the Scope 3 insert is not off e.mu")
	}
}

// Test 14 (B3/S-CR1/S-CR2): a phase-3 collision — a cross-project publisher wins
// the global drawer-id key WHILE this capture is inserting (injected via the
// Insert hook, which runs off e.mu in phase 2) — must leave the just-inserted
// vector LIVE (search drops it as metadata-less) and must NOT tombstone it. This
// reaches phase-2 Insert under the collision (the id is absent from e.metadata at
// phase 1), so the "kept vector stays live" assertion has teeth on the VECTOR,
// not only the collision counter. Mutant: restore the phase-3 idx.Delete retract
// (tombstones this project's just-inserted vector).
func TestCaptureCollisionKeepsVector(t *testing.T) {
	withThresholds(t, 1, 1)
	e, _, h := warmHNSWProject(t, "collide")
	ctx := context.Background()
	const id = "collideid"
	v1 := oneHotVec(384, 11)

	// During phase-2 Insert (off e.mu), simulate a concurrent cross-project
	// publisher claiming the global key with DIFFERENT content.
	var once bool
	insertHook = func() {
		if once {
			return
		}
		once = true
		e.mu.Lock()
		e.metadata[id] = drawerMeta{Project: "other", Content: "content bar"}
		e.mu.Unlock()
	}
	t.Cleanup(func() { insertHook = nil })

	before := e.collisions.Load()
	d := storage.Drawer{ID: id, Content: "content foo", SourceType: "session", FiledAt: "t"}
	if err := e.IndexDrawers(ctx, []DrawerInput{{Project: "collide", Wing: "wing", Room: "room", Drawer: d, Vec: v1}}); err != nil {
		t.Fatal(err)
	}

	// The vector we inserted must still be live and unchanged (no tombstone).
	h.mu.RLock()
	ent, live := h.st.live[id]
	tombs := h.st.g.Len() - len(h.st.live)
	h.mu.RUnlock()
	if !live {
		t.Fatal("the phase-3 collision tombstoned the just-inserted vector (S-CR2: no idx.Delete retract)")
	}
	if !equalVectors(ent.vec, v1) {
		t.Fatal("the just-inserted vector was replaced, not kept")
	}
	if tombs != 0 {
		t.Fatalf("a tombstone was created, want 0 (no retract): %d", tombs)
	}
	// We did not overwrite the winner's metadata, and the collision was counted.
	e.mu.RLock()
	meta := e.metadata[id]
	e.mu.RUnlock()
	if meta.Content != "content bar" {
		t.Fatalf("metadata content = %q, want the cross-project winner's (bar) — we must not publish over it", meta.Content)
	}
	if e.collisions.Load() != before+1 {
		t.Fatalf("collisions = %d, want %d (one collision)", e.collisions.Load(), before+1)
	}
}

// Test 15 (B3): two batch rows with the same id are deduplicated by the
// reservation, so only one insert lands and no tombstone is created. Mutant:
// drop the reservation (the second insert tombstones the first key).
func TestCaptureSameIdNoDoubleInsert(t *testing.T) {
	withThresholds(t, 1, 1)
	e, _, h := warmHNSWProject(t, "dup")
	ctx := context.Background()
	const id = "dupid"
	d := storage.Drawer{ID: id, Content: "same content", SourceType: "session", FiledAt: "t"}
	tombBefore := hnswTombs(h)
	batch := []DrawerInput{
		{Project: "dup", Wing: "wing", Room: "room", Drawer: d, Vec: oneHotVec(384, 21)},
		{Project: "dup", Wing: "wing", Room: "room", Drawer: d, Vec: oneHotVec(384, 22)},
	}
	if err := e.IndexDrawers(ctx, batch); err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, lid := range h.liveIDs() {
		if lid == id {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("want the id live once, got %d", live)
	}
	if got := hnswTombs(h); got != tombBefore {
		t.Fatalf("the reservation did not prevent the second insert: tombstones %d -> %d", tombBefore, got)
	}
}

// B-CR1: commitGraphAction steps the chain on a genuinely-successful write, and
// a genuine write failure (skipWriteErr) neither steps the chain nor marks it
// broken (a failed write never bumps gen, so nothing may be remembered as
// current off it). The success sub carries the step-wiring teeth; the failure
// sub documents the guard.
func TestCommitGraphActionChainStep(t *testing.T) {
	withThresholds(t, 1, 1)
	v := testVault(t)
	const project = "cgstep"
	addDrawers(t, v, project, 1)
	e := newSelEngine(t, v)
	ctx := context.Background()

	// A saved graph exists to delete.
	if _, err := e.RebuildExplicit(ctx, project); err != nil {
		t.Fatal(err)
	}
	if !hasGraphFile(t, v, project) {
		t.Fatal("precondition: no graph to delete")
	}

	// Success: a graphDelete writes (removes the file) and steps the chain.
	g0, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := e.lockProject(ctx, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	chain := genChain{cur: g0}
	e.commitGraphAction(ctx, pl, indexstore.NoTimeout, project, nil, graphDelete, &chain)
	pl.release()
	if chain.cur == g0 {
		t.Fatal("a successful graph write did not step the chain")
	}
	if chain.broken {
		t.Fatal("a successful graph write marked the chain broken")
	}

	// Failure: a plain write error (skipWriteErr) does not step or break the
	// chain. Induced by making index/<p>/ unwritable; skipped when running as
	// root (chmod does not restrict root).
	if os.Geteuid() == 0 {
		return
	}
	if _, err := e.RebuildExplicit(ctx, project); err != nil {
		t.Fatal(err)
	}
	h, ok := installedIndex(t, e, project).(*hnswIndex)
	if !ok {
		t.Fatalf("precondition: want *hnswIndex, got %T", installedIndex(t, e, project))
	}
	dir, err := v.IndexDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	g1, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		t.Fatal(err)
	}
	pl2, err := e.lockProject(ctx, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	chain2 := genChain{cur: g1}
	e.commitGraphAction(ctx, pl2, indexstore.NoTimeout, project, h, graphSave, &chain2)
	pl2.release()
	if chain2.cur != g1 {
		t.Fatalf("a failed write stepped the chain: %+v -> %+v", g1, chain2.cur)
	}
	if chain2.broken {
		t.Fatal("a failed write (not a commit failure) marked the chain broken")
	}
}

// --- parity: brute is the oracle --------------------------------------------

// The selected HNSW index answers the same neighbours the brute oracle does,
// within a recall bar — never identical order (HNSW is approximate). Brute stays
// the oracle. Mutant: route selection to brute (the *hnswIndex assertion fails).
func TestHNSWSelectionBruteParity(t *testing.T) {
	withThresholds(t, 1, 1)
	v := testVault(t)
	const project = "parity"
	addDrawers(t, v, project, 30)
	e := newSelEngine(t, v)
	if _, err := e.RebuildExplicit(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	h, ok := installedIndex(t, e, project).(*hnswIndex)
	if !ok {
		t.Fatalf("parity precondition: the selected index must be *hnswIndex, got %T", installedIndex(t, e, project))
	}

	// Build the brute oracle from exactly the vectors the HNSW index holds.
	var oids []string
	var ovecs [][]float32
	h.mu.RLock()
	for id, ent := range h.st.live {
		oids = append(oids, id)
		ovecs = append(ovecs, ent.vec)
	}
	h.mu.RUnlock()
	oracle := newBruteIndex(384)
	if err := oracle.Build(ovecs, oids); err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(7))
	const k = 5
	hits, total := 0, 0
	for q := 0; q < 20; q++ {
		query := make([]float32, 384)
		for i := range query {
			query[i] = rng.Float32()*2 - 1
		}
		bres, err := oracle.Search(query, k)
		if err != nil {
			t.Fatal(err)
		}
		hres, err := h.Search(query, k)
		if err != nil {
			t.Fatal(err)
		}
		hset := map[string]bool{}
		for _, r := range hres {
			hset[r.ID] = true
		}
		for _, r := range bres {
			total++
			if hset[r.ID] {
				hits++
			}
		}
	}
	if recall := float64(hits) / float64(total); recall < 0.90 {
		t.Fatalf("HNSW recall@%d vs the brute oracle = %.3f, want >= 0.90", k, recall)
	}
}
