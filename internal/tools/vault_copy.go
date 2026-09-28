// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

// vp_vault_copy is the MCP surface of `vp vault copy`: it copies projects from
// another vault's PUBLISHED remote into the vault this server serves. Every
// check and write lives in storage.PlanCopy / storage.ApplyCopy; this file
// only decodes and renders. It is served over HTTP `vp mcp serve` only with
// --allow-writes (it is not in ReadOnlyServeToolNames), and a stale binary
// admits only its plan action (vaultCopyReadOnly).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

type vaultCopyParams struct {
	Action string   `json:"action"`
	Slugs  []string `json:"slugs"`
	From   string   `json:"from"`
	At     string   `json:"at"`
	Expect string   `json:"expect"`
}

var vaultCopySchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {"type": "string", "enum": ["plan", "apply"], "description": "\"plan\" is the dry run: it reads the source remote into a private snapshot, runs every refusal and returns the file list, the push targets, the digest and the exact real-run command line, writing nothing to the vault. \"apply\" plans again, then copies, commits once and publishes exactly that commit to every remote of the served vault, or refuses and rolls back."},
		"slugs": {"type": "array", "items": {"type": "string"}, "description": "Project slugs to copy, under the same names. Each must be absent from the served vault and hold at least one content file at the source."},
		"from": {"type": "string", "description": "The source vault's PUBLISHED git remote URL. Never a host path: copy reads another vault only through its remote."},
		"at": {"type": "string", "description": "The full source commit the plan was made against (plan returns it). A source tip that moved past it is accepted only if it is an ancestor of the tip and no copied project changed."},
		"expect": {"type": "string", "description": "The digest plan returned. apply refuses unless the plan still digests to it. The digest binds the projects, the served vault's identity, the source URL, each project's footprint hash and every file's blob id, never a HEAD, so an unrelated push to either vault does not change it."}
	},
	"required": ["action", "slugs", "from"]
}`)

// vaultCopyReadOnly admits plan, which writes nothing to the vault. An
// allow-list: an action added later is refused by a stale binary until it is
// named here.
var vaultCopyReadOnly = readOnlyIf(func(p vaultCopyParams) bool {
	return p.Action == "plan"
})

// VaultCopyTool registers vp_vault_copy.
func VaultCopyTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:         "vp_vault_copy",
		Mutating:     true,
		ReadOnlyWhen: vaultCopyReadOnly,
		// 🔴 THIS TEXT IS PART OF THE SURFACE AND MUST TRACK THE HANDLER.
		Description: "Copy projects from another vault's published git remote INTO the vault " +
			"this server serves (receiver-run; the source vault is only read, through its remote). " +
			"\"plan\" fetches the source into a private blobless snapshot and returns the file list, " +
			"every refusal, the push targets, the digest and the exact real-run command line; it " +
			"writes nothing to the vault. \"apply\" re-plans, writes a pending marker, copies each " +
			"project's footprint (Projects/<slug>/ and palace/<slug>/ content, never .surface, " +
			"Projects/<slug>/config.toml or .local/**) from the snapshot, makes ONE commit carrying " +
			"Vp-Copy-* trailers, checks the committed footprint hash against the source's, and " +
			"publishes exactly that commit to every remote with no rebase and no force; if the " +
			"first remote moved it resets and rolls back. It refuses a project the vault already " +
			"holds or records a departure for, a source that is one of the vault's own remotes, a " +
			"format mismatch, an empty footprint, and any vault not at every remote's tip. A re-run " +
			"of an interrupted apply finishes or rolls back that same run. There is no --no-push.",
		Schema:  vaultCopySchema,
		Handler: vaultCopyHandler(vault),
	}
}

type vaultCopyResult struct {
	Action   string              `json:"action"`
	Plan     *storage.CopyPlan   `json:"plan,omitempty"`
	Commit   string              `json:"commit,omitempty"`
	Redo     storage.RedoOutcome `json:"redo,omitempty"`
	Undo     []string            `json:"undo,omitempty"`
	Complete bool                `json:"complete"`
}

func vaultCopyHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p vaultCopyParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if vault == nil || vault.Root == "" {
			return nil, apperr.Caller(fmt.Errorf("no vault is bound: vp_vault_copy acts only on the vault this server serves"))
		}
		req := storage.CopyRequest{Vault: vault.Root, Projects: p.Slugs, From: p.From, At: p.At, Expect: p.Expect}
		switch p.Action {
		case "plan":
			plan, err := storage.PlanCopy(req)
			if err != nil {
				return nil, classifyCopyErr(err)
			}
			return &vaultCopyResult{Action: "plan", Plan: plan, Complete: true}, nil
		case "apply":
			res, err := storage.ApplyCopy(req)
			if err != nil {
				return nil, classifyCopyErr(err)
			}
			return &vaultCopyResult{Action: "apply", Plan: res.Plan, Commit: res.Commit, Redo: res.Redo, Undo: res.Undo, Complete: true}, nil
		default:
			return nil, apperr.Caller(fmt.Errorf("invalid action %q: expected plan or apply", p.Action))
		}
	}
}

// classifyCopyErr marks refusals the caller must act on as caller errors.
func classifyCopyErr(err error) error {
	var pending *storage.LifecyclePendingError
	if errors.Is(err, storage.ErrCopyRefused) || errors.As(err, &pending) {
		return apperr.Caller(err)
	}
	return err
}
