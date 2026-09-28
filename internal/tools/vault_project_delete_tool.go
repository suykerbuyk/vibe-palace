// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// 🔴 THE TARGETS ARE `slugs`, NEVER `project`, for vp_config_bind's reason. The
// dispatch seam refuses a mutating call whose `project` names a departed
// project, and a leftovers-only re-run targets exactly such a project; the
// core (storage.PlanDelete/ApplyDelete) makes every refusal a delete needs.
var vaultProjectDeleteSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"action":   {"type": "string", "enum": ["plan", "apply"], "description": "plan: the dry run — every check, the file lists, the leftovers that git revert cannot restore, the push targets and the digest; writes nothing. apply: delete, commit and publish, then remove the leftovers."},
		"slugs":    {"type": "array", "items": {"type": "string"}, "description": "The project slugs to delete from this server's vault."},
		"moved_to": {"type": "string", "description": "The destination vault's published remote URL. The delete refuses unless that remote holds a verified copy of every project. Exactly one of moved_to and discard."},
		"discard":  {"type": "boolean", "description": "Delete with no destination: the project is discarded (departure kind deleted)."},
		"expect":   {"type": "string", "description": "apply: the digest plan returned; apply refuses on any mismatch."}
	},
	"required": ["action", "slugs"]
}`)

type vaultProjectDeleteParams struct {
	Action  string   `json:"action"`
	Slugs   []string `json:"slugs"`
	MovedTo string   `json:"moved_to"`
	Discard bool     `json:"discard"`
	Expect  string   `json:"expect"`
}

var vaultProjectDeleteReadOnly = readOnlyIf(func(p vaultProjectDeleteParams) bool { return p.Action == "plan" })

// VaultProjectDeleteTool is vp_vault_project_delete, the MCP twin of `vp vault
// project delete`. It is registered on the stdio transport only (operator
// ruling Q2): a delete is never offered over `vp mcp serve`.
func VaultProjectDeleteTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:         "vp_vault_project_delete",
		Mutating:     true,
		ReadOnlyWhen: vaultProjectDeleteReadOnly,
		Description: "Delete projects from this server's vault in one published commit that carries their departure " +
			"records. With moved_to, the destination vault's published remote must hold a verified copy (footprint " +
			"equal at the copy commit, its trailer, this vault's HEAD and the destination tip); with discard, the " +
			"project is discarded. The vault must equal every remote's live tip. The commit is published to every " +
			"remote exactly, never rebased; ignored leftovers (not restorable by git revert) are removed only after " +
			"every remote holds the commit. Run plan first and pass its digest as expect. A re-run finishes an " +
			"unfinished run. Returns the plan, or {plan, commit, redo, leftover_files_removed, kept[], undo[]}.",
		Schema:  vaultProjectDeleteSchema,
		Handler: vaultProjectDeleteHandler(vault),
	}
}

func vaultProjectDeleteHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p vaultProjectDeleteParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, apperr.Caller(fmt.Errorf("parse params: %w", err))
		}
		req := storage.DeleteRequest{Projects: p.Slugs, MovedTo: p.MovedTo, Discard: p.Discard, Expect: p.Expect}
		switch p.Action {
		case "plan":
			plan, err := storage.PlanDelete(vault.Root, req)
			if err != nil {
				return nil, apperr.Caller(err)
			}
			return plan, nil
		case "apply":
			res, err := storage.ApplyDelete(vault.Root, req)
			if err != nil {
				return nil, apperr.Caller(err)
			}
			return res, nil
		default:
			return nil, apperr.Caller(fmt.Errorf("action %q: want plan or apply", p.Action))
		}
	}
}
