// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

type frictionTrendsParams struct {
	Project string `json:"project"`
	Weeks   int    `json:"weeks,omitempty"`
}

var frictionTrendsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug."
		},
		"weeks": {
			"type": "integer",
			"description": "Number of weeks to look back (default 8, max 52)."
		}
	},
	"required": ["project"]
}`)

// FrictionTrendsTool returns the MCP tool for vp_get_friction_trends.
func FrictionTrendsTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_get_friction_trends",
		Description: "Get weekly friction score trends for a project. Returns per-week session count, average friction, and max friction.",
		Schema:      frictionTrendsSchema,
		Handler:     frictionTrendsHandler(vault),
	}
}

func frictionTrendsHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		var p frictionTrendsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		weeks := p.Weeks
		if weeks <= 0 {
			weeks = 8
		}
		if weeks > 52 {
			weeks = 52
		}
		return capture.GetFrictionTrends(vault, p.Project, weeks)
	}
}
