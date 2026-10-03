// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"bytes"
	"log/slog"
	"math"
	"math/rand"
	"strings"
	"testing"
)

// contractImpls is every VectorIndex implementation. The contract tests below
// run against each, so the two cannot drift into two contracts.
var contractImpls = []struct {
	name string
	make func(dims int) VectorIndex
}{
	{"brute", func(dims int) VectorIndex { return newBruteIndex(dims) }},
	{"hnsw", func(dims int) VectorIndex { return newHNSWIndex(dims, provisionalHNSWParams) }},
}

// forEachImpl runs fn as one subtest per implementation.
func forEachImpl(t *testing.T, fn func(t *testing.T, mk func(dims int) VectorIndex)) {
	t.Helper()
	for _, impl := range contractImpls {
		t.Run(impl.name, func(t *testing.T) { fn(t, impl.make) })
	}
}

// captureLog routes the default slog logger into a buffer for one test. It
// swaps a process-wide default, so a test using it must not run in parallel.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// distanceTo returns id's distance in results, or -1 when id is absent.
func distanceTo(results []VectorResult, id string) float32 {
	for _, r := range results {
		if r.ID == id {
			return r.Distance
		}
	}
	return -1
}

// TestContractBuildDuplicateIDsLastWins: Build deduplicates ids within a batch
// before adding anything, and the last occurrence wins. Brute force at HEAD
// kept both entries (Len 3) and only remembered the last in byID.
func TestContractBuildDuplicateIDsLastWins(t *testing.T) {
	forEachImpl(t, func(t *testing.T, mk func(int) VectorIndex) {
		idx := mk(3)
		err := idx.Build(
			[][]float32{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}},
			[]string{"a", "b", "a"},
		)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got := idx.Len(); got != 2 {
			t.Fatalf("Len = %d, want 2 distinct ids", got)
		}
		res, err := idx.Search([]float32{1, 0, 0}, 10)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(res) != 2 {
			t.Fatalf("Search returned %d results, want 2 (one per distinct id): %v", len(res), res)
		}
		// a's first vector {1,0,0} was superseded by {0,0,1}: a sits at
		// distance 1 from the query, not 0.
		if d := distanceTo(res, "a"); math.Abs(float64(d)-1) > 1e-5 {
			t.Errorf("a's distance = %v, want 1 (the LAST occurrence, {0,0,1})", d)
		}
	})
}

// TestContractDuplicateThenDeleteLeavesNoGhost: deleting an id that Build saw
// twice removes it entirely. At HEAD brute force deleted only the entry byID
// pointed at, and the earlier duplicate stayed in entries, returned by every
// search, forever.
func TestContractDuplicateThenDeleteLeavesNoGhost(t *testing.T) {
	forEachImpl(t, func(t *testing.T, mk func(int) VectorIndex) {
		idx := mk(3)
		if err := idx.Build(
			[][]float32{{1, 0, 0}, {0, 1, 0}, {0.9, 0.1, 0}},
			[]string{"a", "b", "a"},
		); err != nil {
			t.Fatalf("Build: %v", err)
		}
		if !idx.Delete("a") {
			t.Fatal("Delete(a) = false, want true")
		}
		if got := idx.Len(); got != 1 {
			t.Errorf("Len = %d after deleting a, want 1", got)
		}
		res, err := idx.Search([]float32{1, 0, 0}, 10)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if distanceTo(res, "a") >= 0 {
			t.Errorf("deleted id a is still returned: %v", res)
		}
	})
}

// TestContractKZeroIsEmpty pins k = 0 → an empty result and no error.
func TestContractKZeroIsEmpty(t *testing.T) {
	forEachImpl(t, func(t *testing.T, mk func(int) VectorIndex) {
		idx := mk(3)
		if err := idx.Insert("a", []float32{1, 0, 0}); err != nil {
			t.Fatal(err)
		}
		res, err := idx.Search([]float32{1, 0, 0}, 0)
		if err != nil {
			t.Fatalf("Search k=0: %v", err)
		}
		if len(res) != 0 {
			t.Errorf("Search k=0 = %v, want empty", res)
		}
	})
}

// TestContractKNegativeIsError: at HEAD brute force returned an empty result
// for k < 0, the same as for k = 0.
func TestContractKNegativeIsError(t *testing.T) {
	forEachImpl(t, func(t *testing.T, mk func(int) VectorIndex) {
		idx := mk(3)
		if err := idx.Insert("a", []float32{1, 0, 0}); err != nil {
			t.Fatal(err)
		}
		if res, err := idx.Search([]float32{1, 0, 0}, -1); err == nil {
			t.Errorf("Search k=-1 = %v, nil error; want an error", res)
		}
	})
}

