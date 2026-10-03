// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"math/rand"
	"reflect"
	"sync"
	"testing"

	"github.com/coder/hnsw"
)

// noRebuild returns p with tombstone rebuilds disabled, for a test that needs
// its tombstones to stay put.
func noRebuild(p hnswParams) hnswParams {
	p.TombstoneRatio = 0
	return p
}

// tombstoneCount returns the number of tombstoned keys still in h's graph.
func tombstoneCount(h *hnswIndex) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.st.tombstones()
}

// TestDistanceNameIsPinned pins the registered distance name, and reports
// TestMain's check that the library resolves it to cosineNaNGuard before any
// constructor ran. Renaming the constant makes every saved graph fail Import;
// moving the registration into a constructor makes TestMain's Export fail.
func TestDistanceNameIsPinned(t *testing.T) {
	if distanceName != "vp-cosine-nanguard" {
		t.Errorf("distanceName = %q, want %q: a renamed distance makes every saved graph fail Import",
			distanceName, "vp-cosine-nanguard")
	}
	if distanceRegistrationErr != nil {
		t.Errorf("before any test ran, the library registry did not resolve %q to cosineNaNGuard: %v",
			distanceName, distanceRegistrationErr)
	}
}

// TestGuardedDistanceSurvivesExportImport: a graph built by hnswIndex, with the
// production distance in force, exports, imports into a fresh graph with the
// same distance function, and answers searches identically.
func TestGuardedDistanceSurvivesExportImport(t *testing.T) {
	const dims, n = 32, 200
	rng := rand.New(rand.NewSource(11))
	idx := newHNSWIndex(dims, provisionalHNSWParams)
	for i := range n {
		if err := idx.Insert(fmt.Sprintf("v%d", i), randomUnitVector(rng, dims)); err != nil {
			t.Fatal(err)
		}
	}

	var buf bytes.Buffer
	idx.mu.RLock()
	err := idx.st.g.Export(&buf)
	idx.mu.RUnlock()
	if err != nil {
		t.Fatalf("Export with the production distance: %v", err)
	}
	imported := hnsw.NewGraph[uint64]()
	if err := imported.Import(&buf); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if reflect.ValueOf(imported.Distance).Pointer() != reflect.ValueOf(cosineNaNGuard).Pointer() {
		t.Fatal("imported graph's Distance is not cosineNaNGuard")
	}
	for q := range 10 {
		query := randomUnitVector(rng, dims)
		want := idx.st.g.SearchWithDistance(query, 10)
		got := imported.SearchWithDistance(query, 10)
		if len(got) != len(want) {
			t.Fatalf("query %d: %d results after import, %d before", q, len(got), len(want))
		}
		for i := range want {
			if got[i].Key != want[i].Key || got[i].Distance != want[i].Distance {
				t.Errorf("query %d rank %d: (%d, %v) after import, (%d, %v) before",
					q, i, got[i].Key, got[i].Distance, want[i].Key, want[i].Distance)
			}
		}
	}
}

