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
		Name: "vp_list_projects",
		Description: "List every project in the vault — the union of the palace/ store and the " +
			"Projects/ history tree. A project present in only one tree is reported in drift; " +
			"drift is absent when the two trees agree.",
		Schema:  listProjectsSchema,
		Handler: listProjectsHandler(vault),
	}
}

// projectDrift is one project that exists in only ONE of the vault's two trees.
// It is reported, never silently resolved: a project with history and no palace/
// store is unsearchable, and a palace/ store with no history is a leftover. Both
// are real drift, and this enumerator is the only thing positioned to see them.
type projectDrift struct {
	Slug       string `json:"slug"`
	InPalace   bool   `json:"in_palace"`
	InProjects bool   `json:"in_projects"`
}

// listProjectsHandler enumerates the UNION of both trees.
//
// It used to call ListProjects, which reads only palace/ — so it silently
// omitted every project whose history was captured but never drawer-indexed. In
// the live vault that was 5 projects and 73 session notes, including one with 35
// notes: an agent asking this tool "what is in this vault?" was told a subset and
// could not tell. The tool's name promised the vault and it delivered one tree.
//
// drift is omitted entirely when the trees agree — silent when healthy, loud when
// not, like every other signal in this system.
func listProjectsHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, _ json.RawMessage) (any, error) {
		all, err := vault.ListAllProjects()
		if err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}

		projects := make([]string, 0, len(all))
		drift := []projectDrift{}
		for _, p := range all {
			projects = append(projects, p.Slug)
			if !p.Complete() {
				drift = append(drift, projectDrift{
					Slug:       p.Slug,
					InPalace:   p.InPalace,
					InProjects: p.InProjects,
				})
			}
		}

		out := map[string]any{"projects": projects}
		if len(drift) > 0 {
			out["drift"] = drift
		}
		return out, nil
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