// TestContractUnusableVectorsAreSkippedAndLogged: a zero, NaN or infinite
// vector is never stored, by Build or by Insert, and each skip is logged with
// its id. At HEAD brute force stored them all.
func TestContractUnusableVectorsAreSkippedAndLogged(t *testing.T) {
	nan := float32(math.NaN())
	inf := float32(math.Inf(1))
	forEachImpl(t, func(t *testing.T, mk func(int) VectorIndex) {
		logs := captureLog(t)
		idx := mk(3)
		if err := idx.Build(
			[][]float32{{1, 0, 0}, {0, 0, 0}, {nan, 1, 0}, {0, inf, 0}},
			[]string{"good", "zero", "nan", "inf"},
		); err != nil {
			t.Fatalf("Build: %v", err)
		}
		if got := idx.Len(); got != 1 {
			t.Errorf("Len after Build = %d, want 1 (only 'good')", got)
		}
		for _, tc := range []struct {
			id  string
			vec []float32
		}{{"zero2", []float32{0, 0, 0}}, {"nan2", []float32{0, nan, 0}}} {
			if err := idx.Insert(tc.id, tc.vec); err != nil {
				t.Errorf("Insert(%s) = %v, want nil (a skip, not an error)", tc.id, err)
			}
		}
		if got := idx.Len(); got != 1 {
			t.Errorf("Len after Inserts = %d, want 1", got)
		}
		res, err := idx.Search([]float32{0, 1, 0}, 10)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(res) != 1 || res[0].ID != "good" {
			t.Errorf("Search = %v, want only 'good'", res)
		}
		for _, id := range []string{"zero", "nan", "inf", "zero2", "nan2"} {
			if !strings.Contains(logs.String(), "id="+id+"\n") {
				t.Errorf("no log line names skipped id %q; log:\n%s", id, logs)
			}
		}
	})
}

// TestContractUnusableQueryIsError: a zero, NaN or infinite query is an error.
// At HEAD brute force scored a zero query 1.0 against every vector and returned
// them all.
func TestContractUnusableQueryIsError(t *testing.T) {
	forEachImpl(t, func(t *testing.T, mk func(int) VectorIndex) {
		idx := mk(3)
		if err := idx.Insert("a", []float32{1, 0, 0}); err != nil {
			t.Fatal(err)
		}
		for name, q := range map[string][]float32{
			"zero": {0, 0, 0},
			"nan":  {float32(math.NaN()), 1, 0},
			"inf":  {float32(math.Inf(-1)), 1, 0},
		} {
			if res, err := idx.Search(q, 5); err == nil {
				t.Errorf("Search(%s query) = %v, nil error; want an error", name, res)
			}
		}
	})
}

// TestContractWrongDimsIsError: Build, Insert and Search each refuse a vector
// of the wrong dimensionality.
func TestContractWrongDimsIsError(t *testing.T) {
	forEachImpl(t, func(t *testing.T, mk func(int) VectorIndex) {
		idx := mk(3)
		if err := idx.Build([][]float32{{1, 0, 0}, {1, 0}}, []string{"a", "b"}); err == nil {
			t.Error("Build with a 2-dim vector: nil error")
		}
		if err := idx.Insert("a", []float32{1, 0, 0, 0}); err == nil {
			t.Error("Insert with a 4-dim vector: nil error")
		}
		if err := idx.Insert("a", []float32{1, 0, 0}); err != nil {
			t.Fatal(err)
		}
		if _, err := idx.Search([]float32{1, 0}, 1); err == nil {
			t.Error("Search with a 2-dim query: nil error")
		}
	})
}

