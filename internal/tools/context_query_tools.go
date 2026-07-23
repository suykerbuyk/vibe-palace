// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
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

// resolveResult carries the resolved body plus the digest of the exact bytes
// that produced it. Sha256 is the on-disk SHA-256 of the project-tier file
// (Projects/<project>/<resource>.md) and is EMPTY when the content came from a
// vault template or the embedded default — i.e. when no project file exists.
// A compare-and-set writer reads it as "assert absent". It is never omitempty:
// the field is always present so the output shape stays stable.
type resolveResult struct {
	Project string `json:"project"`
	Content string `json:"content"`
	Source  string `json:"source"`
	Sha256  string `json:"sha256"`
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
// vp_get_doctrine
// ---------------------------------------------------------------------------

var getDoctrineSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug the doctrine is resolved for (a project may override the embedded copy)."}
	},
	"required": ["project"]
}`)

// doctrineResult is resolveResult plus the always-present resource URI, so a
// host whose channel truncates the inline body can page the full doctrine via
// vp_read_resource — the same content-URI idiom vp_get_task uses.
type doctrineResult struct {
	resolveResult
	DoctrineURI string `json:"doctrine_uri"`
}

// GetDoctrineTool serves the generic Vibe-Palace operating manual (ADR-008).
// The doctrine is binary-owned and fetched ON DEMAND — it is deliberately not
// inlined into the bootstrap payload, whose thin workflow carries only a
// minimal contract paragraph pointing here. It resolves through the same
// 3-tier precedence as workflow/resume so a project can fork it as a glide
// path, but the embedded copy is the tested floor.
func GetDoctrineTool(resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name: "vp_get_doctrine",
		Description: "Get the Vibe-Palace operating doctrine: the generic, " +
			"host-agnostic manual (pair-programming contract, investigation-first " +
			"workflow, task-management rules, vault-accessor/air-gap rules, core " +
			"principles). Served from the binary on demand — it is not part of the " +
			"bootstrap payload. Fetch it once at session start and follow it for " +
			"the rest of the session.",
		Schema:  getDoctrineSchema,
		Handler: getDoctrineHandler(resolver),
	}
}

func getDoctrineHandler(resolver *vpctx.Resolver) mcp.HandlerFunc {
	base := resolveHandler(resolver, "doctrine")
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		res, err := base(ctx, params)
		if err != nil {
			return nil, err
		}
		rr := res.(resolveResult)
		return doctrineResult{
			resolveResult: rr,
			DoctrineURI:   mcp.DoctrineURI(rr.Project),
		}, nil
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
		content, source, sha, err := resolver.ResolveDigest(resource, p.Project)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", resource, err)
		}
		return resolveResult{
			Project: p.Project,
			Content: content,
			Source:  source,
			Sha256:  sha,
		}, nil
	}
}

// ---------------------------------------------------------------------------
// vp_update_resume
// ---------------------------------------------------------------------------

type updateResumeParams struct {
	Project        string `json:"project"`
	Content        string `json:"content"`
	ExpectedSha256 string `json:"expected_sha256"`
}

var updateResumeSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"content": {"type": "string", "description": "New resume content (markdown). Compose it from the RAW bytes vp_vault_read returns, never from vp_get_resume / vp_bootstrap_context, whose bodies are placeholder-expanded ({{PROJECT}}, {{DATE}})."},
		"expected_sha256": {"type": "string", "description": "Compare-and-set guard: the SHA-256 vp_vault_read returned for the raw bytes you composed from. Pass the empty string ONLY to assert no resume.md exists yet (first write). Required: there is no blind-overwrite path."}
	},
	"required": ["project", "content", "expected_sha256"]
}`)

func UpdateResumeTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_update_resume",
		Mutating: true,
		Description: "Replace a project's resume.md in full (whole-file " +
			"rewrite), guarded by compare-and-set: expected_sha256 must match the " +
			"resume's current on-disk SHA-256 (or be the empty string to assert no " +
			"resume.md exists yet), otherwise the write is REFUSED. Intended for " +
			"full-file regeneration and migrations; for a routine edit to one " +
			"section, prefer vp_vault_read + vp_vault_edit, which rewrites only the " +
			"region you name. Source the body from vp_vault_read " +
			"(Projects/<project>/resume.md) and submit the sha it returns. Do NOT " +
			"compose the body from vp_get_resume or vp_bootstrap_context: those " +
			"serve placeholder-EXPANDED content ({{PROJECT}}, {{DATE}}) while their " +
			"sha is over the raw bytes, so writing that body back passes the " +
			"compare-and-set and silently bakes the expanded values onto disk, " +
			"destroying the live tokens. On a conflict the refusal carries the " +
			"current sha: re-read and resubmit, never force.",
		Schema:  updateResumeSchema,
		Handler: updateResumeHandler(vault),
	}
}

// resumeConflictError renders a compare-and-set refusal as a machine-parseable
// failure: the message embeds a JSON object carrying the CURRENT on-disk sha, so
// the agent's retry is mechanical (re-read, recompose, resubmit) rather than a
// regex over prose. It is returned as an ERROR, never as a successful result
// with a conflict flag — mcp.Registry turns a non-nil handler error into an
// isError=true tool result, and a conflict must never look like success.
func resumeConflictError(err error, expected string) error {
	current := ""
	var conflict *storage.ResumeConflictError
	if errors.As(err, &conflict) {
		current = conflict.Current
	}
	detail, merr := json.Marshal(map[string]any{
		"conflict":        true,
		"current_sha256":  current,
		"expected_sha256": expected,
		"remedy":          "re-read the resume with vp_vault_read (Projects/<project>/resume.md), recompose your edit against those raw bytes, and resubmit with the sha it returned; do not force. Do NOT recompose from vp_get_resume: its body is placeholder-expanded, so writing it back would bake {{PROJECT}}/{{DATE}} onto disk.",
	})
	if merr != nil {
		return fmt.Errorf("resume compare-and-set failed: %w", err)
	}
	return fmt.Errorf("resume compare-and-set failed: %s", detail)
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
		if err := vault.WriteResume(p.Project, p.Content, p.ExpectedSha256); err != nil {
			if errors.Is(err, vaultfs.ErrShaConflict) {
				return nil, resumeConflictError(err, p.ExpectedSha256)
			}
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
