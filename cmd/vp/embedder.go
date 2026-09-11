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
// counts constructions. That guard covers ONLY the sites routed through here —
// setupEmbedder (both `vp migrate` subcommands), `vp search`, and bootstrap()
// (which captures the value once, before its lazy closure). `vp check`'s
// Embedder row constructs through check.CheckEmbedder → embedder.NewONNX,
// bypasses this seam, and is unguarded.
var newVaultEmbedder = func(v *storage.Vault, cfg storage.Config) (embedder.Embedder, error) {
	e, err := embedder.NewONNX(
		cfg.EmbedderModel, v.VaultLocalDir()+"/models",
		cfg.EmbedderMaxSeqLen, cfg.EmbedderBatchSize,
	)
	if err != nil {
		return nil, err
	}
	return e, nil
}
