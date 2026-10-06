// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestRetiredSplitMergeToolsAreGone pins the U12 retirement of the
// vp_vault_split and vp_vault_merge MCP tools
// (rename-docs-harness-deletion-and-registration). The roster-count check in
// TestRegisterAll would catch a re-add by number; this one names the retired
// tools so a reviewer sees intent, and it can fail — re-add either
// MustRegister line in register.go and the matching assertion reddens.
func TestRetiredSplitMergeToolsAreGone(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)
	srv := mcp.NewServer(vault)
	eng := search.NewEngine(embedder.NewMock(384), vault, storage.Config{SearchDefaultLimit: 10})
	RegisterAll(srv.Registry(), resolver, vault, eng)

	present := map[string]bool{}
	for _, tool := range srv.Registry().List() {
		present[tool.Name] = true
	}
	for _, name := range []string{"vp_vault_split", "vp_vault_merge"} {
		if present[name] {
			t.Errorf("retired tool %q is still registered on the MCP surface", name)
		}
	}
}
