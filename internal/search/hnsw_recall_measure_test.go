// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
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
// k = 30, of TestHNSWClusteredChurnRecall50k at the provisional (16, 100): the
// lowest of 5 recorded runs (0.9498, at k = 30; k = 10 ranged 0.9514-0.9520)
// minus churnFloorMargin, rounded down. A build is not reproducible (the
// library ranges over Go maps), so the bar is a repeated-run minimum, never
// one run's value. The runs are recorded in the task.
const clusteredChurnRecallBar50k = 0.92

// realVectorRecallBar is the recall@10 bar on real vectors (ADR-014): 0.90 at
// both k. The operator sets it otherwise if no measured cell reaches it.
const realVectorRecallBar = 0.90

// replacedProbeSample is how many replaced ids the old-vector probe searches.
const replacedProbeSample = 200

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
	if err := idx.Build(ho.Pool, ho.PoolIDs); err != nil {
		t.Fatal(err)
	}
	if idx.Len() != len(ho.PoolIDs) || tombstoneCount(idx) != 0 {
		t.Errorf("Len %d with %d tombstones, want %d and 0: queries must stay out of the index", idx.Len(), tombstoneCount(idx), len(ho.PoolIDs))
	}
	live := newLiveSet(zipLive(ho.PoolIDs, ho.Pool))
	oracle := newBruteIndex(embeddingDims)
	if err := oracle.Build(ho.Pool, ho.PoolIDs); err != nil {
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
// itself: M ∈ {16, 24, 32} × ef ∈ {100, 200, 400} × n ∈ {5k, 10k, 20k, 50k,
// all}, in ascending n, one subtest per cell, each result appended and fsynced
// to VP_HNSW_GRID_OUT, resuming past cells already recorded under this run
// identity. The library has one ef (EfSearch builds and searches), so each cell
// builds and searches at its single ef. It asserts only that its inputs loaded;
// the crossover table is printed at the end.
func TestHNSWMeasureGrid(t *testing.T) {
	requireHNSWMeasure(t)
	in := requireRealInputs(t, true)
	corpus, ho, all := loadRealHeldOut(t, in)
	ident := gridIdentity{IDsSHA256: sortedIDsHash(corpus.IDs), Fingerprint: in.Fingerprint, Seed: realPartitionSeed,
		NQ: len(ho.Queries), Library: hnswLibraryVersion, Harness: gridHarnessVersion}
	var cells []gridCell
	for _, n := range gridSizes(all) {
		for _, m := range []int{16, 24, 32} {
			for _, ef := range []int{100, 200, 400} {
				cells = append(cells, gridCell{m, ef, n})
			}
		}
	}
	runGrid(t, in.GridOut, ident, cells, func(t *testing.T, c gridCell) gridResult {
		return measureGridCell(t, c, ho)
	})
	var table strings.Builder
	if err := writeGridTable(&table, in.GridOut, ident); err != nil {
		t.Fatal(err)
	}
	t.Logf("crossover table (every cell recorded under this run identity):\n%s", table.String())
}

// gridSizes is the grid's n axis: 5k, 10k, 20k and 50k, then the whole
// corpus, in ascending order and without duplicates, so a corpus of exactly
// 50,000 is not measured, and recorded, twice.
func gridSizes(all int) []int {
	sizes := []int{5000, 10000, 20000, 50000}
	if !slices.Contains(sizes, all) {
		sizes = append(sizes, all)
	}
	slices.Sort(sizes)
	return sizes
}

// measureGridCell measures one cell: build the corpus Pool[:n] with churn's
// colliding ids (pinned rebuilds off), time and weigh it against brute force,
// measure recall and latency at both k, then churn and measure recall again.
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
		return b, b.Build(vecs, ids)
	})
	if err != nil {
		t.Fatal(err)
	}
	res.BruteHeap = bheap
	rep, err := measureRecall(idx, brute, newLiveSet(before), ho.Queries)
	if err != nil {
		t.Fatalf("before churn: %v", err)
	}
	res.RecallK10, res.RecallK30, res.SetOverlapK10 = rep.Recall[0], rep.Recall[1], rep.SetOverlap
	res.ReturnedK10, res.ReturnedK30 = rep.Returned[0], rep.Returned[1]
	res.HNSWK10, res.HNSWK30 = rep.Latency[0], rep.Latency[1]
	res.BruteK10, res.BruteK30 = queryLatency(t, brute, ho.Queries, recallKs[0]), queryLatency(t, brute, ho.Queries, recallKs[1])
	brute = nil

	churnMutate(t, idx, rng, &cr, fresh)
	after, err := measureRecall(idx, nil, newLiveSet(cr.live), ho.Queries)
	if err != nil {
		t.Fatalf("after churn: %v", err)
	}
	res.ChurnRecallK10, res.ChurnRecallK30 = after.Recall[0], after.Recall[1]
	t.Logf("recall@10 %.4f @30 %.4f; after churn %.4f / %.4f; build %.1fs; HNSW p50@30 %.2f ms, brute %.2f ms",
		res.RecallK10, res.RecallK30, res.ChurnRecallK10, res.ChurnRecallK30, res.BuildSeconds, res.HNSWK30[0], res.BruteK30[0])
	return res
}

// queryLatency returns idx's query p50 and p99 in ms at k.
func queryLatency(t *testing.T, idx VectorIndex, queries [][]float32, k int) [2]float64 {
	t.Helper()
	ds := make([]time.Duration, 0, len(queries))
	for _, q := range queries {
		s := time.Now()
		if _, err := idx.Search(q, k); err != nil {
			t.Fatal(err)
		}
		ds = append(ds, time.Since(s))
	}
	return [2]float64{percentile(ds, 50), percentile(ds, 99)}
}
