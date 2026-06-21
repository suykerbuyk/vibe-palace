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
	if len(tools) != 66 {
		t.Fatalf("registered %d tools, want 66", len(tools))
	}

	wantNames := map[string]bool{
		"vp_bootstrap_context":       true,
		"vp_get_command":             true,
		"vp_get_skill":               true,
		"vp_list_commands":           true,
		"vp_list_skills":             true,
		"vp_cmd":                     true,
		"vp_skill":                   true,
		"vp_get_skill_section":       true,
		"vp_palace_status":           true,
		"vp_list_wings":              true,
		"vp_list_rooms":              true,
		"vp_traverse":                true,
		"vp_find_tunnels":            true,
		"vp_health":                  true,
		"vp_kg_query":                true,
		"vp_kg_add":                  true,
		"vp_kg_invalidate":           true,
		"vp_kg_timeline":             true,
		"vp_kg_stats":                true,
		"vp_search":                  true,
		"vp_search_cross_project":    true,
		"vp_capture_session":         true,
		"vp_get_friction_trends":     true,
		"vp_search_sessions":         true,
		"vp_get_session_detail":      true,
		"vp_get_project_context":     true,
		"vp_get_effectiveness":       true,
		"vp_get_workflow":            true,
		"vp_get_resume":              true,
		"vp_update_resume":           true,
		"vp_get_knowledge":           true,
		"vp_list_learnings":          true,
		"vp_get_learning":            true,
		"vp_list_projects":           true,
		"vp_append_iteration":        true,
		"vp_list_tasks":              true,
		"vp_get_task":                true,
		"vp_manage_task":             true,
		"vp_read_resource":           true,
		"vp_init":                    true,
		"vp_vault_sync":              true,
		"vp_vault_tidy":              true,
		"vp_refresh_index":           true,
		"vp_vault_read":              true,
		"vp_vault_list":              true,
		"vp_vault_exists":            true,
		"vp_vault_sha256":            true,
		"vp_vault_write":             true,
		"vp_vault_edit":              true,
		"vp_vault_delete":            true,
		"vp_vault_move":              true,
		"vp_memory_write":            true,
		"vp_memory_read":             true,
		"vp_memory_list":             true,
		"vp_memory_delete":           true,
		"vp_memory_harvest":          true,
		"vp_ingest_commit_msg":       true,
		"vp_thread_insert":           true,
		"vp_thread_replace":          true,
		"vp_thread_remove":           true,
		"vp_carried_add":             true,
		"vp_carried_remove":          true,
		"vp_carried_promote_to_task": true,
		"vp_collect_wrap_state":      true,
		"vp_stamp_iter":              true,
		"vp_preflight_wrap":          true,
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
	if len(tools) != 57 {
		t.Fatalf("registered %d tools with nil engine, want 57", len(tools))
	}
}
