// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

// vp_vault_rename is the MCP surface of `vp vault rename`: it renames a project
// from one slug to another INSIDE the vault this server serves. Every check and
// write lives in storage.PlanRename / storage.ApplyRename; this file only
// decodes and renders. Like vp_vault_copy it is served over HTTP `vp mcp serve`
// only with --allow-writes (it is not in ReadOnlyServeToolNames), and a stale
// binary admits only its plan action (vaultRenameReadOnly).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

type vaultRenameParams struct {
	Action string `json:"action"`
	From   string `json:"from"`
	To     string `json:"to"`
	Expect string `json:"expect"`
}

var vaultRenameSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {"type": "string", "enum": ["plan", "apply"], "description": "\"plan\" is the dry run: it runs every refusal and returns the move set, the rewrite class counts, the push targets, the digest and the exact real-run command line, writing nothing to the vault. \"apply\" plans again, moves the footprint, rewrites the identifiers, writes a renamed departure record, commits once and publishes exactly that commit to every remote, or refuses and rolls back."},
		"from": {"type": "string", "description": "The project's current slug. It must be present (tracked) in the served vault."},
		"to": {"type": "string", "description": "The new slug. It must be absent from the served vault (no Projects/<to>, no palace/<to>, no departure record)."},
		"expect": {"type": "string", "description": "The digest plan returned. apply refuses unless the plan still digests to it. The digest binds the slugs, the served vault's identity, the move set and the rewrite class counts, never a HEAD, so an unrelated push does not change it."}
	},
	"required": ["action", "from", "to"]
}`)

// vaultRenameReadOnly admits plan, which writes nothing to the vault.
var vaultRenameReadOnly = readOnlyIf(func(p vaultRenameParams) bool {
	return p.Action == "plan"
})

// VaultRenameTool registers vp_vault_rename.
func VaultRenameTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:         "vp_vault_rename",
		Mutating:     true,
		ReadOnlyWhen: vaultRenameReadOnly,
		// 🔴 THIS TEXT IS PART OF THE SURFACE AND MUST TRACK THE HANDLER.
		Description: "Rename a project from one slug to another INSIDE the vault this server serves " +
			"(in-vault; no other vault is touched). \"plan\" returns the move set, the rewrite class " +
			"counts, the push targets, the digest and the exact real-run command line; it writes nothing. " +
			"\"apply\" re-plans, writes a pending marker, moves the footprint (Projects/<from>/ -> " +
			"Projects/<to>/ and palace/<from>/ -> palace/<to>/), rewrites every stored identifier " +
			"(frontmatter, note paths, manifests, drawer ids, headings, baseline), writes a renamed " +
			"departure record for the old slug, makes ONE commit carrying Vp-Rename-* trailers, checks " +
			"that nothing still names the old slug, and publishes exactly that commit to every remote " +
			"with no rebase and no force; if the first remote moved it resets and rolls back. It refuses " +
			"a new slug the vault already holds or records a departure for, a vault not at every remote's " +
			"tip, a dirty footprint and a format mismatch. There is no --no-push.",
		Schema:  vaultRenameSchema,
		Handler: vaultRenameHandler(vault),
	}
}

type vaultRenameResult struct {
	Action string              `json:"action"`
	Plan   *storage.RenamePlan `json:"plan,omitempty"`
	Commit string              `json:"commit,omitempty"`
	Redo   storage.RedoOutcome `json:"redo,omitempty"`
	Undo   []string            `json:"undo,omitempty"`
	// HostLocal reports the host-local index step (index store move, imports
	// carry), run after the tracked commit published.
	HostLocal *indexstore.RenameHostLocalResult `json:"host_local,omitempty"`
	Complete  bool                              `json:"complete"`
}

func vaultRenameHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		var p vaultRenameParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if vault == nil || vault.Root == "" {
			return nil, apperr.Caller(fmt.Errorf("no vault is bound: vp_vault_rename acts only on the vault this server serves"))
		}
		req := storage.RenameRequest{Vault: vault.Root, From: p.From, To: p.To, Expect: p.Expect}
		switch p.Action {
		case "plan":
			plan, err := storage.PlanRename(req)
			if err != nil {
				return nil, classifyRenameErr(err)
			}
			return &vaultRenameResult{Action: "plan", Plan: plan, Complete: true}, nil
		case "apply":
			res, err := storage.ApplyRename(req)
			if err != nil {
				return nil, classifyRenameErr(err)
			}
			out := &vaultRenameResult{Action: "apply", Plan: res.Plan, Commit: res.Commit, Redo: res.Redo, Undo: res.Undo, Complete: true}
			// The tracked rename has published; now the host-local index step
			// (storage cannot import indexstore, so it is driven from here).
			if res.Commit != "" {
				hl, herr := indexstore.AdoptRenamedProject(ctx, vault, p.From, p.To)
				if herr != nil {
					return nil, fmt.Errorf("the rename committed and published, but the host-local index step failed (re-run the rename, or run `vp index rebuild %s`): %w", p.To, herr)
				}
				out.HostLocal = &hl
			}
			return out, nil
		default:
			return nil, apperr.Caller(fmt.Errorf("invalid action %q: expected plan or apply", p.Action))
		}
	}
}

// classifyRenameErr marks refusals the caller must act on as caller errors.
func classifyRenameErr(err error) error {
	var pending *storage.LifecyclePendingError
	if errors.Is(err, storage.ErrRenameRefused) || errors.As(err, &pending) {
		return apperr.Caller(err)
	}
	return err
}
