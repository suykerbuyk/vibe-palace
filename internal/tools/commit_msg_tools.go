// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// vp_ingest_commit_msg — read-from-file commit-message ingest.
//
// This intentionally diverges from vibe-vault's vv_set_commit_msg(content):
// the message is emitted exactly once. The agent has already written the
// project-root commit.msg itself; this tool READS that file off disk and
// stamps the vault copy at Projects/<slug>/commit.msg via atomicfile.Write
// (which auto-stamps the MCP surface version). No content parameter exists.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

var ingestCommitMsgSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug. If omitted, detected from project_path."},
		"project_path": {"type": "string", "description": "Absolute path to the local project repo root that holds commit.msg. Required."}
	},
	"required": ["project_path"]
}`)

// IngestCommitMsgTool reads <project_path>/commit.msg off disk and writes the
// stamped vault copy at Projects/<slug>/commit.msg.
func IngestCommitMsgTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_ingest_commit_msg",
		Mutating: true,
		Description: "Ingest a commit message the agent already wrote to " +
			"<project_path>/commit.msg into the vault at " +
			"Projects/<slug>/commit.msg (atomic, surface-stamped). There is no " +
			"content parameter — the file is read off disk so the message is " +
			"emitted exactly once. project_path is the local project repo root " +
			"(read with normal filesystem I/O, not vault-relative). If project is " +
			"omitted it is detected from project_path. Errors if commit.msg is " +
			"missing or empty. Returns {project, vault_path, bytes_written}.",
		Schema: ingestCommitMsgSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Project     string `json:"project"`
				ProjectPath string `json:"project_path"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.ProjectPath == "" {
				return nil, fmt.Errorf("project_path is required")
			}

			// Refuse to author a commit message for an empty diff. Resolve to
			// the repo ROOT first: project_path is not validated as a root, and
			// probing a subdirectory would shallow-stat as not-a-repo and take
			// the permit branch, silently defeating this check.
			//
			// Only a CLEAN repo refuses. A non-repo project is a legitimate
			// wrap target, and a flaky/slow probe must never block one — so
			// GitNotARepo, GitDirty, and any probe error all permit.
			root := wrapstate.ResolveProjectRoot(args.ProjectPath)
			if state, err := wrapstate.ProjectGitState(root); err == nil && state == wrapstate.GitClean {
				return nil, fmt.Errorf("refusing to write commit.msg: the project repo at %q "+
					"has no uncommitted changes — there is nothing to describe", root)
			}

			// Resolve the project slug: explicit wins, else detect from the
			// project repo root.
			slug := args.Project
			if slug == "" {
				detected, err := project.DetectProject(args.ProjectPath)
				if err != nil {
					return nil, fmt.Errorf("detect project from %q: %w", args.ProjectPath, err)
				}
				slug = detected
			}

			// Read the project-root commit.msg (normal filesystem I/O — this
			// is NOT a vault path).
			src := filepath.Join(args.ProjectPath, "commit.msg")
			data, err := os.ReadFile(src)
			if err != nil {
				if os.IsNotExist(err) {
					return nil, fmt.Errorf("commit.msg not found at %q; write it before ingesting", src)
				}
				return nil, fmt.Errorf("read %q: %w", src, err)
			}
			if len(strings.TrimSpace(string(data))) == 0 {
				return nil, fmt.Errorf("commit.msg at %q is empty", src)
			}

			// Write the stamped vault copy. atomicfile.Write stamps the MCP
			// surface version for vault.Root/dest on success.
			dest, err := vault.CommitMsgFile(slug)
			if err != nil {
				return nil, err
			}
			if err := atomicfile.Write(vault.Root, dest, data); err != nil {
				return nil, fmt.Errorf("write vault commit.msg: %w", err)
			}

			return map[string]any{
				"project":       slug,
				"vault_path":    dest,
				"bytes_written": len(data),
			}, nil
		},
	}
}
