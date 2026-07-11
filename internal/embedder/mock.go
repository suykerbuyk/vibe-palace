// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
)

// MockEmbedder produces deterministic vectors from text hashes for testing.
// It makes no semantic claims — "similar" texts do NOT produce similar vectors.
type MockEmbedder struct {
	dims int
}

// NewMock creates a MockEmbedder with the given dimensionality.
func NewMock(dims int) *MockEmbedder {
	return &MockEmbedder{dims: dims}
}

// Embed returns a deterministic vector derived from hashing the input text.
func (m *MockEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	return hashToVector(text, m.dims), nil
}

// EmbedBatch returns deterministic vectors for each input text.
func (m *MockEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i, t := range texts {
		result[i] = hashToVector(t, m.dims)
	}
	return result, nil
}

// Dimensions returns the configured dimensionality.
func (m *MockEmbedder) Dimensions() int { return m.dims }

// Close is a no-op for the mock.
func (m *MockEmbedder) Close() error { return nil }

// hashToVector produces a deterministic, L2-normalized float32 vector from text.
func hashToVector(text string, dims int) []float32 {
	vec := make([]float32, dims)
	h := sha256.Sum256([]byte(text))

	// Use hash bytes as seed material, extending with rehashing.
	seed := h[:]
	for i := range dims {
		if i%8 == 0 && i > 0 {
			next := sha256.Sum256(seed)
			seed = next[:]
		}
		bits := binary.LittleEndian.Uint32(seed[(i%8)*4:])
		vec[i] = float32(bits)/float32(math.MaxUint32)*2 - 1 // [-1, 1]
	}

	// L2 normalize.
	var norm float32
	for _, v := range vec {
		norm += v * v
	}
	norm = float32(math.Sqrt(float64(norm)))
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec
}
