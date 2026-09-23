// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Claude native-memory MCP tools (vp_memory_*).
//
// These are thin JSON-Schema + (de)serialisation wrappers over the
// project-scoped memory storage layer (storage.Vault.{Write,Read,List,Delete}
// Memory). Memories live under {vault}/Projects/{project}/memory/{rel} and
// mirror Claude's native auto-memory frontmatter format.
//
// Scope: each tool accepts an optional "scope" arg. Only scope=project (or
// empty/default) is honored in v1. scope=global is rejected with a clear,
// forward-looking error — global memory is deferred to v2 (tracked by
// cross-project-learnings-tools).

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/memory"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// globalScopeDeferred is the error returned when a memory tool is invoked with
// scope=global. Global memory is not implemented in v1.
const globalScopeDeferred = "global memory scope is deferred to v2 (tracked by cross-project-learnings-tools); use scope=project"

// checkScope validates the optional scope arg. Empty and "project" are honored;
// "global" is rejected; any other value is an error.
func checkScope(scope string) error {
	switch scope {
	case "", "project":
		return nil
	case "global":
		return fmt.Errorf("%s", globalScopeDeferred)
	default:
		return fmt.Errorf("invalid scope %q: only \"project\" is supported in v1", scope)
	}
}

// ---------------------------------------------------------------------------
// vp_memory_write
// ---------------------------------------------------------------------------

type memoryWriteParams struct {
	Project     string `json:"project"`
	Rel         string `json:"rel"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"`
	Body        string `json:"body"`
	Scope       string `json:"scope,omitempty"`
}

var memoryWriteSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project":     {"type": "string", "description": "Project slug."},
		"rel":         {"type": "string", "description": "Memory filename relative to the memory dir (e.g. \"pref-foo.md\")."},
		"name":        {"type": "string", "description": "Short memory name (frontmatter)."},
		"description": {"type": "string", "description": "One-line description (frontmatter)."},
		"type":        {"type": "string", "description": "Memory type: user, feedback, project, or reference."},
		"body":        {"type": "string", "description": "Memory body (markdown)."},
		"scope":       {"type": "string", "description": "Memory scope. Only \"project\" (default) is supported in v1; \"global\" is deferred to v2."}
	},
	"required": ["project", "rel", "name", "type", "body"]
}`)

// MemoryWriteTool writes a project-scoped native memory file.
func MemoryWriteTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_memory_write",
		Mutating: true,
		Description: "Write a project-scoped Claude native memory file under " +
			"Projects/<project>/memory/<rel>. type must be one of user, " +
			"feedback, project, reference. scope=global is deferred to v2.",
		Schema: memoryWriteSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var p memoryWriteParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if err := checkScope(p.Scope); err != nil {
				return nil, err
			}
			if p.Project == "" {
				return nil, fmt.Errorf("project is required")
			}
			if p.Rel == "" {
				return nil, fmt.Errorf("rel is required")
			}
			if p.Name == "" {
				return nil, fmt.Errorf("name is required")
			}
			if p.Type == "" {
				return nil, fmt.Errorf("type is required")
			}
			meta := storage.MemoryMeta{
				Name:        p.Name,
				Description: p.Description,
				Type:        p.Type,
			}
			if err := vault.WriteMemory(p.Project, p.Rel, meta, p.Body); err != nil {
				return nil, fmt.Errorf("write memory: %w", err)
			}
			return map[string]string{"status": "written", "rel": p.Rel}, nil
		},
	}
}

// ---------------------------------------------------------------------------
// vp_memory_read
// ---------------------------------------------------------------------------

type memoryReadParams struct {
	Project string `json:"project"`
	Rel     string `json:"rel"`
	Scope   string `json:"scope,omitempty"`
}

type memoryReadResult struct {
	Meta storage.MemoryMeta `json:"meta"`
	Body string             `json:"body"`
}

var memoryReadSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"rel":     {"type": "string", "description": "Memory filename relative to the memory dir."},
		"scope":   {"type": "string", "description": "Memory scope. Only \"project\" (default) is supported in v1; \"global\" is deferred to v2."}
	},
	"required": ["project", "rel"]
}`)

// MemoryReadTool reads a single project-scoped native memory file.
func MemoryReadTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_memory_read",
		Description: "Read a project-scoped Claude native memory file. Returns {meta, body}.",
		Schema:      memoryReadSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var p memoryReadParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if err := checkScope(p.Scope); err != nil {
				return nil, err
			}
			if p.Project == "" {
				return nil, fmt.Errorf("project is required")
			}
			if p.Rel == "" {
				return nil, fmt.Errorf("rel is required")
			}
			meta, body, err := vault.ReadMemory(p.Project, p.Rel)
			if err != nil {
				return nil, fmt.Errorf("read memory: %w", err)
			}
			return memoryReadResult{Meta: meta, Body: body}, nil
		},
	}
}

// ---------------------------------------------------------------------------
// vp_memory_list
// ---------------------------------------------------------------------------

type memoryListParams struct {
	Project string `json:"project"`
	Limit   int    `json:"limit,omitempty"`
	Scope   string `json:"scope,omitempty"`
}

