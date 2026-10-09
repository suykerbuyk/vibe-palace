// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build ORT

// These tests exercise the native ONNX-Runtime backend and run ONLY under
// `-tags ORT` with the native libs available. Run them with:
//
//	CGO_ENABLED=1 CGO_LDFLAGS="-L$HOME/vp-ort-spike/libs" \
//	  VP_ONNX_LIBRARY_PATH="$HOME/vp-ort-spike/libs" \
//	  VP_ORT_TEST_MODEL_CACHE="$HOME/vp-ort-spike/models" \
//	  go test -tags ORT -run ORT ./internal/embedder/
//
// VP_ONNX_LIBRARY_PATH is the directory holding libonnxruntime.so (required, or
// the test skips). VP_ORT_TEST_MODEL_CACHE is an optional warm model cache dir;
// without it the model is downloaded into the project cache like the Go tests.
package embedder

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

const miniLM = "sentence-transformers/all-MiniLM-L6-v2"

// ortModelCacheDir returns the model cache the ORT tests should use: a warm one
// if VP_ORT_TEST_MODEL_CACHE is set, else the shared project cache.
func ortModelCacheDir(t *testing.T) string {
	t.Helper()
	if d := strings.TrimSpace(os.Getenv("VP_ORT_TEST_MODEL_CACHE")); d != "" {
		return d
	}
	return testutil.ProjectCacheDir(t)
}

// requireORTLibs skips the test unless the native ONNX-Runtime library directory
// is set and actually holds libonnxruntime.so. libtokenizers.a is already linked
// into this -tags ORT test binary.
func requireORTLibs(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping ORT test in short mode (requires model + native libs)")
	}
	dir := strings.TrimSpace(os.Getenv(EnvONNXLibraryPath))
	if dir == "" {
		t.Skipf("%s unset; skipping ORT backend test", EnvONNXLibraryPath)
	}
	if _, err := os.Stat(filepath.Join(dir, "libonnxruntime.so")); err != nil {
		t.Skipf("libonnxruntime.so not found in %s=%s: %v", EnvONNXLibraryPath, dir, err)
	}
}

func newTestORT(t *testing.T, maxSeqLen int) *ONNXEmbedder {
	t.Helper()
	requireORTLibs(t)
	emb, err := NewONNXBackend(BackendORT, miniLM, ortModelCacheDir(t), maxSeqLen, 32)
	if err != nil {
		t.Fatalf("NewONNXBackend(ort): %v", err)
	}
	t.Cleanup(func() { emb.Close() })
	return emb
}

// TestORTAvailableUnderTag confirms the seam reports the ORT backend as present
// in a -tags ORT binary (the mirror of TestDefaultBuildHasNoORT).
func TestORTAvailableUnderTag(t *testing.T) {
	if !ORTAvailable {
		t.Fatal("ORTAvailable is false under -tags ORT; the ORT session seam was not compiled in")
	}
}

// TestORTEmbedsAndIsBackendTagged proves the native backend produces a normalized
// 384-dim vector and that the embedder records its backend as ort (which is what
// drives the distinct ` backend=ort` fingerprint).
func TestORTEmbedsAndIsBackendTagged(t *testing.T) {
	emb := newTestORT(t, 256)
	if emb.backend != BackendORT {
		t.Errorf("emb.backend = %q, want %q", emb.backend, BackendORT)
	}
	if emb.tokenizer != nil {
		t.Error("ORT embedder must not carry a Go truncatingTokenizer; truncation lives in the rust tokenizer")
	}
	vec, err := emb.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 384 {
		t.Fatalf("len(vec) = %d, want 384", len(vec))
	}
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	if norm = math.Sqrt(norm); math.Abs(norm-1.0) > 0.01 {
		t.Errorf("L2 norm = %f, want ~1.0", norm)
	}
}

// TestORTLongInputDoesNotCrashAndMatchesTruncation is the direct fix for the
// Phase 0 crash: a ~600-word input (well over the 256-token limit) embeds
// successfully on the ORT/rust path (no gomlx-style position-table blow-up), and
// it embeds the SAME as its first-254-token prefix — proving the rust
// tokenizer's truncation replicates vp's behaviour=2 256-token rule rather than
// merely not crashing.
func TestORTLongInputDoesNotCrashAndMatchesTruncation(t *testing.T) {
	emb := newTestORT(t, 256)
	ctx := context.Background()
	long := wordsText(600)
	ref := wordsText(254) // [CLS] + 254 single-token words + [SEP] = 256 tokens

	got, err := emb.Embed(ctx, long)
	if err != nil {
		t.Fatalf("Embed(long) on ORT crashed/failed — truncation not wired: %v", err)
	}
	if len(got) != 384 {
		t.Fatalf("Embed(long) len = %d, want 384", len(got))
	}
	want, err := emb.Embed(ctx, ref)
	if err != nil {
		t.Fatalf("Embed(ref): %v", err)
	}
	if c := cosineSim(got, want); c < 0.999 {
		t.Errorf("cos(Embed(long), Embed(first 254 tokens)) = %.6f on ORT, want >= 0.999 (rust truncation should match vp's 256-token rule)", c)
	}
}

// TestORTGoParityOnUntruncatedText pins the Phase 0 parity finding: on short,
// untruncated text the ORT (rust tokenizer) and Go (Go tokenizer) backends
// produce near-identical vectors — the ONNX math is the same and the tokenizers
// agree token-for-token. The Go and ORT embedders are built and torn down
// sequentially so only one ORT session is ever live.
func TestORTGoParityOnUntruncatedText(t *testing.T) {
	requireORTLibs(t)
	ctx := context.Background()
	cacheDir := ortModelCacheDir(t)
	texts := []string{
		"the cat sat on the mat",
		"vector search over authored vault artifacts",
		"hello world",
		wordsText(40), // still well under 256 tokens: untruncated on both paths
	}

	// Go backend first, then release it before opening the ORT session.
	goEmb, err := NewONNXBackend(BackendGo, miniLM, cacheDir, 256, 32)
	if err != nil {
		t.Fatalf("NewONNXBackend(go): %v", err)
	}
	goVecs, err := goEmb.EmbedBatch(ctx, texts)
	goEmb.Close()
	if err != nil {
		t.Fatalf("go EmbedBatch: %v", err)
	}

	ortEmb, err := NewONNXBackend(BackendORT, miniLM, cacheDir, 256, 32)
	if err != nil {
		t.Fatalf("NewONNXBackend(ort): %v", err)
	}
	ortVecs, err := ortEmb.EmbedBatch(ctx, texts)
	ortEmb.Close()
	if err != nil {
		t.Fatalf("ort EmbedBatch: %v", err)
	}

	if len(goVecs) != len(ortVecs) {
		t.Fatalf("vector count mismatch: go %d, ort %d", len(goVecs), len(ortVecs))
	}
	for i := range texts {
		c := cosineSim(goVecs[i], ortVecs[i])
		if c < 0.9999 {
			t.Errorf("text %d %q: cos(go, ort) = %.7f, want >= 0.9999 (same regime on untruncated text)", i, texts[i], c)
		}
	}
}
