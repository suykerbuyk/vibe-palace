// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"sync"
	"testing"
)

func TestBuildAndSearch(t *testing.T) {
	idx := NewVectorIndex(3)

	vectors := [][]float32{
		{1, 0, 0},
		{0, 1, 0},
		{0, 0, 1},
		{0.9, 0.1, 0},
	}
	ids := []string{"x", "y", "z", "near-x"}

	if err := idx.Build(vectors, ids); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if idx.Len() != 4 {
		t.Fatalf("Len = %d, want 4", idx.Len())
	}

	results, err := idx.Search([]float32{1, 0, 0}, 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	// Closest to [1,0,0] should be "x" (distance 0), then "near-x".
	if results[0].ID != "x" {
		t.Errorf("nearest = %q, want x", results[0].ID)
	}
	if results[1].ID != "near-x" {
		t.Errorf("second nearest = %q, want near-x", results[1].ID)
	}
}

func TestExactRecall(t *testing.T) {
	dims := 32
	n := 500
	k := 10
	rng := rand.New(rand.NewSource(42))

	vectors := make([][]float32, n)
	ids := make([]string, n)
	for i := range n {
		vec := make([]float32, dims)
		for j := range vec {
			vec[j] = rng.Float32()*2 - 1
		}
		vectors[i] = vec
		ids[i] = fmt.Sprintf("v%d", i)
	}

	idx := NewVectorIndex(dims)
	if err := idx.Build(vectors, ids); err != nil {
		t.Fatal(err)
	}

	query := make([]float32, dims)
	for j := range query {
		query[j] = rng.Float32()*2 - 1
	}

	// Index search.
	indexResults, err := idx.Search(query, k)
	if err != nil {
		t.Fatal(err)
	}

	// Independent brute-force for verification.
	type distID struct {
		id   string
		dist float64
	}
	var brute []distID
	for i, v := range vectors {
		brute = append(brute, distID{ids[i], testCosineDistance(query, v)})
	}
	sort.Slice(brute, func(i, j int) bool { return brute[i].dist < brute[j].dist })

	// Brute-force index must have 100% recall.
	for i := range k {
		if indexResults[i].ID != brute[i].id {
			t.Errorf("rank %d: got %s, want %s", i, indexResults[i].ID, brute[i].id)
		}
	}
}

func TestIncrementalInsert(t *testing.T) {
	idx := NewVectorIndex(3)

	if err := idx.Insert("a", []float32{1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Insert("b", []float32{0, 1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := idx.Insert("c", []float32{0, 0, 1}); err != nil {
		t.Fatal(err)
	}

	if idx.Len() != 3 {
		t.Fatalf("Len = %d, want 3", idx.Len())
	}

	results, err := idx.Search([]float32{1, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "a" {
		t.Errorf("expected nearest to be 'a', got %v", results)
	}
}

func TestInsertReplace(t *testing.T) {
	idx := NewVectorIndex(3)
	_ = idx.Insert("a", []float32{1, 0, 0})
	_ = idx.Insert("a", []float32{0, 1, 0}) // replace

	if idx.Len() != 1 {
		t.Fatalf("Len = %d, want 1 after replace", idx.Len())
	}

	results, _ := idx.Search([]float32{0, 1, 0}, 1)
	if results[0].ID != "a" || results[0].Distance > 0.001 {
		t.Errorf("replaced vector not found correctly: %+v", results[0])
	}
}

func TestDelete(t *testing.T) {
	idx := NewVectorIndex(3)
	_ = idx.Insert("a", []float32{1, 0, 0})
	_ = idx.Insert("b", []float32{0, 1, 0})

	if !idx.Delete("a") {
		t.Error("Delete should return true for existing key")
	}
	if idx.Len() != 1 {
		t.Errorf("Len = %d, want 1", idx.Len())
	}

	results, _ := idx.Search([]float32{1, 0, 0}, 2)
	for _, r := range results {
		if r.ID == "a" {
			t.Error("deleted key 'a' still appears in results")
		}
	}
}

func TestDeleteNonexistent(t *testing.T) {
	idx := NewVectorIndex(3)
	if idx.Delete("nope") {
		t.Error("Delete should return false for nonexistent key")
	}
}

func TestConcurrentSearchInsert(t *testing.T) {
	idx := NewVectorIndex(3)
	_ = idx.Insert("seed", []float32{0.5, 0.5, 0.5})

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v := float32(i) / 50.0
			_ = idx.Insert(
				fmt.Sprintf("ins-%d", i),
				[]float32{v, 1 - v, 0.5},
			)
		}(i)
	}
	for range 50 {
		wg.Go(func() {
			_, _ = idx.Search([]float32{0.5, 0.5, 0.5}, 5)
		})
	}
	wg.Wait()

	if idx.Len() < 10 {
		t.Errorf("expected at least 10 vectors after concurrent ops, got %d", idx.Len())
	}
}

func TestEmptyIndex(t *testing.T) {
	idx := NewVectorIndex(3)
	results, err := idx.Search([]float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results from empty index, got %d", len(results))
	}
}

func TestDimensionMismatch(t *testing.T) {
	idx := NewVectorIndex(3)
	err := idx.Insert("a", []float32{1, 0})
	if err == nil {
		t.Error("expected error for dimension mismatch on insert")
	}

	_ = idx.Insert("a", []float32{1, 0, 0})
	_, err = idx.Search([]float32{1, 0}, 1)
	if err == nil {
		t.Error("expected error for dimension mismatch on search")
	}
}

func TestBuildMismatchedLengths(t *testing.T) {
	idx := NewVectorIndex(3)
	err := idx.Build([][]float32{{1, 0, 0}}, []string{"a", "b"})
	if err == nil {
		t.Error("expected error for mismatched vectors/ids lengths")
	}
}

func TestSearchKZero(t *testing.T) {
	idx := NewVectorIndex(3)
	_ = idx.Insert("a", []float32{1, 0, 0})
	results, err := idx.Search([]float32{1, 0, 0}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Errorf("expected nil for k=0, got %v", results)
	}
}

func TestSearchKLargerThanIndex(t *testing.T) {
	idx := NewVectorIndex(3)
	_ = idx.Insert("a", []float32{1, 0, 0})
	_ = idx.Insert("b", []float32{0, 1, 0})

	results, err := idx.Search([]float32{1, 0, 0}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results when k > index size, got %d", len(results))
	}
}

func testCosineDistance(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 1
	}
	return 1 - dot/(math.Sqrt(normA)*math.Sqrt(normB))
}
