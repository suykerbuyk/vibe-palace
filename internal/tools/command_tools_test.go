// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
)

func writeVaultFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	p := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func testResolverOnly(t *testing.T) (*vpctx.Resolver, string) {
	t.Helper()
	root := t.TempDir()
	return vpctx.NewResolver(root), root
}

// --- GetCommand tests ---

func TestGetCommandEmbedded(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := GetCommandTool(resolver)

	result, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"restart"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	r, ok := result.(getResourceResult)
	if !ok {
		t.Fatalf("result type = %T, want getResourceResult", result)
	}
	if r.Name != "restart" {
		t.Errorf("Name = %q, want %q", r.Name, "restart")
	}
	if r.Source != "embedded" {
		t.Errorf("Source = %q, want %q", r.Source, "embedded")
	}
	if !strings.Contains(r.Content, "Context Restoration") {
		t.Error("expected restart content")
	}
}

func TestGetCommandVaultOverride(t *testing.T) {
	resolver, root := testResolverOnly(t)

	writeVaultFile(t, root, "Templates/commands/restart.md", "custom restart command")

	tool := GetCommandTool(resolver)
	result, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"restart","project":"proj"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	r := result.(getResourceResult)
	if r.Source != "vault" {
		t.Errorf("Source = %q, want %q", r.Source, "vault")
	}
	if r.Content != "custom restart command" {
		t.Errorf("Content = %q, want vault override", r.Content)
	}
}

func TestGetCommandProjectOverride(t *testing.T) {
	resolver, root := testResolverOnly(t)

	writeVaultFile(t, root, "Templates/commands/restart.md", "vault version")
	writeVaultFile(t, root, "Projects/proj/commands/restart.md", "project version")

	tool := GetCommandTool(resolver)
	result, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"restart","project":"proj"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	r := result.(getResourceResult)
	if r.Source != "project" {
		t.Errorf("Source = %q, want %q", r.Source, "project")
	}
	if r.Content != "project version" {
		t.Errorf("Content = %q, want project version", r.Content)
	}
}

func TestGetCommandNotFound(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := GetCommandTool(resolver)

	_, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"nonexistent"}`))
	if err == nil {
		t.Fatal("expected error for nonexistent command, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found' message", err.Error())
	}
}

// --- GetSkill tests ---

func TestGetSkillFromVault(t *testing.T) {
	resolver, root := testResolverOnly(t)
	writeVaultFile(t, root, "Templates/skills/analyze/SKILL.md", "analyze skill content")

	tool := GetSkillTool(resolver)
	result, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"analyze"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	r := result.(getResourceResult)
	if r.Name != "analyze" {
		t.Errorf("Name = %q, want %q", r.Name, "analyze")
	}
	if r.Source != "vault" {
		t.Errorf("Source = %q, want %q", r.Source, "vault")
	}
}

func TestGetSkillNotFound(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := GetSkillTool(resolver)

	_, err := tool.Handler(context.Background(), json.RawMessage(`{"name":"nonexistent"}`))
	if err == nil {
		t.Fatal("expected error for nonexistent skill, got nil")
	}
}

// --- ListCommands tests ---

func TestListCommandsEmbedded(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := ListCommandsTool(resolver)

	result, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	r, ok := result.(listResourceResult)
	if !ok {
		t.Fatalf("result type = %T, want listResourceResult", result)
	}

	// 8 embedded commands: cancel-plan, capture, execute-plan, license, makefile, restart, review-plan, wrap.
	if len(r.Resources) != 8 {
		t.Fatalf("got %d commands, want 8: %v", len(r.Resources), r.Resources)
	}

	for _, ri := range r.Resources {
		if ri.Source != "embedded" {
			t.Errorf("command %q source = %q, want embedded", ri.Name, ri.Source)
		}
	}
}

func TestListCommandsMerged(t *testing.T) {
	resolver, root := testResolverOnly(t)

	// Add vault override and new vault command.
	writeVaultFile(t, root, "Templates/commands/restart.md", "vault restart")
	writeVaultFile(t, root, "Templates/commands/deploy.md", "deploy")

	// Add project command.
	writeVaultFile(t, root, "Projects/proj/commands/custom.md", "custom")

	tool := ListCommandsTool(resolver)
	result, err := tool.Handler(context.Background(), json.RawMessage(`{"project":"proj"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	r := result.(listResourceResult)
	// 10 total: cancel-plan, capture, custom, deploy, execute-plan, license, makefile, restart, review-plan, wrap.
	if len(r.Resources) != 10 {
		t.Fatalf("got %d commands, want 10: %v", len(r.Resources), r.Resources)
	}

	// No duplicates.
	seen := make(map[string]bool)
	for _, ri := range r.Resources {
		if seen[ri.Name] {
			t.Errorf("duplicate command: %q", ri.Name)
		}
		seen[ri.Name] = true
	}

	// restart should come from vault (overrides embedded).
	for _, ri := range r.Resources {
		if ri.Name == "restart" && ri.Source != "vault" {
			t.Errorf("restart source = %q, want vault", ri.Source)
		}
	}
}

// --- ListSkills tests ---

func TestListSkillsEmpty(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	tool := ListSkillsTool(resolver)

	result, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	r := result.(listResourceResult)
	// Embedded seed ships startup-analyst; no vault/project overrides means
	// exactly the one embedded skill is listed.
	if len(r.Resources) != 1 {
		t.Errorf("expected 1 embedded skill, got %d", len(r.Resources))
	}
}

func TestListSkillsFromVault(t *testing.T) {
	resolver, root := testResolverOnly(t)

	writeVaultFile(t, root, "Templates/skills/analyze/SKILL.md", "analyze")
	writeVaultFile(t, root, "Templates/skills/summarize/SKILL.md", "summarize")

	tool := ListSkillsTool(resolver)
	result, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	r := result.(listResourceResult)
	// Two vault-tier skills plus the embedded startup-analyst seed.
	if len(r.Resources) != 3 {
		t.Fatalf("got %d skills, want 3", len(r.Resources))
	}
}

// --- Tool schema tests ---

func TestToolSchemas(t *testing.T) {
	resolver, _ := testResolverOnly(t)

	tools := []struct {
		name string
		tool func(*vpctx.Resolver) mcp.Tool
	}{
		{"GetCommand", GetCommandTool},
		{"GetSkill", GetSkillTool},
		{"ListCommands", ListCommandsTool},
		{"ListSkills", ListSkillsTool},
	}

	for _, tt := range tools {
		t.Run(tt.name, func(t *testing.T) {
			tool := tt.tool(resolver)
			if tool.Name == "" {
				t.Error("tool Name should not be empty")
			}
			if tool.Description == "" {
				t.Error("tool Description should not be empty")
			}
			var schema map[string]any
			if err := json.Unmarshal(tool.Schema, &schema); err != nil {
				t.Fatalf("invalid schema JSON: %v", err)
			}
		})
	}
}
