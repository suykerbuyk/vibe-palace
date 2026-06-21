// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"fmt"
	"os"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/enrichment"
	"github.com/suykerbuyk/vibe-palace/internal/llm"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// NewEnricherFromConfig resolves a session Enricher from the project's
// resolved [enrichment] configuration. It is the single bridge both capture
// entry points (the SessionEnd hook and the vp_capture_session MCP tool)
// share to turn config into a runnable Enricher.
//
// Return contract:
//   - Disabled config (!cfg.Enabled) returns (nil, nil): not an error, just a
//     signal that enrichment is off. Callers treat a nil Enricher as disabled.
//   - Enabled but unresolvable (missing api-key env, bad provider) returns
//     (nil, err): callers warn-log and proceed with a nil Enricher (plain
//     capture). Enrichment must never fail a capture.
//   - Enabled and resolvable returns (enricher, nil).
//
// vaultRoot is used to load any vault-level system-prompt override; an empty
// string falls back to the embedded template.
func NewEnricherFromConfig(cfg storage.EnrichmentConfig, vaultRoot string) (*enrichment.Enricher, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	key := ""
	if cfg.APIKeyEnv != "" {
		key = os.Getenv(cfg.APIKeyEnv)
	}
	if cfg.APIKeyEnv == "" || key == "" {
		return nil, fmt.Errorf("enrichment enabled but api key env %q is unset", cfg.APIKeyEnv)
	}

	llmCfg := llm.Config{
		Endpoint:  cfg.BaseURL,
		Model:     cfg.Model,
		APIKey:    key,
		MaxTokens: cfg.MaxTokens,
	}
	completer, err := llm.NewCompleter(cfg.Provider, llmCfg)
	if err != nil {
		return nil, fmt.Errorf("enrichment: build completer: %w", err)
	}

	systemPrompt := enrichment.LoadSystemPrompt(vaultRoot)
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	return enrichment.NewEnricher(completer, cfg.Model, timeout, systemPrompt), nil
}
