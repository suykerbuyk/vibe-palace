// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestIntegrationScopedResolutionFullStack proves that palace-scoped command
// resolution works end-to-end through the MCP tool layer: room shadows wing
// shadows project shadows vault shadows embedded.
func TestIntegrationScopedResolutionFullStack(t *testing.T) {
	h := newHarness(t, false)
	root := h.Vault.Root

	// Set up commands at all 5 tiers.
	writeVaultFile(t, root, "Projects/test-proj/commands/backend/api/gen.md",
		"# Generate OpenAPI\nRoom-scoped: generate API docs for {{WING}}/{{ROOM}}.")
	writeVaultFile(t, root, "Projects/test-proj/commands/backend/.wing/lint.md",
		"# Lint Backend\nWing-scoped: lint {{WING}} code.")
	writeVaultFile(t, root, "Projects/test-proj/commands/deploy.md",
		"# Deploy\nProject-scoped: deploy {{PROJECT}}.")
	writeVaultFile(t, root, "Templates/commands/custom-vault.md",
		"# Custom Vault\nVault template command.")
	// Embedded: restart, wrap, review-plan, cancel-plan, capture

	h.registerAllTools(t)

	// --- vp_cmd execute: room-scoped command ---
	result := h.callTool(t, "vp_cmd", map[string]any{
		"name":    "gen",
		"project": "test-proj",
		"wing":    "backend",
		"room":    "api",
	})
	if !strings.Contains(result, "Source: room") {
		t.Errorf("room command: expected Source: room, got: %.200s", result)
	}
	if !strings.Contains(result, "Wing: backend") {
		t.Error("room command: expected Wing: backend in frame header")
	}
	if !strings.Contains(result, "Room: api") {
		t.Error("room command: expected Room: api in frame header")
	}
	// Verify template expansion.
	if !strings.Contains(result, "backend/api") {
		t.Error("room command: {{WING}}/{{ROOM}} not expanded")
	}

	// --- vp_cmd execute: wing-scoped command ---
	result = h.callTool(t, "vp_cmd", map[string]any{
		"name":    "lint",
		"project": "test-proj",
		"wing":    "backend",
	})
	if !strings.Contains(result, "Source: wing") {
		t.Errorf("wing command: expected Source: wing, got: %.200s", result)
	}
	if !strings.Contains(result, "Wing: backend") {
		t.Error("wing command: expected Wing in frame header")
	}
	if !strings.Contains(result, "lint backend code") {
		t.Error("wing command: {{WING}} not expanded")
	}

	// --- vp_cmd execute: project-scoped command ---
	result = h.callTool(t, "vp_cmd", map[string]any{
		"name":    "deploy",
		"project": "test-proj",
	})
	if !strings.Contains(result, "Source: project") {
		t.Errorf("project command: expected Source: project, got: %.200s", result)
	}
	if !strings.Contains(result, "deploy test-proj") {
		t.Error("project command: {{PROJECT}} not expanded")
	}

	// --- vp_cmd execute: embedded command (fallthrough with scope) ---
	result = h.callTool(t, "vp_cmd", map[string]any{
		"name":    "restart",
		"project": "test-proj",
		"wing":    "backend",
		"room":    "api",
	})
	if !strings.Contains(result, "Source: embedded") {
		t.Errorf("embedded fallthrough: expected Source: embedded, got: %.200s", result)
	}
	if !strings.Contains(result, "Context Restoration") {
		t.Error("embedded fallthrough: missing restart content")
	}

	// --- vp_list_commands: scoped listing merges all tiers ---
	result = h.callTool(t, "vp_list_commands", map[string]any{
		"project": "test-proj",
		"wing":    "backend",
		"room":    "api",
	})
	// Should include: gen(room), lint(wing), deploy(project),
	// custom-vault(vault), plus 5 embedded commands.
	for _, name := range []string{"gen", "lint", "deploy", "custom-vault", "restart", "wrap"} {
		if !strings.Contains(result, name) {
			t.Errorf("scoped listing missing %q", name)
		}
	}

	// --- vp_get_command: room-scoped ---
	result = h.callTool(t, "vp_get_command", map[string]any{
		"name":    "gen",
		"project": "test-proj",
		"wing":    "backend",
		"room":    "api",
	})
	var getResult struct {
		Source  string `json:"source"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(result), &getResult); err != nil {
		t.Fatalf("parse get_command result: %v", err)
	}
	if getResult.Source != "room" {
		t.Errorf("get_command source = %q, want room", getResult.Source)
	}
}

// TestIntegrationScopedShadowing proves that higher tiers shadow lower tiers
// when the same command name exists at multiple levels.
func TestIntegrationScopedShadowing(t *testing.T) {
	h := newHarness(t, false)
	root := h.Vault.Root

	// Same command "lint" at room, wing, and project level.
	writeVaultFile(t, root, "Projects/test-proj/commands/lint.md", "project lint")
	writeVaultFile(t, root, "Projects/test-proj/commands/backend/.wing/lint.md", "wing lint")
	writeVaultFile(t, root, "Projects/test-proj/commands/backend/api/lint.md", "room lint")

	h.registerAllTools(t)

	// With room scope: room shadows wing and project.
	result := h.callTool(t, "vp_cmd", map[string]any{
		"name":    "lint",
		"project": "test-proj",
		"wing":    "backend",
		"room":    "api",
	})
	if !strings.Contains(result, "Source: room") {
		t.Errorf("expected room source, got: %.200s", result)
	}
	if !strings.Contains(result, "room lint") {
		t.Error("expected room content")
	}

	// With wing scope only: wing shadows project.
	result = h.callTool(t, "vp_cmd", map[string]any{
		"name":    "lint",
		"project": "test-proj",
		"wing":    "backend",
	})
	if !strings.Contains(result, "Source: wing") {
		t.Errorf("expected wing source, got: %.200s", result)
	}

	// Without scope: project level.
	result = h.callTool(t, "vp_cmd", map[string]any{
		"name":    "lint",
		"project": "test-proj",
	})
	if !strings.Contains(result, "Source: project") {
		t.Errorf("expected project source, got: %.200s", result)
	}
}

// TestIntegrationScopedSkillResolution proves skill resolution uses the same
// 5-tier scoping as commands.
func TestIntegrationScopedSkillResolution(t *testing.T) {
	h := newHarness(t, false)
	root := h.Vault.Root

	writeVaultFile(t, root, "Projects/test-proj/skills/backend/api/owasp/SKILL.md",
		"# OWASP Checklist\nRoom-scoped security skill.")

	h.registerAllTools(t)

	result := h.callTool(t, "vp_skill", map[string]any{
		"name":    "owasp",
		"project": "test-proj",
		"wing":    "backend",
		"room":    "api",
	})
	if !strings.Contains(result, "ACTIVATE SKILL") {
		t.Error("expected skill activation frame")
	}
	if !strings.Contains(result, "Source: room") {
		t.Errorf("expected room source for skill, got: %.200s", result)
	}
	if !strings.Contains(result, "OWASP Checklist") {
		t.Error("expected skill content")
	}
}

// TestIntegrationBootstrapScopedCommands proves that bootstrap includes
// palace-scoped commands when wing/room are provided.
func TestIntegrationBootstrapScopedCommands(t *testing.T) {
	h := newHarness(t, false)
	root := h.Vault.Root

	writeVaultFile(t, root, "Projects/test-proj/commands/backend/api/gen.md",
		"# Generate\nRoom command.")
	writeVaultFile(t, root, "Projects/test-proj/commands/backend/.wing/lint.md",
		"# Lint\nWing command.")

	h.registerAllTools(t)

	result := h.callTool(t, "vp_bootstrap_context", map[string]any{
		"project": "test-proj",
		"wing":    "backend",
		"room":    "api",
	})

	var bootstrap struct {
		AvailableCommands []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"available_commands"`
	}
	if err := json.Unmarshal([]byte(result), &bootstrap); err != nil {
		t.Fatalf("parse bootstrap: %v", err)
	}

	cmdMap := make(map[string]string)
	for _, cmd := range bootstrap.AvailableCommands {
		cmdMap[cmd.Name] = cmd.Source
	}
	if cmdMap["gen"] != "room" {
		t.Errorf("gen source = %q, want room", cmdMap["gen"])
	}
	if cmdMap["lint"] != "wing" {
		t.Errorf("lint source = %q, want wing", cmdMap["lint"])
	}
	if _, ok := cmdMap["restart"]; !ok {
		t.Error("embedded 'restart' should still appear in scoped bootstrap")
	}
}

// TestIntegrationScopedResolutionOverrideEmbedded proves that a room-level
// command can shadow an embedded default.
func TestIntegrationScopedResolutionOverrideEmbedded(t *testing.T) {
	h := newHarness(t, false)
	root := h.Vault.Root

	// Override the embedded "restart" at room level.
	writeVaultFile(t, root, "Projects/test-proj/commands/backend/api/restart.md",
		"# Custom Room Restart\nRoom-specific restart procedure.")

	h.registerAllTools(t)

	result := h.callTool(t, "vp_cmd", map[string]any{
		"name":    "restart",
		"project": "test-proj",
		"wing":    "backend",
		"room":    "api",
	})
	if !strings.Contains(result, "Source: room") {
		t.Errorf("expected room source overriding embedded, got: %.200s", result)
	}
	if !strings.Contains(result, "Room-specific restart procedure") {
		t.Error("expected room override content, not embedded content")
	}

	// Without scope, should still get embedded.
	result = h.callTool(t, "vp_cmd", map[string]any{
		"name":    "restart",
		"project": "test-proj",
	})
	if !strings.Contains(result, "Source: embedded") {
		t.Errorf("without scope, expected embedded source, got: %.200s", result)
	}
}
