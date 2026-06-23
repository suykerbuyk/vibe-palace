// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"fmt"
	"math"
	"sort"
	"sync"
)

// VectorResult is a raw result from the vector index before metadata enrichment.
type VectorResult struct {
	ID       string
	Distance float32
}

// vectorEntry stores a single indexed vector.
type vectorEntry struct {
	id  string
	vec []float32
}

// VectorIndex is a brute-force vector search index with cosine distance.
// It provides exact (100% recall) nearest-neighbor search suitable for
// collections up to ~100K vectors. For larger collections, this could be
// swapped for an approximate index such as HNSW (deferred — validated
// upstream `coder/hnsw@main`, parked behind this boundary).
type VectorIndex struct {
	mu      sync.RWMutex
	entries []vectorEntry
	byID    map[string]int // id -> index in entries
	dims    int
}

// NewVectorIndex creates an empty vector index for the given dimensionality.
func NewVectorIndex(dims int) *VectorIndex {
	return &VectorIndex{
		byID: make(map[string]int),
		dims: dims,
	}
}

// Build bulk-loads vectors into the index, replacing any existing data.
func (vi *VectorIndex) Build(vectors [][]float32, ids []string) error {
	if len(vectors) != len(ids) {
		return fmt.Errorf("vectors length %d != ids length %d", len(vectors), len(ids))
	}
	for i, v := range vectors {
		if len(v) != vi.dims {
			return fmt.Errorf("vector %d has %d dims, want %d", i, len(v), vi.dims)
		}
	}

	entries := make([]vectorEntry, len(vectors))
	byID := make(map[string]int, len(vectors))
	for i := range vectors {
		entries[i] = vectorEntry{id: ids[i], vec: vectors[i]}
		byID[ids[i]] = i
	}

	vi.mu.Lock()
	vi.entries = entries
	vi.byID = byID
	vi.mu.Unlock()
	return nil
}

// Insert adds or replaces a single vector in the index.
func (vi *VectorIndex) Insert(id string, vector []float32) error {
	if len(vector) != vi.dims {
		return fmt.Errorf("vector has %d dims, want %d", len(vector), vi.dims)
	}

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
func (vi *VectorIndex) Search(query []float32, k int) ([]VectorResult, error) {
	if len(query) != vi.dims {
		return nil, fmt.Errorf("query has %d dims, want %d", len(query), vi.dims)
	}
	if k <= 0 {
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
func (vi *VectorIndex) Delete(id string) bool {
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
func (vi *VectorIndex) Len() int {
	vi.mu.RLock()
	defer vi.mu.RUnlock()
	return len(vi.entries)
}

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
