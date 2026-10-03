// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"sync"
)

// VectorResult is a raw result from the vector index before metadata enrichment.
type VectorResult struct {
	ID       string
	Distance float32
}

// VectorIndex is the nearest-neighbour index behind one project's search. It
// has two implementations behind ONE contract, which the shared contract test
// (vector_index_contract_test.go) runs against both:
//
//   - bruteIndex: exact (100% recall) cosine search. It is what production
//     uses, and the exactness oracle every approximate index is measured
//     against.
//   - hnswIndex: an approximate HNSW graph (github.com/coder/hnsw) behind a
//     safety wrapper, for large projects. Choosing an implementation per
//     project is not this package's job yet (ADR-014, decision 6; task
//     hnsw-graph-file-envelope-and-warm-start).
//
// The contract:
//   - Build replaces the whole contents. Duplicate ids within one batch are
//     deduplicated before anything is added; the LAST occurrence wins, so Len
//     counts distinct ids.
//   - Insert adds or replaces one vector. Re-inserting an identical vector is a
//     no-op.
//   - An unusable vector (see usableVector) given to Build or Insert is skipped
//     and logged, never stored. Inserting one for an id that is already stored
//     DELETES that id, exactly as Build drops an id whose last occurrence is
//     unusable: the id's newest value cannot be searched, so no older one is.
//   - Both store a copy of the caller's slice, never the slice itself.
//   - A vector or query of the wrong dimensionality is an error.
//   - Search: k = 0 returns an empty result, k < 0 is an error, and an
//     unusable query is an error. A k larger than the index returns at most
//     Len results; brute force returns exactly min(k, Len), while an HNSW
//     graph can return fewer (its layer 0 can split into islands on
//     well-separated clusters).
//   - Close releases the index. Brute force holds nothing to release.
type VectorIndex interface {
	Build(vectors [][]float32, ids []string) error
	Insert(id string, vector []float32) error
	Search(query []float32, k int) ([]VectorResult, error)
	Delete(id string) bool
	Len() int
	Close() error
}

// indexKind names a VectorIndex implementation for newIndex.
type indexKind int

const (
	kindBrute indexKind = iota
	kindHNSW
)

// newIndex is the one construction primitive for a project's index. It never
// decides which kind a project gets; its caller does. An unknown kind is an
// error, never a silent fallback.
func newIndex(kind indexKind, dims int, params hnswParams) (VectorIndex, error) {
	switch kind {
	case kindBrute:
		return newBruteIndex(dims), nil
	case kindHNSW:
		return newHNSWIndex(dims, params), nil
	default:
		return nil, fmt.Errorf("unknown vector index kind %d", kind)
	}
}

// checkVector reports why v cannot be stored: wrong dimensionality is an error
// the caller returns, while an unusable vector is a skip (ok=false, err=nil)
// the caller logs.
func checkVector(v []float32, dims int) (ok bool, err error) {
	if len(v) != dims {
		return false, fmt.Errorf("vector has %d dims, want %d", len(v), dims)
	}
	return usableVector(v), nil
}

// The bounds on a usable vector's squared norm, computed in float32. The HNSW
// distance (vek32's cosine) divides the dot product by sqrt(|a|²·|b|²) in
// float32. Keeping every |v|² within [2^-60, 2^60] keeps that product within
// [2^-120, 2^120], inside float32's normal range, for ANY pair of usable
// vectors, so the score never underflows to a division by zero or overflows to
// a zero similarity. A NaN or infinite component fails the bounds too.
// Embeddings are L2-normalised (|v|² ≈ 1), far inside them.
const (
	minUsableNorm2 = 0x1p-60
	maxUsableNorm2 = 0x1p60
)

// usableVector reports whether cosine distance is computable for v against any
// other usable vector: its float32 squared norm lies within [minUsableNorm2,
// maxUsableNorm2]. This rejects a zero vector, a NaN or infinite component, and
// a finite vector so small or so large that its float32 norm under- or
// overflows. One rule serves both implementations, so neither stores a vector
// the other would not.
func usableVector(v []float32) bool {
	var n2 float32
	for _, x := range v {
		n2 += x * x
	}
	return n2 >= minUsableNorm2 && n2 <= maxUsableNorm2
}

// checkQuery validates a Search call's arguments, shared by both
// implementations so their errors cannot drift apart.
func checkQuery(query []float32, k, dims int) error {
	if len(query) != dims {
		return fmt.Errorf("query has %d dims, want %d", len(query), dims)
	}
	if k < 0 {
		return fmt.Errorf("search k = %d, want >= 0", k)
	}
	if !usableVector(query) {
		return fmt.Errorf("query vector is unusable: zero, non-finite, or a norm out of range")
	}
	return nil
}

