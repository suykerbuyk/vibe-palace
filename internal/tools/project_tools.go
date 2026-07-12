// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// ---------------------------------------------------------------------------
// vp_list_projects
// ---------------------------------------------------------------------------

var listProjectsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {},
	"required": []
}`)

func ListProjectsTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_list_projects",
		Description: "List all projects in the palace.",
		Schema:      listProjectsSchema,
		Handler:     listProjectsHandler(vault),
	}
}

func listProjectsHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, _ json.RawMessage) (any, error) {
		projects, err := vault.ListProjects()
		if err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		if projects == nil {
			projects = []string{}
		}
		return map[string]any{"projects": projects}, nil
	}
}

// ---------------------------------------------------------------------------
// vp_append_iteration
// ---------------------------------------------------------------------------

type appendIterationParams struct {
	Project string `json:"project"`
	Content string `json:"content"`
}

var appendIterationSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"content": {"type": "string", "description": "Iteration narrative content (markdown). MUST open with a canonical H2 header — \"## Iteration N — title\". H3 is reserved for sub-sections inside the narrative; an H3 iteration header is rejected."}
	},
	"required": ["project", "content"]
}`)

func AppendIterationTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_append_iteration",
		Mutating:    true,
		Description: "Append an iteration narrative to the project's iterations file.",
		Schema:      appendIterationSchema,
		Handler:     appendIterationHandler(vault),
	}
}

func appendIterationHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p appendIterationParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.Content == "" {
			return nil, fmt.Errorf("content is required")
		}
		// Reject a non-canonical heading at the door. iterations.md is read by
		// NextIterFromIterationsMD to derive the next iteration number, and a
		// header the reader disagrees with is how the counter silently drifts.
		if err := wrapstate.ValidateIterationNarrative(p.Content); err != nil {
			return nil, err
		}
		if err := vault.AppendIteration(p.Project, p.Content); err != nil {
			return nil, fmt.Errorf("append iteration: %w", err)
		}
		return map[string]string{"status": "appended", "project": p.Project}, nil
	}
}
