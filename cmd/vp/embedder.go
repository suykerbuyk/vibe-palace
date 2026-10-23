// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// newVaultEmbedder constructs the ONNX embedder for a vault: the configured
// model, cached under the vault's machine-local models directory. On a cold
// cache that is a ~90 MB download, so every caller validates its inputs first
// and constructs only when it will actually embed.
//
// It returns embedder.NewONNX's error unwrapped — each caller owns its own
// "embedder:" prefix — and never a typed-nil *ONNXEmbedder inside the
// interface.
//
// It is a package variable so the cmd/vp tests can replace it:
// setupTestVaultEnv installs forbidVaultEmbedder, which fails any test that
// constructs the model, and stubVaultEmbedder substitutes an embedder and
// counts constructions. That guard covers the sites routed through here —
// setupEmbedder (`vp migrate mempalace`), `vp search`, bootstrap()
// (which captures the value once, before its lazy closure), and `vp check`'s
// Embedder row, whose check.CheckEmbedder is handed a closure over this
// variable by gatherCheckResults.
var newVaultEmbedder = func(v *storage.Vault, cfg storage.Config) (embedder.Embedder, error) {
	// RequestedBackend, not ResolveBackend: honor the operator's ask (config +
	// VP_EMBEDDER_BACKEND) and let NewONNXBackend fail loud if "ort" is asked of
	// a default binary. ResolveBackend would mask that to "go" — correct for the
	// embed-cache fingerprint, wrong for construction, where silence would embed
	// with a backend the operator did not choose.
	e, err := embedder.NewONNXBackend(
		embedder.RequestedBackend(cfg.EmbedderBackend),
		cfg.EmbedderModel, v.VaultLocalDir()+"/models",
		cfg.EmbedderMaxSeqLen, cfg.EmbedderBatchSize,
	)
	if err != nil {
		return nil, err
	}
	return e, nil
}

// sharedModelKey is the key under which a vault's embedder model is shared in the
// process-wide embedder.SharedModelCache (U4). It is byte-aligned with the embed-
// cache fingerprint each engine stamps — search.NewEngine stores
// FingerprintBackend(ResolveBackend(cfg.EmbedderBackend), cfg.EmbedderModel,
// cfg.EmbedderMaxSeqLen) — so a shared model never serves an engine whose cache
// fingerprint disagrees:
//   - Backend is the ResolveBackend-MASKED value (ort→go on a default binary),
//     NOT RequestedBackend. Construction (newVaultEmbedder) still uses
//     RequestedBackend so an "ort" ask fails loud on a default build; the KEY uses
//     the resolved value because that is what the fingerprint (and the resulting
//     vectors) depend on.
//   - MaxSeq is the CONFIGURED EmbedderMaxSeqLen (pre-clamp), exactly as the
//     fingerprint keys it.
//   - NOT the models dir (weights are content-identical per model name, so keying
//     on the per-vault dir would split one model into N) and NOT BatchSize (it
//     only slices input, never changes a vector, and is absent from the fingerprint).
func sharedModelKey(cfg storage.Config) embedder.ModelKey {
	return embedder.ModelKey{
		Backend: embedder.ResolveBackend(cfg.EmbedderBackend),
		Model:   cfg.EmbedderModel,
		MaxSeq:  cfg.EmbedderMaxSeqLen,
	}
}
