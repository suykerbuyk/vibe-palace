// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var indexStatusSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug."
		}
	},
	"required": ["project"]
}`)

// IndexStatusTool returns the read-only MCP tool vp_index_status. It registers
// whether or not a search engine exists (the nil-engine path), because it never
// needs one: its handler builds its own no-embedder engine on demand, and the
// coverage derivation loads no model (index-coverage-instrument, Scope 6, R2).
func IndexStatusTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_index_status",
		Description: "This host's search-index coverage for a project: which of the seven ordered " +
			"states it is in (absent, stale, legacy, unbuilt, notes, partial, current) and why, " +
			"with the session counts behind it — n of m sessions ingested, and the pending, " +
			"backlog and failing archives separately. It also reports an index run in progress, " +
			"read from the run lock's advisory holder record while its pid is alive — it takes " +
			"NO lock. It opens no archive and loads no embedder. This is the read side; " +
			"`vp index rebuild <project>` (or vp_refresh_index) is the explicit build path.",
		Schema:  indexStatusSchema,
		Handler: indexStatusHandler(vault),
	}
}

func indexStatusHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p projectParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		cfg, err := vault.LoadConfig(p.Project)
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		cov, err := indexCoverageNoEmbedder(vault, cfg, p.Project)
		if err != nil {
			return nil, fmt.Errorf("index status %q: %w", p.Project, err)
		}
		return cov, nil
	}
}

// indexCoverageNoEmbedder derives coverage over a NO-EMBEDDER engine: the lazy
// embedder's constructor is never called on the coverage path (Stale() and
// TrulyEmpty() load no model), so no ONNX model is loaded. The constructor
// returns an error rather than a model, as a guard: if some future code path
// ever tried to embed here, it would fail loudly instead of loading silently.
func indexCoverageNoEmbedder(vault *storage.Vault, cfg storage.Config, project string) (search.Coverage, error) {
	emb := embedder.NewLazy(func() (embedder.Embedder, error) {
		return nil, errors.New("vp_index_status loads no embedder")
	})
	eng := search.NewEngine(emb, vault, cfg)
	defer eng.Close()
	return eng.IndexCoverage(project)
}
