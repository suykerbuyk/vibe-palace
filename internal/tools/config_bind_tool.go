// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// 🔴 THE TARGET IS `slug`, NEVER `project`. The dispatch seam refuses a
// mutating call whose `project` names a project that departed this server's
// vault (mcp.refuseDepartedProject), and binding a departed project to the
// vault it moved to is this tool's whole purpose. `slug` is invisible to that
// seam and to its naming pin, so the exemption is declared, scoped to this one
// tool, by TestOnlyTheBindToolTargetsAProjectBySlug.
var configBindSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"slug":             {"type": "string", "description": "The project's slug, as its checkouts' .vibe-palace.toml [project].name names it. Give slug or slugs, not both."},
		"slugs":            {"type": "array", "items": {"type": "string"}, "description": "Several project slugs, all bound to vault_path in ONE compare-and-set write of the host config, so the bind is all-or-nothing and config.toml.bak is the true pre-image. Give slug or slugs, not both."},
		"vault_path":       {"type": "string", "description": "The vault the project lives in on this host. Absolute, or starting with ~/; written into the host's [project_vaults] exactly as given."},
		"mode":             {"type": "string", "enum": ["moved", "new"], "description": "moved (default): the project was split out of this host's default vault, which must hold its moved-to-vault departure record. new: the project was born in another vault and the default vault never held it."},
		"checkouts":        {"type": "array", "items": {"type": "string"}, "description": "Absolute checkout roots to verify: after the write each must resolve the vault through the binding, or the config is restored."},
		"allow_unlabelled": {"type": "boolean", "description": "Bind even though the departure record names no destination to match against the vault's remotes."},
		"dry_run":          {"type": "boolean", "description": "Check everything and report the change; write nothing."}
	},
	"required": ["vault_path"]
}`)

type configBindParams struct {
	Slug            string   `json:"slug"`
	Slugs           []string `json:"slugs"`
	VaultPath       string   `json:"vault_path"`
	Mode            string   `json:"mode"`
	Checkouts       []string `json:"checkouts"`
	AllowUnlabelled bool     `json:"allow_unlabelled"`
	DryRun          bool     `json:"dry_run"`
}

// ConfigBindTool is vp_config_bind: bind a project to a vault on THIS host,
// the one [project_vaults] line in the global config (ADR-012). It is
// registered on the stdio transport only — on `vp mcp serve` the host config
// and the checkout paths belong to the server host, not the caller.
func ConfigBindTool() mcp.Tool {
	return mcp.Tool{
		Name:     "vp_config_bind",
		Mutating: true,
		Description: "Bind projects to a vault on THIS host: writes one [project_vaults].<slug> line per slug into the " +
			"host's global config (ADR-012), all in one write, so every checkout of each project on this host resolves that vault. " +
			"Use it after a vault split (mode moved: the host's default vault must record the project as moved " +
			"to another vault, and one of the target vault's git remotes must equal the recorded destination) or " +
			"for a project born in another vault (mode new). It refuses a target vault that holds any departure record " +
			"for the project, whatever its directory still holds. It never writes a checkout — a committed " +
			".vibe-palace.toml never carries vault_path — never re-points an existing binding, and never creates " +
			"the global config. The write is compare-and-set on the file's bytes and is verified through the " +
			"resolver from every named checkout; any failure restores the file. A running MCP server resolved " +
			"its vault at startup, so after a bind it refuses writes until the AI host reloads it. Returns " +
			"{config_path, change, backup_path, vault, checkouts[], grep_hits[], grep_total, warnings[], dry_run}.",
		Schema:  configBindSchema,
		Handler: configBindHandler,
	}
}

func configBindHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p configBindParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, apperr.Caller(fmt.Errorf("parse params: %w", err))
	}
	mode := storage.BindMode(p.Mode)
	if p.Mode == "" {
		mode = storage.BindMoved
	}
	for _, c := range p.Checkouts {
		if !filepath.IsAbs(c) {
			return nil, apperr.Caller(fmt.Errorf("checkout %q is not an absolute path", c))
		}
	}
	slugs := p.Slugs
	switch {
	case p.Slug != "" && len(p.Slugs) > 0:
		return nil, apperr.Caller(fmt.Errorf("give slug or slugs, not both"))
	case p.Slug != "":
		slugs = []string{p.Slug}
	case len(slugs) == 0:
		return nil, apperr.Caller(fmt.Errorf("name a project: slug or slugs"))
	}
	rep, err := storage.BindProjectVaults(storage.BindVaultsRequest{
		Slugs: slugs, VaultPath: p.VaultPath, Mode: mode, Checkouts: p.Checkouts,
		AllowUnlabelled: p.AllowUnlabelled, DryRun: p.DryRun,
	})
	if err != nil {
		return nil, apperr.Caller(err)
	}
	return rep, nil
}