// TestContractInsertUpserts: inserting an existing id replaces its vector.
func TestContractInsertUpserts(t *testing.T) {
	forEachImpl(t, func(t *testing.T, mk func(int) VectorIndex) {
		idx := mk(3)
		for _, v := range [][]float32{{1, 0, 0}, {0, 1, 0}} {
			if err := idx.Insert("a", v); err != nil {
				t.Fatal(err)
			}
		}
		if err := idx.Insert("b", []float32{0, 0, 1}); err != nil {
			t.Fatal(err)
		}
		if got := idx.Len(); got != 2 {
			t.Errorf("Len = %d, want 2", got)
		}
		res, err := idx.Search([]float32{0, 1, 0}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 1 || res[0].ID != "a" || math.Abs(float64(res[0].Distance)) > 1e-5 {
			t.Errorf("Search(a's new vector) = %v, want a at distance 0", res)
		}
		res, err = idx.Search([]float32{1, 0, 0}, 10)
		if err != nil {
			t.Fatal(err)
		}
		if d := distanceTo(res, "a"); math.Abs(float64(d)-1) > 1e-5 {
			t.Errorf("a's distance from its OLD vector = %v, want 1: %v", d, res)
		}
	})
}

// crossImplTolerance bounds how far the two implementations' distances may
// differ for the same pair: brute force scores in float64 and rounds once,
// while the HNSW distance is vek32's float32 SIMD cosine.
const crossImplTolerance = 1e-5

// TestContractImplementationsAgree: on a corpus small enough that the HNSW
// search at the provisional EfSearch is exhaustive, both implementations
// return the same ids, in the same order, with distances within
// crossImplTolerance.
func TestContractImplementationsAgree(t *testing.T) {
	const dims, n, k = 32, 200, 10
	rng := rand.New(rand.NewSource(7))
	vecs := make([][]float32, n)
	ids := make([]string, n)
	for i := range vecs {
		vecs[i] = randomUnitVector(rng, dims)
		ids[i] = "v" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	brute := newBruteIndex(dims)
	hn := newHNSWIndex(dims, provisionalHNSWParams)
	for _, idx := range []VectorIndex{brute, hn} {
		if err := idx.Build(vecs, ids); err != nil {
			t.Fatal(err)
		}
	}
	for q := range 20 {
		query := randomUnitVector(rng, dims)
		want, err := brute.Search(query, k)
		if err != nil {
			t.Fatal(err)
		}
		got, err := hn.Search(query, k)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("query %d: hnsw returned %d results, brute %d", q, len(got), len(want))
		}
		for i := range want {
			if got[i].ID != want[i].ID {
				t.Errorf("query %d rank %d: hnsw %s, brute %s", q, i, got[i].ID, want[i].ID)
			}
			if diff := math.Abs(float64(got[i].Distance - want[i].Distance)); diff > crossImplTolerance {
				t.Errorf("query %d rank %d: distance differs by %g (> %g)", q, i, diff, crossImplTolerance)
			}
		}
	}
}

// TestContractHugeKIsBounded: a k far beyond the index (here 1<<40) never sizes
// an allocation by k. The HNSW request is capped at the graph's size; brute
// force clamps to Len. On this three-vector index both return every vector;
// that is not a general claim for HNSW (see the VectorIndex contract).
func TestContractHugeKIsBounded(t *testing.T) {
	forEachImpl(t, func(t *testing.T, mk func(int) VectorIndex) {
		idx := mk(3)
		for id, v := range map[string][]float32{"a": {1, 0, 0}, "b": {0, 1, 0}, "c": {0, 0, 1}} {
			if err := idx.Insert(id, v); err != nil {
				t.Fatal(err)
			}
		}
		res, err := idx.Search([]float32{1, 0.1, 0}, 1<<40)
		if err != nil {
			t.Fatalf("Search k=1<<40: %v", err)
		}
		if len(res) != 3 || res[0].ID != "a" {
			t.Errorf("Search k=1<<40 = %v, want all three vectors, a first", res)
		}
	})
}

// TestContractExtremeNormVectorsAreUnusable: a finite vector whose float32
// squared norm leaves [2^-60, 2^60] is unusable, in Build, Insert and as a
// query, for both implementations alike. Below the range the HNSW cosine
// divides by an underflowed zero; above it the norm product overflows. Each
// case was stored by brute force and silently unsearchable in HNSW before.
func TestContractExtremeNormVectorsAreUnusable(t *testing.T) {
	extremes := map[string][]float32{
		"underflow": {1e-30, 1e-30, 0}, // |v|² underflows float32 to 0
		"overflow":  {3e38, 3e38, 0},   // |v|² overflows float32 to +Inf
		"too-small": {1e-10, 0, 0},     // |v|² = 1e-20 < 2^-60
		"too-large": {1e10, 0, 0},      // |v|² = 1e20 > 2^60
	}
	forEachImpl(t, func(t *testing.T, mk func(int) VectorIndex) {
		captureLog(t)
		for name, v := range extremes {
			idx := mk(3)
			if err := idx.Build([][]float32{{0, 1, 0}, v}, []string{"ok", name}); err != nil {
				t.Fatalf("%s: Build: %v", name, err)
			}
			if err := idx.Insert(name+"-ins", v); err != nil {
				t.Fatalf("%s: Insert: %v", name, err)
			}
			if got := idx.Len(); got != 1 {
				t.Errorf("%s: Len = %d, want 1 (only 'ok' stored)", name, got)
			}
			if res, err := idx.Search(v, 5); err == nil {
				t.Errorf("%s: Search with it as the query = %v, nil error; want an error", name, res)
			}
		}
		// Inside the range, a norm far from 1 is still usable.
		idx := mk(3)
		if err := idx.Insert("big", []float32{1e4, 0, 0}); err != nil {
			t.Fatal(err)
		}
		if got := idx.Len(); got != 1 {
			t.Errorf("a vector of norm 1e4 was not stored (Len %d)", got)
		}
	})
}

// TestContractUnusableInsertDeletesTheID: inserting an unusable vector for an
// id that is stored deletes it, so its older vector is no longer searchable.
// Build already treats an id whose last occurrence is unusable as absent.
func TestContractUnusableInsertDeletesTheID(t *testing.T) {
	forEachImpl(t, func(t *testing.T, mk func(int) VectorIndex) {
		captureLog(t)
		idx := mk(3)
		for id, v := range map[string][]float32{"a": {1, 0, 0}, "b": {0, 1, 0}} {
			if err := idx.Insert(id, v); err != nil {
				t.Fatal(err)
			}
		}
		if err := idx.Insert("a", []float32{0, 0, 0}); err != nil {
			t.Fatalf("Insert(a, zero) = %v, want nil", err)
		}
		if got := idx.Len(); got != 1 {
			t.Errorf("Len = %d, want 1", got)
		}
		res, err := idx.Search([]float32{1, 0, 0}, 5)
		if err != nil {
			t.Fatal(err)
		}
		if distanceTo(res, "a") >= 0 {
			t.Errorf("a's old vector is still searchable after an unusable upsert: %v", res)
		}
	})
}

// TestContractStoresACopy: mutating a slice after handing it to Build or
// Insert changes nothing the index stores.
func TestContractStoresACopy(t *testing.T) {
	forEachImpl(t, func(t *testing.T, mk func(int) VectorIndex) {
		idx := mk(3)
		built := []float32{1, 0, 0}
		if err := idx.Build([][]float32{built}, []string{"b"}); err != nil {
			t.Fatal(err)
		}
		inserted := []float32{0, 1, 0}
		if err := idx.Insert("i", inserted); err != nil {
			t.Fatal(err)
		}
		built[0], built[2] = 0, 1
		inserted[1], inserted[2] = 0, 1
		for id, q := range map[string][]float32{"b": {1, 0, 0}, "i": {0, 1, 0}} {
			res, err := idx.Search(q, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(res) != 1 || res[0].ID != id || math.Abs(float64(res[0].Distance)) > 1e-5 {
				t.Errorf("Search(%s's original vector) = %v, want %s at distance 0: the caller's later write leaked in", id, res, id)
			}
		}
	})
}

// TestNewIndexUnknownKindIsError: newIndex refuses a kind it does not know
// rather than falling back to one it does.
func TestNewIndexUnknownKindIsError(t *testing.T) {
	if idx, err := newIndex(indexKind(99), 3, provisionalHNSWParams); err == nil {
		t.Errorf("newIndex(kind 99) = %T, nil error; want an error", idx)
	}
	for _, kind := range []indexKind{kindBrute, kindHNSW} {
		if _, err := newIndex(kind, 3, provisionalHNSWParams); err != nil {
			t.Errorf("newIndex(kind %d): %v", kind, err)
		}
	}
}

// TestCandidateCountSaturates: an absurd search limit (vp search -n) cannot
// overflow the candidate count into a negative k, which Search refuses.
func TestCandidateCountSaturates(t *testing.T) {
	if got := candidateCount(10); got != 30 {
		t.Errorf("candidateCount(10) = %d, want 30", got)
	}
	for _, limit := range []int{math.MaxInt, math.MaxInt/3 + 1, 1 << 62} {
		if got := candidateCount(limit); got <= 0 {
			t.Errorf("candidateCount(%d) = %d, want a positive count", limit, got)
		}
	}
}
