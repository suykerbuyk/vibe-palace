// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Wrap-state + anchor tools (vp_collect_wrap_state, vp_stamp_iter,
// vp_preflight_wrap). These back the /wrap and /restart command surface: the
// collector returns every mechanically-computable fact the orchestrator needs,
// the stamper writes the project-side anchors under .vibe-palace/, and the
// preflight probe gates on surface compatibility while warning on dirty trees.
//
// All computation lives in internal/wrapstate; this layer resolves vault and
// project paths (via storage.(*Vault) helpers and internal/project detection)
// and marshals the results.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// resolveWrapProject returns the project slug, preferring the explicit value
// and falling back to detection from dir.
func resolveWrapProject(explicit, dir string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return project.DetectProject(dir)
}

// ---------------------------------------------------------------------------
// vp_collect_wrap_state
// ---------------------------------------------------------------------------

var collectWrapStateSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug. If omitted, detected from the working directory."}
	}
}`)

// CollectWrapStateTool returns the full wrap-state record for the current
// project.
func CollectWrapStateTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_collect_wrap_state",
		Description: "Return the full wrap-state record for the current project: " +
			"iter_n (a PREVIEW of the next iteration number, max ## Iteration N + 1; " +
			"vp_append_iteration mints the AUTHORITATIVE number under the file lock), " +
			"branch, last_iter_anchor_sha, " +
			"commits_since_last_iter, files_changed, task_deltas (added/retired/" +
			"cancelled via .vibe-palace/last-tasks-snapshot.json diff), test_counts " +
			"(parsed best-effort from doc/TESTING.md headline), " +
			"vault_has_uncommitted_writes, project_has_uncommitted_writes, and shape " +
			"(fresh-feature | planning | bookkeeping). Used by /wrap to compose the " +
			"iter narrative inline.",
		Schema: collectWrapStateSchema,
		Handler: func(ctx context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Project string `json:"project"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}

			cwd, err := os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("get working directory: %w", err)
			}
			projectRoot := wrapstate.ResolveProjectRoot(cwd)

			slug, err := resolveWrapProject(args.Project, projectRoot)
			if err != nil {
				return nil, err
			}

			iterPath, err := vault.IterationsFile(slug)
			if err != nil {
				return nil, err
			}
			tasksDir, err := vault.TasksDir(slug)
			if err != nil {
				return nil, err
			}

			runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			res, err := wrapstate.Collect(runCtx, wrapstate.CollectInput{
				VaultRoot:      vault.Root,
				Project:        slug,
				IterationsPath: iterPath,
				TasksDir:       tasksDir,
				ProjectRoot:    projectRoot,
			})
			if err != nil {
				return nil, err
			}
			return res, nil
		},
	}
}

// ---------------------------------------------------------------------------
// vp_stamp_iter
// ---------------------------------------------------------------------------

var stampIterSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug. If omitted, detected from project_path."},
		"project_path": {"type": "string", "description": "Absolute path to the local project repo root. Required."},
		"iter": {"type": "integer", "minimum": 1, "description": "The iteration number to stamp (>= 1)."}
	},
	"required": ["project_path", "iter"]
}`)

// StampIterTool writes the project-side anchors .vibe-palace/last-iter and
// .vibe-palace/last-tasks-snapshot.json.
func StampIterTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_stamp_iter",
		Mutating: true,
		Description: "Write the current iteration number to .vibe-palace/last-iter " +
			"AND a snapshot of the vault-side tasks/ tree (active/done/cancelled " +
			"slug sets) to .vibe-palace/last-tasks-snapshot.json at the project " +
			"root. Atomically replaces both files (project-root files, not vault " +
			"artifacts). last-iter is the canonical project-side anchor for the " +
			"wrap pipeline; last-tasks-snapshot.json anchors vp_collect_wrap_state's " +
			"task_deltas computation. Every wrap MUST stamp. project_path is " +
			"required and must be absolute. Returns {anchor_path, iter, " +
			"bytes_written, snapshot_path, snapshot_bytes}.",
		Schema: stampIterSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Project     string `json:"project"`
				ProjectPath string `json:"project_path"`
				Iter        int    `json:"iter"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.ProjectPath == "" {
				return nil, fmt.Errorf("project_path is required")
			}

			slug, err := resolveWrapProject(args.Project, args.ProjectPath)
			if err != nil {
				return nil, fmt.Errorf("detect project from %q: %w", args.ProjectPath, err)
			}
			tasksDir, err := vault.TasksDir(slug)
			if err != nil {
				return nil, err
			}

			res, err := wrapstate.StampIter(wrapstate.StampInput{
				Project:     slug,
				ProjectRoot: args.ProjectPath,
				TasksDir:    tasksDir,
				Iter:        args.Iter,
			})
			if err != nil {
				return nil, err
			}
			return res, nil
		},
	}
}

// ---------------------------------------------------------------------------
// vp_preflight_wrap
// ---------------------------------------------------------------------------

var preflightWrapSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug. If omitted, detected from project_path; if detection fails the vault_dirty probe degrades to whole-vault scope."},
		"project_path": {"type": "string", "description": "Absolute path to the local project repo root, or any path inside it. Required — the project is NEVER inferred from the server's working directory."}
	},
	"required": ["project_path"]
}`)

// PreflightWrapTool runs /wrap's readiness probe.
func PreflightWrapTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_preflight_wrap",
		Description: "Run /wrap's readiness probe over the repo at project_path: " +
			"surface compatibility (error if binary < vault stamp), vault dirty " +
			"(warning, scoped to Projects/<project>/), project dirty (warning), " +
			"and an unconsumed commit.msg (warning — the file is present on a " +
			"CLEAN tree, meaning a message was authored and never consumed, so " +
			"the next `git commit -F commit.msg` would reland it on different " +
			"work). Returns {ok, warnings[], errors[], notes[]}. Only ok=false " +
			"halts the wrap; warnings and notes never flip it.",
		Schema: preflightWrapSchema,
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

			// Resolve from the CALLER's path, never os.Getwd(). vp mcp is
			// long-lived and its cwd is the host's launch directory, not the
			// project the agent is working in — the same defect
			// internal/check/selector.go:14-19 documents. Under the old
			// resolution this probe reported on whichever repo the server
			// happened to be launched from.
			projectRoot := wrapstate.ResolveProjectRoot(args.ProjectPath)

			// Best-effort project resolution: a failure must not fail the
			// preflight — degrade to empty project (whole-vault scope).
			//
			// Deliberately NOT gated by project.RequireKnownProject, unlike the
			// commit tools. That gate exists to stop a write from scaffolding a
			// phantom Projects/<slug>/ from an unmanaged directory; this handler
			// writes nothing at all. Adding it here would refuse a read-only
			// probe for a hazard that cannot occur, and would break the
			// documented degrade-to-whole-vault-scope path above.
			slug, perr := resolveWrapProject(args.Project, projectRoot)
			if perr != nil {
				slug = ""
			}

			res := wrapstate.Preflight(vault.Root, projectRoot, slug)
			return res, nil
		},
	}
}
