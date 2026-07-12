// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"context"
	"math"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

func newTestONNX(t *testing.T) *ONNXEmbedder {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping ONNX test in short mode (requires model download)")
	}
	emb, err := NewONNX("sentence-transformers/all-MiniLM-L6-v2", testutil.ProjectCacheDir(t), 256, 32)
	if err != nil {
		t.Fatalf("NewONNX: %v", err)
	}
	t.Cleanup(func() { emb.Close() })
	return emb
}

func TestONNXDimensions(t *testing.T) {
	emb := newTestONNX(t)
	dims, err := emb.Dimensions()
	if err != nil {
		t.Fatalf("Dimensions: %v", err)
	}
	if dims != 384 {
		t.Errorf("Dimensions() = %d, want 384", dims)
	}
}

func TestONNXEmbed(t *testing.T) {
	emb := newTestONNX(t)
	vec, err := emb.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 384 {
		t.Fatalf("len(vec) = %d, want 384", len(vec))
	}

	// Check L2 norm is approximately 1.0 (normalized).
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if math.Abs(norm-1.0) > 0.01 {
		t.Errorf("L2 norm = %f, want ~1.0", norm)
	}
}

func TestONNXEmbedDeterminism(t *testing.T) {
	emb := newTestONNX(t)
	ctx := context.Background()

	v1, err := emb.Embed(ctx, "determinism test")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := emb.Embed(ctx, "determinism test")
	if err != nil {
		t.Fatal(err)
	}

	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("vectors differ at index %d: %f vs %f", i, v1[i], v2[i])
		}
	}
}

func TestONNXEmbedBatchConsistency(t *testing.T) {
	emb := newTestONNX(t)
	ctx := context.Background()
	texts := []string{"hello world", "how are you", "testing batch"}

	// Individual embeddings.
	singles := make([][]float32, len(texts))
	for i, text := range texts {
		var err error
		singles[i], err = emb.Embed(ctx, text)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Batch embedding.
	batch, err := emb.EmbedBatch(ctx, texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != len(texts) {
		t.Fatalf("batch len = %d, want %d", len(batch), len(texts))
	}

	// Compare: batch should produce very similar results to individual calls.
	// They may not be bitwise identical due to batching effects, but should be
	// extremely close (cosine similarity > 0.999).
	for i := range texts {
		sim := cosineSim(singles[i], batch[i])
		if sim < 0.999 {
			t.Errorf("text %d: cosine similarity between single and batch = %f, want > 0.999", i, sim)
		}
	}
}

func TestONNXEmbedBatchEmpty(t *testing.T) {
	emb := newTestONNX(t)
	result, err := emb.EmbedBatch(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Errorf("expected nil for empty batch, got %v", result)
	}
}

func TestONNXEmbedContextCancellation(t *testing.T) {
	emb := newTestONNX(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := emb.Embed(ctx, "should fail")
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestONNXEmbedSimilarity(t *testing.T) {
	emb := newTestONNX(t)
	ctx := context.Background()

	related1, _ := emb.Embed(ctx, "the cat sat on the mat")
	related2, _ := emb.Embed(ctx, "a kitten rested on the rug")
	unrelated, _ := emb.Embed(ctx, "quantum computing uses qubits for parallel computation")

	simRelated := cosineSim(related1, related2)
	simUnrelated := cosineSim(related1, unrelated)

	if simRelated <= simUnrelated {
		t.Errorf("related similarity (%f) should be > unrelated similarity (%f)", simRelated, simUnrelated)
	}
}

// --- Mock tests ---

func TestMockEmbedder(t *testing.T) {
	m := NewMock(384)
	dims, err := m.Dimensions()
	if err != nil {
		t.Fatalf("Dimensions: %v", err)
	}
	if dims != 384 {
		t.Errorf("Dimensions() = %d, want 384", dims)
	}

	ctx := context.Background()
	v1, err := m.Embed(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(v1) != 384 {
		t.Fatalf("len = %d, want 384", len(v1))
	}

	// Check determinism.
	v2, _ := m.Embed(ctx, "hello")
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("mock not deterministic at index %d", i)
		}
	}

	// Different text produces different vector.
	v3, _ := m.Embed(ctx, "world")
	same := true
	for i := range v1 {
		if v1[i] != v3[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("different texts produced identical vectors")
	}

	// Check L2 norm ≈ 1.0.
	var norm float64
	for _, v := range v1 {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if math.Abs(norm-1.0) > 0.001 {
		t.Errorf("mock L2 norm = %f, want ~1.0", norm)
	}
}

func TestMockEmbedBatch(t *testing.T) {
	m := NewMock(384)
	batch, err := m.EmbedBatch(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 3 {
		t.Fatalf("batch len = %d, want 3", len(batch))
	}
	for i, v := range batch {
		if len(v) != 384 {
			t.Errorf("batch[%d] len = %d, want 384", i, len(v))
		}
	}
}

func TestMockClose(t *testing.T) {
	m := NewMock(384)
	if err := m.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// cosineSim computes cosine similarity between two vectors.
func cosineSim(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
