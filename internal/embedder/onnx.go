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

// ONNXEmbedder implements Embedder using the hugot pure-Go ONNX backend.
type ONNXEmbedder struct {
	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline
	mu       sync.Mutex
	dims     int
	batchSz  int
}

// NewONNX creates an ONNXEmbedder. modelCacheDir is where model files are
// downloaded and cached (e.g., {vault}/.local/models/).
func NewONNX(modelName, modelCacheDir string, maxSeqLen, batchSize int) (*ONNXEmbedder, error) {
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
		session:  session,
		pipeline: pipeline,
		dims:     len(probe.Embeddings[0]),
		batchSz:  batchSize,
	}, nil
}

// Embed returns a normalized embedding vector for a single text.
func (e *ONNXEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	result, err := e.pipeline.RunPipeline([]string{text})
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

		e.mu.Lock()
		out, err := e.pipeline.RunPipeline(chunk)
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
