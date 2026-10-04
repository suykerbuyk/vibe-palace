// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/modfile"
)

// The recall-measurement harness shared by the 50k clustered test, the 10k
// slice, the real-vector run and the grid (task
// hnsw-parameters-from-real-vector-recall-and-production-wiring, Scope 2).

// heldOutQueryCount is how many held-out queries a corpus of union vectors
// gets: 1,000 or 2% of the union, whichever is smaller.
func heldOutQueryCount(union int) int { return min(1000, union*2/100) }

// heldOut is a seeded partition of a loaded corpus: the queries, and the pool
// the corpus is taken from in order (the corpus at size n is Pool[:n], and what
// follows is the reserve fresh vectors are drawn from).
type heldOut struct {
	QueryIDs []string
	Queries  [][]float32
	PoolIDs  []string
	Pool     [][]float32
}

// partitionHeldOut takes a seeded permutation of the ids: the first nq are the
// queries, the rest the pool. Queries never enter an index, so a query cannot
// find itself and the measured index holds no tombstones before its churn.
func partitionHeldOut(ids []string, vecs [][]float32, nq int, seed int64) heldOut {
	perm := rand.New(rand.NewSource(seed)).Perm(len(ids))
	var ho heldOut
	for i, j := range perm {
		if i < nq {
			ho.QueryIDs = append(ho.QueryIDs, ids[j])
			ho.Queries = append(ho.Queries, vecs[j])
			continue
		}
		ho.PoolIDs = append(ho.PoolIDs, ids[j])
		ho.Pool = append(ho.Pool, vecs[j])
	}
	return ho
}

// checkHeldOutVectors checks a partition's vectors, not only its ids: each
// query and pool vector is the vector of the id beside it (byID is the loaded
// corpus), and no query vector equals any pool vector, so no query can find
// itself under another id.
func checkHeldOutVectors(ho heldOut, byID map[string][]float32) error {
	for i, id := range ho.QueryIDs {
		if !slices.Equal(ho.Queries[i], byID[id]) {
			return fmt.Errorf("query %d is not the vector of its id %s", i, id)
		}
	}
	pool := make(map[string]bool, len(ho.Pool))
	for i, id := range ho.PoolIDs {
		if !slices.Equal(ho.Pool[i], byID[id]) {
			return fmt.Errorf("pool entry %d is not the vector of its id %s", i, id)
		}
		pool[string(encodeVec(ho.Pool[i]))] = true
	}
	for i, q := range ho.Queries {
		if pool[string(encodeVec(q))] {
			return fmt.Errorf("query %d (%s) is the same vector as a pool entry", i, ho.QueryIDs[i])
		}
	}
	return nil
}

// churnCorpusSize is the largest n whose churn's fresh draws fit in a pool of
// the given size after the corpus: n + churnFreshCount(n) <= pool.
func churnCorpusSize(pool int) int {
	n := pool * 100 / 126
	for n > 0 && n+churnFreshCount(n) > pool {
		n--
	}
	for n+1+churnFreshCount(n+1) <= pool {
		n++
	}
	return n
}

// reserveDrawer returns a fresh() for churn that hands out the reserve in
// order, and fails the test, naming the shortfall, if churn asks for more.
func reserveDrawer(t *testing.T, reserve [][]float32) func() []float32 {
	next := 0
	return func() []float32 {
		if next == len(reserve) {
			t.Fatalf("churn needs more fresh vectors than the %d left after the queries and the corpus", len(reserve))
		}
		next++
		return reserve[next-1]
	}
}

// liveSet is the oracle's view: every live id's CURRENT vector.
type liveSet struct {
	byID map[string][]float32
	vecs [][]float32
}

func newLiveSet(byID map[string][]float32) liveSet {
	ls := liveSet{byID: byID, vecs: make([][]float32, 0, len(byID))}
	for _, v := range byID {
		ls.vecs = append(ls.vecs, v)
	}
	return ls
}

// thresholds returns τ for each k: the k-th smallest cosineDistanceF32 from q
// to the live set, gtThreshold's boundary, from one sort.
func thresholds(q []float32, live liveSet, ks ...int) []float32 {
	d := make([]float32, len(live.vecs))
	for i, v := range live.vecs {
		d[i] = cosineDistanceF32(q, v)
	}
	slices.Sort(d)
	out := make([]float32, len(ks))
	for i, k := range ks {
		out[i] = d[min(k, len(d))-1]
	}
	return out
}

