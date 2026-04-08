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

func TestRegisterAll(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)
	srv := mcp.NewServer(vault)
	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := search.NewEngine(embedder.NewMock(384), vault, cfg)

	RegisterAll(srv.Registry(), resolver, vault, eng)

	tools := srv.Registry().List()
	if len(tools) != 11 {
		t.Fatalf("registered %d tools, want 11", len(tools))
	}

	wantNames := map[string]bool{
		"vp_bootstrap_context":    true,
		"vp_get_command":          true,
		"vp_get_skill":           true,
		"vp_list_commands":        true,
		"vp_list_skills":          true,
		"vp_cmd":                  true,
		"vp_skill":                true,
		"vp_search":               true,
		"vp_search_cross_project": true,
		"vp_capture_session":      true,
		"vp_get_friction_trends":  true,
	}
	for _, tool := range tools {
		if !wantNames[tool.Name] {
			t.Errorf("unexpected tool registered: %q", tool.Name)
		}
		delete(wantNames, tool.Name)
	}
	for name := range wantNames {
		t.Errorf("expected tool not registered: %q", name)
	}
}

func TestRegisterAllNilEngine(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)
	srv := mcp.NewServer(vault)

	RegisterAll(srv.Registry(), resolver, vault, nil)

	tools := srv.Registry().List()
	if len(tools) != 7 {
		t.Fatalf("registered %d tools with nil engine, want 7", len(tools))
	}
}
