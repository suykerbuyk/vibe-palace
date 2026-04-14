// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testSetup(t *testing.T) (*storage.Vault, *vpctx.Resolver) {
	t.Helper()
	root := t.TempDir()
	vault := storage.NewVault(root)
	resolver := vpctx.NewResolver(root)
	return vault, resolver
}

func TestBootstrapEmptyVault(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)

	params := json.RawMessage(`{"project":"test-proj"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	br, ok := result.(BootstrapResult)
	if !ok {
		t.Fatalf("result type = %T, want BootstrapResult", result)
	}

	if br.Project != "test-proj" {
		t.Errorf("Project = %q, want %q", br.Project, "test-proj")
	}
	// Workflow should come from embedded defaults.
	if !strings.Contains(br.Workflow, "Pair Programming") {
		t.Error("expected embedded workflow content")
	}
	// Resume should come from embedded defaults.
	if !strings.Contains(br.Resume, "test-proj") {
		t.Error("expected resume with expanded project name")
	}
	// Empty vault = no tasks, no sessions, no KG.
	if len(br.ActiveTasks) != 0 {
		t.Errorf("ActiveTasks = %d, want 0", len(br.ActiveTasks))
	}
	if len(br.RecentSessions) != 0 {
		t.Errorf("RecentSessions = %d, want 0", len(br.RecentSessions))
	}
}

func TestBootstrapWithTasks(t *testing.T) {
	vault, resolver := testSetup(t)

	// Create two tasks.
	if err := vault.CreateTask("test-proj", "fix-bug", "Fix the bug", "", "high"); err != nil {
		t.Fatal(err)
	}
	if err := vault.CreateTask("test-proj", "add-feature", "Add feature", "", "medium"); err != nil {
		t.Fatal(err)
	}

	tool := BootstrapContextTool(resolver, vault)
	params := json.RawMessage(`{"project":"test-proj"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	br := result.(BootstrapResult)
	if len(br.ActiveTasks) != 2 {
		t.Fatalf("ActiveTasks = %d, want 2", len(br.ActiveTasks))
	}
}

func TestBootstrapWithSessions(t *testing.T) {
	vault, resolver := testSetup(t)

	// Create 7 sessions across two dates.
	for i := 0; i < 4; i++ {
		_, err := vault.WriteSession("test-proj", storage.SessionMeta{
			Date:    "2026-04-06",
			Title:   "session",
			Summary: "work",
			Tag:     "implementation",
		}, "body")
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		_, err := vault.WriteSession("test-proj", storage.SessionMeta{
			Date:    "2026-04-07",
			Title:   "session",
			Summary: "more work",
			Tag:     "implementation",
		}, "body")
		if err != nil {
			t.Fatal(err)
		}
	}

	tool := BootstrapContextTool(resolver, vault)
	params := json.RawMessage(`{"project":"test-proj"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	br := result.(BootstrapResult)
	if len(br.RecentSessions) != 5 {
		t.Fatalf("RecentSessions = %d, want 5", len(br.RecentSessions))
	}
	// Most recent first.
	if br.RecentSessions[0].Date != "2026-04-07" {
		t.Errorf("first session date = %q, want 2026-04-07", br.RecentSessions[0].Date)
	}
}

func TestBootstrapTokenBudget(t *testing.T) {
	vault, resolver := testSetup(t)

	// Create sessions so the response is non-trivial.
	for i := 0; i < 5; i++ {
		_, err := vault.WriteSession("test-proj", storage.SessionMeta{
			Date:    "2026-04-07",
			Title:   "session with a long title to inflate size",
			Summary: strings.Repeat("detail ", 50),
			Tag:     "implementation",
		}, "body")
		if err != nil {
			t.Fatal(err)
		}
	}

	tool := BootstrapContextTool(resolver, vault)
	// Very small token budget to force truncation.
	params := json.RawMessage(`{"project":"test-proj","max_tokens":200}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	br := result.(BootstrapResult)
	// Should have truncated some sessions.
	if len(br.RecentSessions) >= 5 {
		t.Errorf("expected truncated sessions, got %d", len(br.RecentSessions))
	}
}

func TestBootstrapDefaultTokenBudget(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)

	// Omit max_tokens — should default to 8000.
	params := json.RawMessage(`{"project":"test-proj"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	br := result.(BootstrapResult)
	if br.Project != "test-proj" {
		t.Errorf("Project = %q, want %q", br.Project, "test-proj")
	}
}

func TestBootstrapIncludesCommands(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)

	params := json.RawMessage(`{"project":"test-proj"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	br := result.(BootstrapResult)
	// Should have at least the 4 embedded commands.
	if len(br.AvailableCommands) < 4 {
		t.Fatalf("AvailableCommands = %d, want >= 4", len(br.AvailableCommands))
	}

	found := false
	for _, cmd := range br.AvailableCommands {
		if cmd.Name == "restart" {
			found = true
			if cmd.Source != "embedded" {
				t.Errorf("restart source = %q, want embedded", cmd.Source)
			}
			if cmd.Brief == "" {
				t.Error("restart brief should not be empty")
			}
			if cmd.Alias != "vpc-restart" {
				t.Errorf("restart alias = %q, want %q", cmd.Alias, "vpc-restart")
			}
			// Brief must not be truncated mid-word: ellipsis-terminated briefs
			// are allowed, but a raw mid-word slice is the regression we care
			// about. The word-boundary snap guarantees the last char is
			// either a full letter or the ellipsis.
			if strings.HasSuffix(cmd.Brief, " ") {
				t.Errorf("restart brief has trailing space: %q", cmd.Brief)
			}
		}
		// Every command must carry a vpc- alias.
		if cmd.Alias != "vpc-"+cmd.Name {
			t.Errorf("alias for %q = %q, want %q", cmd.Name, cmd.Alias, "vpc-"+cmd.Name)
		}
	}
	if !found {
		t.Error("expected restart in AvailableCommands")
	}
	if br.CommandInvocation == "" {
		t.Error("CommandInvocation should be populated when commands are present")
	}
	if !strings.Contains(br.CommandInvocation, "vpc-") {
		t.Errorf("CommandInvocation missing vpc- reference: %q", br.CommandInvocation)
	}
}

func TestBootstrapCommandsTruncationOrder(t *testing.T) {
	vault, resolver := testSetup(t)

	// Create sessions to inflate size.
	for i := 0; i < 5; i++ {
		_, err := vault.WriteSession("test-proj", storage.SessionMeta{
			Date:    "2026-04-07",
			Title:   "session with a long title to inflate size",
			Summary: strings.Repeat("detail ", 50),
			Tag:     "implementation",
		}, "body")
		if err != nil {
			t.Fatal(err)
		}
	}

	tool := BootstrapContextTool(resolver, vault)

	// Get full result, then compute a budget that excludes sessions
	// but keeps workflow + resume + tasks + commands.
	fullResult, _ := tool.Handler(context.Background(), json.RawMessage(`{"project":"test-proj"}`))
	fullBR := fullResult.(BootstrapResult)

	// Compute size without sessions to find a budget that forces session shedding.
	withoutSessions := fullBR
	withoutSessions.RecentSessions = nil
	noSessionJSON, _ := json.Marshal(withoutSessions)
	// Budget slightly above no-sessions size keeps commands but sheds sessions.
	budget := len(noSessionJSON)/4 + 10
	params, _ := json.Marshal(bootstrapParams{Project: "test-proj", MaxTokens: budget})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	br := result.(BootstrapResult)
	// Sessions should be truncated.
	if len(br.RecentSessions) >= 5 {
		t.Errorf("expected sessions to be truncated, got %d", len(br.RecentSessions))
	}
	// Commands should survive (shed last).
	if len(br.AvailableCommands) == 0 {
		t.Error("commands should survive truncation before sessions")
	}
}

// TestCommandInvocationUsesCanonicalToolNames pairs with
// agentfile.TestManagedBlockStructure — together they guarantee the block
// copy and the bootstrap directive agree on the tool names, so the two
// message surfaces the model sees can never drift apart.
func TestCommandInvocationUsesCanonicalToolNames(t *testing.T) {
	if !strings.Contains(commandInvocationDirective, "`"+agentfile.CommandToolName+"`") {
		t.Errorf("directive missing canonical command tool name %q: %q",
			agentfile.CommandToolName, commandInvocationDirective)
	}
	if !strings.Contains(commandInvocationDirective, "`"+agentfile.SkillToolName+"`") {
		t.Errorf("directive missing canonical skill tool name %q: %q",
			agentfile.SkillToolName, commandInvocationDirective)
	}
	// Defense in depth: the old name must not reappear via copy/paste.
	for _, stale := range []string{"vp_get_command", "vp_get_skill"} {
		if strings.Contains(commandInvocationDirective, stale) {
			t.Errorf("directive contains stale tool name %q: %q", stale, commandInvocationDirective)
		}
	}
}

func TestBootstrapIncludesSkills(t *testing.T) {
	// Seed a vault-level skill — no embedded skills exist yet, so the
	// resolver's Templates/skills tier is the first place a skill can live.
	root := t.TempDir()
	writeVaultFile(t, root, "Templates/skills/analyze/SKILL.md", "# Analyze\n\nPerform deep analysis of the codebase.")
	writeVaultFile(t, root, "Templates/skills/summarize/SKILL.md", "Summarize content concisely.")

	vault := storage.NewVault(root)
	resolver := vpctx.NewResolver(root)
	tool := BootstrapContextTool(resolver, vault)

	result, err := tool.Handler(context.Background(), json.RawMessage(`{"project":"test-proj"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	br := result.(BootstrapResult)
	if len(br.AvailableSkills) < 2 {
		t.Fatalf("AvailableSkills = %d, want >= 2 (seeded analyze + summarize)", len(br.AvailableSkills))
	}
	names := map[string]skillSummary{}
	for _, sk := range br.AvailableSkills {
		names[sk.Name] = sk
		if sk.Alias != "vps-"+sk.Name {
			t.Errorf("skill %q alias = %q, want %q", sk.Name, sk.Alias, "vps-"+sk.Name)
		}
		if sk.Source == "" {
			t.Errorf("skill %q missing source", sk.Name)
		}
	}
	if _, ok := names["analyze"]; !ok {
		t.Error("seeded skill 'analyze' missing from AvailableSkills")
	}
	if s := names["analyze"]; s.Brief == "" {
		t.Error("seeded skill 'analyze' missing brief")
	}
}

func TestBootstrapPostInstructionsPopulated(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)

	result, err := tool.Handler(context.Background(), json.RawMessage(`{"project":"test-proj"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	br := result.(BootstrapResult)
	if br.PostBootstrapInstructions == "" {
		t.Fatal("PostBootstrapInstructions should always be populated")
	}
	if !strings.Contains(br.PostBootstrapInstructions, "vpc-") {
		t.Errorf("PostBootstrapInstructions missing vpc- reference: %q", br.PostBootstrapInstructions)
	}
	if !strings.Contains(br.PostBootstrapInstructions, "vps-") {
		t.Errorf("PostBootstrapInstructions missing vps- reference: %q", br.PostBootstrapInstructions)
	}
	// First two command aliases should appear as examples.
	if len(br.AvailableCommands) >= 2 {
		for i := 0; i < 2; i++ {
			want := br.AvailableCommands[i].Alias
			if !strings.Contains(br.PostBootstrapInstructions, want) {
				t.Errorf("PostBootstrapInstructions missing example %q: %q",
					want, br.PostBootstrapInstructions)
			}
		}
	}
}

func TestBootstrapPostInstructionsSurvivesTruncation(t *testing.T) {
	vault, resolver := testSetup(t)

	// Force heavy truncation that drops commands + skills.
	for i := 0; i < 5; i++ {
		_, err := vault.WriteSession("test-proj", storage.SessionMeta{
			Date:    "2026-04-07",
			Title:   "session",
			Summary: strings.Repeat("detail ", 50),
			Tag:     "implementation",
		}, "body")
		if err != nil {
			t.Fatal(err)
		}
	}

	tool := BootstrapContextTool(resolver, vault)
	// Very small budget — commands and skills should be shed.
	params := json.RawMessage(`{"project":"test-proj","max_tokens":100}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	br := result.(BootstrapResult)
	if len(br.AvailableCommands) != 0 {
		t.Errorf("expected commands to be shed under tight budget, got %d", len(br.AvailableCommands))
	}
	// PostBootstrapInstructions must survive the shed so the model still
	// announces that vp_cmd / vp_skill exist.
	if br.PostBootstrapInstructions == "" {
		t.Error("PostBootstrapInstructions should survive command/skill truncation")
	}
	// Degraded copy should name the canonical tools.
	if !strings.Contains(br.PostBootstrapInstructions, agentfile.CommandToolName) ||
		!strings.Contains(br.PostBootstrapInstructions, agentfile.SkillToolName) {
		t.Errorf("degraded PostBootstrapInstructions missing canonical tool names: %q",
			br.PostBootstrapInstructions)
	}
}

func TestBootstrapToolSchema(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)

	if tool.Name != "vp_bootstrap_context" {
		t.Errorf("Name = %q, want %q", tool.Name, "vp_bootstrap_context")
	}
	if tool.Description == "" {
		t.Error("Description should not be empty")
	}

	// Schema should be valid JSON.
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Fatalf("invalid schema JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
}