// tieEpsilon admits ties at the k boundary. τ and each hit's distance come from
// the same cosineDistanceF32 on the same vectors, so it covers ties only, never
// a float32-against-float64 gap.
const tieEpsilon = 1e-5

// tieAwareRecall scores one query's results against its CURRENT live vectors.
// It first checks every result, as checkChurnedSearches does: a non-live id,
// an id returned twice, or a reported distance that is not the distance to the
// id's current vector (a stale vector answered) is an error. A hit is a result
// whose RECOMPUTED distance is within τ + tieEpsilon, where τ is the k-th
// oracle distance (thresholds). The index's own reported distance never
// decides a hit. Recall = min(hits, k) / min(k, live n).
func tieAwareRecall(q []float32, res []VectorResult, live liveSet, k int, tau float32) (float64, error) {
	seen := make(map[string]bool, len(res))
	hits := 0
	for _, r := range res {
		vec, ok := live.byID[r.ID]
		if !ok {
			return 0, fmt.Errorf("%s returned, but it is not live", r.ID)
		}
		if seen[r.ID] {
			return 0, fmt.Errorf("%s returned twice in one result", r.ID)
		}
		seen[r.ID] = true
		d := cosineDistanceF32(q, vec)
		if math.Abs(float64(r.Distance-d)) > crossImplTolerance {
			return 0, fmt.Errorf("%s at reported distance %v, but its current vector is at %v: a stale vector answered", r.ID, r.Distance, d)
		}
		if d <= tau+tieEpsilon {
			hits++
		}
	}
	want := min(k, len(live.byID))
	if want == 0 {
		return 1, nil
	}
	return float64(min(hits, k)) / float64(want), nil
}

// returnedStats summarises how many results each query got at one k.
type returnedStats struct {
	Min, P1, P50 int
	// ShortFrac is the fraction of queries that got fewer than min(k, live n).
	ShortFrac float64
}

func summarizeReturned(lens []int, want int) returnedStats {
	if len(lens) == 0 {
		return returnedStats{}
	}
	s := slices.Clone(lens)
	slices.Sort(s)
	short := 0
	for _, l := range s {
		if l < want {
			short++
		}
	}
	return returnedStats{
		Min:       s[0],
		P1:        s[len(s)/100],
		P50:       s[len(s)/2],
		ShortFrac: float64(short) / float64(len(s)),
	}
}

// percentile returns the p-th percentile (0-100) of ds, in milliseconds.
func percentile(ds []time.Duration, p int) float64 {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	i := min(len(s)-1, len(s)*p/100)
	return float64(s[i].Microseconds()) / 1000
}

// recallKs are the two k every recall is measured at: 10, and the engine's
// actual request for a limit of 10 (candidateCount, 30).
var recallKs = []int{10, candidateCount(10)}

// recallReport is one recall measurement of an index against a live set.
type recallReport struct {
	Recall     []float64       // tie-aware, per recallKs
	SetOverlap float64         // set-overlap recall@10 against brute force; information only
	Returned   []returnedStats // per recallKs
	Latency    [][2]float64    // HNSW query p50 and p99 in ms, per recallKs
}

// measureRecall runs every query at both k and scores it tie-aware against
// live. oracle, when not nil, also gives set-overlap recall@10. Any result that
// fails tieAwareRecall's checks is an error.
func measureRecall(idx VectorIndex, oracle VectorIndex, live liveSet, queries [][]float32) (recallReport, error) {
	rep := recallReport{
		Recall:   make([]float64, len(recallKs)),
		Returned: make([]returnedStats, len(recallKs)),
		Latency:  make([][2]float64, len(recallKs)),
	}
	lens := make([][]int, len(recallKs))
	times := make([][]time.Duration, len(recallKs))
	var overlap float64
	for _, q := range queries {
		taus := thresholds(q, live, recallKs...)
		for i, k := range recallKs {
			start := time.Now()
			res, err := idx.Search(q, k)
			times[i] = append(times[i], time.Since(start))
			if err != nil {
				return rep, err
			}
			r, err := tieAwareRecall(q, res, live, k, taus[i])
			if err != nil {
				return rep, err
			}
			rep.Recall[i] += r
			lens[i] = append(lens[i], len(res))
			if k == 10 && oracle != nil {
				want, err := oracle.Search(q, k)
				if err != nil {
					return rep, err
				}
				overlap += recallAt(res, want)
			}
		}
	}
	for i, k := range recallKs {
		rep.Recall[i] /= float64(len(queries))
		rep.Returned[i] = summarizeReturned(lens[i], min(k, len(live.byID)))
		rep.Latency[i] = [2]float64{percentile(times[i], 50), percentile(times[i], 99)}
	}
	rep.SetOverlap = overlap / float64(len(queries))
	return rep, nil
}

