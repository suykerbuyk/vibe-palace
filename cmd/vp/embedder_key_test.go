// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestSharedModelKeyMatchesFingerprint proves the U4 shared-model cache key is
// byte-aligned with the embed-cache fingerprint each engine stamps
// (search.NewEngine stores FingerprintBackend(ResolveBackend(cfg.EmbedderBackend),
// model, maxseq)). If the key and the fingerprint ever diverge, a shared model
// could serve an engine whose cache fingerprint disagrees — a silent cross-wire.
func TestSharedModelKeyMatchesFingerprint(t *testing.T) {
	cfgs := []storage.Config{
		{EmbedderBackend: "go", EmbedderModel: "sentence-transformers/all-MiniLM-L6-v2", EmbedderMaxSeqLen: 256, EmbedderBatchSize: 32},
		{EmbedderBackend: "", EmbedderModel: "m", EmbedderMaxSeqLen: 512, EmbedderBatchSize: 1},
		{EmbedderBackend: "ort", EmbedderModel: "m", EmbedderMaxSeqLen: 256, EmbedderBatchSize: 8},
	}
	for _, cfg := range cfgs {
		k := sharedModelKey(cfg)
		// The key's own fields: ResolveBackend-masked backend + CONFIGURED max-seq.
		if want := embedder.ResolveBackend(cfg.EmbedderBackend); k.Backend != want {
			t.Errorf("key Backend = %q, want ResolveBackend(%q) = %q", k.Backend, cfg.EmbedderBackend, want)
		}
		if k.MaxSeq != cfg.EmbedderMaxSeqLen {
			t.Errorf("key MaxSeq = %d, want configured %d", k.MaxSeq, cfg.EmbedderMaxSeqLen)
		}
		// Cross-check: the fingerprint derived from the key's three fields equals
		// the one the engine stamps from cfg.
		keyFP := embedder.FingerprintBackend(k.Backend, k.Model, k.MaxSeq)
		engineFP := embedder.FingerprintBackend(embedder.ResolveBackend(cfg.EmbedderBackend), cfg.EmbedderModel, cfg.EmbedderMaxSeqLen)
		if keyFP != engineFP {
			t.Errorf("key fingerprint %q != engine fingerprint %q for cfg %+v", keyFP, engineFP, cfg)
		}
	}
}

// TestSharedModelKeyIgnoresBatchDistinguishesMaxSeq proves the key's shape: two
// configs differing ONLY in batch size collapse to one key (batch only slices
// input, never changes a vector), while a different CONFIGURED max-seq is a
// distinct key (distinct model) — it never silently merges two max-seqs the
// clamp would collapse.
func TestSharedModelKeyIgnoresBatchDistinguishesMaxSeq(t *testing.T) {
	base := storage.Config{EmbedderBackend: "go", EmbedderModel: "m", EmbedderMaxSeqLen: 256, EmbedderBatchSize: 32}
	batchDiff := base
	batchDiff.EmbedderBatchSize = 1
	if sharedModelKey(base) != sharedModelKey(batchDiff) {
		t.Errorf("batch-only difference produced distinct keys; batch must not be in the key")
	}

	maxSeqDiff := base
	maxSeqDiff.EmbedderMaxSeqLen = 512
	if sharedModelKey(base) == sharedModelKey(maxSeqDiff) {
		t.Errorf("different configured max-seq collapsed to one key; max-seq must distinguish models")
	}
}
