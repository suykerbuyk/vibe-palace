// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"context"
	"sync"
)

// SharedModelCache lends ONE embedding model to N callers (U4 plan §2.1). A
// routing `vp mcp` serves N vaults; naive routing would hold N resident ONNX
// models (~310 MB each, private anonymous heap — the OS shares nothing across
// processes, so the sharing MUST be in-process). This cache collapses that to
// one model per distinct (backend, model, maxseq) key: engines sharing a key
// share one *ONNXEmbedder, whose own e.mu already serializes inference and whose
// Close now fails closed (ErrEmbedderClosed) so a last-handle Close can never
// leave a live pipeline over a destroyed executor.
//
// The registry (commit 2b, internal/vaultstack) owns one SharedModelCache and
// hands each VaultStack the Handle it gets from Acquire. This file is the cache
// and the refcounted Handle only; it wires into nothing.

// ModelKey identifies a shareable model. It is the embed-cache fingerprint's
// inputs, and NOTHING else: NOT the per-vault models directory (the weights are
// content-identical for a given model name, so keying on the dir would split
// identical models) and NOT the batch size (it only slices input, never changes
// a vector, and is absent from FingerprintBackend).
//
// The CALLER (commit 2b, cmd/vp/embedder.go) builds the key as
//
//	ModelKey{Backend: ResolveBackend(cfg.EmbedderBackend), Model: cfg.EmbedderModel,
//	         MaxSeq: cfg.EmbedderMaxSeqLen}
//
// so the key is byte-aligned with FingerprintBackend (engine.go stamps that same
// resolved backend + configured max-seq on each engine's EmbedCache). Backend is
// the ResolveBackend-MASKED value (ort→go on a default binary), not
// RequestedBackend, and MaxSeq is the CONFIGURED max-seq (pre-clamp), exactly as
// the fingerprint keys it — so a shared model never serves an engine whose cache
// fingerprint disagrees.
type ModelKey struct {
	Backend string
	Model   string
	MaxSeq  int
}

// SharedModelCache is a process-global (as used by the registry) store of
// shared, refcounted models keyed by ModelKey. The zero value is usable; prefer
// NewSharedModelCache for clarity.
type SharedModelCache struct {
	mu      sync.Mutex
	entries map[ModelKey]*sharedEntry
}

// NewSharedModelCache returns an empty cache.
func NewSharedModelCache() *SharedModelCache {
	return &SharedModelCache{entries: make(map[ModelKey]*sharedEntry)}
}

// sharedEntry is one shared model plus the number of live handles to it. emb is
// a *LazyEmbedder, so the (~310 MB) model is built at most once per key, on the
// first use through any handle — acquiring a stack never blocks on the load.
// refs is guarded by SharedModelCache.mu.
type sharedEntry struct {
	emb  Embedder
	refs int
}

// Handle is a per-caller reference to a shared model. It is an Embedder (so an
// engine can hold it exactly where it holds any embedder), it forwards Ready()
// so the engine's warm-wait (EmbedderReady) observes the real lazy construction
// state, and its Close releases THIS handle's reference — the underlying model
// is closed only when the last handle closes.
type Handle interface {
	Embedder
	Ready() bool
	Close() error
}

// Acquire returns a Handle to the shared model for key k, constructing the model
// lazily (and once per key) via construct. construct is wrapped in NewLazy, so it
// runs on the first Embed/EmbedBatch/Dimensions through any handle, not here;
// Acquire itself only bumps the refcount and never blocks on the model load.
func (c *SharedModelCache) Acquire(ctx context.Context, k ModelKey, construct func() (Embedder, error)) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[ModelKey]*sharedEntry)
	}
	e, ok := c.entries[k]
	if !ok {
		// NewLazy memoizes a construct failure per key (do not hot-loop a 310 MB
		// download); the registry memoizes no failure for the STACK build, only
		// here for the MODEL build.
		e = &sharedEntry{emb: NewLazy(construct)}
		c.entries[k] = e
	}
	e.refs++
	return &handle{entry: e, cache: c, key: k}, nil
}

// handle is one reference returned by Acquire. It delegates all Embedder work to
// the shared entry's model and owns exactly one refcount decrement (via once).
type handle struct {
	entry *sharedEntry
	cache *SharedModelCache
	key   ModelKey
	once  sync.Once
	// err captures the result of the underlying Close on the last-ref path so a
	// caller that reads Close's error still gets it.
	err error
}

func (h *handle) Embed(ctx context.Context, text string) ([]float32, error) {
	return h.entry.emb.Embed(ctx, text)
}

func (h *handle) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return h.entry.emb.EmbedBatch(ctx, texts)
}

func (h *handle) Dimensions() (int, error) {
	return h.entry.emb.Dimensions()
}

// Ready forwards to the inner *LazyEmbedder's Ready (construction-state report
// that never triggers construction), so a routed engine's EmbedderReady is
// accurate and the warm wait is not skipped before the model exists. A model
// that somehow does not report readiness is treated as not-ready (false) rather
// than falsely ready.
func (h *handle) Ready() bool {
	if r, ok := h.entry.emb.(interface{ Ready() bool }); ok {
		return r.Ready()
	}
	return false
}

// Close releases this handle's reference exactly once. The sync.Once wraps the
// ENTIRE cache-mutex critical section: it takes c.mu, decrements refs, and at
// zero removes the entry from the map THEN closes the underlying model — all
// still inside the Once and under the held lock, so a concurrent Acquire of the
// same key (which blocks on c.mu) can never attach to a dying entry; it finds
// the key gone and builds a fresh one. A second Close on the same handle is a
// no-op.
func (h *handle) Close() error {
	h.once.Do(func() {
		h.cache.mu.Lock()
		defer h.cache.mu.Unlock()
		h.entry.refs--
		if h.entry.refs <= 0 {
			if h.cache.entries[h.key] == h.entry {
				delete(h.cache.entries, h.key)
			}
			h.err = h.entry.emb.Close()
		}
	})
	return h.err
}
