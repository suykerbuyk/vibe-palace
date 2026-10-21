// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Worktree-reconcile reporter (vp_worktree_reconcile). A read-only probe that
// enumerates every git worktree of the project at project_path and classifies
// each for safe reclamation: role (main/harness/vibe-palace), on/off-convention,
// merged-into-local-main, dirty, and — computed once — whether local main is on
// the LIVE remote (one git ls-remote, never a fetch, never a tracking ref). It
// NEVER removes, fetches, or writes anything.
//
// All logic lives in internal/worktree (pure, no vault/taskgraph import, mirroring
// internal/planscan). This layer only resolves the project root from the REQUIRED
// project_path and marshals the report. Like vp_repo_freshness, it never infers a
// cwd: a long-lived vp mcp server's working directory is not the checkout.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/worktree"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

var worktreeReconcileSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project_path": {
			"type": "string",
			"description": "Absolute path to the project's git checkout to reconcile. REQUIRED — this tool never infers a cwd, because a long-lived vp mcp server's cwd is not the checkout the operator means (mirrors vp_repo_freshness)."
		},
		"check_remote": {
			"type": "boolean",
			"description": "Probe the live remote (git ls-remote) to confirm local main is pushed — the signal that gates 'reclaimable' (default true). false skips all network and nothing is reclaimable. It never fetches and never reads a tracking ref."
		}
	},
	"required": ["project_path"]
}`)

// WorktreeReconcileTool exposes the read-only worktree reconciler as a
// non-mutating MCP tool. It targets the PROJECT repo (not the vault), so it takes
// no vault and carries no vault surface gate.
func WorktreeReconcileTool() mcp.Tool {
	return mcp.Tool{
		Name:     "vp_worktree_reconcile",
		Mutating: false,
		Description: "Read-only reporter: enumerate every git worktree of the project at project_path " +
			"and classify each — role (main/harness/vibe-palace), on/off-convention, " +
			"merged-into-local-main, dirty — plus whether local main is on the LIVE remote (one git " +
			"ls-remote, computed once), and a verdict: reclaimable (on-convention, clean, merged, main " +
			"pushed, and — for an epic integration tree — no task worktree/branch left), keep (with a " +
			"reason), or skip (the main checkout and harness .claude/worktrees trees). Returns " +
			"{main_on_remote, entries:[{path, branch, name, role, on_convention, merged, dirty, pushed, " +
			"verdict, reason}]}. Strictly read-only: it never removes, fetches, or reads a tracking ref. " +
			"The human removes a reclaimable tree after their push.",
		Schema: worktreeReconcileSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				ProjectPath string `json:"project_path"`
				CheckRemote *bool  `json:"check_remote"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if strings.TrimSpace(args.ProjectPath) == "" {
				return nil, fmt.Errorf("project_path is required")
			}
			checkRemote := true
			if args.CheckRemote != nil {
				checkRemote = *args.CheckRemote
			}
			root := wrapstate.ResolveProjectRoot(args.ProjectPath)
			return worktree.New(root).Reconcile(worktree.ReconcileOptions{CheckRemote: checkRemote})
		},
	}
}
