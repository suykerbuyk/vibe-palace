// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import "context"

// Embedder generates vector embeddings from text.
// Implementations must be safe for concurrent use.
//
// Dimensions returns an error because an implementation may defer construction
// of the underlying model until first use (see NewLazy), at which point the
// dimensionality is not yet known and the load may fail.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	Dimensions() (int, error)
	Close() error
}
