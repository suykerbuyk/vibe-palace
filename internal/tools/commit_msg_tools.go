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
	"log/slog"
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

// hookStatusString renders a storage.HookStatus for the MCP result. The wire
// vocabulary is closed and stable; an unknown value is reported as such rather
// than silently mapped onto a neighbouring state.
func hookStatusString(s storage.HookStatus) string {
	switch s {
	case storage.HookInstalled:
		return "installed"
	case storage.HookCurrent:
		return "current"
	case storage.HookMissing:
		return "missing"
	case storage.HookStale:
		return "stale"
	case storage.HookForeign:
		return "foreign"
	case storage.HookSharedHooksPath:
		return "shared_hooks_path"
	case storage.HookNoRepo:
		return "no_repo"
	case storage.HookError:
		return "error"
	default:
		return "unknown"
	}
}

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
			"missing or empty. Also installs the project's post-commit hook that " +
			"deletes commit.msg once the commit consuming it lands, so the " +
			"trailing `&& rm commit.msg` stops being load-bearing; that install " +
			"is idempotent, refuses a foreign hook or a repo with core.hooksPath " +
			"set, and never fails the ingest. Returns {project, vault_path, " +
			"bytes_written, commit_msg_hook{status, detail}}.",
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

			// Refuse to scaffold a phantom vault project from an unmanaged
			// directory. atomicfile.Write below lazily creates Projects/<slug>/
			// on first write, so without this gate a basename-derived slug in
			// any dirty directory silently materializes a vault project. Key on
			// the resolved repo ROOT, not args.ProjectPath.
			if err := project.RequireKnownProject(slug, vault.Root, root); err != nil {
				return nil, err
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

			// Install the post-commit reaper on the repo this message is being
			// authored for. This is the wrap-time reach into an EXISTING clone:
			// such a clone never re-runs `vp init`, and the hook is what makes
			// the `&& rm commit.msg` optional rather than load-bearing.
			//
			// It belongs on THIS tool and not on a preflight because this is the
			// commit.msg lifecycle: the tool that publishes the message installs
			// the thing that consumes it. Idempotent, refuses rather than
			// clobbers, and never fails the ingest — a hook that could not be
			// installed is reported, not raised, because the message itself
			// landed.
			hookRep := storage.InstallPostCommitHook(root)
			if hookRep.Status != storage.HookInstalled && hookRep.Status != storage.HookCurrent {
				slog.Warn("post-commit hook not installed", "reason", hookRep.Detail, "root", root)
			}

			return map[string]any{
				"project":       slug,
				"vault_path":    dest,
				"bytes_written": len(data),
				"commit_msg_hook": map[string]any{
					"status": hookStatusString(hookRep.Status),
					"detail": hookRep.Detail,
				},
			}, nil
		},
	}
}