// dedupBatch validates a Build batch and returns the positions to keep: one per
// distinct id, its LAST occurrence, in order of those positions. A position
// whose vector is zero or non-finite is dropped and logged, so an id whose last
// occurrence is unusable is absent from the index.
func dedupBatch(vectors [][]float32, ids []string, dims int) ([]int, error) {
	if len(vectors) != len(ids) {
		return nil, fmt.Errorf("vectors length %d != ids length %d", len(vectors), len(ids))
	}
	for i, v := range vectors {
		if len(v) != dims {
			return nil, fmt.Errorf("vector %d has %d dims, want %d", i, len(v), dims)
		}
	}
	last := make(map[string]int, len(ids))
	for i, id := range ids {
		last[id] = i
	}
	keep := make([]int, 0, len(last))
	for i, id := range ids {
		if last[id] != i {
			continue
		}
		if !usableVector(vectors[i]) {
			slog.Warn("vector index: skipping an unusable vector (zero, non-finite, or a norm out of range)", "id", id)
			continue
		}
		keep = append(keep, i)
	}
	return keep, nil
}

// equalVectors reports whether a and b hold the same values.
func equalVectors(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// vectorEntry stores a single indexed vector.
type vectorEntry struct {
	id  string
	vec []float32
}

// bruteIndex is an exact vector index: every search scores every vector by
// cosine distance. It suits collections up to ~100K vectors, and it is the
// exactness oracle the approximate hnswIndex is tested against.
type bruteIndex struct {
	mu      sync.RWMutex
	entries []vectorEntry
	byID    map[string]int // id -> index in entries
	dims    int
}

// newBruteIndex creates an empty brute-force index for the given dimensionality.
func newBruteIndex(dims int) *bruteIndex {
	return &bruteIndex{
		byID: make(map[string]int),
		dims: dims,
	}
}

// Build bulk-loads vectors into the index, replacing any existing data.
func (vi *bruteIndex) Build(vectors [][]float32, ids []string) error {
	keep, err := dedupBatch(vectors, ids, vi.dims)
	if err != nil {
		return err
	}

	entries := make([]vectorEntry, len(keep))
	byID := make(map[string]int, len(keep))
	for n, i := range keep {
		entries[n] = vectorEntry{id: ids[i], vec: slices.Clone(vectors[i])}
		byID[ids[i]] = n
	}

	vi.mu.Lock()
	vi.entries = entries
	vi.byID = byID
	vi.mu.Unlock()
	return nil
}

// Insert adds or replaces a single vector in the index.
func (vi *bruteIndex) Insert(id string, vector []float32) error {
	ok, err := checkVector(vector, vi.dims)
	if err != nil {
		return err
	}
	if !ok {
		slog.Warn("vector index: skipping an unusable vector (zero, non-finite, or a norm out of range); deleting any stored value", "id", id)
		vi.Delete(id)
		return nil
	}
	vector = slices.Clone(vector)

	vi.mu.Lock()
	defer vi.mu.Unlock()

	if idx, ok := vi.byID[id]; ok {
		vi.entries[idx].vec = vector
		return nil
	}
	vi.byID[id] = len(vi.entries)
	vi.entries = append(vi.entries, vectorEntry{id: id, vec: vector})
	return nil
}

// Search returns the k nearest neighbors to query, sorted by distance.
// Uses cosine distance (1 - cosine_similarity). For L2-normalized vectors,
// this produces the same ranking as euclidean distance.
func (vi *bruteIndex) Search(query []float32, k int) ([]VectorResult, error) {
	if err := checkQuery(query, k, vi.dims); err != nil {
		return nil, err
	}
	if k == 0 {
		return nil, nil
	}

	vi.mu.RLock()
	defer vi.mu.RUnlock()

	if len(vi.entries) == 0 {
		return nil, nil
	}

	results := make([]VectorResult, len(vi.entries))
	for i, e := range vi.entries {
		results[i] = VectorResult{
			ID:       e.id,
			Distance: cosineDistanceF32(query, e.vec),
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Distance < results[j].Distance
	})

	if k > len(results) {
		k = len(results)
	}
	return results[:k], nil
}

// Delete removes a vector from the index. Returns true if found and deleted.
func (vi *bruteIndex) Delete(id string) bool {
	vi.mu.Lock()
	defer vi.mu.Unlock()

	idx, ok := vi.byID[id]
	if !ok {
		return false
	}

	last := len(vi.entries) - 1
	if idx != last {
		vi.entries[idx] = vi.entries[last]
		vi.byID[vi.entries[idx].id] = idx
	}
	vi.entries = vi.entries[:last]
	delete(vi.byID, id)
	return true
}

// Len returns the number of vectors in the index.
func (vi *bruteIndex) Len() int {
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	return len(vi.entries)
}

// Close is a no-op: brute force owns no goroutine and no file.
func (vi *bruteIndex) Close() error { return nil }

// cosineDistanceF32 computes 1 - cosine_similarity between two float32 vectors.
func cosineDistanceF32(a, b []float32) float32 {
	var dot, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	if normA == 0 || normB == 0 {
		return 1
	}
	return float32(1 - dot/(math.Sqrt(normA)*math.Sqrt(normB)))
}
