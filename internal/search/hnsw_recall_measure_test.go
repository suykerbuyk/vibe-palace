// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The HNSW recall measurements of task
// hnsw-parameters-from-real-vector-recall-and-production-wiring. Every test here
// builds 10k vectors or more, so each is behind requireHNSWMeasure
// (VP_HNSW_MEASURE=1) and runs in its own make target, never under -race:
//   - make hnsw-measure: TestHNSWClusteredChurnRecall50k, TestHNSWHarnessSlice10k
//     (also the nightly .github/workflows/hnsw-measure.yml);
//   - make hnsw-measure-real: TestHNSWRealVectorRecall;
//   - make hnsw-measure-grid: TestHNSWMeasureGrid.
//
// The real-vector test and the grid also skip unless VP_HNSW_REAL_CACHE is set
// (and, for the grid, VP_HNSW_GRID_OUT); once it is, a missing input fails. So
// a bare `VP_HNSW_MEASURE=1 go test ./internal/search/` runs the 50k test, the
// 10k slice and TestHNSWTombstoneMeasurement, under go test's default
// 10-minute timeout, which a 50k build can exceed: use the make targets.

// clusteredChurnRecallBar50k is the tie-aware recall bar, at k = 10 and at
// k = 30, of TestHNSWClusteredChurnRecall50k. It was derived at the former
// (16, 100) as the lowest of 5 recorded runs (0.9498 at k = 30; k = 10 ranged
// 0.9514-0.9520) minus churnFloorMargin, rounded down. The test now builds at
// the chosen (32, 100) (provisionalHNSWParams); 0.92 stays as a CONSERVATIVE
// floor that a higher M clears with more margin — it was not re-tightened,
// because re-deriving it needs a fresh 5-run synthetic measurement at (32, 100)
// (gated, out of the re-baseline commit's scope), and a loose synthetic floor is
// safe (the real-vector bar is realVectorRecallBar). A build is not reproducible
// (the library ranges over Go maps), so the bar is a repeated-run minimum, never
// one run's value.
const clusteredChurnRecallBar50k = 0.92

// realVectorRecallBar is the recall@10 bar on real vectors (ADR-014): 0.90 at
// both k. The operator sets it otherwise if no measured cell reaches it.
const realVectorRecallBar = 0.90

// recallTierN and recallQueriesLarge tier the post-churn recall query count
// DOWN at large n, where the τ ground truth measureRecall computes per query is
// O(queries×n) and dominates the cell. Below recallTierN the full held-out set
// is used (cheap); at or above it, recallQueriesLarge queries. At N=400 and a
// measured recall ≈0.95 the standard error is sqrt(p(1-p)/N) ≈ 0.011, far inside
// the ~0.05 margin to the 0.90 bar, so the pass/fail decision stays unambiguous.
const (
	recallTierN        = 25000
	recallQueriesLarge = 400
)

// replacedProbeSample is how many replaced ids the old-vector probe searches.
const replacedProbeSample = 200

// deletedProbeSample is how many deleted ids the own-vector probe searches.
const deletedProbeSample = 200

