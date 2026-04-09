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
	if len(tools) != 26 {
		t.Fatalf("registered %d tools, want 26", len(tools))
	}

	wantNames := map[string]bool{
		"vp_bootstrap_context":    true,
		"vp_get_command":          true,
		"vp_get_skill":           true,
		"vp_list_commands":        true,
		"vp_list_skills":          true,
		"vp_cmd":                  true,
		"vp_skill":                true,
		"vp_palace_status":        true,
		"vp_list_wings":           true,
		"vp_list_rooms":           true,
		"vp_traverse":             true,
		"vp_find_tunnels":         true,
		"vp_health":               true,
		"vp_kg_query":             true,
		"vp_kg_add":               true,
		"vp_kg_invalidate":        true,
		"vp_kg_timeline":          true,
		"vp_kg_stats":             true,
		"vp_search":               true,
		"vp_search_cross_project": true,
		"vp_capture_session":      true,
		"vp_get_friction_trends":  true,
		"vp_search_sessions":      true,
		"vp_get_session_detail":   true,
		"vp_get_project_context":  true,
		"vp_get_effectiveness":    true,
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
	if len(tools) != 18 {
		t.Fatalf("registered %d tools with nil engine, want 18", len(tools))
	}
}
