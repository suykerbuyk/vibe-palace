// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
)

// TestManualTool exercises the self-describing vp_manual capability service: it
// must return the resolved doctrine, the LIVE tool inventory (projected from a
// fully-populated Registry, so it includes both read-only and mutating tools
// with correct flags), the project's commands and skills, and the non-empty
// server instructions string.
func TestManualTool(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)

	// Build a fully-registered registry so reg.List() sees the whole surface —
	// including vp_manual itself, which RegisterAll adds.
	srv := mcp.NewServer(vault)
	reg := srv.Registry()
	RegisterAll(reg, resolver, vault, nil)

	tool := ManualTool(reg, resolver)
	if tool.Name != "vp_manual" {
		t.Fatalf("name = %q, want vp_manual", tool.Name)
	}
	if tool.Mutating {
		t.Error("vp_manual must be read-only (Mutating=false)")
	}

	params, _ := json.Marshal(map[string]string{"project": "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res, ok := result.(ManualResult)
	if !ok {
		t.Fatalf("result type = %T, want ManualResult", result)
	}

	// 1. Doctrine — resolved like vp_get_doctrine (embedded floor for a project
	//    with no override), non-empty, with the always-present resource URI.
	if res.Doctrine.Content == "" {
		t.Error("doctrine content is empty")
	}
	if res.Doctrine.Source != "embedded" {
		t.Errorf("doctrine source = %q, want embedded", res.Doctrine.Source)
	}
	if want := mcp.DoctrineURI("test-proj"); res.Doctrine.DoctrineURI != want {
		t.Errorf("doctrine_uri = %q, want %q", res.Doctrine.DoctrineURI, want)
	}
	for _, want := range []string{"Pair Programming", "Task Management", "Core Principles"} {
		if !strings.Contains(res.Doctrine.Content, want) {
			t.Errorf("doctrine content missing %q", want)
		}
	}

	// 2. Tool inventory — must include known tools with the right mutating flag,
	//    and must include vp_manual itself (proof List() ran at dispatch time
	//    against the fully-built registry).
	byName := make(map[string]mcp.ToolInfo, len(res.Tools))
	for _, ti := range res.Tools {
		byName[ti.Name] = ti
	}
	if len(res.Tools) == 0 {
		t.Fatal("tools inventory is empty")
	}
	bootstrap, ok := byName["vp_bootstrap_context"]
	if !ok {
		t.Fatal("tools inventory missing vp_bootstrap_context")
	}
	if bootstrap.Mutating {
		t.Error("vp_bootstrap_context should be read-only in the inventory")
	}
	if bootstrap.Description == "" {
		t.Error("vp_bootstrap_context inventory entry has empty description")
	}
	if len(bootstrap.Schema) == 0 {
		t.Error("vp_bootstrap_context inventory entry has empty schema")
	}
	if _, ok := byName["vp_manual"]; !ok {
		t.Error("tools inventory missing vp_manual itself — List() did not see the full registry")
	}
	// A known MUTATING tool must carry the flag, so the report distinguishes
	// read-only from write tools.
	if upd, ok := byName["vp_update_resume"]; !ok {
		t.Error("tools inventory missing vp_update_resume")
	} else if !upd.Mutating {
		t.Error("vp_update_resume must be reported as mutating")
	}

	// 3. Commands and skills — embedded defaults yield a non-empty command set,
	//    with the vpc-/vps- aliases populated.
	if len(res.Commands) == 0 {
		t.Error("commands inventory is empty (expected embedded defaults)")
	}
	for _, c := range res.Commands {
		if c.Name == "" {
			t.Error("command entry has empty name")
		}
		if !strings.HasPrefix(c.Alias, "vpc-") {
			t.Errorf("command %q alias = %q, want vpc- prefix", c.Name, c.Alias)
		}
	}
	for _, s := range res.Skills {
		if !strings.HasPrefix(s.Alias, "vps-") {
			t.Errorf("skill %q alias = %q, want vps- prefix", s.Name, s.Alias)
		}
	}

	// 4. Server instructions — the exact exported string, non-empty and pointing
	//    clients at bootstrap.
	if res.ServerInstructions == "" {
		t.Error("server_instructions is empty")
	}
	if !strings.Contains(res.ServerInstructions, "vp_bootstrap_context") {
		t.Errorf("server_instructions missing vp_bootstrap_context: %q", res.ServerInstructions)
	}
	if res.ServerInstructions != mcp.ServerInstructions {
		t.Error("server_instructions does not match mcp.ServerInstructions")
	}
}

// TestManualToolRequiresProject pins the guard: an empty project is a caller
// error, not a silent embedded fallback.
func TestManualToolRequiresProject(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)
	srv := mcp.NewServer(vault)
	RegisterAll(srv.Registry(), resolver, vault, nil)

	tool := ManualTool(srv.Registry(), resolver)
	params, _ := json.Marshal(map[string]string{})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for missing project, got nil")
	}
}