// TestRegistrationDoesNotRaceExport (meaningful under -race): constructing
// indexes while others Export must not touch the library's unlocked distance
// registry. A registration made at construction time writes that map while
// Export iterates it, which the race detector reports.
func TestRegistrationDoesNotRaceExport(t *testing.T) {
	const dims = 8
	rng := rand.New(rand.NewSource(3))
	existing := make([]*hnswIndex, 8)
	for i := range existing {
		existing[i] = newHNSWIndex(dims, provisionalHNSWParams)
		for j := range 20 {
			if err := existing[i].Insert(fmt.Sprintf("e%d", j), randomUnitVector(rng, dims)); err != nil {
				t.Fatal(err)
			}
		}
	}
	vec := randomUnitVector(rng, dims)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				idx, err := newIndex(kindHNSW, dims, provisionalHNSWParams)
				if err != nil {
					t.Errorf("newIndex: %v", err)
					return
				}
				_ = idx.Insert("x", vec)
			}
		})
	}
	for _, idx := range existing {
		wg.Go(func() {
			for range 20 {
				idx.mu.RLock()
				err := idx.st.g.Export(io.Discard)
				idx.mu.RUnlock()
				if err != nil {
					t.Errorf("Export: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
}

// TestCosineNaNGuard: a zero vector on either side yields +Inf, never NaN, and
// an ordinary pair keeps its cosine distance.
func TestCosineNaNGuard(t *testing.T) {
	zero := []float32{0, 0, 0}
	v := []float32{1, 0, 0}
	for name, d := range map[string]float32{
		"zero,v":    cosineNaNGuard(zero, v),
		"v,zero":    cosineNaNGuard(v, zero),
		"zero,zero": cosineNaNGuard(zero, zero),
	} {
		if !math.IsInf(float64(d), 1) {
			t.Errorf("cosineNaNGuard(%s) = %v, want +Inf", name, d)
		}
	}
	if d := cosineNaNGuard(v, []float32{0, 1, 0}); math.Abs(float64(d)-1) > 1e-6 {
		t.Errorf("cosineNaNGuard of orthogonal vectors = %v, want 1", d)
	}
}

// nanTarget is the stored vector nanForTarget scores as NaN.
var nanTarget = []float32{0, 0, 1}

// nanForTarget is cosine distance, except that any pair involving nanTarget
// scores NaN: a stand-in for a score the guard does not catch.
func nanForTarget(a, b []float32) float32 {
	if equalVectors(a, nanTarget) || equalVectors(b, nanTarget) {
		return float32(math.NaN())
	}
	return cosineDistanceF32(a, b)
}

// TestNaNScoreGuard: a result whose distance is not finite never reaches the
// caller, whatever the distance function did.
func TestNaNScoreGuard(t *testing.T) {
	params := provisionalHNSWParams
	params.distance = nanForTarget
	idx := newHNSWIndex(3, params)
	vecs := map[string][]float32{
		"x":      {1, 0, 0},
		"y":      {0, 1, 0},
		"target": nanTarget,
		"xy":     {0.7, 0.7, 0},
		"xz":     {0.7, 0, 0.7},
	}
	for id, v := range vecs {
		if err := idx.Insert(id, v); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range [][]float32{{1, 0, 0}, {0, 0, 1}, {0.5, 0.5, 0.5}} {
		res, err := idx.Search(q, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			if r.ID == "target" {
				t.Errorf("query %v returned the NaN-scored vector: %v", q, res)
			}
			if d := float64(r.Distance); math.IsNaN(d) || math.IsInf(d, 0) {
				t.Errorf("query %v returned a non-finite distance: %v", q, res)
			}
		}
	}
}

// TestHNSWIdenticalReinsertIsNoop: re-inserting an id's current vector creates
// no tombstone and counts as no mutation.
func TestHNSWIdenticalReinsertIsNoop(t *testing.T) {
	idx := newHNSWIndex(3, provisionalHNSWParams)
	if err := idx.Insert("a", []float32{1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	gen := idx.gen
	if err := idx.Insert("a", []float32{1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if got := tombstoneCount(idx); got != 0 {
		t.Errorf("tombstones = %d after an identical re-insert, want 0", got)
	}
	if idx.gen != gen {
		t.Errorf("generation moved %d → %d on an identical re-insert", gen, idx.gen)
	}
}

// TestHNSWUpsertTombstonesTheOldKey: an upsert never re-adds a key (which
// panics upstream, issue #15); it tombstones the old key and adds a new one.
func TestHNSWUpsertTombstonesTheOldKey(t *testing.T) {
	idx := newHNSWIndex(3, provisionalHNSWParams)
	for _, v := range [][]float32{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}} {
		if err := idx.Insert("a", v); err != nil {
			t.Fatal(err)
		}
	}
	if got := idx.Len(); got != 1 {
		t.Errorf("Len = %d, want 1", got)
	}
	if got := tombstoneCount(idx); got != 2 {
		t.Errorf("tombstones = %d after two upserts, want 2", got)
	}
}

// TestHNSWSearchFillsKPastTombstones: when tombstones crowd the first library
// answer, Search widens its request until it has k live results.
func TestHNSWSearchFillsKPastTombstones(t *testing.T) {
	const dims, n, k = 16, 1000, 10
	rng := rand.New(rand.NewSource(5))
	idx := newHNSWIndex(dims, noRebuild(provisionalHNSWParams))
	vecs := make([][]float32, n)
	ids := make([]string, n)
	for i := range vecs {
		vecs[i] = randomUnitVector(rng, dims)
		ids[i] = fmt.Sprintf("v%d", i)
	}
	if err := idx.Build(vecs, ids); err != nil {
		t.Fatal(err)
	}
	// Keep only every 50th id: 20 live among 1000 keys, far fewer than the
	// EfSearch (100) results the first library call returns.
	for i, id := range ids {
		if i%50 != 0 {
			idx.Delete(id)
		}
	}
	for q := range 10 {
		before := idx.librarySearches.Load()
		res, err := idx.Search(randomUnitVector(rng, dims), k)
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != k {
			t.Errorf("query %d: %d results, want %d live results", q, len(res), k)
		}
		if calls := idx.librarySearches.Load() - before; calls < 2 {
			t.Errorf("query %d: %d library searches; the first answer cannot hold %d live results", q, calls, k)
		}
	}
}

// TestHNSWConcurrentSearchDuringInsert (meaningful under -race): eight readers
// search while one writer inserts. The library has no lock of its own.
func TestHNSWConcurrentSearchDuringInsert(t *testing.T) {
	const dims = 16
	idx := newHNSWIndex(dims, provisionalHNSWParams)
	seed := rand.New(rand.NewSource(9))
	if err := idx.Insert("seed", randomUnitVector(seed, dims)); err != nil {
		t.Fatal(err)
	}
	writes := make([][]float32, 500)
	for i := range writes {
		writes[i] = randomUnitVector(seed, dims)
	}
	queries := make([][]float32, 8)
	for i := range queries {
		queries[i] = randomUnitVector(seed, dims)
	}

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Go(func() {
		defer close(done)
		for i, v := range writes {
			if err := idx.Insert(fmt.Sprintf("w%d", i%400), v); err != nil {
				t.Errorf("Insert: %v", err)
				return
			}
		}
	})
	for _, q := range queries {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
				}
				if _, err := idx.Search(q, 5); err != nil {
					t.Errorf("Search: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
	if got := idx.Len(); got != 401 {
		t.Errorf("Len = %d, want 401", got)
	}
}

// churnResult is the live state a churn run leaves behind.
type churnResult struct {
	live    map[string][]float32 // id -> current vector
	deleted map[string]bool      // ids deleted and never re-inserted
}

// churn builds idx from vecs with colliding ids injected, deletes 30% of the
// ids, re-inserts 20% of the original count with new vectors (deleted ids
// first), and upserts 5% of the live ids with new vectors. fresh draws a new
// vector. It returns the live set an exact index must agree with.
func churn(t *testing.T, idx VectorIndex, rng *rand.Rand, vecs [][]float32, fresh func() []float32) churnResult {
	t.Helper()
	n := len(vecs)
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("c%d", i)
	}
	// Colliding ids inside one Build batch: 1% of the ids appear a second time
	// with a different vector, and the later occurrence must win.
	batchIDs := append([]string(nil), ids...)
	batchVecs := append([][]float32(nil), vecs...)
	live := make(map[string][]float32, n)
	for i, id := range ids {
		live[id] = vecs[i]
	}
	for i := 0; i < n/100; i++ {
		id := ids[rng.Intn(n)]
		v := fresh()
		batchIDs = append(batchIDs, id)
		batchVecs = append(batchVecs, v)
		live[id] = v
	}
	if err := idx.Build(batchVecs, batchIDs); err != nil {
		t.Fatalf("Build: %v", err)
	}

	deleted := make(map[string]bool)
	order := rng.Perm(n)
	for _, i := range order[:n*30/100] {
		if !idx.Delete(ids[i]) {
			t.Fatalf("Delete(%s) = false for a live id", ids[i])
		}
		delete(live, ids[i])
		deleted[ids[i]] = true
	}
	reinserted := 0
	for _, i := range order[:n*30/100] {
		if reinserted == n*20/100 {
			break
		}
		v := fresh()
		if err := idx.Insert(ids[i], v); err != nil {
			t.Fatalf("re-Insert: %v", err)
		}
		live[ids[i]] = v
		delete(deleted, ids[i])
		reinserted++
	}
	upserted := 0
	for _, i := range order[n*30/100:] {
		if upserted == n*5/100 {
			break
		}
		v := fresh()
		if err := idx.Insert(ids[i], v); err != nil {
			t.Fatalf("upsert Insert: %v", err)
		}
		live[ids[i]] = v
		upserted++
	}
	return churnResult{live: live, deleted: deleted}
}

// checkChurnedSearches runs queries against idx and fails on any id that is
// not live, any distance that does not match the id's CURRENT vector, and a Len
// that differs from the live count.
func checkChurnedSearches(t *testing.T, idx VectorIndex, cr churnResult, queries [][]float32) {
	t.Helper()
	if got := idx.Len(); got != len(cr.live) {
		t.Errorf("Len = %d, want the live count %d", got, len(cr.live))
	}
	for q, query := range queries {
		res, err := idx.Search(query, 10)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		for _, r := range res {
			vec, ok := cr.live[r.ID]
			if !ok {
				t.Fatalf("query %d returned %s, which is not live (deleted: %v)", q, r.ID, cr.deleted[r.ID])
			}
			if want := cosineDistanceF32(query, vec); math.Abs(float64(r.Distance-want)) > crossImplTolerance {
				t.Fatalf("query %d: %s at distance %v, but its current vector is at %v (a stale vector answered)",
					q, r.ID, r.Distance, want)
			}
		}
	}
}

// TestHNSWChurnSafety (-short, under -race in make test): deletes, re-inserts
// and colliding ids on 300 seeded random vectors, with tombstone rebuilds
// enabled. A wrapper that calls the library's Delete and re-adds keys panics
// here; a wrapper that returns deleted or stale entries fails the checks. The
// fixture is the smallest that still kills both churn mutants (repeated three
// times) and still triggers a rebuild; it was 2k × 384 dimensions (19.8 s
// under -race) before.
func TestHNSWChurnSafety(t *testing.T) {
	const n, dims = 300, 32
	rng := rand.New(rand.NewSource(2026))
	gen := newRandomGen(2026, dims)

	idx := newHNSWIndex(dims, provisionalHNSWParams)
	t.Cleanup(func() { _ = idx.Close() })
	cr := churn(t, idx, rng, gen.take(n), gen.next)
	queries := make([][]float32, 0, 100)
	for id := range cr.live {
		if len(queries) == 50 {
			break
		}
		queries = append(queries, cr.live[id])
	}
	queries = append(queries, gen.take(50)...)
	// Searched while any rebuild may still be running: the live graph must be
	// correct throughout.
	checkChurnedSearches(t, idx, cr, queries)

	// Deterministic: the trigger runs synchronously inside the write that
	// crosses the threshold, and the first crossing (90 deletes on 300 ids
	// pass both MinTombstones, 64, and the ratio) happens before any rebuild
	// can have swapped, so the count cannot depend on scheduling.
	if got := idx.rebuildsStarted.Load(); got == 0 {
		t.Fatal("no tombstone rebuild started during the churn; the fixture no longer exercises one")
	}
	// Wait for whatever rebuild is in flight, then search the result again.
	if b := currentRebuild(idx); b != nil {
		waitOrDump(t, b.done, "the churn's rebuild")
	}
	if idx.swaps.Load()+idx.abandoned.Load() == 0 {
		t.Error("a rebuild started but neither swapped nor was abandoned")
	}
	checkChurnedSearches(t, idx, cr, queries)
}

// churnFloorMargin is the stated margin under the measured recall.
const churnFloorMargin = 0.02

// churnRecallFloor is the recall@10 floor at the provisional (M, EfSearch) =
// (16, 100) after churn on the seeded 5k RANDOM corpus: the LOWEST recall
// measured over 12 runs (0.4645; highest 0.5075) minus churnFloorMargin,
// rounded down. The minimum, not one run, because a build is not reproducible
// even with a seeded Rng: the library keeps neighbours in Go maps, whose
// iteration order is randomised. EfSearch 20 measured at most 0.1850 over the
// same runs. The fixture is random, not clustered, because on the clustered
// corpus the gap between EfSearch 100 and 20 after churn (0.9745 against
// 0.9635) is smaller than the margin. The runs are recorded in task
// vector-index-interface-and-coder-hnsw-wrapper.
const churnRecallFloor = 0.44

// TestHNSWChurnRecallFloor5k (VP_HNSW_SLOW=1; the hnsw CI job, without -race):
// after churn on a seeded random 5k corpus, recall@10 against brute force at the
// provisional parameters stays at or above churnRecallFloor, and the same churn
// at the library's default EfSearch of 20 falls below it by at least
// churnFloorMargin, so the floor can catch a wrapper that leaves EfSearch at 20.
func TestHNSWChurnRecallFloor5k(t *testing.T) {
	requireSlowHNSW(t)
	ef100 := measureChurnRecall(t, provisionalHNSWParams)
	ef20params := provisionalHNSWParams
	ef20params.EfSearch = 20
	ef20 := measureChurnRecall(t, ef20params)
	t.Logf("recall@10 after churn at 5k random: (16,100) = %.4f, (16,20) = %.4f", ef100, ef20)
	if ef100 < churnRecallFloor {
		t.Errorf("recall@10 at the provisional parameters = %.4f, below the floor %.4f", ef100, churnRecallFloor)
	}
	if ef20 > churnRecallFloor-churnFloorMargin {
		t.Errorf("recall@10 at EfSearch 20 = %.4f, not at least %.2f below the floor %.4f: the floor cannot catch EfSearch left at 20",
			ef20, churnFloorMargin, churnRecallFloor)
	}
}

// measureChurnRecall churns a seeded random 5k corpus in an hnswIndex built
// with params, and returns its mean recall@10 against a brute-force index
// holding the same live set, over 200 seeded queries.
func measureChurnRecall(t *testing.T, params hnswParams) float64 {
	t.Helper()
	const n, k = 5000, 10
	rng := rand.New(rand.NewSource(5000))
	gen := newRandomGen(5000, embeddingDims)

	idx := newHNSWIndex(embeddingDims, noRebuild(params))
	cr := churn(t, idx, rng, gen.take(n), gen.next)
	oracle := newBruteIndex(embeddingDims)
	for id, v := range cr.live {
		if err := oracle.Insert(id, v); err != nil {
			t.Fatal(err)
		}
	}
	queries := gen.take(200)
	var sum float64
	for _, q := range queries {
		want, err := oracle.Search(q, k)
		if err != nil {
			t.Fatal(err)
		}
		got, err := idx.Search(q, k)
		if err != nil {
			t.Fatal(err)
		}
		sum += recallAt(got, want)
	}
	return sum / float64(len(queries))
}

// TestHNSWTombstoneHeavySearchCost (VP_HNSW_SLOW=1): at 15% tombstones on a
// seeded random 10k index, a query costs at most twice the distance evaluations it
// cost with none, and makes at most 1 + ⌈log2(Len/k)⌉ library searches.
// Fetching k plus the tombstone count instead raises the library's search
// breadth with every tombstone (implementor3 measured 6.1 ms at 0% against
// 58.9 ms at 15%). Costs are counted, never timed.
func TestHNSWTombstoneHeavySearchCost(t *testing.T) {
	requireSlowHNSW(t)
	const n, k = 10000, 10
	// The tombstone threshold is pinned to "never", so the 15% phase measures
	// a graph that really holds its tombstones.
	params := noRebuild(provisionalHNSWParams)
	params.distance = countingCosine
	idx := newHNSWIndex(embeddingDims, params)
	// Random, not clustered: on the clustered corpus layer 0 splits into
	// per-cluster islands, so a wider request cannot visit more nodes and
	// the fetch-k-plus-tombstones mutant costs nothing extra there.
	gen := newRandomGen(10000, embeddingDims)
	vecs := gen.take(n)
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("t%d", i)
	}
	if err := idx.Build(vecs, ids); err != nil {
		t.Fatal(err)
	}
	queries := gen.take(100)

	costPerQuery := func() (evals float64, maxCalls int64) {
		start := distanceEvals.Load()
		for _, q := range queries {
			before := idx.librarySearches.Load()
			if _, err := idx.Search(q, k); err != nil {
				t.Fatal(err)
			}
			maxCalls = max(maxCalls, idx.librarySearches.Load()-before)
		}
		return float64(distanceEvals.Load()-start) / float64(len(queries)), maxCalls
	}
	baseEvals, _ := costPerQuery()

	rng := rand.New(rand.NewSource(10002))
	for _, i := range rng.Perm(n)[:n*15/100] {
		idx.Delete(ids[i])
	}
	if got := tombstoneCount(idx); got != n*15/100 {
		t.Fatalf("tombstones = %d, want %d: the comparison needs a graph that really holds them", got, n*15/100)
	}
	evals, maxCalls := costPerQuery()
	callBound := 1 + int64(math.Ceil(math.Log2(float64(idx.Len())/k)))
	t.Logf("distance evaluations per query: %.0f at 0%% tombstones, %.0f at 15%%; max library searches per query %d (bound %d)",
		baseEvals, evals, maxCalls, callBound)
	if evals > 2*baseEvals {
		t.Errorf("distance evaluations per query = %.0f at 15%% tombstones, more than 2× the %.0f at 0%%", evals, baseEvals)
	}
	if maxCalls > callBound {
		t.Errorf("a query made %d library searches, above 1 + ⌈log2(Len/k)⌉ = %d", maxCalls, callBound)
	}
	if got := idx.rebuildsStarted.Load(); got != 0 {
		t.Errorf("%d rebuilds started; with the threshold pinned to never, the comparison is vacuous", got)
	}
}
