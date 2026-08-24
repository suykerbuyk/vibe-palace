// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"context"
	"fmt"
	"sync"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
)

// defaultMaxSeqLen is all-MiniLM-L6-v2's position-embedding table size. A
// maxSeqLen <= 0 falls back to this rather than to "unlimited": hugot derives
// its batch sequence length from the actual tokens produced, so an untruncated
// input can still exceed the model's position table and panic on the shape
// mismatch (measured 2026-08-24: a 515-token batch against a 512-position
// model, `pipeline.RunPipeline` broadcast panic).
const defaultMaxSeqLen = 512

// ONNXEmbedder implements Embedder using the hugot pure-Go ONNX backend.
type ONNXEmbedder struct {
	session   *hugot.Session
	pipeline  *pipelines.FeatureExtractionPipeline
	mu        sync.Mutex
	dims      int
	batchSz   int
	maxSeqLen int
}

// NewONNX creates an ONNXEmbedder. modelCacheDir is where model files are
// downloaded and cached (e.g., {vault}/.local/models/).
func NewONNX(modelName, modelCacheDir string, maxSeqLen, batchSize int) (*ONNXEmbedder, error) {
	if maxSeqLen <= 0 {
		maxSeqLen = defaultMaxSeqLen
	}

	session, err := hugot.NewGoSession()
	if err != nil {
		return nil, fmt.Errorf("create go session: %w", err)
	}

	dlOpts := hugot.NewDownloadOptions()
	dlOpts.OnnxFilePath = "onnx/model.onnx"
	modelPath, err := hugot.DownloadModel(modelName, modelCacheDir, dlOpts)
	if err != nil {
		session.Destroy()
		return nil, fmt.Errorf("download model %s: %w", modelName, err)
	}

	config := hugot.FeatureExtractionConfig{
		ModelPath:    modelPath,
		Name:         "vp-embedder",
		OnnxFilename: "onnx/model.onnx",
		Options: []hugot.FeatureExtractionOption{
			pipelines.WithNormalization(),
		},
	}

	pipeline, err := hugot.NewPipeline(session, config)
	if err != nil {
		session.Destroy()
		return nil, fmt.Errorf("create pipeline: %w", err)
	}

	// Probe dimensions with a test embedding.
	probe, err := pipeline.RunPipeline([]string{"probe"})
	if err != nil {
		session.Destroy()
		return nil, fmt.Errorf("probe embedding dimensions: %w", err)
	}
	if len(probe.Embeddings) == 0 || len(probe.Embeddings[0]) == 0 {
		session.Destroy()
		return nil, fmt.Errorf("probe returned empty embedding")
	}

	return &ONNXEmbedder{
		session:   session,
		pipeline:  pipeline,
		dims:      len(probe.Embeddings[0]),
		batchSz:   batchSize,
		maxSeqLen: maxSeqLen,
	}, nil
}

// truncateForModel bounds text to at most maxSeqLen-2 runes — a conservative
// stand-in for token count, since word-piece tokenization cannot produce more
// tokens than there are runes to consume. The -2 reserves room for the
// [CLS] and [SEP] special tokens hugot adds around the content tokens: at
// maxSeqLen itself (e.g. the 512-position default), a rune-for-rune worst
// case can still tokenize to maxSeqLen content tokens and, with CLS+SEP,
// overflow the position table by 2 — which is exactly the measured 515-vs-512
// panic. No tokenizer dependency is added; the floor of 1 keeps a tiny
// maxSeqLen (e.g. a test value of 2) from truncating to nothing.
func (e *ONNXEmbedder) truncateForModel(text string) string {
	limit := e.maxSeqLen - 2
	if limit < 1 {
		limit = 1
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// Embed returns a normalized embedding vector for a single text.
func (e *ONNXEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	result, err := e.pipeline.RunPipeline([]string{e.truncateForModel(text)})
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if len(result.Embeddings) == 0 {
		return nil, fmt.Errorf("embed returned no results")
	}
	return result.Embeddings[0], nil
}

// EmbedBatch returns normalized embedding vectors for multiple texts.
// Inputs are processed in chunks of batchSize with context checks between chunks.
func (e *ONNXEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	result := make([][]float32, 0, len(texts))

	for start := 0; start < len(texts); start += e.batchSz {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		end := min(start+e.batchSz, len(texts))
		chunk := texts[start:end]
		truncated := make([]string, len(chunk))
		for i, t := range chunk {
			truncated[i] = e.truncateForModel(t)
		}

		e.mu.Lock()
		out, err := e.pipeline.RunPipeline(truncated)
		e.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("embed batch chunk [%d:%d]: %w", start, end, err)
		}
		result = append(result, out.Embeddings...)
	}

	return result, nil
}

// Dimensions returns the embedding dimensionality (384 for all-MiniLM-L6-v2).
// The model is already loaded, so this never fails.
func (e *ONNXEmbedder) Dimensions() (int, error) { return e.dims, nil }

// Close releases the hugot session and pipeline resources.
func (e *ONNXEmbedder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != nil {
		e.session.Destroy()
		e.session = nil
	}
	return nil
}