// checkReplacedVectorsGone searches with the OLD vectors of up to sample
// replaced ids (a seeded sample) at k. A replaced id may be absent, or present
// at the distance of its CURRENT vector; an id found at its old vector (about
// 0), or a deleted id found at all, is an error.
func checkReplacedVectorsGone(idx VectorIndex, cr churnResult, seed int64, sample, k int) (int, error) {
	ids := make([]string, 0, len(cr.replaced))
	for id := range cr.replaced {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rand.New(rand.NewSource(seed)).Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	probed := 0
	for _, id := range ids[:min(sample, len(ids))] {
		for _, old := range cr.replaced[id] {
			if !usableVector(old) {
				continue
			}
			res, err := idx.Search(old, k)
			if err != nil {
				return probed, err
			}
			probed++
			d := distanceTo(res, id)
			if d < 0 {
				continue
			}
			cur, live := cr.live[id]
			if !live {
				return probed, fmt.Errorf("%s returned for its old vector, but it is deleted", id)
			}
			if want := cosineDistanceF32(old, cur); math.Abs(float64(d-want)) > crossImplTolerance {
				return probed, fmt.Errorf("%s at distance %v from its old vector, but its current vector is at %v: the old vector answered", id, d, want)
			}
		}
	}
	return probed, nil
}

// retainedHeap builds an index and returns it with the Go heap it retains:
// HeapAlloc after two GCs, before and after build, the index kept alive. Both
// indexes live wholly on the Go heap (the vendored coder/hnsw is pure Go: no
// cgo, no mmap), so this is in-process and portable where RSS is distorted by
// lazy page return. It also returns TotalAlloc across the build, the build's
// allocation churn. The caller drops every earlier index first.
func retainedHeap(build func() (VectorIndex, error)) (VectorIndex, uint64, uint64, error) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&before)
	idx, err := build()
	if err != nil {
		return nil, 0, 0, err
	}
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(idx)
	var retained uint64
	if after.HeapAlloc > before.HeapAlloc {
		retained = after.HeapAlloc - before.HeapAlloc
	}
	return idx, retained, after.TotalAlloc - before.TotalAlloc, nil
}

// TestHNSWLibraryVersionMatchesGoMod (row 4): hnswLibraryVersion, an input of
// the graph fingerprint, is the `require` pseudo-version for
// github.com/coder/hnsw in go.mod, not the `replace` target (a local path with
// no version).
func TestHNSWLibraryVersionMatchesGoMod(t *testing.T) {
	data, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	mf, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, r := range mf.Require {
		if r.Mod.Path == "github.com/coder/hnsw" {
			got = r.Mod.Version
		}
	}
	if got == "" {
		t.Fatal("go.mod has no require for github.com/coder/hnsw")
	}
	if got != hnswLibraryVersion {
		t.Errorf("hnswLibraryVersion = %q, go.mod requires %q: a stale graph-fingerprint input", hnswLibraryVersion, got)
	}
}

