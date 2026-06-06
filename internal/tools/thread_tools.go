// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Surgical resume.md editors (vp_thread_*). These operate on the ## Open
// Threads section of Projects/<slug>/resume.md, inserting, replacing, and
// removing individual ### slug blocks without rewriting the whole file. The
// markdown machinery lives in internal/mdutil (pure string ops); this layer
// reads resume.md off disk, applies the edit, and writes it back through
// internal/atomicfile so the write is atomic and surface-stamped.
//
// The slug "Carried forward" is reserved: vp_thread_replace and
// vp_thread_remove refuse it, directing callers to the vp_carried_* tools.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/mdutil"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// openThreadsSection is the ## heading the surgical editors target.
const openThreadsSection = "Open Threads"

// carriedForwardSlug is the reserved ### sub-heading owned by the vp_carried_*
// tools; the vp_thread_* mutators refuse to touch it.
const carriedForwardSlug = "Carried forward"

// readResumeForEdit resolves and reads Projects/<project>/resume.md, returning
// its content and absolute path.
func readResumeForEdit(vault *storage.Vault, project string) (content, absPath string, err error) {
	abs, err := vault.ResumeFile(project)
	if err != nil {
		return "", "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("resume.md not found for project %q", project)
		}
		return "", "", fmt.Errorf("read resume: %w", err)
	}
	return string(data), abs, nil
}

// writeResumeEdit atomically writes updated resume content (surface-stamped) and
// returns the standard result payload.
func writeResumeEdit(vault *storage.Vault, absPath, project, updated, slug, position string) (any, error) {
	if err := atomicfile.Write(vault.Root, absPath, []byte(updated)); err != nil {
		return nil, fmt.Errorf("write resume: %w", err)
	}
	res := map[string]any{
		"project":       project,
		"vault_path":    absPath,
		"bytes_written": len(updated),
		"slug":          slug,
	}
	if position != "" {
		res["position"] = position
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// vp_thread_insert
// ---------------------------------------------------------------------------

var threadInsertSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"position": {
			"type": "object",
			"description": "Where to insert. mode is one of: top, bottom, after, before. For after/before, provide anchor_slug.",
			"properties": {
				"mode": {"type": "string", "enum": ["top", "bottom", "after", "before"]},
				"anchor_slug": {"type": "string", "description": "Slug of adjacent sub-heading (required for after/before modes)."}
			},
			"required": ["mode"]
		},
		"slug": {"type": "string", "description": "Slug for the new ### sub-heading. Must not already exist in Open Threads."},
		"body": {"type": "string", "description": "Body content for the new sub-section (everything after the ### heading line)."}
	},
	"required": ["project", "position", "slug", "body"]
}`)

// ThreadInsertTool inserts a new ### slug block under ## Open Threads.
func ThreadInsertTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_thread_insert",
		Description: "Insert a new ### slug block into the ## Open Threads " +
			"section of a project's resume.md. The slug must not already exist. " +
			"The body must NOT include the ### heading line — the tool emits it. " +
			"position.mode is one of top, bottom, after, before (after/before " +
			"require position.anchor_slug). The slug 'Carried forward' is " +
			"reserved for the vp_carried_* tools.",
		Schema: threadInsertSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Project  string `json:"project"`
				Position struct {
					Mode       string `json:"mode"`
					AnchorSlug string `json:"anchor_slug"`
				} `json:"position"`
				Slug string `json:"slug"`
				Body string `json:"body"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.Project == "" {
				return nil, fmt.Errorf("project is required")
			}
			if args.Slug == "" {
				return nil, fmt.Errorf("slug is required")
			}
			if args.Body == "" {
				return nil, fmt.Errorf("body is required")
			}
			if args.Position.Mode == "" {
				return nil, fmt.Errorf("position.mode is required")
			}

			content, absPath, err := readResumeForEdit(vault, args.Project)
			if err != nil {
				return nil, err
			}

			pos := mdutil.InsertPosition{Mode: args.Position.Mode, AnchorSlug: args.Position.AnchorSlug}
			updated, err := mdutil.InsertSubsection(content, openThreadsSection, pos, args.Slug, args.Body)
			if err != nil {
				return nil, err
			}

			posLabel := args.Position.Mode
			if args.Position.AnchorSlug != "" {
				posLabel = args.Position.Mode + ":" + args.Position.AnchorSlug
			}
			return writeResumeEdit(vault, absPath, args.Project, updated, args.Slug, posLabel)
		},
	}
}

