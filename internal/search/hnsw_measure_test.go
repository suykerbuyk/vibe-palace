// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"
)

// TestHNSWTombstoneMeasurement (VP_HNSW_MEASURE=1, run by hand, asserts
// nothing): the measurement behind provisionalHNSWParams.TombstoneRatio. For a
// seeded random corpus of 5k and 20k vectors it tombstones 0, 5, 10, 15 and 20%
// of the ids in turn and logs recall@10 against brute force, distance
// evaluations per query and wall-clock latency per query. It then starts one
// rebuild at the 20% point and lands writes on the index while it builds, and
// logs how long the swap's replay of those writes took. The table is recorded
// in task vector-index-interface-and-coder-hnsw-wrapper.
func TestHNSWTombstoneMeasurement(t *testing.T) {
	requireHNSWMeasure(t)
	for _, n := range []int{5000, 20000} {
		measureTombstones(t, n)
	}
}

// requireHNSWMeasure skips a test unless VP_HNSW_MEASURE=1, the gate of every
// HNSW measurement: this by-hand table and the tests of the three
// hnsw-measure* targets (ADR-014: an environment gate, not a build tag). It is
// never set by `make test`, and -short alone cannot confine these tests,
// because `make model-test` runs this package without -short.
func requireHNSWMeasure(t *testing.T) {
	t.Helper()
	if os.Getenv("VP_HNSW_MEASURE") != "1" {
		t.Skip("measurement: set VP_HNSW_MEASURE=1")
	}
}

func measureTombstones(t *testing.T, n int) {
	const k, nq = 10, 100
	gen := newRandomGen(int64(n), embeddingDims)
	vecs := gen.take(n)
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("m%d", i)
	}
	params := noRebuild(provisionalHNSWParams)
	params.distance = countingCosine
	idx := newHNSWIndex(embeddingDims, params)
	start := time.Now()
	if err := idx.Build(vecs, ids); err != nil {
		t.Fatal(err)
	}
	t.Logf("n=%d build %.1fs", n, time.Since(start).Seconds())
	live := make(map[string][]float32, n)
	for i, id := range ids {
		live[id] = vecs[i]
	}
	queries := gen.take(nq)
	order := rand.New(rand.NewSource(int64(n) + 1)).Perm(n)
	deleted := 0

	t.Logf("| n | tombstones | recall@10 | distance evals/query | library searches/query | latency/query |")
	for _, pct := range []int{0, 5, 10, 15, 20} {
		for ; deleted < n*pct/100; deleted++ {
			id := ids[order[deleted]]
			idx.Delete(id)
			delete(live, id)
		}
		oracle := newBruteIndex(embeddingDims)
		for id, v := range live {
			if err := oracle.Insert(id, v); err != nil {
				t.Fatal(err)
			}
		}
		var recall float64
		var elapsed time.Duration
		evals0, calls0 := distanceEvals.Load(), idx.librarySearches.Load()
		for _, q := range queries {
			s := time.Now()
			got, err := idx.Search(q, k)
			elapsed += time.Since(s)
			if err != nil {
				t.Fatal(err)
			}
			want, err := oracle.Search(q, k)
			if err != nil {
				t.Fatal(err)
			}
			recall += recallAt(got, want)
		}
		t.Logf("| %d | %d%% | %.3f | %.0f | %.2f | %.2f ms |", n, pct, recall/nq,
			float64(distanceEvals.Load()-evals0)/nq,
			float64(idx.librarySearches.Load()-calls0)/nq,
			float64(elapsed.Microseconds())/1000/nq)
	}

	// One rebuild at 20%, with writes landing while it builds.
	idx.mu.Lock()
	idx.params.TombstoneRatio = 0.10
	idx.params.MinTombstones = 1
	idx.mu.Unlock()
	idx.Delete(ids[order[deleted]])
	deleted++
	b := currentRebuild(idx)
	if b == nil {
		t.Fatal("no rebuild started at 20% tombstones")
	}
	// The worst case: a writer running flat out, on the live upper half of
	// the ids, until the rebuild has swapped.
	writes := 0
	rng := rand.New(rand.NewSource(int64(n) + 2))
	upper := ids[n/2:]
	start = time.Now()
writing:
	for {
		select {
		case <-b.done:
			break writing
		default:
		}
		id := upper[rng.Intn(len(upper))]
		if writes%2 == 0 {
			_ = idx.Insert(id, gen.next())
		} else {
			idx.Delete(id)
		}
		writes++
	}
	<-b.done
	t.Logf("n=%d rebuild at 20%%: %.1fs with %d writes landing during it; swaps=%d abandoned=%d; last swap: %d off-lock catch-up rounds, then %d mutations replayed in %.2f ms under the write lock",
		n, time.Since(start).Seconds(), writes, idx.swaps.Load(), idx.abandoned.Load(),
		idx.lastCatchUpRounds.Load(), idx.lastReplayLen.Load(), float64(idx.lastReplayNanos.Load())/1e6)
}