// TestTieAwareRecall (row 12): ties at the k boundary count as hits; a result
// scored by its reported distance against a far CURRENT vector is a stale
// vector; a non-live or repeated id is an error.
func TestTieAwareRecall(t *testing.T) {
	u := func(v ...float32) []float32 { return normalize(v) }
	q := u(1, 0, 0, 0)
	twin := u(1, 0.2, 0, 0)
	live := newLiveSet(map[string][]float32{
		"near":  u(1, 0.05, 0, 0),
		"twinA": twin,
		"twinB": slices.Clone(twin),
		"far":   u(0, 0, 1, 0),
	})
	const k = 2
	tau := thresholds(q, live, k)[0]

	// The oracle lists near and twinA; returning twinB, an identical vector,
	// is just as correct.
	got := []VectorResult{{ID: "near", Distance: cosineDistanceF32(q, live.byID["near"])}, {ID: "twinB", Distance: cosineDistanceF32(q, twin)}}
	r, err := tieAwareRecall(q, got, live, k, tau)
	if err != nil || r != 1 {
		t.Errorf("tie at the boundary: recall %v err %v, want 1 and no error", r, err)
	}
	if so := recallAt(got, []VectorResult{{ID: "near"}, {ID: "twinA"}}); so >= 1 {
		t.Errorf("set-overlap scored %v; the fixture must make it undercount for this row to mean anything", so)
	}

	// "far" reported at twin's distance: the index answered with a vector the
	// id no longer holds.
	stale := []VectorResult{{ID: "near", Distance: cosineDistanceF32(q, live.byID["near"])}, {ID: "far", Distance: cosineDistanceF32(q, twin)}}
	if _, err := tieAwareRecall(q, stale, live, k, tau); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("stale vector: err = %v, want the stale-vector error", err)
	}
	dead := []VectorResult{{ID: "gone", Distance: 0}}
	if _, err := tieAwareRecall(q, dead, live, k, tau); err == nil || !strings.Contains(err.Error(), "not live") {
		t.Errorf("dead id: err = %v, want the not-live error", err)
	}
	dup := []VectorResult{got[0], got[0]}
	if _, err := tieAwareRecall(q, dup, live, k, tau); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("duplicate id: err = %v, want the duplicate error", err)
	}
	// A stale distance only slightly off (1e-3, far above crossImplTolerance)
	// is still a stale vector: the check is the tolerance, not a coarse gap.
	near := live.byID["near"]
	slightly := []VectorResult{{ID: "near", Distance: cosineDistanceF32(q, near) + 1e-3}}
	if _, err := tieAwareRecall(q, slightly, live, k, tau); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Errorf("distance 1e-3 off its current vector: err = %v, want the stale-vector error", err)
	}
}

// atCosine returns a unit vector in 4 dimensions whose cosine with (1,0,0,0) is
// c, so its cosine distance from that query is 1 - c.
func atCosine(c float64) []float32 {
	return normalize([]float32{float32(c), float32(math.Sqrt(1 - c*c)), 0, 0})
}

// TestTieAwareRecallMissBeyondTau (row 12): the hit rule itself. Every live
// distance is distinct, so there is no tie at k or k+1: "a" is the k-th
// neighbour and "b" the (k+1)-th, 5e-4 beyond τ, well outside tieEpsilon. A
// result that returns b instead of a has missed one neighbour and must score
// below 1; one that returns both true neighbours scores 1. And with fewer live
// ids than k, recall is over min(k, live n), so returning all of them is 1.
func TestTieAwareRecallMissBeyondTau(t *testing.T) {
	q := []float32{1, 0, 0, 0}
	live := newLiveSet(map[string][]float32{
		"near": atCosine(0.999),  // distance 0.001
		"a":    atCosine(0.99),   // 0.010, the k-th
		"b":    atCosine(0.9895), // 0.0105, the (k+1)-th
		"far":  atCosine(0.5),    // 0.5
	})
	const k = 2
	tau := thresholds(q, live, k)[0]
	res := func(ids ...string) []VectorResult {
		out := make([]VectorResult, len(ids))
		for i, id := range ids {
			out[i] = VectorResult{ID: id, Distance: cosineDistanceF32(q, live.byID[id])}
		}
		return out
	}
	if d := cosineDistanceF32(q, live.byID["b"]) - tau; d < 4e-4 || d > 6e-4 {
		t.Fatalf("fixture: b is %v beyond τ, want about 5e-4 (no tie at k/k+1)", d)
	}
	if r, err := tieAwareRecall(q, res("near", "a"), live, k, tau); err != nil || r != 1 {
		t.Errorf("both true neighbours: recall %v err %v, want 1", r, err)
	}
	r, err := tieAwareRecall(q, res("near", "b"), live, k, tau)
	if err != nil || r != 0.5 {
		t.Errorf("the (k+1)-th neighbour in place of the k-th: recall %v err %v, want 0.5", r, err)
	}
	if r, err := tieAwareRecall(q, res("near", "far"), live, k, tau); err != nil || r != 0.5 {
		t.Errorf("a far result in place of the k-th: recall %v err %v, want 0.5", r, err)
	}

	small := newLiveSet(map[string][]float32{"near": atCosine(0.999), "a": atCosine(0.99)})
	const bigK = 3
	smallTau := thresholds(q, small, bigK)[0]
	got := []VectorResult{{ID: "near", Distance: cosineDistanceF32(q, small.byID["near"])}, {ID: "a", Distance: cosineDistanceF32(q, small.byID["a"])}}
	if r, err := tieAwareRecall(q, got, small, bigK, smallTau); err != nil || r != 1 {
		t.Errorf("k = 3 over 2 live ids, both returned: recall %v err %v, want 1 (the denominator is min(k, live n))", r, err)
	}
}

