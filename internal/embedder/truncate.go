// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"fmt"
	"sync/atomic"

	"github.com/gomlx/go-huggingface/tokenizers/api"
	"github.com/knights-analytics/hugot/pipelines"
)

// installGoTruncation wires token-level truncation into the pure-Go hugot
// pipeline and returns a verifier proving (after the dimension probe) that
// hugot actually tokenized through the wrapper. It is backend-agnostic in the
// sense that it works for any pipeline carrying a Go tokenizer — on the default
// build it is the only path, and on an -tags ORT build it still serves a Go
// session (backend=go). The nil tk return slot is for symmetry with the ORT
// rust path, which has no truncatingTokenizer.
//
// The returned *truncatingTokenizer is stored on ONNXEmbedder so the default
// build's tests can assert the wrapper's call count and limit.
func installGoTruncation(pipeline *pipelines.FeatureExtractionPipeline, maxSeqLen int) (func() error, *truncatingTokenizer, error) {
	// COUPLING: this reaches into hugot internals (hugot@v0.7.0
	// backends/tokenizer_go.go: tokenizeInputsGo calls
	// GoTokenizer.Tokenizer.EncodeWithAnnotations). A hugot upgrade
	// (evaluate-hugot-v0-7-8-upgrade) that renames the field or stops calling
	// through it must fail here, never embed untruncated input silently: the
	// probe's verify() proves the wrapper was invoked.
	if pipeline.Model == nil || pipeline.Model.Tokenizer == nil || pipeline.Model.Tokenizer.GoTokenizer == nil {
		return nil, nil, fmt.Errorf("embedder: hugot pipeline has no Go tokenizer to truncate through")
	}
	tk := &truncatingTokenizer{Tokenizer: pipeline.Model.Tokenizer.GoTokenizer.Tokenizer, maxLen: maxSeqLen}
	pipeline.Model.Tokenizer.GoTokenizer.Tokenizer = tk

	verify := func() error {
		if tk.calls.Load() == 0 {
			return fmt.Errorf("embedder: hugot did not tokenize through the truncating tokenizer; token truncation would be bypassed (see evaluate-hugot-v0-7-8-upgrade)")
		}
		return nil
	}
	return verify, tk, nil
}

// truncatingTokenizer truncates the token IDs hugot feeds the model to
// maxLen, keeping the final [SEP]: [CLS] t1..t(maxLen-2) [SEP], which is
// Hugging Face / sentence-transformers truncation. It works on IDs, never on
// text, so the kept tokens are exactly the full encoding's prefix. Only
// EncodeWithAnnotations is overridden: it is the one call hugot makes.
type truncatingTokenizer struct {
	api.Tokenizer
	maxLen int
	calls  atomic.Int64
}

func (t *truncatingTokenizer) EncodeWithAnnotations(text string) api.AnnotatedEncoding {
	t.calls.Add(1)
	enc := t.Tokenizer.EncodeWithAnnotations(text)
	limit := max(t.maxLen, 2)
	if len(enc.IDs) <= limit {
		return enc
	}
	enc.IDs = keepHeadAndLast(enc.IDs, limit)
	if len(enc.SpecialTokensMask) > limit {
		enc.SpecialTokensMask = keepHeadAndLast(enc.SpecialTokensMask, limit)
	}
	if len(enc.Spans) > limit {
		enc.Spans = keepHeadAndLast(enc.Spans, limit)
	}
	return enc
}

// keepHeadAndLast returns s[:n-1] followed by s's last element, as a new slice.
func keepHeadAndLast[T any](s []T, n int) []T {
	out := make([]T, 0, n)
	out = append(out, s[:n-1]...)
	return append(out, s[len(s)-1])
}
