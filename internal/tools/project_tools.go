// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	Project   string `json:"project"`
	Title     string `json:"title"`
	Narrative string `json:"narrative"`
	Iteration *int   `json:"iteration,omitempty"`
}

var appendIterationSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"title": {"type": "string", "description": "Iteration title — the text after the em dash in the canonical header. Do NOT include the \"## Iteration N —\" prefix; the server writes it."},
		"narrative": {"type": "string", "description": "Iteration narrative body (markdown), WITHOUT a leading \"## Iteration N\" header — the server composes the canonical H2 header from the derived number and title. Use H3 (###) for sub-sections inside the narrative."},
		"iteration": {"type": "integer", "description": "OPTIONAL override for the iteration number. Omit it in normal use: the server derives the next number under the file lock. Supply it only to backfill a specific number; if it disagrees with what the server would mint, the result reports both (overridden=true, derived_n)."}
	},
	"required": ["project", "title", "narrative"]
}`)

func AppendIterationTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_append_iteration",
		Mutating:    true,
		Description: "Append an iteration narrative to the project's iterations file. The server mints the iteration number under the file lock and writes the canonical \"## Iteration N — title\" header itself; supply only title and narrative (and, rarely, an iteration override).",
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
		if strings.TrimSpace(p.Title) == "" {
			return nil, fmt.Errorf("title is required")
		}
		if strings.TrimSpace(p.Narrative) == "" {
			return nil, fmt.Errorf("narrative is required")
		}
		// The server owns the header, so a narrative that carries its own
		// "## Iteration N" header would produce a duplicate. Reject it at the
		// door and name the fix, rather than silently emitting two headers.
		if wrapstate.ContainsIterationHeader(p.Narrative) {
			return nil, fmt.Errorf("narrative must not contain its own \"## Iteration N\" header: the server composes it from the derived number and the title — pass only the body")
		}

		n, derived, err := vault.AppendIterationOwned(p.Project, p.Title, p.Narrative, p.Iteration)
		if err != nil {
			return nil, fmt.Errorf("append iteration: %w", err)
		}

		result := map[string]any{"status": "appended", "project": p.Project, "iter_n": n}
		if p.Iteration != nil {
			// Honored, but say so loudly: a disagreement with the derived number
			// means the caller's model of the vault is stale, and that is worth
			// surfacing rather than silently correcting.
			result["overridden"] = true
			result["derived_n"] = derived
		}
		return result, nil
	}
}