// TestHeldOutQueriesNotInCorpus (row 13): the partition's queries and pool are
// disjoint and cover the corpus, and an index built from the pool holds no
// tombstone: queries are left out before Build, never deleted after it.
func TestHeldOutQueriesNotInCorpus(t *testing.T) {
	const n = 500
	gen := newRandomGen(13, 16)
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("p/%d", i)
	}
	vecs := gen.take(n)
	nq := heldOutQueryCount(n)
	ho := partitionHeldOut(ids, vecs, nq, 1)
	if len(ho.QueryIDs) != nq || len(ho.PoolIDs) != n-nq {
		t.Fatalf("%d queries and %d pool, want %d and %d", len(ho.QueryIDs), len(ho.PoolIDs), nq, n-nq)
	}
	pool := make(map[string]bool, len(ho.PoolIDs))
	for _, id := range ho.PoolIDs {
		pool[id] = true
	}
	for _, id := range ho.QueryIDs {
		if pool[id] {
			t.Errorf("query %s is also in the corpus pool", id)
		}
	}
	byID := zipLive(ids, vecs)
	if err := checkHeldOutVectors(ho, byID); err != nil {
		t.Error(err)
	}
	for _, c := range []struct{ union, want int }{{500, 10}, {50000, 1000}, {73531, 1000}} {
		if got := heldOutQueryCount(c.union); got != c.want {
			t.Errorf("heldOutQueryCount(%d) = %d, want %d (1,000 or 2%%, whichever is smaller)", c.union, got, c.want)
		}
	}
	idx := newHNSWIndex(16, noRebuild(provisionalHNSWParams))
	defer idx.Close()
	if err := idx.Build(ho.Pool, ho.PoolIDs); err != nil {
		t.Fatal(err)
	}
	if idx.Len() != len(ho.PoolIDs) || tombstoneCount(idx) != 0 {
		t.Errorf("Len %d with %d tombstones, want %d and 0", idx.Len(), tombstoneCount(idx), len(ho.PoolIDs))
	}
}

// gridHarnessVersion is part of the grid's run identity. Bump it whenever the
// recall definition, the churn shape, the cell runner or the hnswParams fields
// the grid holds fixed (TombstoneRatio, MinTombstones) change, so a resumed
// grid never mixes cells measured by different code.
const gridHarnessVersion = 1

// gridIdentity is what a grid cell's result depends on besides (M, ef, n).
type gridIdentity struct {
	IDsSHA256   string `json:"ids_sha256"` // over the sorted namespaced ids
	Fingerprint string `json:"fingerprint"`
	Seed        int64  `json:"seed"` // the held-out permutation seed
	NQ          int    `json:"nq"`
	Library     string `json:"library"`
	Harness     int    `json:"harness"`
}

