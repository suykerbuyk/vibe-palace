// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ---------------------------------------------------------------------------
// vp_get_workflow
// ---------------------------------------------------------------------------

var getWorkflowSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."}
	},
	"required": ["project"]
}`)

type resolveResult struct {
	Project string `json:"project"`
	Content string `json:"content"`
	Source  string `json:"source"`
}

func GetWorkflowTool(resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_get_workflow",
		Description: "Get the workflow template for a project.",
		Schema:      getWorkflowSchema,
		Handler:     resolveHandler(resolver, "workflow"),
	}
}

// ---------------------------------------------------------------------------
// vp_get_resume
// ---------------------------------------------------------------------------

var getResumeSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."}
	},
	"required": ["project"]
}`)

func GetResumeTool(resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_get_resume",
		Description: "Get the resume for a project.",
		Schema:      getResumeSchema,
		Handler:     resolveHandler(resolver, "resume"),
	}
}

// resolveHandler returns a handler that resolves a named resource for a project.
func resolveHandler(resolver *vpctx.Resolver, resource string) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p projectParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		content, source, err := resolver.Resolve(resource, p.Project)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", resource, err)
		}
		return resolveResult{
			Project: p.Project,
			Content: content,
			Source:  source,
		}, nil
	}
}

// ---------------------------------------------------------------------------
// vp_update_resume
// ---------------------------------------------------------------------------

type updateResumeParams struct {
	Project string `json:"project"`
	Content string `json:"content"`
}

var updateResumeSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"content": {"type": "string", "description": "New resume content (markdown)."}
	},
	"required": ["project", "content"]
}`)

func UpdateResumeTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_update_resume",
		Description: "Replace a project's resume.md in full (whole-file " +
			"rewrite). Intended for full-file regeneration or migrations — for " +
			"routine edits to individual ## Open Threads entries prefer the " +
			"surgical vp_thread_* tools (insert/replace/remove) and vp_carried_* " +
			"tools, which mutate a single ### block without rewriting the file.",
		Schema:  updateResumeSchema,
		Handler: updateResumeHandler(vault),
	}
}

func updateResumeHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p updateResumeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.Content == "" {
			return nil, fmt.Errorf("content is required")
		}
		if err := vault.WriteResume(p.Project, p.Content); err != nil {
			return nil, fmt.Errorf("write resume: %w", err)
		}
		return map[string]string{"status": "updated", "project": p.Project}, nil
	}
}

// ---------------------------------------------------------------------------
// vp_get_knowledge
// ---------------------------------------------------------------------------

type getKnowledgeParams struct {
	Project string `json:"project"`
	Limit   int    `json:"limit,omitempty"`
}

type knowledgeResult struct {
	Stats   storage.KGStats  `json:"stats"`
	Triples []storage.Triple `json:"triples"`
}

var getKnowledgeSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"limit":   {"type": "integer", "description": "Maximum triples to return (default 100)."}
	},
	"required": ["project"]
}`)

func GetKnowledgeTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_get_knowledge",
		Description: "Get full knowledge graph snapshot: stats and all triples.",
		Schema:      getKnowledgeSchema,
		Handler:     getKnowledgeHandler(vault),
	}
}

func getKnowledgeHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p getKnowledgeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}

		limit := p.Limit
		if limit <= 0 {
			limit = 100
		}

		stats, err := vault.KGStats(p.Project)
		if err != nil {
			return nil, fmt.Errorf("kg stats: %w", err)
		}

		triples, err := vault.ListTriples(p.Project)
		if err != nil {
			return nil, fmt.Errorf("list triples: %w", err)
		}
		if triples == nil {
			triples = []storage.Triple{}
		}
		if len(triples) > limit {
			triples = triples[:limit]
		}

		return knowledgeResult{Stats: stats, Triples: triples}, nil
	}
}
