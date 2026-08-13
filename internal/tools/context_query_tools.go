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
//
// 🔴 CONTENT IS DECLARED LAST, AND THAT IS A TRANSPORT CONTRACT. encoding/json
// emits struct fields in declaration order and nothing on the response path
// re-serializes through a map (mcp.marshalResult hands the value straight to
// mcplib.NewToolResultJSON), so declaration order is wire order is CUT order on
// a host with a flat inline cap. Content is the only unbounded field here; the
// metadata that tells a caller which tier answered and what digest to
// compare-and-set against is small and now sits on the near side of any cut.
// Stranded after the body, Sha256 reached only the callers whose body already
// fit — that is, exactly the callers who did not need it.
type resolveResult struct {
	Project string `json:"project"`
	Source  string `json:"source"`
	Sha256  string `json:"sha256"`
	Content string `json:"content"`
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
//
// 🔴 THE URI IS DECLARED FIRST, AHEAD OF THE EMBEDDED BODY. It used to be
// declared after the embedding, which put doctrine_uri at byte 9,213 of a
// 9,265-byte result: reachable only because this project's doctrine happens to
// fit under a host's cap. A recovery handle that arrives only when the body it
// rescues already arrived is not a mitigation, it is a coincidence — and it
// fails on the first project that forks a larger doctrine, and it already fails
// inside vp_manual, which nests this struct behind 56 KB of tool inventory.
// Embedded fields are flattened at the embedding's position (encoding/json
// sorts by index path), so declaring DoctrineURI above resolveResult puts it
// first on the wire.
//
// No `complete` sentinel here on purpose: doctrineResult is NESTED inside
// ManualResult, and a sentinel in the middle of a document is a sentinel that
// survives the cut it is supposed to detect. vp_manual carries the terminal one
// for both.
type doctrineResult struct {
	DoctrineURI string `json:"doctrine_uri"`
	resolveResult
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

// resumeResult is resolveResult plus the recovery handle and the terminal
// sentinel. The URI was MINTED but never emitted: mcp.ResumeURI existed, the
// resource template was registered, vp_read_resource served it — and the one
// tool whose whole job is to hand back the resume never told the caller the
// address. Measured 2026-08-12 the resume runs 27,219 bytes on mlnx-sw-os,
// 1.4x a real host's 19,968-byte flat inline cap, so the body an agent got was
// already a prefix and the agent had no way to learn either fact.
//
// vp_get_workflow keeps the bare resolveResult: it measures 15,343 bytes,
// under the cap, and this task is scoped to the tools measured over it.
type resumeResult struct {
	// ResumeURI leads. Page the full body with vp_read_resource; Sha256 (just
	// below, inside resolveResult) is the digest to compare-and-set against
	// after you do.
	ResumeURI string `json:"resume_uri"`
	resolveResult

	// 🔴 THE TERMINAL SENTINEL — last field, no omitempty, always true on a
	// successful return. Its ABSENCE is the signal: present ⇒ every byte of
	// this document arrived, absent ⇒ the host cut it, whether or not the host
	// said so (Claude Code truncates silently). No omitempty because a false
	// bool would vanish and make "cut" and "whole" serialize identically.
	// Anything declared after this field re-opens the hole.
	Complete bool `json:"complete"`
}

func GetResumeTool(resolver *vpctx.Resolver) mcp.Tool {
	return mcp.Tool{
		Name: "vp_get_resume",
		Description: "Get the resume for a project. The result LEADS with `resume_uri` and ENDS with `complete`: " +
			"if you do not see `complete: true`, your host truncated the body — re-read it in pages via `resume_uri` with vp_read_resource.",
		Schema:  getResumeSchema,
		Handler: getResumeHandler(resolver),
	}
}

func getResumeHandler(resolver *vpctx.Resolver) mcp.HandlerFunc {
	base := resolveHandler(resolver, "resume")
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		res, err := base(ctx, params)
		if err != nil {
			return nil, err
		}
		rr := res.(resolveResult)
		return resumeResult{
			ResumeURI:     mcp.ResumeURI(rr.Project),
			resolveResult: rr,
			Complete:      true,
		}, nil
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

// knowledgeResult leads with the stats (small, and the only thing that reports
// how much of the graph exists) and ends with the triples, which are unbounded:
// `limit` has no documented maximum and the live vault measures 2,759,612 bytes
// at limit=100000 — 138x a real host's flat inline cap — with even the DEFAULT
// limit of 100 running 22,099 bytes on a busy project. Giving this tool a
// paging hatch needs a design and is filed separately; what it gets here is the
// ability to SAY it was cut.
type knowledgeResult struct {
	Stats   storage.KGStats  `json:"stats"`
	Triples []storage.Triple `json:"triples"`

	// 🔴 THE TERMINAL SENTINEL — last field, no omitempty, always true on a
	// successful return; its ABSENCE is the signal. Absent ⇒ the host cut this
	// document, and the triple list you are holding is a prefix of a prefix:
	// re-ask with a smaller `limit`. Anything declared below re-opens the hole.
	Complete bool `json:"complete"`
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
		Name: "vp_get_knowledge",
		Description: "Get full knowledge graph snapshot: stats and all triples. The result ENDS with `complete`: " +
			"if you do not see `complete: true`, your host truncated it — re-ask with a smaller `limit`.",
		Schema:  getKnowledgeSchema,
		Handler: getKnowledgeHandler(vault),
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

		return knowledgeResult{Stats: stats, Triples: triples, Complete: true}, nil
	}
}
