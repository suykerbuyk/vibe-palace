// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gomlx/go-huggingface/tokenizers/api"
	"github.com/gomlx/go-huggingface/tokenizers/hftokenizer"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
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

// TestTruncatingTokenizerKeepsHFTruncation pins Hugging Face truncation on
// the token IDs, without compiling the model: an over-limit encoding becomes
// [CLS] + the full encoding's first maxLen-2 content tokens + [SEP], and an
// under-limit one is returned unchanged. The go-huggingface tokenizer ignores
// its own MaxLen, which is why this wrapper exists at all.
func TestTruncatingTokenizerKeepsHFTruncation(t *testing.T) {
	path := filepath.Join(testutil.ProjectCacheDir(t), "sentence-transformers_all-MiniLM-L6-v2", "tokenizer.json")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("tokenizer not cached at %s: %v", path, err)
	}
	inner, err := hftokenizer.NewFromFile(nil, path)
	if err != nil {
		t.Fatalf("load tokenizer: %v", err)
	}
	if err := inner.With(api.EncodeOptions{AddSpecialTokens: true, IncludeSpecialTokensMask: true}); err != nil {
		t.Fatal(err)
	}
	tk := &truncatingTokenizer{Tokenizer: inner, maxLen: 16}

	long := strings.Repeat("Unaffable résumé-writers re-tokenize 12,345 words; ", 20)
	full := inner.EncodeWithAnnotations(long)
	if len(full.IDs) <= 16 {
		t.Fatalf("fixture too short: %d tokens", len(full.IDs))
	}
	got := tk.EncodeWithAnnotations(long)
	want := append(append([]int{}, full.IDs[:15]...), full.IDs[len(full.IDs)-1])
	if !slices.Equal(got.IDs, want) {
		t.Errorf("truncated IDs = %v, want %v", got.IDs, want)
	}
	if len(got.SpecialTokensMask) != 16 || got.SpecialTokensMask[0] != 1 || got.SpecialTokensMask[15] != 1 {
		t.Errorf("special-token mask not trimmed to [CLS]...[SEP]: %v", got.SpecialTokensMask)
	}
	if tk.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", tk.calls.Load())
	}

	short := inner.EncodeWithAnnotations("hello world")
	if got := tk.EncodeWithAnnotations("hello world"); !slices.Equal(got.IDs, short.IDs) {
		t.Errorf("under-limit input changed: %v, want %v", got.IDs, short.IDs)
	}
}

// singleTokenWords are each one WordPiece token in all-MiniLM-L6-v2's
// vocabulary, so a text built from them has exactly one token per word.
var singleTokenWords = strings.Fields("the cat sat on a mat while dogs ran in green fields near old stone walls and birds sang")

func wordsText(n int) string {
	w := make([]string, n)
	for i := range w {
		w[i] = singleTokenWords[i%len(singleTokenWords)]
	}
	return strings.Join(w, " ")
}

// TestEmbedLongInputEqualsTokenTruncatedEmbedding is the acceptance test of
// embedder-truncates-by-characters-not-tokens: at maxSeqLen=256 a long input
// embeds exactly as its first 254 tokens do, and not as its first 254
// characters (the 23bedcc rune cut this replaced). It also fails loudly if a
// hugot upgrade stops tokenizing through the wrapper.
func TestEmbedLongInputEqualsTokenTruncatedEmbedding(t *testing.T) {
	emb := newTestONNX(t) // maxSeqLen 256
	ctx := context.Background()
	long := wordsText(600)
	ref := wordsText(254)
	if n := len(emb.tokenizer.Tokenizer.Encode(ref)); n != 256 {
		t.Fatalf("reference text tokenizes to %d ids, want 256 ([CLS] + 254 + [SEP]); the word list is no longer single-token", n)
	}

	before := emb.tokenizer.calls.Load()
	got, err := emb.Embed(ctx, long)
	if err != nil {
		t.Fatalf("Embed(long): %v", err)
	}
	if emb.tokenizer.calls.Load() == before {
		t.Fatal("Embed did not tokenize through the truncating tokenizer: hugot's call path changed (evaluate-hugot-v0-7-8-upgrade)")
	}
	want, err := emb.Embed(ctx, ref)
	if err != nil {
		t.Fatalf("Embed(ref): %v", err)
	}
	if c := cosineSim(got, want); c < 0.999999 {
		t.Errorf("cos(Embed(long), Embed(first 254 tokens)) = %.7f, want >= 0.999999", c)
	}
	runeCut, err := emb.Embed(ctx, string([]rune(long)[:254]))
	if err != nil {
		t.Fatalf("Embed(rune cut): %v", err)
	}
	if c := cosineSim(got, runeCut); c >= 0.99 {
		t.Errorf("cos(Embed(long), Embed(first 254 runes)) = %.7f: the long input is still being cut by characters", c)
	}
}

