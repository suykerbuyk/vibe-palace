// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Plan-orphan reporter (vp_scan_plans). A read-only probe that scans Claude
// Code's FLAT ~/.claude/plans/*.md directory and, for each stray plan, reports
// the absolute paths grepped from its body and an attribution verdict —
// managed (belongs to a vibe-palace-managed project, with the slug), unmanaged
// (a real dir with no .vibe-palace.toml), or none (no absolute path in the body,
// so it is unattributable). It NEVER promotes, deletes, or writes anything.
//
// All logic lives in internal/planscan; this layer only resolves the Claude
// home (via archive.ClaudeHome, honoring CLAUDE_HOME) and the vault root, then
// marshals the Report. Claude-only: Grok and Zed have no plans dir, so the
// scan returns an empty report there.

package tools

import (
	"context"
	"encoding/json"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/planscan"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var scanPlansSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug. Accepted for parity with other project-scoped read tools; the plans scan always covers every ~/.claude/plans/*.md file and attributes each against the whole vault, so this value does not narrow the report."}
	}
}`)

// ScanPlansTool exposes the orphaned-plan reporter as a non-mutating MCP tool.
func ScanPlansTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_scan_plans",
		Description: "Read-only reporter: scan Claude Code's flat ~/.claude/plans/*.md " +
			"directory (honors CLAUDE_HOME) and, for each stray plan file, report the " +
			"absolute paths grepped from its body and an attribution — managed " +
			"(belongs to a vibe-palace-managed project; project holds the slug), " +
			"unmanaged (resolves to a real dir with no .vibe-palace.toml; candidate_dir " +
			"is the evidence), or none (no absolute path in the body, unattributable). " +
			"Multi-root plans set ambiguous=true and list every candidate rather than " +
			"guess one owner. Returns {plans_dir, strays:[{file, abs_paths[], " +
			"resolution:{kind, project, candidate_dir, candidates[], ambiguous}}]}. " +
			"An absent plans dir (Grok/Zed hosts) returns an empty report. Strictly " +
			"read-only: never promotes, deletes, or writes.",
		Schema: scanPlansSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Project string `json:"project"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}

			claudeHome, err := archive.ClaudeHome()
			if err != nil {
				return nil, err
			}

			return planscan.Scan(claudeHome, vault.Root)
		},
	}
}
