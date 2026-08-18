// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"context"
	"sync"
	"sync/atomic"
)

// LazyEmbedder defers construction of the underlying Embedder until the first
// call that actually needs it. Loading an ONNX model can take tens of seconds
// (and download ~90MB on a cold cache), which is far too slow to sit in front
// of the MCP initialize handshake — the host times out and the session dies
// with zero tools. Wrapping the real constructor lets the server come up
// immediately and pay the model cost on the first search instead.
//
// Construction happens at most once. A construction failure is memoized and
// returned to every subsequent caller rather than retried in a hot loop.
type LazyEmbedder struct {
	construct func() (Embedder, error)
	once      sync.Once
	built     atomic.Bool // true once construct has run, success or not
	emb       Embedder
	err       error
}

// NewLazy returns an Embedder that calls construct on first use. construct is
// invoked at most once, regardless of concurrent callers.
func NewLazy(construct func() (Embedder, error)) *LazyEmbedder {
	return &LazyEmbedder{construct: construct}
}

// get forces construction and returns the underlying embedder, or the memoized
// construction error.
func (l *LazyEmbedder) get() (Embedder, error) {
	l.once.Do(func() {
		l.emb, l.err = l.construct()
		l.built.Store(true)
	})
	return l.emb, l.err
}

// Embed forces construction, then delegates.
func (l *LazyEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	emb, err := l.get()
	if err != nil {
		return nil, err
	}
	return emb.Embed(ctx, text)
}

// EmbedBatch forces construction, then delegates.
func (l *LazyEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	emb, err := l.get()
	if err != nil {
		return nil, err
	}
	return emb.EmbedBatch(ctx, texts)
}

// Dimensions forces construction, then delegates. The dimensionality is a
// property of the model, so it cannot be known before the model is loaded.
func (l *LazyEmbedder) Dimensions() (int, error) {
	emb, err := l.get()
	if err != nil {
		return 0, err
	}
	return emb.Dimensions()
}

// Ready reports whether the underlying embedder has already been constructed
// successfully. It never triggers construction — bootstrap uses this to refuse
// a semantic ranker path that would stall on a cold ONNX load (investigation E).
// A failed construct leaves Ready false so callers keep falling back.
func (l *LazyEmbedder) Ready() bool {
	return l.built.Load() && l.emb != nil && l.err == nil
}

// Close releases the underlying embedder if — and only if — it was ever
// constructed. Close must never trigger construction: loading a 90MB model at
// shutdown purely to close it would defeat the point of being lazy.
func (l *LazyEmbedder) Close() error {
	// built is stored inside once.Do, so observing true also publishes l.emb.
	if !l.built.Load() || l.emb == nil {
		return nil
	}
	return l.emb.Close()
}
