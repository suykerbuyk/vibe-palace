// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
)

type searchParams struct {
	Query    string `json:"query"`
	Project  string `json:"project"`
	Wing     string `json:"wing,omitempty"`
	Room     string `json:"room,omitempty"`
	Hall     string `json:"hall,omitempty"`
	DateFrom string `json:"date_from,omitempty"`
	DateTo   string `json:"date_to,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type crossSearchParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

var searchSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"query": {
			"type": "string",
			"description": "Semantic search query."
		},
		"project": {
			"type": "string",
			"description": "Project slug to search within."
		},
		"wing": {
			"type": "string",
			"description": "Filter to a specific wing."
		},
		"room": {
			"type": "string",
			"description": "Filter to a specific room."
		},
		"hall": {
			"type": "string",
			"description": "Filter to a specific hall (e.g. facts, opinions)."
		},
		"date_from": {
			"type": "string",
			"description": "Start date filter (YYYY-MM-DD, inclusive)."
		},
		"date_to": {
			"type": "string",
			"description": "End date filter (YYYY-MM-DD, inclusive)."
		},
		"limit": {
			"type": "integer",
			"description": "Maximum results to return (default 10, max 50)."
		}
	},
	"required": ["query", "project"]
}`)

var crossSearchSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"query": {
			"type": "string",
			"description": "Semantic search query."
		},
		"limit": {
			"type": "integer",
			"description": "Maximum results to return (default 10, max 50)."
		}
	},
	"required": ["query"]
}`)

const maxSearchLimit = 50

// SearchTool returns the MCP tool for vp_search.
func SearchTool(engine *search.Engine) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_search",
		Description: "Semantic search within a project's knowledge base. Returns ranked results with text, metadata, and relevance scores.",
		Schema:      searchSchema,
		Handler:     searchHandler(engine),
	}
}

// SearchCrossProjectTool returns the MCP tool for vp_search_cross_project.
func SearchCrossProjectTool(engine *search.Engine) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_search_cross_project",
		Description: "Cross-project semantic search across all indexed projects. Read-only.",
		Schema:      crossSearchSchema,
		Handler:     crossSearchHandler(engine),
	}
}

func searchHandler(engine *search.Engine) mcp.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		var p searchParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Query == "" {
			return nil, fmt.Errorf("query is required")
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}

		limit := clampLimit(p.Limit)

		results, err := engine.Search(ctx, p.Query, search.SearchFilters{
			Project:  p.Project,
			Wing:     p.Wing,
			Room:     p.Room,
			Hall:     p.Hall,
			DateFrom: p.DateFrom,
			DateTo:   p.DateTo,
			Limit:    limit,
		})
		if err != nil {
			return nil, fmt.Errorf("search: %w", err)
		}
		if results == nil {
			results = []search.SearchResult{}
		}

		return results, nil
	}
}

func crossSearchHandler(engine *search.Engine) mcp.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		var p crossSearchParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Query == "" {
			return nil, fmt.Errorf("query is required")
		}

		limit := clampLimit(p.Limit)

		results, err := engine.Search(ctx, p.Query, search.SearchFilters{
			Limit: limit,
		})
		if err != nil {
			return nil, fmt.Errorf("search: %w", err)
		}
		if results == nil {
			results = []search.SearchResult{}
		}

		return results, nil
	}
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return 0 // let engine use default
	}
	if limit > maxSearchLimit {
		return maxSearchLimit
	}
	return limit
}