// assertChurnedRecall is the shared churn assertion: tie-aware recall at both k
// against cr's live set is at or above bar; no result names a deleted id or
// answers with a vector its id no longer holds (tieAwareRecall's checks); and
// no replaced id is found at its old vector.
func assertChurnedRecall(t *testing.T, idx VectorIndex, cr churnResult, queries [][]float32, bar float64, probeSeed int64) {
	t.Helper()
	if idx.Len() != len(cr.live) {
		t.Errorf("Len = %d, want the live count %d", idx.Len(), len(cr.live))
	}
	live := newLiveSet(cr.live)
	oracle := newBruteIndex(embeddingDims)
	for id, v := range cr.live {
		if err := oracle.Insert(id, v); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := measureRecall(idx, oracle, live, queries)
	if err != nil {
		t.Fatalf("after churn: %v", err)
	}
	logRecallReport(t, "after churn", rep)
	for i, k := range recallKs {
		if rep.Recall[i] < bar {
			t.Errorf("tie-aware recall@%d after churn = %.4f, below the bar %.4f", k, rep.Recall[i], bar)
		}
	}
	probed, err := checkReplacedVectorsGone(idx, cr, probeSeed, replacedProbeSample, candidateCount(10))
	switch {
	case err != nil:
		t.Errorf("old-vector probe, after %d searches: %v", probed, err)
	case probed == 0:
		t.Error("the old-vector probe searched nothing: churn recorded no replaced vector")
	default:
		t.Logf("old-vector probe: %d searches with replaced vectors, none answered by an old vector", probed)
	}
	// The real-path must-fail for the tombstone filter: a deleted id's own
	// vector is distance 0 from its dead key, so removing the filter in
	// hnswIndex.Search returns a non-live id here on any corpus, synthetic or
	// real, without depending on the dead key ranking into a held-out query.
	deletedProbed, err := checkDeletedVectorsGone(idx, cr, probeSeed+1, deletedProbeSample, candidateCount(10))
	switch {
	case err != nil:
		t.Errorf("deleted-id probe, after %d searches: %v", deletedProbed, err)
	case deletedProbed == 0:
		t.Error("the deleted-id probe searched nothing: churn recorded no net-deleted vector")
	default:
		t.Logf("deleted-id probe: %d searches with deleted ids' own vectors, none answered by a tombstone", deletedProbed)
	}
}

// logRecallReport logs one recallReport.
func logRecallReport(t *testing.T, label string, rep recallReport) {
	t.Helper()
	for i, k := range recallKs {
		t.Logf("%s: k=%d tie-aware recall %.4f; returned min %d p1 %d p50 %d, short %.3f; HNSW p50 %.2f ms p99 %.2f ms",
			label, k, rep.Recall[i], rep.Returned[i].Min, rep.Returned[i].P1, rep.Returned[i].P50,
			rep.Returned[i].ShortFrac, rep.Latency[i][0], rep.Latency[i][1])
	}
	t.Logf("%s: set-overlap recall@10 %.4f (information only)", label, rep.SetOverlap)
}

// TestHNSWClusteredChurnRecall50k (row 2; VP_HNSW_MEASURE=1, make
// hnsw-measure): a seeded clustered 50k corpus at the provisional parameters,
// with tombstone rebuilds pinned off so the churn measures a graph that really
// holds its tombstones, churned by the shared churn helper. Tie-aware recall at
// k = 10 and 30 must meet clusteredChurnRecallBar50k; no deleted id and no old
// vector may answer. Claimed breaks: the tombstone filter removed, and
// EfSearch left at 20, which falls well under the bar here (0.78-0.81 at
// k = 10 and 0.87-0.90 at k = 30 over three runs) although at 5k the
// clustered gap is under the margin.
func TestHNSWClusteredChurnRecall50k(t *testing.T) {
	requireHNSWMeasure(t)
	const n, nq = 50000, 500
	start := time.Now()
	gen := newStandardClusteredGen(50000)
	idx := newHNSWIndex(embeddingDims, noRebuild(provisionalHNSWParams))
	defer idx.Close()
	cr := churn(t, idx, rand.New(rand.NewSource(50001)), gen.take(n), gen.next)
	t.Logf("n=%d built and churned in %.1fs at (M, EfSearch) = (%d, %d)", n, time.Since(start).Seconds(),
		provisionalHNSWParams.M, provisionalHNSWParams.EfSearch)
	assertChurnedRecall(t, idx, cr, gen.take(nq), clusteredChurnRecallBar50k, 50002)
	t.Logf("wall %.1fs", time.Since(start).Seconds())
}

// TestHNSWHarnessSlice10k (row 5; VP_HNSW_MEASURE=1, make hnsw-measure): a
// generated 10k cache tree plus a copy of the committed decoys goes through the
// real loader, the held-out partition and both recall measures. The decoys are
// skipped and counted, and no query is in the index: a query id in any result
// fails tieAwareRecall as a non-live id.
func TestHNSWHarnessSlice10k(t *testing.T) {
	requireHNSWMeasure(t)
	root := t.TempDir()
	copyTree(t, decoyRoot, root)
	gen := newStandardClusteredGen(10000)
	for p, count := range map[string]int{"proj-g1": 6000, "proj-g2": 4000} {
		vecs := make(map[string][]float32, count)
		for i := range count {
			vecs[fmt.Sprintf("%08x", i)] = gen.next()
		}
		writeCacheProject(t, root, p, decoyFingerprint, vecs)
	}
	corpus, err := loadRealCache(os.DirFS(root), []string{"proj-a", "proj-b", "proj-g1", "proj-g2", "proj-n", "proj-w", "proj-x"}, decoyFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	logRealCacheReport(t, corpus.Report)
	if a := corpus.Report.Projects["proj-a"]; a.Skips[skipNotVec] != 1 || a.Skips[skipShort] != 1 || a.Skips[skipLong] != 1 {
		t.Errorf("proj-a skips %v: the decoys were not each skipped and counted", a.Skips)
	}
	if w := corpus.Report.Projects["proj-w"]; !w.SidecarMismatch || w.Loaded != 0 {
		t.Errorf("proj-w %+v: the mismatched sidecar was accepted", *w)
	}
	if n := corpus.Report.Projects["proj-n"]; !n.SidecarMismatch || n.Loaded != 0 {
		t.Errorf("proj-n %+v: a project with no sidecar was accepted", *n)
	}
	if got, want := len(corpus.IDs), 10000+3; got != want {
		t.Fatalf("loaded %d vectors, want %d (10,000 generated and 3 valid decoys)", got, want)
	}
	nq := heldOutQueryCount(len(corpus.IDs))
	ho := partitionHeldOut(corpus.IDs, corpus.Vecs, nq, 10)
	if err := checkHeldOutVectors(ho, zipLive(corpus.IDs, corpus.Vecs)); err != nil {
		t.Fatal(err)
	}
	idx := newHNSWIndex(embeddingDims, noRebuild(provisionalHNSWParams))
	defer idx.Close()
	if err := idx.Build(context.Background(), ho.Pool, ho.PoolIDs); err != nil {
		t.Fatal(err)
	}
	if idx.Len() != len(ho.PoolIDs) || tombstoneCount(idx) != 0 {
		t.Errorf("Len %d with %d tombstones, want %d and 0: queries must stay out of the index", idx.Len(), tombstoneCount(idx), len(ho.PoolIDs))
	}
	live := newLiveSet(zipLive(ho.PoolIDs, ho.Pool))
	oracle := newBruteIndex(embeddingDims)
	if err := oracle.Build(context.Background(), ho.Pool, ho.PoolIDs); err != nil {
		t.Fatal(err)
	}
	rep, err := measureRecall(idx, oracle, live, ho.Queries)
	if err != nil {
		t.Fatalf("held-out queries: %v", err)
	}
	logRecallReport(t, fmt.Sprintf("10k slice, %d held-out queries", nq), rep)
}

// zipLive maps ids to vectors.
func zipLive(ids []string, vecs [][]float32) map[string][]float32 {
	m := make(map[string][]float32, len(ids))
	for i, id := range ids {
		m[id] = vecs[i]
	}
	return m
}

// copyTree copies the regular files under src into dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Seeds of the real-vector run: the held-out permutation and the churn.
const (
	realPartitionSeed = 73531
	realChurnSeed     = 73532
)

// loadRealHeldOut loads the allow-listed union, logs its report and partitions
// it. It fails, naming the shortfall, when the union cannot give a 50k corpus
// after its queries and churn's fresh vectors.
func loadRealHeldOut(t *testing.T, in realInputs) (realCorpus, heldOut, int) {
	t.Helper()
	corpus, err := loadRealCache(os.DirFS(in.CacheDir), in.Allow, in.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	logRealCacheReport(t, corpus.Report)
	nq := heldOutQueryCount(len(corpus.IDs))
	ho := partitionHeldOut(corpus.IDs, corpus.Vecs, nq, realPartitionSeed)
	// The run that decides the numbers checks the loader paired each id with its
	// own vector (the 10k slice makes the same check); a mis-pairing would let a
	// query find itself under another id and inflate recall.
	if err := checkHeldOutVectors(ho, zipLive(corpus.IDs, corpus.Vecs)); err != nil {
		t.Fatal(err)
	}
	n := churnCorpusSize(len(ho.Pool))
	t.Logf("union %d vectors: %d held-out queries, corpus %d, %d fresh vectors reserved for churn", len(corpus.IDs), nq, n, len(ho.Pool)-n)
	if n < 50000 {
		t.Fatalf("the union gives a %d-vector corpus after %d queries and churn's %d fresh vectors; the measurement needs at least 50,000 (short by %d): stop and report, do not rebuild",
			n, nq, churnFreshCount(n), 50000-n)
	}
	return corpus, ho, n
}

// TestHNSWRealVectorRecall (row 3; make hnsw-measure-real, the operator): the
// row-2 assertions on the real allow-listed union against realVectorRecallBar,
// with churn's fresh vectors drawn from the union's reserve. Skips unless
// VP_HNSW_REAL_CACHE is set; once it is, a missing input fails.
func TestHNSWRealVectorRecall(t *testing.T) {
	requireHNSWMeasure(t)
	in := requireRealInputs(t, false)
	start := time.Now()
	_, ho, n := loadRealHeldOut(t, in)
	idx := newHNSWIndex(embeddingDims, noRebuild(provisionalHNSWParams))
	defer idx.Close()
	cr := churn(t, idx, rand.New(rand.NewSource(realChurnSeed)), ho.Pool[:n], reserveDrawer(t, ho.Pool[n:]))
	t.Logf("n=%d built and churned in %.1fs at (M, EfSearch) = (%d, %d)", n, time.Since(start).Seconds(),
		provisionalHNSWParams.M, provisionalHNSWParams.EfSearch)
	assertChurnedRecall(t, idx, cr, ho.Queries, realVectorRecallBar, realChurnSeed+1)
	t.Logf("wall %.1fs", time.Since(start).Seconds())
}

// TestHNSWMeasureGrid (make hnsw-measure-grid, the operator) is the measurement
// itself. It builds the re-scoped run-2 cell list (gridCells), runs one subtest
// per cell in ascending n, appends and fsyncs each result to VP_HNSW_GRID_OUT,
// and resumes past cells already recorded under this run identity. The library
// has one ef (EfSearch builds and searches), so each cell builds and searches at
// its single ef. It asserts only that its inputs loaded; the crossover and T/L
// table is printed at the end.
//
// Run 1 (99 planned cells over M∈{16,24,32}×ef∈{100,200,400}×n{5k..50k,all})
// timed out at 45 cells but was decisive (grid-run1-partial-45cells.jsonl):
// (16,100) FAILS the 0.90 post-churn recall@10 bar at n≥10k and ef=400 only adds
// latency for no recall gain — both were MEASURED and REJECTED, not dropped
// untested. Run 2 narrows to M∈{24,32}×ef∈{100,200} (gridMs/gridEfs) and
// re-scopes the n-axis (gridNLow + the leader-only high-n confirmation).
func TestHNSWMeasureGrid(t *testing.T) {
	requireHNSWMeasure(t)
	in := requireRealInputs(t, true)
	corpus, ho, all := loadRealHeldOut(t, in)
	ident := gridIdentity{IDsSHA256: sortedIDsHash(corpus.IDs), Fingerprint: in.Fingerprint, Seed: realPartitionSeed,
		NQ: len(ho.Queries), Library: hnswLibraryVersion, Harness: gridHarnessVersion}
	cells := gridCells(all)
	runGrid(t, in.GridOut, ident, cells, func(t *testing.T, c gridCell) gridResult {
		return measureGridCell(t, c, ho)
	})
	var table strings.Builder
	if err := writeGridTable(&table, in.GridOut, ident); err != nil {
		t.Fatal(err)
	}
	t.Logf("crossover table (every cell recorded under this run identity):\n%s", table.String())
}

// The re-scoped run-2 parameter set, narrowed by run 1's 45-cell result
// (grid-run1-partial-45cells.jsonl): M=16 failed the 0.90 post-churn recall@10
// bar at n≥10k and ef=400 only added latency for no recall gain, so both are
// dropped. The survivors are M∈{24,32} × ef∈{100,200}.
var (
	gridMs  = []int{24, 32}
	gridEfs = []int{100, 200}
	// gridNLow is measured for EVERY (M,ef) pair: the dense 1k–5k region pins T
	// (run 1 showed HNSW already beats brute at 5k, so T is below 5000) and its
	// hysteresis bound L, and 10k/25k carry the recall curve.
	gridNLow = []int{1000, 2000, 3000, 4000, 5000, 10000, 25000}
	// grid50kPairs get a 50000 confirmation cell; gridAllPairs get a
	// whole-corpus (104k) cell. T and L are pinned in gridNLow under full churn,
	// so the high-n cells only confirm recall at scale — and a 104k cell is
	// inherently multi-ten-minute, so only the single top candidate (32,100) runs
	// it (grok's "do not run four 104k-class cells"), while both leaders get 50k.
	grid50kPairs = [][2]int{{32, 100}, {24, 100}}
	gridAllPairs = [][2]int{{32, 100}}
)

// gridCells builds the run-2 cell list for a corpus of `all` vectors: every
// (M,ef) pair at each gridNLow size, a 50000 cell for grid50kPairs, and a
// whole-corpus cell for gridAllPairs. A high-n size already in gridNLow, or
// larger than the corpus, is skipped, and the whole-corpus size is added once.
func gridCells(all int) []gridCell {
	var cells []gridCell
	for _, n := range gridNLow {
		if n > all {
			continue
		}
		for _, m := range gridMs {
			for _, ef := range gridEfs {
				cells = append(cells, gridCell{m, ef, n})
			}
		}
	}
	if 50000 <= all && !slices.Contains(gridNLow, 50000) {
		for _, p := range grid50kPairs {
			cells = append(cells, gridCell{p[0], p[1], 50000})
		}
	}
	if all != 50000 && !slices.Contains(gridNLow, all) {
		for _, p := range gridAllPairs {
			cells = append(cells, gridCell{p[0], p[1], all})
		}
	}
	return cells
}

// measureGridCell measures one cell: build the corpus Pool[:n] with churn's
// colliding ids (pinned rebuilds off), weigh it and its brute-force baseline,
// time the latency crossover at both k over a query subset, then churn and
// measure the POST-churn recall (the only recall pass, the 0.90 bar).
func measureGridCell(t *testing.T, c gridCell, ho heldOut) gridResult {
	var res gridResult
	params := noRebuild(provisionalHNSWParams)
	params.M, params.EfSearch = c.M, c.Ef
	rng := rand.New(rand.NewSource(int64(c.M)*1_000_003 + int64(c.Ef)*1009 + int64(c.N)))
	fresh := reserveDrawer(t, ho.Pool[c.N:])

	idx := newHNSWIndex(embeddingDims, params)
	defer idx.Close()
	var cr churnResult
	start := time.Now()
	_, heap, alloc, err := retainedHeap(func() (VectorIndex, error) {
		cr = churnBuild(t, idx, rng, ho.Pool[:c.N], fresh)
		return idx, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	res.BuildSeconds, res.HNSWHeap, res.HNSWBuildAlloc = time.Since(start).Seconds(), heap, alloc

	before := maps.Clone(cr.live)
	ids := make([]string, 0, len(before))
	vecs := make([][]float32, 0, len(before))
	for id, v := range before {
		ids, vecs = append(ids, id), append(vecs, v)
	}
	brute, bheap, _, err := retainedHeap(func() (VectorIndex, error) {
		b := newBruteIndex(embeddingDims)
		return b, b.Build(context.Background(), vecs, ids)
	})
	if err != nil {
		t.Fatal(err)
	}
	res.BruteHeap = bheap
	// Latency crossover, timed over a fixed query subset (latencyQueries): a p50
	// over 200 queries × latencyRepeats passes is a robust timing sample, while
	// timing all ~1000 at each large-n cell was costly (the brute side scans the
	// whole corpus per query). Timed on the pre-churn graph; latency tracks n.
	latQ := ho.Queries
	if len(latQ) > latencyQueries {
		latQ = latQ[:latencyQueries]
	}
	res.HNSWK10, res.HNSWK30 = queryLatency(t, idx, latQ, recallKs[0]), queryLatency(t, idx, latQ, recallKs[1])
	res.BruteK10, res.BruteK30 = queryLatency(t, brute, latQ, recallKs[0]), queryLatency(t, brute, latQ, recallKs[1])
	brute = nil

	// The ONLY recall pass is POST-churn: ChurnRecallK10/K30 is the 0.90 bar and
	// the column deriveTL/deriveL read for T and L, so the pre-churn recall pass
	// (and its full O(queries×n) τ ground truth) is dropped as wasted cost —
	// RecallK10/K30, SetOverlap and ReturnedK10/K30 stay zero in run 2. The recall
	// query count is tiered DOWN at large n, where the τ ground truth is
	// O(queries×n): at n≥recallTierN it uses recallQueriesLarge queries. For a
	// measured recall ≈0.95 that is SE=sqrt(p(1-p)/N) ≈ 0.011 at N=400 — far
	// inside the ~0.05 margin to the bar, so the pass/fail decision is unambiguous.
	churnStart := time.Now()
	churnMutate(t, idx, rng, &cr, fresh)
	recallQ := ho.Queries
	if c.N >= recallTierN && len(recallQ) > recallQueriesLarge {
		recallQ = recallQ[:recallQueriesLarge]
	}
	after, err := measureRecall(idx, nil, newLiveSet(cr.live), recallQ)
	if err != nil {
		t.Fatalf("after churn: %v", err)
	}
	res.ChurnRecallK10, res.ChurnRecallK30 = after.Recall[0], after.Recall[1]
	t.Logf("n=%d churn@10 %.4f @30 %.4f (%d queries); build %.1fs; churn+recall %.1fs; HNSW p50@30 %.2f ms, brute %.2f ms",
		c.N, res.ChurnRecallK10, res.ChurnRecallK30, len(recallQ), res.BuildSeconds, time.Since(churnStart).Seconds(), res.HNSWK30.P50, res.BruteK30.P50)
	return res
}

// queryLatency returns idx's query latency at k: the median per-pass p50 and
// p99 over latencyRepeats timed passes (after one untimed warmup), and the
// inter-pass p50 spread. Repeating keeps one GC pause or scheduler quantum from
// pinning a crossover; the spread feeds the tie rule (B3/3d).
func queryLatency(t *testing.T, idx VectorIndex, queries [][]float32, k int) latencyStat {
	t.Helper()
	for _, q := range queries { // warmup, untimed
		if _, err := idx.Search(q, k); err != nil {
			t.Fatal(err)
		}
	}
	p50s := make([]float64, latencyRepeats)
	p99s := make([]float64, latencyRepeats)
	for r := range latencyRepeats {
		ds := make([]time.Duration, 0, len(queries))
		for _, q := range queries {
			s := time.Now()
			if _, err := idx.Search(q, k); err != nil {
				t.Fatal(err)
			}
			ds = append(ds, time.Since(s))
		}
		p50s[r], p99s[r] = percentile(ds, 50), percentile(ds, 99)
	}
	return latencyStat{P50: medianFloat(p50s), P99: medianFloat(p99s), Spread: slices.Max(p50s) - slices.Min(p50s)}
}
