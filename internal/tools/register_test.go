// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
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
	if len(tools) != 76 {
		t.Fatalf("registered %d tools, want 76", len(tools))
	}

	wantNames := map[string]bool{
		"vp_bootstrap_context":           true,
		"vp_get_command":                 true,
		"vp_get_skill":                   true,
		"vp_list_commands":               true,
		"vp_list_skills":                 true,
		"vp_cmd":                         true,
		"vp_skill":                       true,
		"vp_get_skill_section":           true,
		"vp_palace_status":               true,
		"vp_palace_query":                true,
		"vp_palace_backfill_decisions":   true,
		"vp_list_wings":                  true,
		"vp_list_rooms":                  true,
		"vp_traverse":                    true,
		"vp_find_tunnels":                true,
		"vp_health":                      true,
		"vp_kg_query":                    true,
		"vp_kg_add":                      true,
		"vp_kg_invalidate":               true,
		"vp_kg_timeline":                 true,
		"vp_kg_stats":                    true,
		"vp_search":                      true,
		"vp_search_cross_project":        true,
		"vp_capture_session":             true,
		"vp_get_friction_trends":         true,
		"vp_search_sessions":             true,
		"vp_get_session_detail":          true,
		"vp_get_project_context":         true,
		"vp_get_effectiveness":           true,
		"vp_get_workflow":                true,
		"vp_get_doctrine":                true,
		"vp_manual":                      true,
		"vp_get_resume":                  true,
		"vp_update_resume":               true,
		"vp_get_knowledge":               true,
		"vp_list_learnings":              true,
		"vp_get_learning":                true,
		"vp_list_projects":               true,
		"vp_audit_vault":                 true,
		"vp_archive_link":                true,
		"vp_append_iteration":            true,
		"vp_get_iteration":               true,
		"vp_list_tasks":                  true,
		"vp_get_task":                    true,
		"vp_manage_task":                 true,
		"vp_read_resource":               true,
		"vp_init":                        true,
		"vp_vault_sync":                  true,
		"vp_vault_tidy":                  true,
		"vp_vault_split":                 true,
		"vp_vault_merge":                 true,
		"vp_vault_status":                true,
		"vp_refresh_index":               true,
		"vp_vault_read":                  true,
		"vp_vault_list":                  true,
		"vp_vault_exists":                true,
		"vp_vault_sha256":                true,
		"vp_vault_write":                 true,
		"vp_vault_edit":                  true,
		"vp_vault_delete":                true,
		"vp_vault_move":                  true,
		"vp_memory_write":                true,
		"vp_memory_read":                 true,
		"vp_memory_list":                 true,
		"vp_memory_delete":               true,
		"vp_memory_harvest":              true,
		"vp_ingest_commit_msg":           true,
		"vp_archive_commit_log":          true,
		"vp_collect_wrap_state":          true,
		"vp_stamp_iter":                  true,
		"vp_enqueue_iteration_summary":   true,
		"vp_trigger_summarization_drain": true,
		"vp_preflight_wrap":              true,
		"vp_surface_check":               true,
		"vp_check":                       true,
		"vp_scan_plans":                  true,
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

// TestWithLaunchSetsOption pins the option-setting mechanics of WithLaunch:
// applying it to a fresh registerOptions must land the exact func reference
// on o.launch. There is no tool yet that calls o.launch (that lands in a
// later, separate piece of work), so there is nothing to observe through the
// registry — this only proves the functional-option plumbing itself, ahead of
// that consumer existing.
func TestWithLaunchSetsOption(t *testing.T) {
	called := false
	fake := detachlaunch.LaunchFunc(func(binary string, args []string, logPath string) (int, error) {
		called = true
		return 42, nil
	})

	var o registerOptions
	WithLaunch(fake)(&o)

	if o.launch == nil {
		t.Fatal("WithLaunch did not set o.launch")
	}
	// Prove it's the SAME func (not just a non-nil func) by invoking it
	// through the option-set field and checking the closure's own side effect
	// fired, since Go func values are not comparable with ==.
	if _, err := o.launch("bin", nil, "log"); err != nil {
		t.Fatalf("o.launch: %v", err)
	}
	if !called {
		t.Fatal("o.launch is not the func passed to WithLaunch")
	}
}

// TestRegisterAllZeroOptionsUnchanged is a regression anchor: RegisterAll
// called with zero options (the ~16 existing call sites across the tree, plus
// TestRegisterAll/TestRegisterAllNilEngine above) must still register exactly
// the same tool set after WithLaunch/o.launch were added. It does not exercise
// o.launch at all (nothing yet consumes it) — it only proves adding the new
// option/defaulting line did not change RegisterAll's observable behavior for
// every caller that passes no options.
func TestRegisterAllZeroOptionsUnchanged(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)
	srv := mcp.NewServer(vault)
	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := search.NewEngine(embedder.NewMock(384), vault, cfg)

	RegisterAll(srv.Registry(), resolver, vault, eng)

	if got := len(srv.Registry().List()); got != 76 {
		t.Fatalf("registered %d tools with zero options, want 76 (unchanged)", got)
	}
}

func TestRegisterAllNilEngine(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)
	srv := mcp.NewServer(vault)

	RegisterAll(srv.Registry(), resolver, vault, nil)

	tools := srv.Registry().List()
	if len(tools) != 67 {
		t.Fatalf("registered %d tools with nil engine, want 67", len(tools))
	}
}