// ---------------------------------------------------------------------------
// vp_thread_replace
// ---------------------------------------------------------------------------

var threadReplaceSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"slug": {"type": "string", "description": "Normalized slug of the ### sub-heading to replace (text up to first ' — ' or end-of-line)."},
		"body": {"type": "string", "description": "New body content (replaces everything after the heading up to the next ### or ##)."}
	},
	"required": ["project", "slug", "body"]
}`)

// ThreadReplaceTool replaces the body of an existing ### slug block.
func ThreadReplaceTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_thread_replace",
		Description: "Replace the body of an existing ### slug block in the " +
			"## Open Threads section of a project's resume.md. The body must NOT " +
			"include the ### heading line — the original heading is preserved " +
			"verbatim. A missing slug, or an ambiguous (multi-match) slug, is a " +
			"hard error. The slug 'Carried forward' is reserved for the " +
			"vp_carried_* tools.",
		Schema: threadReplaceSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Project string `json:"project"`
				Slug    string `json:"slug"`
				Body    string `json:"body"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.Project == "" {
				return nil, fmt.Errorf("project is required")
			}
			if args.Slug == "" {
				return nil, fmt.Errorf("slug is required")
			}
			if args.Body == "" {
				return nil, fmt.Errorf("body is required")
			}
			if args.Slug == carriedForwardSlug {
				return nil, fmt.Errorf("refusing to replace Carried forward sub-section; use vp_carried_* tools")
			}

			content, absPath, err := readResumeForEdit(vault, args.Project)
			if err != nil {
				return nil, err
			}

			updated, err := mdutil.ReplaceSubsectionBody(content, openThreadsSection, args.Slug, args.Body)
			if err != nil {
				return nil, err
			}
			return writeResumeEdit(vault, absPath, args.Project, updated, args.Slug, "")
		},
	}
}

// ---------------------------------------------------------------------------
// vp_thread_remove
// ---------------------------------------------------------------------------

var threadRemoveSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"slug": {"type": "string", "description": "Normalized slug of the ### sub-heading to remove."}
	},
	"required": ["project", "slug"]
}`)

// ThreadRemoveTool removes a ### slug block from ## Open Threads.
func ThreadRemoveTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_thread_remove",
		Description: "Remove a ### slug block from the ## Open Threads section " +
			"of a project's resume.md. A missing slug, or an ambiguous " +
			"(multi-match) slug, is a hard error. The slug 'Carried forward' is " +
			"reserved for the vp_carried_* tools.",
		Schema: threadRemoveSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Project string `json:"project"`
				Slug    string `json:"slug"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.Project == "" {
				return nil, fmt.Errorf("project is required")
			}
			if args.Slug == "" {
				return nil, fmt.Errorf("slug is required")
			}
			if args.Slug == carriedForwardSlug {
				return nil, fmt.Errorf("refusing to remove Carried forward sub-section; use vp_carried_* tools")
			}

			content, absPath, err := readResumeForEdit(vault, args.Project)
			if err != nil {
				return nil, err
			}

			updated, err := mdutil.RemoveSubsection(content, openThreadsSection, args.Slug)
			if err != nil {
				return nil, err
			}
			return writeResumeEdit(vault, absPath, args.Project, updated, args.Slug, "")
		},
	}
}