// TestONNXEmbedOver512TokensDoesNotPanic keeps the 515-token fix: a 2000-token
// input embeds at the 512 default and at a configured 1024, which must be
// clamped to the model's 512-position table.
func TestONNXEmbedOver512TokensDoesNotPanic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ONNX test in short mode (requires model download)")
	}
	long := strings.Repeat("word ", 2000)
	for _, seq := range []int{0, 1024} {
		emb, err := NewONNX("sentence-transformers/all-MiniLM-L6-v2", testutil.ProjectCacheDir(t), seq, 32)
		if err != nil {
			t.Fatalf("NewONNX(maxSeqLen=%d): %v", seq, err)
		}
		if emb.tokenizer.maxLen != 512 {
			t.Errorf("maxSeqLen=%d: token limit = %d, want 512", seq, emb.tokenizer.maxLen)
		}
		vecs, err := emb.EmbedBatch(context.Background(), []string{long, "short"})
		if err != nil {
			t.Fatalf("maxSeqLen=%d: EmbedBatch over 512 tokens: %v", seq, err)
		}
		for i, v := range vecs {
			if len(v) != 384 {
				t.Errorf("maxSeqLen=%d: vec %d len %d, want 384", seq, i, len(v))
			}
		}
		emb.Close()
	}
}

// TestONNXEmbedTruncatesOversizedInput pins the fix for
// restart-must-not-block-on-refresh-index: NewONNX previously accepted
// maxSeqLen and never used it, so hugot could be handed enough text to
// tokenize past the model's position-embedding table and panic on the shape
// mismatch (measured 2026-08-24: a 515-token batch against a 512-position
// model). A tiny maxSeqLen against ordinary long text is the cheapest way to
// prove truncation runs at all, without needing to reproduce the exact
// token count that triggered the original crash.
func TestONNXEmbedTruncatesOversizedInput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ONNX test in short mode (requires model download)")
	}
	emb, err := NewONNX("sentence-transformers/all-MiniLM-L6-v2", testutil.ProjectCacheDir(t), 8, 32)
	if err != nil {
		t.Fatalf("NewONNX: %v", err)
	}
	t.Cleanup(func() { emb.Close() })

	long := strings.Repeat("word ", 1000)

	vec, err := emb.Embed(context.Background(), long)
	if err != nil {
		t.Fatalf("Embed with oversized input panicked/errored instead of truncating: %v", err)
	}
	if len(vec) != 384 {
		t.Fatalf("len(vec) = %d, want 384", len(vec))
	}

	batch, err := emb.EmbedBatch(context.Background(), []string{long, long})
	if err != nil {
		t.Fatalf("EmbedBatch with oversized input panicked/errored instead of truncating: %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("len(batch) = %d, want 2", len(batch))
	}
	for i, v := range batch {
		if len(v) != 384 {
			t.Errorf("batch[%d] len = %d, want 384", i, len(v))
		}
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

// TestModelCacheLockPathMatchesHugotColonStripping pins the Review
// (2026-09-11) Medium finding: modelCacheLockPath must replicate hugot's own
// colon-suffix strip (hugot@v0.7.0 downloader.go:DownloadModel splits an HF
// revision-pinned "org/model:revision" name on ":" and keeps only the first
// part before substituting "/" for "_") byte-for-byte. A lock keyed on a path
// that still contains the ":revision" suffix would guard a DIFFERENT path
// than the one hugot actually writes to, silently defeating the whole fix for
// any future colon-bearing embedder.model config -- not triggered by today's
// only configured model name, but exactly the kind of latent bug this task
// exists to prevent.
func TestModelCacheLockPathMatchesHugotColonStripping(t *testing.T) {
	cacheDir := "/cache/models"

	tests := []struct {
		name      string
		modelName string
		want      string
	}{
		{
			name:      "no colon (today's only configured model)",
			modelName: "sentence-transformers/all-MiniLM-L6-v2",
			want:      "/cache/models/sentence-transformers_all-MiniLM-L6-v2",
		},
		{
			name:      "HF revision-pinned name strips everything from the colon onward",
			modelName: "org/model:revision",
			want:      "/cache/models/org_model",
		},
		{
			name:      "colon with no further slash",
			modelName: "org:main",
			want:      "/cache/models/org",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelCacheLockPath(cacheDir, tc.modelName); got != tc.want {
				t.Errorf("modelCacheLockPath(%q, %q) = %q, want %q", cacheDir, tc.modelName, got, tc.want)
			}
		})
	}
}

// TestNewONNXLockWaitTimeoutIsCleanNotHang proves NewONNX gives up cleanly on
// a stuck lock-holder instead of blocking indefinitely. It never reaches
// hugot.NewGoSession/DownloadModel -- the lock is already held before NewONNX
// gets that far -- so it needs no network access and can run unconditionally,
// including under -short, unlike the rest of this file's ONNX tests.
func TestNewONNXLockWaitTimeoutIsCleanNotHang(t *testing.T) {
	dir := t.TempDir()
	modelName := "sentence-transformers/all-MiniLM-L6-v2"
	lockTarget := modelCacheLockPath(dir, modelName)

	release, err := vaultlock.Acquire(dir, lockTarget)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	old := modelCacheLockTimeout
	modelCacheLockTimeout = 100 * time.Millisecond
	defer func() { modelCacheLockTimeout = old }()

	start := time.Now()
	_, err = NewONNX(modelName, dir, 0, 1)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, vaultlock.ErrLockWaitTimeout) {
		t.Fatalf("NewONNX error = %v, want wrapping vaultlock.ErrLockWaitTimeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("NewONNX took %s, want ~100ms (proves it does not hang)", elapsed)
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
