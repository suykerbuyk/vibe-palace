// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build ORT

package embedder

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daulet/tokenizers"
	"github.com/knights-analytics/hugot/pipelines"
)

// installTruncation installs maxSeqLen token truncation on an -tags ORT binary.
// It dispatches on the tokenizer the session actually built: an ORT session
// carries a rust tokenizer (installRustTruncation), while a Go session on this
// same binary (backend=go, used by the parity test) carries the Go tokenizer and
// reuses the shared installGoTruncation. The session seam, not a build tag,
// decides which one is present at runtime, so the dispatch must be on the
// tokenizer, not on the tag.
func installTruncation(pipeline *pipelines.FeatureExtractionPipeline, maxSeqLen int, modelPath string) (func() error, *truncatingTokenizer, error) {
	if pipeline.Model != nil && pipeline.Model.Tokenizer != nil && pipeline.Model.Tokenizer.RustTokenizer != nil {
		return installRustTruncation(pipeline, maxSeqLen, modelPath)
	}
	return installGoTruncation(pipeline, maxSeqLen)
}

// installRustTruncation replicates vp's behaviour=2 token truncation on the
// ORT/rust tokenizer path, so a long input does not reach the model untruncated
// (Phase 0 measured a gomlx position-table crash on the raw Go path, and the
// rust path only truncated at the model's 512-position limit, not vp's
// maxSeqLen). It is MANDATORY: onnx.go's Go-tokenizer guard has no counterpart
// here, so without this the ORT path either hard-fails or embeds a different
// regime than the Go path.
//
// It enables the rust tokenizer's own (Hugging Face) truncation at
// max_length=maxSeqLen, right-direction. HF truncation accounts for the special
// tokens the post-processor adds, so a long input encodes to
// [CLS] + the first (maxSeqLen-2) content tokens + [SEP] — token-for-token the
// same shape vp's Go keepHeadAndLast produces, which is what makes the two
// backends' truncation match rather than merely "not crash".
//
// hugot builds its rust tokenizer with tokenizers.FromBytes (no truncation), so
// there is no truncation to toggle on the live object; this re-reads the model's
// tokenizer.json and builds a truncation-enabled replacement, swaps it in, and
// takes over its Close. The original tokenizer is freed here and the pipeline's
// Destroy is repointed at the replacement, so each C tokenizer is closed exactly
// once (the original's old Destroy closure is dropped, not left to double-free).
func installRustTruncation(pipeline *pipelines.FeatureExtractionPipeline, maxSeqLen int, modelPath string) (func() error, *truncatingTokenizer, error) {
	tok := pipeline.Model.Tokenizer
	rt := tok.RustTokenizer
	if rt == nil || rt.Tokenizer == nil {
		return nil, nil, fmt.Errorf("embedder: ORT pipeline has no rust tokenizer to truncate through")
	}

	tkBytes, err := os.ReadFile(filepath.Join(modelPath, "tokenizer.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("embedder: read tokenizer.json for ORT truncation: %w", err)
	}
	limit := maxSeqLen
	if limit < 2 {
		limit = 2
	}
	truncTk, err := tokenizers.FromBytesWithTruncation(tkBytes, uint32(limit), tokenizers.TruncationDirectionRight)
	if err != nil {
		return nil, nil, fmt.Errorf("embedder: build truncating rust tokenizer (max_length=%d): %w", limit, err)
	}

	// Swap in the truncation-enabled tokenizer and free the original. Nothing
	// else references the original after the swap, so closing it now is safe;
	// the pipeline's Tokenizer.Destroy closure still captured the original, so
	// repoint it at the replacement to avoid both a leak of truncTk and a
	// double-close of the original.
	old := rt.Tokenizer
	rt.Tokenizer = truncTk
	_ = old.Close()
	tok.Destroy = func() error { return truncTk.Close() }

	// Defense in depth: hugot's tokenizeInputsRust also hard-slices at
	// MaxAllowedTokens. Lower it to maxSeqLen so that even if native truncation
	// ever failed to apply, the sequence can never exceed the model, and in the
	// normal case (native truncation already <= maxSeqLen) this slice never
	// fires and so never mangles a correctly-truncated encoding.
	tok.MaxAllowedTokens = limit

	verify := func() error {
		// Prove native truncation is actually active: a far-over-limit input
		// must come back truncated to <= maxSeqLen ids. This is the ORT path's
		// equivalent of the Go path's calls>0 check — a direct, post-probe
		// assertion that truncation is wired in.
		long := strings.Repeat("truncate ", limit*4)
		ids, _ := truncTk.Encode(long, true)
		if len(ids) > limit {
			return fmt.Errorf("embedder: ORT rust-tokenizer truncation not active: %d ids > max_length %d", len(ids), limit)
		}
		return nil
	}
	return verify, nil, nil
}