// key is the identity's sha256, which a resume matches on.
func (g gridIdentity) key() string {
	b, _ := json.Marshal(g)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// sortedIDsHash is the sha256 over the sorted ids, NUL-separated.
func sortedIDsHash(ids []string) string {
	s := slices.Clone(ids)
	sort.Strings(s)
	h := sha256.New()
	for _, id := range s {
		h.Write([]byte(id))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// gridCell is one (M, ef, n) point of the grid.
type gridCell struct{ M, Ef, N int }

func (c gridCell) name() string { return fmt.Sprintf("M%d/ef%d/n%d", c.M, c.Ef, c.N) }

// gridResult is everything one cell records. Nothing in it is asserted.
type gridResult struct {
	RecallK10, RecallK30     float64
	SetOverlapK10            float64
	ReturnedK10, ReturnedK30 returnedStats
	BuildSeconds             float64
	HNSWK10, HNSWK30         [2]float64 // query p50, p99 in ms
	BruteK10, BruteK30       [2]float64
	HNSWHeap, BruteHeap      uint64 // retained bytes
	HNSWBuildAlloc           uint64 // TotalAlloc across the build
	ChurnRecallK10           float64
	ChurnRecallK30           float64
}

// gridLine is one fsynced line of the grid's results file.
type gridLine struct {
	Identity    string       `json:"identity"`
	Components  gridIdentity `json:"components"`
	M           int          `json:"m"`
	Ef          int          `json:"ef"`
	N           int          `json:"n"`
	Result      gridResult   `json:"result"`
	WallSeconds float64      `json:"wall_seconds"`
}

// readGridLines reads the results file, truncating a torn final line (one with
// no newline, or one that does not parse) so the next append starts clean. A
// missing file is empty.
func readGridLines(path string) ([]gridLine, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keep := len(data)
	if i := bytes.LastIndexByte(data, '\n'); i < len(data)-1 {
		keep = i + 1
	}
	var out []gridLine
	sc := bufio.NewScanner(bytes.NewReader(data[:keep]))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var l gridLine
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			out = append(out, l)
		}
	}
	if keep < len(data) {
		if err := os.Truncate(path, int64(keep)); err != nil {
			return nil, err
		}
	}
	return out, sc.Err()
}

// runGrid runs every cell not already recorded under ident, one subtest per
// cell, appending and fsyncing each result line before the next cell starts.
func runGrid(t *testing.T, path string, ident gridIdentity, cells []gridCell, run func(*testing.T, gridCell) gridResult) {
	t.Helper()
	lines, err := readGridLines(path)
	if err != nil {
		t.Fatal(err)
	}
	key := ident.key()
	done := map[gridCell]bool{}
	for _, l := range lines {
		if l.Identity == key {
			done[gridCell{l.M, l.Ef, l.N}] = true
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, c := range cells {
		if done[c] {
			t.Logf("%s: recorded under this run identity, skipped", c.name())
			continue
		}
		t.Run(c.name(), func(t *testing.T) {
			start := time.Now()
			res := run(t, c)
			if t.Failed() {
				return
			}
			b, err := json.Marshal(gridLine{Identity: key, Components: ident, M: c.M, Ef: c.Ef, N: c.N,
				Result: res, WallSeconds: time.Since(start).Seconds()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write(append(b, '\n')); err != nil {
				t.Fatal(err)
			}
			if err := f.Sync(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// writeGridTable prints the crossover table from the results file, for every
// cell recorded under ident, resumed ones included: per (M, ef), the smallest n
// at which HNSW query p50 beats brute force at k = 30 and at k = 10, beside
// build time and memory.
func writeGridTable(w io.Writer, path string, ident gridIdentity) error {
	lines, err := readGridLines(path)
	if err != nil {
		return err
	}
	key := ident.key()
	type pair struct{ M, Ef int }
	byPair := map[pair][]gridLine{}
	for _, l := range lines {
		if l.Identity == key {
			byPair[pair{l.M, l.Ef}] = append(byPair[pair{l.M, l.Ef}], l)
		}
	}
	pairs := make([]pair, 0, len(byPair))
	for p := range byPair {
		pairs = append(pairs, p)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].M != pairs[j].M {
			return pairs[i].M < pairs[j].M
		}
		return pairs[i].Ef < pairs[j].Ef
	})
	fmt.Fprintln(w, "| M | ef | n | recall@10 | recall@30 | churn recall@10 | churn recall@30 | HNSW p50@30 ms | brute p50@30 ms | HNSW p50@10 ms | brute p50@10 ms | build s | HNSW heap MiB | brute heap MiB |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, p := range pairs {
		ls := byPair[p]
		sort.Slice(ls, func(i, j int) bool { return ls[i].N < ls[j].N })
		cross30, cross10 := "none", "none"
		for _, l := range ls {
			r := l.Result
			fmt.Fprintf(w, "| %d | %d | %d | %.4f | %.4f | %.4f | %.4f | %.2f | %.2f | %.2f | %.2f | %.1f | %.1f | %.1f |\n",
				l.M, l.Ef, l.N, r.RecallK10, r.RecallK30, r.ChurnRecallK10, r.ChurnRecallK30,
				r.HNSWK30[0], r.BruteK30[0], r.HNSWK10[0], r.BruteK10[0], r.BuildSeconds,
				float64(r.HNSWHeap)/(1<<20), float64(r.BruteHeap)/(1<<20))
			if cross30 == "none" && r.HNSWK30[0] < r.BruteK30[0] {
				cross30 = fmt.Sprint(l.N)
			}
			if cross10 == "none" && r.HNSWK10[0] < r.BruteK10[0] {
				cross10 = fmt.Sprint(l.N)
			}
		}
		fmt.Fprintf(w, "crossover (M=%d, ef=%d): HNSW p50 beats brute force from n = %s at k = 30, n = %s at k = 10\n",
			p.M, p.Ef, cross30, cross10)
	}
	return nil
}

// TestHNSWMeasureGridResumes (row 14): with a stub cell runner, the grid driver
// skips the cells recorded under this run identity, re-runs a cell recorded
// under an identity that differs in any one component, ignores and truncates a
// torn final line, and appends each finished cell before the next starts.
func TestHNSWMeasureGridResumes(t *testing.T) {
	ident := gridIdentity{IDsSHA256: sortedIDsHash([]string{"p/b", "p/a"}), Fingerprint: "fp", Seed: 7, NQ: 20,
		Library: hnswLibraryVersion, Harness: gridHarnessVersion}
	others := map[string]gridIdentity{}
	for _, c := range []struct {
		name string
		edit func(*gridIdentity)
	}{
		{"ids", func(g *gridIdentity) { g.IDsSHA256 = sortedIDsHash([]string{"p/a"}) }},
		{"fingerprint", func(g *gridIdentity) { g.Fingerprint = "fp2" }},
		{"seed", func(g *gridIdentity) { g.Seed = 8 }},
		{"nq", func(g *gridIdentity) { g.NQ = 21 }},
		{"library", func(g *gridIdentity) { g.Library = "v0.0.0" }},
		{"harness", func(g *gridIdentity) { g.Harness = gridHarnessVersion + 1 }},
	} {
		g := ident
		c.edit(&g)
		others[c.name] = g
	}
	if sortedIDsHash([]string{"p/a", "p/b"}) != ident.IDsSHA256 {
		t.Error("the ids hash depends on order; it must be over the sorted ids")
	}

	path := t.TempDir() + "/grid.jsonl"
	done := gridCell{16, 100, 5000}
	line := func(g gridIdentity, c gridCell) string {
		b, _ := json.Marshal(gridLine{Identity: g.key(), Components: g, M: c.M, Ef: c.Ef, N: c.N})
		return string(b) + "\n"
	}
	// One cell per component, recorded only under the identity that differs
	// in that component: every one must re-run.
	var content strings.Builder
	content.WriteString(line(ident, done))
	var cells = []gridCell{done}
	n := 6000
	names := make([]string, 0, len(others))
	for name := range others {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := gridCell{16, 100, n}
		n += 1000
		content.WriteString(line(others[name], c))
		cells = append(cells, c)
	}
	fresh := gridCell{32, 400, 50000}
	cells = append(cells, fresh)
	content.WriteString(`{"identity":"torn`)
	if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	var ran []gridCell
	runGrid(t, path, ident, cells, func(t *testing.T, c gridCell) gridResult {
		// Every earlier cell this run finished is already on disk.
		lines, err := readGridLines(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, prev := range ran {
			found := false
			for _, l := range lines {
				if l.Identity == ident.key() && (gridCell{l.M, l.Ef, l.N}) == prev {
					found = true
				}
			}
			if !found {
				t.Errorf("starting %s, the finished cell %s is not yet in the results file", c.name(), prev.name())
			}
		}
		ran = append(ran, c)
		return gridResult{RecallK10: float64(c.N)}
	})
	if slices.Contains(ran, done) {
		t.Errorf("the cell recorded under this identity re-ran")
	}
	if len(ran) != len(cells)-1 {
		t.Errorf("ran %d cells %v, want %d: every cell recorded under another identity, and the new one, must run",
			len(ran), ran, len(cells)-1)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("torn")) {
		t.Errorf("the torn final line was not truncated:\n%s", data)
	}
	lines, err := readGridLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(lines), 1+len(others)+len(ran); got != want {
		t.Errorf("%d parseable lines, want %d", got, want)
	}
	for _, c := range []struct {
		all  int
		want []int
	}{
		{50000, []int{5000, 10000, 20000, 50000}},
		{57500, []int{5000, 10000, 20000, 50000, 57500}},
	} {
		if got := gridSizes(c.all); !slices.Equal(got, c.want) {
			t.Errorf("gridSizes(%d) = %v, want %v", c.all, got, c.want)
		}
	}
	var buf bytes.Buffer
	if err := writeGridTable(&buf, path, ident); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "crossover (M=32, ef=400)") {
		t.Errorf("the table omits a cell recorded this run:\n%s", buf.String())
	}
}