type memoryListResult struct {
	Memories []storage.MemoryMeta `json:"memories"`
}

var memoryListSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"limit":   {"type": "integer", "description": "Max entries to return; <=0 (default) returns all."},
		"scope":   {"type": "string", "description": "Memory scope. Only \"project\" (default) is supported in v1; \"global\" is deferred to v2."}
	},
	"required": ["project"]
}`)

// MemoryListTool returns the frontmatter index (name/description/type/rel) for
// a project's memories. Bodies are NOT returned — this is the cheap index.
func MemoryListTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_memory_list",
		Description: "List the frontmatter index (name/description/type/rel) of a " +
			"project's Claude native memories. Bodies are NOT returned — this is " +
			"the cheap index; use vp_memory_read for bodies.",
		Schema: memoryListSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var p memoryListParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if err := checkScope(p.Scope); err != nil {
				return nil, err
			}
			if p.Project == "" {
				return nil, fmt.Errorf("project is required")
			}
			memories, err := vault.ListMemories(p.Project, p.Limit)
			if err != nil {
				return nil, fmt.Errorf("list memories: %w", err)
			}
			if memories == nil {
				memories = []storage.MemoryMeta{}
			}
			return memoryListResult{Memories: memories}, nil
		},
	}
}

// ---------------------------------------------------------------------------
// vp_memory_delete
// ---------------------------------------------------------------------------

type memoryDeleteParams struct {
	Project string `json:"project"`
	Rel     string `json:"rel"`
	Scope   string `json:"scope,omitempty"`
}

var memoryDeleteSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"rel":     {"type": "string", "description": "Memory filename relative to the memory dir."},
		"scope":   {"type": "string", "description": "Memory scope. Only \"project\" (default) is supported in v1; \"global\" is deferred to v2."}
	},
	"required": ["project", "rel"]
}`)

// ---------------------------------------------------------------------------
// vp_memory_harvest
// ---------------------------------------------------------------------------

type memoryHarvestParams struct {
	Project string `json:"project"`
	Cwd     string `json:"cwd"`
	DryRun  bool   `json:"dry_run,omitempty"`
	// Push defaults to true; the pointer distinguishes "omitted" (push) from an
	// explicit false (local-only commit).
	Push *bool `json:"push,omitempty"`
}

var memoryHarvestSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Destination project slug."},
		"cwd":     {"type": "string", "description": "Working directory whose encoded Claude native memory dir to drain (MCP has no transcript)."},
		"dry_run": {"type": "boolean", "description": "If true, classify and report only — zero filesystem/git mutation."},
		"push":    {"type": "boolean", "description": "Push the harvest commit to all remotes (default true; downgrades to local-only when none configured)."}
	},
	"required": ["project", "cwd"]
}`)

// MemoryHarvestTool drains the host-local Claude native memory for a working
// directory into the vault and (non-dry) deletes the originals, committing the
// routed files. One-way: the vault becomes the single source of truth.
func MemoryHarvestTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_memory_harvest",
		Mutating: true,
		Description: "Harvest (one-way drain) Claude's host-local native auto-memory for the " +
			"given cwd into Projects/<project>/memory/, deleting the host-local originals and " +
			"committing the routed files. Typed files route by metadata.type; identical content " +
			"is deduped; same-name/different-content is written with a .harvested-<ts> suffix; " +
			"the MEMORY.md index is dropped. dry_run classifies without mutating; push (default " +
			"true) pushes the commit. A non-Claude host with no native dir is a clean no-op.",
		Schema: memoryHarvestSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var p memoryHarvestParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Project == "" {
				return nil, fmt.Errorf("project is required")
			}
			if p.Cwd == "" {
				return nil, fmt.Errorf("cwd is required")
			}
			nativeDir, err := memory.NativeDirFromCwd(p.Cwd)
			if err != nil {
				return nil, fmt.Errorf("resolve native dir: %w", err)
			}
			push := true
			if p.Push != nil {
				push = *p.Push
			}
			res, err := memory.Harvest(memory.Options{
				VaultRoot: vault.Root,
				Project:   p.Project,
				NativeDir: nativeDir,
				DryRun:    p.DryRun,
				Push:      push,
			})
			if err != nil {
				return nil, fmt.Errorf("harvest: %w", err)
			}
			return res, nil
		},
	}
}

// MemoryDeleteTool deletes a project-scoped native memory file (idempotent).
func MemoryDeleteTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_memory_delete",
		Mutating:    true,
		Description: "Delete a project-scoped Claude native memory file. Idempotent: deleting a missing file is not an error.",
		Schema:      memoryDeleteSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var p memoryDeleteParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if err := checkScope(p.Scope); err != nil {
				return nil, err
			}
			if p.Project == "" {
				return nil, fmt.Errorf("project is required")
			}
			if p.Rel == "" {
				return nil, fmt.Errorf("rel is required")
			}
			if err := vault.DeleteMemory(p.Project, p.Rel); err != nil {
				return nil, fmt.Errorf("delete memory: %w", err)
			}
			return map[string]string{"status": "deleted", "rel": p.Rel}, nil
		},
	}
}
