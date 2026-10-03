// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"math"
	"math/rand"
	"os"
	"testing"
)

// Seeded synthetic corpora for the HNSW tests. Task
// hnsw-parameters-from-real-vector-recall-and-production-wiring's 5k gate reuses
// clusteredGen, so the churn floor here and that gate measure the same data.

// embeddingDims is MiniLM's output dimensionality, the shape of every
// production vector.
const embeddingDims = 384

// requireSlowHNSW skips a test unless VP_HNSW_SLOW=1. The slow HNSW tests run
// only in the `hnsw` CI job: `-short` alone cannot confine them, because
// `make model-test` runs this package without -short (its integration test
// loads the real model).
func requireSlowHNSW(t *testing.T) {
	t.Helper()
	if os.Getenv("VP_HNSW_SLOW") != "1" {
		t.Skip("slow HNSW test: set VP_HNSW_SLOW=1 (the hnsw CI job does)")
	}
}

// randomUnitVector returns an L2-normalised vector with Gaussian components.
func randomUnitVector(rng *rand.Rand, dims int) []float32 {
	v := make([]float32, dims)
	for i := range v {
		v[i] = float32(rng.NormFloat64())
	}
	return normalize(v)
}

// normalize scales v to unit length in place and returns it.
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	n := float32(math.Sqrt(sum))
	for i := range v {
		v[i] /= n
	}
	return v
}

// clusteredGen draws L2-normalised vectors around a fixed set of random unit
// centres: each vector is a centre plus Gaussian noise of standard deviation
// spread per component, renormalised. Real embeddings are clustered by topic.
// Every draw, whether a corpus vector, a replacement vector or a query, comes
// from the SAME centres, so queries land where the corpus is.
type clusteredGen struct {
	rng     *rand.Rand
	centres [][]float32
	spread  float64
}

// newClusteredGen seeds a generator with `clusters` centres in dims dimensions.
func newClusteredGen(seed int64, dims, clusters int, spread float64) *clusteredGen {
	rng := rand.New(rand.NewSource(seed))
	centres := make([][]float32, clusters)
	for i := range centres {
		centres[i] = randomUnitVector(rng, dims)
	}
	return &clusteredGen{rng: rng, centres: centres, spread: spread}
}

// next draws one vector.
func (g *clusteredGen) next() []float32 {
	c := g.centres[g.rng.Intn(len(g.centres))]
	v := make([]float32, len(c))
	for j := range v {
		v[j] = c[j] + float32(g.rng.NormFloat64()*g.spread)
	}
	return normalize(v)
}

// take draws n vectors.
func (g *clusteredGen) take(n int) [][]float32 {
	out := make([][]float32, n)
	for i := range out {
		out[i] = g.next()
	}
	return out
}

// newStandardClusteredGen is the seeded 384-dimension clustered corpus the HNSW
// recall tests share (200 centres, per-component spread 0.05).
func newStandardClusteredGen(seed int64) *clusteredGen {
	return newClusteredGen(seed, embeddingDims, 200, 0.05)
}

// randomGen draws uniformly random L2-normalised vectors: no structure at all,
// the hardest case for an approximate index, so EfSearch moves recall most.
type randomGen struct {
	rng  *rand.Rand
	dims int
}

// newRandomGen seeds a random-vector generator in dims dimensions.
func newRandomGen(seed int64, dims int) *randomGen {
	return &randomGen{rng: rand.New(rand.NewSource(seed)), dims: dims}
}

// next draws one vector.
func (g *randomGen) next() []float32 { return randomUnitVector(g.rng, g.dims) }

// take draws n vectors.
func (g *randomGen) take(n int) [][]float32 {
	out := make([][]float32, n)
	for i := range out {
		out[i] = g.next()
	}
	return out
}

// recallAt returns |got ∩ want| / |want| over result ids.
func recallAt(got, want []VectorResult) float64 {
	if len(want) == 0 {
		return 1
	}
	in := make(map[string]bool, len(want))
	for _, r := range want {
		in[r.ID] = true
	}
	hits := 0
	for _, r := range got {
		if in[r.ID] {
			hits++
		}
	}
	return float64(hits) / float64(len(want))
}
