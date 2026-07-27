// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/resumezone"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testSetup(t *testing.T) (*storage.Vault, *vpctx.Resolver) {
	t.Helper()
	root := t.TempDir()
	vault := bornCurrentTestVault(t, root)
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
	// Workflow should come from embedded defaults. The embedded floor is the
	// THIN post-ADR-008 template: the generic doctrine lives in the on-demand
	// doctrine resource, and the workflow carries the minimal bootstrap
	// contract pointing at it.
	if !strings.Contains(br.Workflow, "vp_get_doctrine") {
		t.Error("expected embedded thin workflow with the doctrine-fetch contract paragraph")
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
	if err := vault.CreateTask("test-proj", storage.TaskSpec{Slug: "fix-bug", Title: "Fix the bug", Content: "", Priority: "high"}); err != nil {
		t.Fatal(err)
	}
	if err := vault.CreateTask("test-proj", storage.TaskSpec{Slug: "add-feature", Title: "Add feature", Content: "", Priority: "medium"}); err != nil {
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
	for range 4 {
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
	for range 3 {
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

func TestBootstrapFrictionTrendWarn(t *testing.T) {
	vault, resolver := testSetup(t)

	now := time.Now()
	// Five older (within 30d, beyond 7d) low-friction sessions, then three
	// recent high-friction sessions: a worsening trend with elevated recent
	// friction. Seeding >5 sessions proves the trend is computed from the FULL
	// history — if it were computed after the 5-session trim, the 30-day window
	// would not see all eight sessions.
	old := now.AddDate(0, 0, -20).Format("2006-01-02")
	recent := now.AddDate(0, 0, -2).Format("2006-01-02")
	for range 5 {
		if _, err := vault.WriteSession("test-proj", storage.SessionMeta{
			Date: old, Title: "calm", FrictionScore: 10,
		}, "body"); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		if _, err := vault.WriteSession("test-proj", storage.SessionMeta{
			Date: recent, Title: "rough", FrictionScore: 90,
		}, "body"); err != nil {
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

	if br.FrictionTrend == nil {
		t.Fatal("FrictionTrend = nil, want a worsening trend")
	}
	if !br.FrictionTrend.Warn {
		t.Errorf("FrictionTrend.Warn = false, want true (direction=%q recent=%.1f)",
			br.FrictionTrend.Direction, br.FrictionTrend.RecentAvg)
	}
	if br.FrictionTrend.Direction != "worsening" {
		t.Errorf("Direction = %q, want worsening", br.FrictionTrend.Direction)
	}
	// Computed from the full history: the 30-day window must cover all 8 sessions,
	// not just the 5 that survive the recent-sessions trim.
	if len(br.FrictionTrend.Windows) < 2 || br.FrictionTrend.Windows[1].SessionCount != 8 {
		t.Errorf("30d window session count = %v, want 8 (full history)", br.FrictionTrend.Windows)
	}
	// The nudge must be appended to the truncation-exempt directive.
	if br.FrictionTrend.Message == "" {
		t.Fatal("expected a non-empty trend Message")
	}
	if !strings.Contains(br.PostBootstrapInstructions, br.FrictionTrend.Message) {
		t.Errorf("PostBootstrapInstructions missing the nudge:\n%s", br.PostBootstrapInstructions)
	}
}

func TestBootstrapNoFrictionTrendEmptyVault(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)
	params := json.RawMessage(`{"project":"test-proj"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	br := result.(BootstrapResult)
	if br.FrictionTrend != nil {
		t.Errorf("FrictionTrend = %+v, want nil on a zero-session vault", br.FrictionTrend)
	}
}

func TestBootstrapTokenBudget(t *testing.T) {
	vault, resolver := testSetup(t)

	// Create sessions so the response is non-trivial.
	for range 5 {
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
	for range 5 {
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
		for i := range 2 {
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
	for range 5 {
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

func TestBootstrapSurfacesMemory(t *testing.T) {
	vault, resolver := testSetup(t)

	mems := []struct {
		rel  string
		meta storage.MemoryMeta
		body string
	}{
		{"prefs.md", storage.MemoryMeta{Name: "prefs", Description: "user preferences", Type: "user"}, "body one"},
		{"arch.md", storage.MemoryMeta{Name: "arch", Description: "architecture notes", Type: "project"}, "body two"},
		{"style.md", storage.MemoryMeta{Name: "style", Description: "style feedback", Type: "feedback"}, "body three"},
	}
	for _, m := range mems {
		if err := vault.WriteMemory("test-proj", m.rel, m.meta, m.body); err != nil {
			t.Fatal(err)
		}
	}

	tool := BootstrapContextTool(resolver, vault)
	// Generous budget so nothing sheds.
	result, err := tool.Handler(context.Background(), json.RawMessage(`{"project":"test-proj","max_tokens":100000}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	br := result.(BootstrapResult)
	if len(br.Memory) != 3 {
		t.Fatalf("Memory = %d, want 3", len(br.Memory))
	}
	byName := map[string]memorySnapshot{}
	for _, m := range br.Memory {
		byName[m.Name] = m
		if m.Rel == "" {
			t.Errorf("memory %q has empty Rel", m.Name)
		}
	}
	if got := byName["prefs"]; got.Description != "user preferences" || got.Type != "user" || got.Rel != "prefs.md" {
		t.Errorf("prefs snapshot = %+v", got)
	}
	if got := byName["arch"]; got.Type != "project" {
		t.Errorf("arch type = %q, want project", got.Type)
	}
	if got := byName["style"]; got.Type != "feedback" {
		t.Errorf("style type = %q, want feedback", got.Type)
	}
}

func TestBootstrapEmptyVaultNoMemory(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)

	result, err := tool.Handler(context.Background(), json.RawMessage(`{"project":"test-proj"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	br := result.(BootstrapResult)
	if len(br.Memory) != 0 {
		t.Errorf("Memory = %d, want 0 for empty vault", len(br.Memory))
	}
}

func TestBootstrapMemoryTruncationOrder(t *testing.T) {
	vault, resolver := testSetup(t)

	// Sessions and memories both sized to be sheddable.
	for range 5 {
		if _, err := vault.WriteSession("test-proj", storage.SessionMeta{
			Date:    "2026-04-07",
			Title:   "session with a long title to inflate size",
			Summary: strings.Repeat("detail ", 50),
			Tag:     "implementation",
		}, "body"); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 5 {
		rel := "mem-" + string(rune('a'+i)) + ".md"
		if err := vault.WriteMemory("test-proj", rel, storage.MemoryMeta{
			Name:        "mem-" + string(rune('a'+i)),
			Description: strings.Repeat("memory detail ", 20),
			Type:        "project",
		}, "ignored body"); err != nil {
			t.Fatal(err)
		}
	}

	tool := BootstrapContextTool(resolver, vault)
	fullResult, _ := tool.Handler(context.Background(), json.RawMessage(`{"project":"test-proj","max_tokens":1000000}`))
	fullBR := fullResult.(BootstrapResult)
	if len(fullBR.Memory) != 5 {
		t.Fatalf("setup: full Memory = %d, want 5", len(fullBR.Memory))
	}

	// Budget that sheds sessions but keeps memory: just above the no-sessions
	// size. The +60 headroom covers the budget report the ladder now honestly
	// measures itself carrying (shed list + counters); it stays far below the
	// ~400-token size of either the sessions or the memory rung, so it cannot
	// change WHICH rungs this test forces.
	withoutSessions := fullBR
	withoutSessions.RecentSessions = nil
	noSessionJSON, _ := json.Marshal(withoutSessions)
	budgetKeepMem := len(noSessionJSON)/4 + 60
	res, _ := tool.Handler(context.Background(), mustParams(t, "test-proj", budgetKeepMem))
	br := res.(BootstrapResult)
	if len(br.RecentSessions) >= 5 {
		t.Errorf("expected sessions shed, got %d", len(br.RecentSessions))
	}
	if len(br.Memory) == 0 {
		t.Error("memory should survive when only sessions need shedding (memory sheds after sessions)")
	}

	// Budget that sheds sessions AND memory but keeps KG: just above the
	// no-sessions, no-memory size. Proves memory sheds before KG is nil'd.
	withoutSessAndMem := withoutSessions
	withoutSessAndMem.Memory = nil
	noSessMemJSON, _ := json.Marshal(withoutSessAndMem)
	budgetShedMem := len(noSessMemJSON)/4 + 60
	res2, _ := tool.Handler(context.Background(), mustParams(t, "test-proj", budgetShedMem))
	br2 := res2.(BootstrapResult)
	if len(br2.Memory) != 0 {
		t.Errorf("expected memory fully shed, got %d", len(br2.Memory))
	}
	if br2.KGSnapshot == nil {
		t.Error("KGSnapshot should survive when shedding memory is enough (memory sheds before KG)")
	}
}

func mustParams(t *testing.T, project string, maxTokens int) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(bootstrapParams{Project: project, MaxTokens: maxTokens})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// bootstrapResult is a small helper that drives the bootstrap tool with the
// given raw params and returns the typed result.
func bootstrapResult(t *testing.T, tool mcp.Tool, params string) BootstrapResult {
	t.Helper()
	result, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return result.(BootstrapResult)
}

// TestBootstrapResumeWorkflowURIs pins that the resource URIs are ALWAYS set,
// independent of slim — they are the byte-for-byte fetch path for any host.
func TestBootstrapResumeWorkflowURIs(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)

	br := bootstrapResult(t, tool, `{"project":"test-proj"}`)
	if br.ResumeURI != "vibe-palace://resume/test-proj" {
		t.Errorf("ResumeURI = %q", br.ResumeURI)
	}
	if br.WorkflowURI != "vibe-palace://workflow/test-proj" {
		t.Errorf("WorkflowURI = %q", br.WorkflowURI)
	}
}

// TestBootstrapResumeIsNeverExcerptedByBytes replaces the three tests that
// covered the deleted byte-axis `slim` path (excerpt-on-large, inline-on-small,
// per-transport default). They asserted a mechanism that no longer exists; this
// asserts the property that replaced it, on the same fixture that used to be cut.
//
// THE POINT: a resume is never reduced for its SIZE alone. Only the token ladder
// reduces it, only under budget pressure, only to its `<!-- vp:pin -->` zone, and
// only while saying so in budget.shed. No request shape and no transport can
// produce a resume body that was silently prefix-cut.
func TestBootstrapResumeIsNeverExcerptedByBytes(t *testing.T) {
	vault, resolver := testSetup(t)
	bigResume := "# Resume\n\n" + strings.Repeat("state line for test-proj\n", 400)
	if err := vault.WriteResume("test-proj", bigResume, ""); err != nil {
		t.Fatal(err)
	}
	tool := BootstrapContextTool(resolver, vault)

	// The old `slim` param is gone from the schema; an unknown key must not
	// resurrect the behavior by any route.
	for _, params := range []string{
		`{"project":"test-proj"}`,
		`{"project":"test-proj","slim":true}`,
	} {
		br := bootstrapResult(t, tool, params)
		if strings.HasPrefix(br.Resume, "⚠ excerpt") {
			t.Errorf("%s: resume was byte-excerpted — the deleted slim path is back", params)
		}
		if br.Resume != bigResume {
			t.Errorf("%s: resume not delivered whole: got %d bytes, want %d", params, len(br.Resume), len(bigResume))
		}
		if br.Budget != nil && len(br.Budget.Shed) > 0 {
			t.Errorf("%s: nothing should have been shed under the default budget, got %v", params, br.Budget.Shed)
		}
	}
}

// TestBootstrapRejectsInvalidProject pins that the handler refuses a non-slug
// project up front, rather than succeeding and advertising resume_uri/
// workflow_uri that the vp_read_resource path would later reject.
func TestBootstrapRejectsInvalidProject(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)

	for _, bad := range []string{"MyApp", "has space", "../escape", ""} {
		params := json.RawMessage(`{"project":"` + bad + `"}`)
		if _, err := tool.Handler(context.Background(), params); err == nil {
			t.Errorf("project %q: expected validation error, got nil", bad)
		}
	}
}

// TestBootstrapSlimFalseKeepsFullBodiesOverBudget is the regression pin for the
// MED finding: the token-budget shed loop must NEVER excerpt resume/workflow.
// Under slim=false, even a tiny max_tokens leaves both fully inline and
// byte-identical to the resolved bodies — only sessions/memory/KG/commands shed.
// An UNMARKED resume is never shed — the server does not guess which half of a
// document nobody annotated was safe to drop — and it says so out loud instead.
//
// 🔴 THIS TEST USED TO ASSERT THE BUG. As TestBootstrapSlimFalseKeepsFullBodiesOverBudget
// it pinned "slim=false keeps resume AND workflow fully inline no matter how far
// over budget", which is precisely the behavior that let the live payload return
// 2.4x over its own max_tokens in silence: the shed loop was forbidden to touch
// the 96% of the payload that was resume+workflow+tasks, so it ran out of rungs
// and gave up without a word. The half worth keeping is "do not silently mangle a
// resume you were told nothing about" — kept here, now with the loud report that
// was missing.
func TestBootstrapUnmarkedResumeIsNeverShedAndSaysSo(t *testing.T) {
	vault, resolver := testSetup(t)
	bigResume := "# Resume\n\n## State\n\n" + strings.Repeat("state line for test-proj\n", 400)
	if err := vault.WriteResume("test-proj", bigResume, ""); err != nil {
		t.Fatal(err)
	}
	wantResume, _, err := resolver.Resolve("resume", "test-proj")
	if err != nil {
		t.Fatal(err)
	}
	wantWorkflow, _, err := resolver.Resolve("workflow", "test-proj")
	if err != nil {
		t.Fatal(err)
	}

	tool := BootstrapContextTool(resolver, vault)
	br := bootstrapResult(t, tool, `{"project":"test-proj","max_tokens":50}`)

	if br.Resume != wantResume {
		t.Errorf("resume with no %s marker was shed anyway (len %d, want %d) — the server guessed", resumezone.ResumePinMarker, len(br.Resume), len(wantResume))
	}
	// The contract is put back when shedding it would not have saved the payload
	// anyway: losing the rules AND blowing the budget is worse than blowing it.
	if br.Workflow != wantWorkflow {
		t.Errorf("workflow excerpted for no benefit: still over budget, so the contract should have been restored (len %d, want %d)", len(br.Workflow), len(wantWorkflow))
	}

	// And the whole point: it is NOT silent about any of it.
	if br.Budget == nil {
		t.Fatal("no budget report on a payload that could not meet its budget — this is the silent overrun, unfixed")
	}
	if !br.Budget.OverBudget {
		t.Errorf("over_budget=false at %d estimated tokens against max_tokens=50", br.Budget.EstimatedTokens)
	}
	if !strings.Contains(br.Budget.Reason, resumezone.ResumePinMarker) {
		t.Errorf("over-budget reason does not name the missing pin marker, so nobody can act on it: %q", br.Budget.Reason)
	}
	if !strings.Contains(br.PostBootstrapInstructions, "over its own token budget") {
		t.Errorf("the over-budget alert never reached the directive the agent actually reads: %q", br.PostBootstrapInstructions)
	}
}

// The ladder reaches the resume's shed zone and STOPS AT THE PIN MARKER.
func TestBootstrapShedsResumeDiaryButNeverThePinnedZone(t *testing.T) {
	vault, resolver := testSetup(t)
	const notes = "NEVER place a .vibe-palace.toml at $HOME"
	const diary = "we shipped the thing and then we shipped another thing"
	resume := "---\ntype: project-resume\n---\n\n# Resume\n\n" +
		"## Project-Specific Behavioral Notes\n" + resumezone.ResumePinMarker + "\n\n- " + notes + "\n\n" +
		"## Current State\n\n" + strings.Repeat("- "+diary+"\n", 500)
	if err := vault.WriteResume("test-proj", resume, ""); err != nil {
		t.Fatal(err)
	}

	tool := BootstrapContextTool(resolver, vault)
	br := bootstrapResult(t, tool, `{"project":"test-proj","max_tokens":2000}`)

	if !strings.Contains(br.Resume, notes) {
		t.Error("the PINNED behavioral notes were shed — the marker did not hold, and these are the notes that stop an agent corrupting the vault")
	}
	if strings.Contains(br.Resume, diary) {
		t.Error("the un-pinned diary survived: the ladder did not actually shed the resume")
	}
	if !strings.Contains(br.Resume, br.ResumeURI) {
		t.Errorf("a reduced resume must carry its resume_uri or the full body is unreachable: %q", br.Resume)
	}
	if br.Budget == nil || !slices.Contains(br.Budget.Shed, shedResumePinned) {
		t.Errorf("resume was reduced but the budget report does not say so: %+v", br.Budget)
	}

	// The digest still covers the FULL RAW file. A caller that pages the whole
	// body back through resume_uri and then writes must find its CAS matches
	// disk — a sha of the pinned zone would collide with nothing that exists.
	full, _, wantSha, err := resolver.ResolveDigest("resume", "test-proj")
	if err != nil {
		t.Fatal(err)
	}
	if br.ResumeSha256 != wantSha {
		t.Errorf("resume_sha256 = %q after shedding, want the digest of the full body %q", br.ResumeSha256, wantSha)
	}
	if len(br.Resume) >= len(full) {
		t.Errorf("resume did not shrink: %d >= %d", len(br.Resume), len(full))
	}
}

// The task list sheds to a COUNT, never to nothing: a payload that drops the
// backlog and leaves no trace reads as "this project has no open work".
func TestBootstrapShedTaskListLeavesTheCountAndSaysWhereToLook(t *testing.T) {
	vault, resolver := testSetup(t)
	resume := "# Resume\n\n## Notes\n" + resumezone.ResumePinMarker + "\n\nterse.\n\n## Current State\n\n" +
		strings.Repeat("- narrative\n", 300)
	if err := vault.WriteResume("test-proj", resume, ""); err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		spec := storage.TaskSpec{
			Slug:     "task-" + string(rune('a'+i)),
			Title:    strings.Repeat("a long title that carries the finding ", 6),
			Priority: "high",
			Content:  strings.Repeat("plan body. ", 40),
		}
		if err := vault.CreateTask("test-proj", spec); err != nil {
			t.Fatal(err)
		}
	}

	tool := BootstrapContextTool(resolver, vault)
	br := bootstrapResult(t, tool, `{"project":"test-proj","max_tokens":400}`)

	if len(br.ActiveTasks) != 0 {
		t.Fatalf("task list survived a 400-token budget: %d tasks", len(br.ActiveTasks))
	}
	if br.ActiveTaskCount != 12 {
		t.Errorf("active_task_count = %d after shedding the list, want 12 — the count is the only thing telling the agent the backlog exists", br.ActiveTaskCount)
	}
	if !strings.Contains(br.PostBootstrapInstructions, "vp_list_tasks") {
		t.Errorf("shed the task list without telling the agent how to get it back: %q", br.PostBootstrapInstructions)
	}
}

// 🔴 THE ALERTS SURVIVE AT THE DEFAULT BUDGET WITH A LIVE-SIZED RESUME.
//
// This is the case that was broken, and it is the reason the whole task is a
// blocker for the vault-audit staleness nag. The alerts (friction, vault
// staleness, health — and soon audit staleness) ride in the TAIL of
// post_bootstrap_instructions. The payload used to come back 2.4x over its own
// budget, so a host that hard-truncates cut exactly the tail, and the highest-value
// content in the payload was the first thing lost. Claude Code spilled to a file
// and recovered it; Grok and Zed would not have.
//
// The resume here is sized like a real one (~50 KB) BECAUSE THE BUG ONLY APPEARS
// AT THAT SIZE. A small fixture passes this test with the mechanism entirely
// broken — which is precisely how it stayed broken.
func TestBootstrapAlertsSurviveDefaultBudgetWithLiveSizedResume(t *testing.T) {
	vault, resolver := testSetup(t)
	resume := "# Resume\n\n## Behavioral Notes\n" + resumezone.ResumePinMarker + "\n\n- never do the bad thing\n\n" +
		"## Current State\n\n" + strings.Repeat("- narrative line that belongs in iterations.md\n", 1100)
	if len(resume) < 50_000 {
		t.Fatalf("test resume is %d bytes — too small to reproduce the defect", len(resume))
	}
	if err := vault.WriteResume("test-proj", resume, ""); err != nil {
		t.Fatal(err)
	}

	tool := BootstrapContextTool(resolver, vault)
	// No max_tokens ⇒ DefaultBootstrapMaxTokens.
	br := bootstrapResult(t, tool, `{"project":"test-proj"}`)

	// The temp vault is not a git repo, so vault staleness is unknown ⇒ it warns.
	// That alert is the canary: if it reached the directive, the tail survived.
	if br.VaultStaleness == nil || !br.VaultStaleness.Warn {
		t.Fatal("expected a vault-staleness warning in a non-git temp vault — test premise broken")
	}
	if !strings.Contains(br.PostBootstrapInstructions, br.VaultStaleness.Message) {
		t.Errorf("the staleness alert did not survive into the directive:\n  directive: %q\n  alert: %q",
			br.PostBootstrapInstructions, br.VaultStaleness.Message)
	}

	raw, err := json.Marshal(br)
	if err != nil {
		t.Fatal(err)
	}
	// Derived from the constant, never a literal: this assertion silently stopped
	// meaning anything the moment the budget moved, which is how four unsynchronised
	// copies of "8000" came to exist in the first place.
	if tokens := len(raw) / 4; tokens > DefaultBootstrapMaxTokens {
		t.Errorf("payload is %d tokens against the default budget of %d — the alerts are riding in a tail a host will truncate", tokens, DefaultBootstrapMaxTokens)
	}
	if !strings.Contains(br.Resume, "never do the bad thing") {
		t.Error("the pinned behavioral note was shed at the default budget")
	}
}

// 🔴 THE FOURTH ALERT SURVIVES THE LADDER — which is the entire reason phase 4 of
// the vault audit was gated on the payload fix.
//
// Adding an audit-staleness nag to a payload that returned 2.4x over budget would
// not have been a feature: it would have been a fourth thing riding in a tail the
// host truncates, reporting success while being silently dropped. This asserts it
// arrives at the DEFAULT max_tokens against a live-sized resume — the case that was
// broken — and that a FRESH audit says nothing at all.
func TestBootstrapAuditStalenessNagSurvivesTheShedLadder(t *testing.T) {
	vault, resolver := testSetup(t)
	resume := "# Resume\n\n## Notes\n" + resumezone.ResumePinMarker + "\n\n- terse\n\n## Current State\n\n" +
		strings.Repeat("- narrative that belongs in iterations.md\n", 1200)
	if err := vault.WriteResume("test-proj", resume, ""); err != nil {
		t.Fatal(err)
	}
	// A corpus far past the churn threshold, and an audit report that anchors it.
	seedSessionNotes(t, vault.Root, "test-proj", 120)
	writeAuditReport(t, vault.Root, "2026-01-01", 5)

	tool := BootstrapContextTool(resolver, vault)
	br := bootstrapResult(t, tool, `{"project":"test-proj"}`)

	if br.AuditStaleness == nil || !br.AuditStaleness.Warn {
		t.Fatalf("no audit-staleness nag on a vault 115 notes past its last audit: %+v", br.AuditStaleness)
	}
	if !strings.Contains(br.PostBootstrapInstructions, br.AuditStaleness.Message) {
		t.Errorf("the nag never reached the directive the agent actually reads — it rode in a field and died in the tail:\n  directive: %q",
			br.PostBootstrapInstructions)
	}
	// It must not have COST us the budget to deliver it.
	if br.Budget != nil && br.Budget.OverBudget {
		t.Errorf("payload went over budget carrying the fourth alert: %+v", br.Budget)
	}

	// The other half, and the one that keeps all four alerts readable: a FRESH audit
	// is SILENT. Four alerts that fire on a healthy vault is how a reader learns to
	// skim all four.
	writeAuditReport(t, vault.Root, time.Now().Format("2006-01-02"), 120)
	fresh := bootstrapResult(t, tool, `{"project":"test-proj"}`)
	if fresh.AuditStaleness != nil {
		t.Errorf("a fresh audit still nagged: %+v", fresh.AuditStaleness)
	}
	if strings.Contains(fresh.PostBootstrapInstructions, "vault audit is STALE") {
		t.Errorf("a fresh audit leaked a nag into the directive: %q", fresh.PostBootstrapInstructions)
	}
}

func seedSessionNotes(t *testing.T, root, project string, n int) {
	t.Helper()
	dir := filepath.Join(root, "Projects", project, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		name := fmt.Sprintf("2026-07-%02d-abcdef12-%02d.md", (i%28)+1, i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("# note\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeAuditReport(t *testing.T, root, date string, sessionNotes int) {
	t.Helper()
	dir := filepath.Join(root, "Audits")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("---\ntype: vault-audit\ndate: %s\nsession_notes: %d\n---\n\n# Vault Audit\n", date, sessionNotes)
	if err := os.WriteFile(filepath.Join(dir, date+"-vault-audit.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Nothing shed, inside budget ⇒ NO budget field. Silent when healthy, exactly
// like Health: an always-on report is the soft signal agents learn to skim.
func TestBootstrapBudgetSilentWhenNothingShed(t *testing.T) {
	vault, resolver := testSetup(t)
	if err := vault.WriteResume("test-proj", "# Resume\n\n## State\n\nsmall.\n", ""); err != nil {
		t.Fatal(err)
	}
	tool := BootstrapContextTool(resolver, vault)
	br := bootstrapResult(t, tool, `{"project":"test-proj"}`)
	if br.Budget != nil {
		t.Errorf("budget reported on a healthy payload that shed nothing: %+v", br.Budget)
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

// TestShedRungTierClassification pins the ADR-009 tier partition as a
// structural property — and pins WHICH HALF OF IT IS ALLOWED TO BE A CONSTANT.
//
// Six rungs have a project-agnostic tier and live in shedRungTier, with
// workflow->excerpt the only core one; active_tasks is deliberately context (the
// count survives the shed and vp_list_tasks recovers the list, so nothing is
// silently lost).
//
// 🔴 resume->pinned MUST NOT BE IN THAT MAP, and its absence is an assertion
// here, not an oversight. A static entry for it encodes a claim about ONE
// project's resume.md as a global constant — which is exactly what iteration 260
// did, and what iteration 262's pin-coverage measurement falsified for 8 of 8
// live projects. Its tier is derived per bootstrap by resumeRungTier and read
// through rungTier, which is what this test checks instead.
func TestShedRungTierClassification(t *testing.T) {
	staticRungs := []string{
		shedRecentSessions, shedMemory, shedKGSnapshot, shedCommands,
		shedActiveTasks, shedWorkflow,
	}
	if len(shedRungTier) != len(staticRungs) {
		t.Errorf("shedRungTier has %d entries, want %d — every rung with a project-agnostic tier must carry one, and no rung with a derived tier may", len(shedRungTier), len(staticRungs))
	}
	wantCore := map[string]bool{shedWorkflow: true}
	for _, r := range staticRungs {
		tier, ok := shedRungTier[r]
		if !ok {
			t.Errorf("rung %q has no ADR-009 tier — an unclassified rung sheds with no tier report", r)
			continue
		}
		if tier != shedTierCore && tier != shedTierContext {
			t.Errorf("rung %q has unknown tier %q", r, tier)
		}
		if got := tier == shedTierCore; got != wantCore[r] {
			t.Errorf("rung %q tier = %q, want core=%v", r, tier, wantCore[r])
		}
		// rungTier is the ONE reader: for a static rung it must return the map's
		// answer regardless of what the resume verdict happened to be.
		for _, rt := range []shedTier{shedTierCore, shedTierContext, ""} {
			if got := rungTier(r, rt); got != tier {
				t.Errorf("rungTier(%q, %q) = %q, want the static tier %q", r, rt, got, tier)
			}
		}
	}

	if tier, ok := shedRungTier[shedResumePinned]; ok {
		t.Errorf("%q carries the static tier %q — its tier is a property of the project's resume, not of the ladder; it belongs to resumeRungTier", shedResumePinned, tier)
	}
	// The resume rung takes the derived verdict, whichever way it fell.
	for _, want := range []shedTier{shedTierCore, shedTierContext} {
		if got := rungTier(shedResumePinned, want); got != want {
			t.Errorf("rungTier(%q, %q) = %q — the derived verdict must be honored, not overridden", shedResumePinned, want, got)
		}
	}

	// ERR DOWNWARD. Neither an unclassified rung nor an unset resume verdict may
	// fall through to "context": the bare map lookup this replaced returned "" for
	// a missing key, which compares unequal to shedTierCore and would drop a new
	// rung's shed out of shed_core in silence.
	if got := rungTier("some_rung_added_without_a_tier", shedTierContext); got != shedTierCore {
		t.Errorf("rungTier(unknown rung) = %q, want %q — absence is not a value", got, shedTierCore)
	}
	if got := rungTier(shedResumePinned, ""); got != shedTierCore {
		t.Errorf("rungTier(%q, unset) = %q, want %q — an unmeasured resume is no answer, not a safe answer", shedResumePinned, got, shedTierCore)
	}
}

// TestResumeRungTierDerivation covers the derivation itself, table-driven,
// including every path that must ERR DOWNWARD to core.
//
// The rule under test: shedding the resume is a core loss unless EVERY un-pinned
// section is positively declared disposable. Absence of a marker is live state,
// and a document this cannot rule on at all is no answer rather than a safe one.
func TestResumeRungTierDerivation(t *testing.T) {
	const (
		pin  = resumezone.ResumePinMarker
		disp = resumezone.ResumeDisposableMarker
	)
	tests := []struct {
		name   string
		resume string
		want   shedTier
	}{{
		name:   "one undeclared section makes the shed a core loss",
		resume: "# R\n\n## Notes\n" + pin + "\n\nrule.\n\n## Current State\n\n- live thread\n",
		want:   shedTierCore,
	}, {
		name:   "every un-pinned section declared disposable is context",
		resume: "# R\n\n## Notes\n" + pin + "\n\nrule.\n\n## Reference\n" + disp + "\n\n| doc | tool |\n",
		want:   shedTierContext,
	}, {
		name:   "one undeclared section among declared ones is still core",
		resume: "# R\n\n## Notes\n" + pin + "\n\nrule.\n\n## Reference\n" + disp + "\n\ntable.\n\n## Open Threads\n\n- live\n",
		want:   shedTierCore,
	}, {
		name:   "a resume that pins everything has nothing un-pinned to rule on",
		resume: "# R\n\n## Notes\n" + pin + "\n\nrule.\n\n## State\n" + pin + "\n\n- live\n",
		want:   shedTierContext,
	}, {
		// The pin wins over a co-located disposable marker (resumezone resolves a
		// contradictory declaration toward keeping content), so the section is
		// ruled-on, not undeclared.
		name:   "a section carrying both markers is ruled on, not undeclared",
		resume: "# R\n\n## Notes\n" + pin + "\n\nrule.\n\n## Both\n" + pin + "\n" + disp + "\n\n- content\n",
		want:   shedTierContext,
	}, {
		// ERR DOWNWARD from here down. None of these can be ruled on; every one
		// reports core.
		name:   "a resume declaring no pin zone cannot be ruled on",
		resume: "# R\n\n## State\n\n- live\n\n## Reference\n" + disp + "\n\ntable.\n",
		want:   shedTierCore,
	}, {
		name:   "a body with no H2 sections at all",
		resume: "---\ntype: project-resume\n---\n\n# R\n\njust a preamble.\n",
		want:   shedTierCore,
	}, {
		name:   "no resume resolved at all",
		resume: "",
		want:   shedTierCore,
	}, {
		// Fence-awareness comes from resumezone, the one reader — a marker quoted
		// as sample text declares nothing, here as everywhere else.
		name:   "a disposable marker quoted inside a code fence declares nothing",
		resume: "# R\n\n## Notes\n" + pin + "\n\nrule.\n\n## Current State\n\n```\n" + disp + "\n```\n\n- live thread\n",
		want:   shedTierCore,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resumeRungTier(tc.resume); got != tc.want {
				t.Errorf("resumeRungTier = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResumeRungTierMatchesTheShippedTemplate is the link test between the tier
// derivation and the artifact it rules on: a project scaffolded by `vp init`
// leaves Current State and Open Threads deliberately UNMARKED (the template says
// so in as many words — they are live state, not reference), so a bootstrap that
// sheds a freshly-scaffolded resume IS a core loss and must report one.
//
// It exists because the falsified constant was never wrong about the mechanism,
// only about the document — so the test that keeps it honest has to read the
// real document.
func TestResumeRungTierMatchesTheShippedTemplate(t *testing.T) {
	_, resolver := testSetup(t)
	body, _, err := resolver.Resolve("resume", "test-proj")
	if err != nil {
		t.Fatalf("resolve the scaffolded resume: %v", err)
	}
	undeclared := resumezone.UndeclaredLiveSections(body)
	if len(undeclared) == 0 {
		t.Fatalf("test premise broken: the shipped resume template declares every section — this test can no longer tell a derived tier from the old constant")
	}
	if got := resumeRungTier(body); got != shedTierCore {
		t.Errorf("resumeRungTier on the shipped template = %q, want %q — it leaves %v undeclared, so shedding it drops live state", got, shedTierCore, undeclared)
	}
}

// TestBootstrapThinWorkflowContractSurvivesShedding pins the ADR-008 shape of
// the un-sheddable contract: the embedded workflow floor is thin (under the
// excerpt cap), so even a hopeless budget cannot trigger the workflow rung —
// the minimal bootstrap-contract paragraph that points a fresh host at
// vp_get_doctrine arrives inline no matter what the ladder shed, and the
// overrun is reported loudly rather than paid for with the contract.
func TestBootstrapThinWorkflowContractSurvivesShedding(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)

	br := bootstrapResult(t, tool, `{"project":"test-proj","max_tokens":50}`)

	if !strings.Contains(br.Workflow, "vp_get_doctrine") {
		t.Error("the doctrine-fetch contract paragraph did not survive the shed ladder — a fresh host has no contract")
	}
	if br.Budget == nil {
		t.Fatal("no budget report on a payload that could not meet its budget")
	}
	if sliceHas(br.Budget.Shed, shedWorkflow) {
		t.Errorf("thin embedded workflow was excerpted — it must sit under the excerpt cap so the rung never fires: shed=%v", br.Budget.Shed)
	}
	if !br.Budget.OverBudget {
		t.Errorf("over_budget=false at %d estimated tokens against max_tokens=50", br.Budget.EstimatedTokens)
	}
}

// TestBootstrapFatWorkflowOverrideRestoredWhenBudgetMissedAnyway keeps the
// core-tier guard covered now that the embedded floor is too small to trigger
// the workflow rung: a fat vault-tier override (the pre-rollout / foreign-vault
// case) IS excerpted during the descent, but when the budget is missed anyway
// the guard puts the contract back — shedding core that buys nothing is the
// ADR-009 defect class. shedRungTier classifies the rung core; this drives the
// restore that enforcement rides on.
func TestBootstrapFatWorkflowOverrideRestoredWhenBudgetMissedAnyway(t *testing.T) {
	vault, resolver := testSetup(t)
	fatWorkflow := "# Fat Workflow\n\n" + strings.Repeat("a standing rule line for the contract\n", 300)
	wfPath := filepath.Join(vault.Root, "Templates", "workflow.md")
	if err := os.MkdirAll(filepath.Dir(wfPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wfPath, []byte(fatWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	wantWorkflow, _, err := resolver.Resolve("workflow", "test-proj")
	if err != nil {
		t.Fatal(err)
	}

	tool := BootstrapContextTool(resolver, vault)
	br := bootstrapResult(t, tool, `{"project":"test-proj","max_tokens":50}`)

	if br.Workflow != wantWorkflow {
		t.Errorf("workflow excerpted for no benefit: still over budget, so the core-tier contract should have been restored (len %d, want %d)", len(br.Workflow), len(wantWorkflow))
	}
	if br.Budget == nil {
		t.Fatal("no budget report on a payload that could not meet its budget")
	}
	if sliceHas(br.Budget.Shed, shedWorkflow) {
		t.Errorf("shed still names %s after the restore: %v", shedWorkflow, br.Budget.Shed)
	}
	if !br.Budget.OverBudget {
		t.Errorf("over_budget=false at %d estimated tokens against max_tokens=50", br.Budget.EstimatedTokens)
	}
}

// coreShed filters to the core-classified rungs and preserves shed order, so
// budget.shed_core reads in the same order the ladder acted.
//
// The filter is TIER-driven, not name-driven, and one of those tiers is now an
// argument: the SAME shed list yields a different shed_core depending on the
// verdict resumeRungTier reached about the project being bootstrapped. This test
// used to assert that a shed naming resume->pinned reported no core loss at all,
// which was the falsified constant expressed as an expectation.
func TestCoreShedFiltersAndPreservesOrder(t *testing.T) {
	shed := []string{shedRecentSessions, shedResumePinned, shedActiveTasks, shedWorkflow}

	// Under-declared resume: the rung is core, and order is preserved — resume
	// before workflow, exactly as the ladder acted.
	if got, want := coreShed(shed, shedTierCore), []string{shedResumePinned, shedWorkflow}; !slices.Equal(got, want) {
		t.Errorf("coreShed(under-declared resume) = %v, want %v", got, want)
	}
	// Fully-declared resume: same shed, same order, one fewer core rung.
	if got, want := coreShed(shed, shedTierContext), []string{shedWorkflow}; !slices.Equal(got, want) {
		t.Errorf("coreShed(fully-declared resume) = %v, want %v", got, want)
	}

	// A shed of nothing but context rungs reports no core loss, whichever way the
	// resume verdict fell — the resume rung is not in that shed to be filtered.
	ctxOnly := []string{shedRecentSessions, shedMemory, shedKGSnapshot, shedActiveTasks}
	for _, tier := range []shedTier{shedTierCore, shedTierContext, ""} {
		if got := coreShed(ctxOnly, tier); len(got) != 0 {
			t.Errorf("context-only shed reported core rungs %v at resume tier %q", got, tier)
		}
	}

	// ERR DOWNWARD: an unset verdict is no answer, so the rung reports core.
	if got, want := coreShed([]string{shedResumePinned}, ""), []string{shedResumePinned}; !slices.Equal(got, want) {
		t.Errorf("coreShed(unmeasured resume) = %v, want %v", got, want)
	}
	if got := coreShed(nil, shedTierContext); got != nil {
		t.Errorf("coreShed(nil) = %v, want nil", got)
	}
}

// TestBootstrapShedCoreReportsTheCoreTier drives the whole tool over BOTH sides
// of the derived resume tier, using two fixtures that differ by ONE marker line.
//
// 🔴 THIS TEST'S FIXTURE USED TO ENCODE THE FALSIFIED PREMISE, which is why the
// expectation could not simply be flipped. It built exactly the under-declared
// resume below — a pinned note plus a 500-line un-pinned `## Current State` —
// and asserted that dropping it was NOT a core loss, because iteration 260 had
// hard-coded the rung context-tier on the strength of one project's editorial
// state. The fixture was right about what the ladder DOES and wrong about what
// it MEANS: that section is live state, nobody ruled on it, and the ladder drops
// it. The intent inverts.
//
// Both halves of the contract are asserted:
//
//   - THE REPORT CHANGES. shed_core names resume->pinned when a section is
//     undeclared, and does not when every un-pinned section is declared disposable.
//   - THE PAYLOAD DOES NOT. Same shed list, byte-identical served resume across
//     the two runs. The tier is REPORTING ONLY — refusing to shed core is the
//     gated sibling task adr-009-arm-fail-loud-bootstrap.
func TestBootstrapShedCoreReportsTheCoreTier(t *testing.T) {
	const diary = "un-pinned diary line that the ladder may drop"
	// The two bodies differ only in the marker line under `## Current State`,
	// and that line lives INSIDE the section the ladder drops — so the reduced
	// resume the agent receives is identical text either way.
	body := func(marker string) string {
		return "# Resume\n\n## Notes\n" + resumezone.ResumePinMarker + "\n\n- terse pinned note\n\n" +
			"## Current State\n" + marker + "\n\n" + strings.Repeat("- "+diary+"\n", 500)
	}
	run := func(t *testing.T, resume string) BootstrapResult {
		t.Helper()
		vault, resolver := testSetup(t)
		if err := vault.WriteResume("test-proj", resume, ""); err != nil {
			t.Fatal(err)
		}
		tool := BootstrapContextTool(resolver, vault)
		br := bootstrapResult(t, tool, `{"project":"test-proj","max_tokens":2000}`)

		if br.Budget == nil || !slices.Contains(br.Budget.Shed, shedResumePinned) {
			t.Fatalf("test premise broken: resume->pinned was not shed at max_tokens=2000: %+v", br.Budget)
		}
		// MECHANISM ONLY, and asserted on BOTH runs: the diary goes, the pinned
		// zone stays. Classifying the rung core must not stop the shed.
		if strings.Contains(br.Resume, diary) {
			t.Error("the un-pinned section survived — the tier report must NOT change shed behavior (that is the gated arm task)")
		}
		if !strings.Contains(br.Resume, "terse pinned note") {
			t.Error("the pinned zone was lost — the shed changed behavior, not just reporting")
		}
		// shed_core is a strict, tier-true subset of shed — never an independent list.
		for _, r := range br.Budget.ShedCore {
			if !slices.Contains(br.Budget.Shed, r) {
				t.Errorf("shed_core entry %q is not in shed %v — the report drifted from the ladder", r, br.Budget.Shed)
			}
			if rungTier(r, resumeRungTier(resume)) != shedTierCore {
				t.Errorf("shed_core entry %q is not classified core", r)
			}
		}
		return br
	}

	var live, declared BootstrapResult
	t.Run("undeclared live section is a core loss", func(t *testing.T) {
		live = run(t, body(""))
		if live.Budget == nil || !slices.Contains(live.Budget.ShedCore, shedResumePinned) {
			t.Errorf("`## Current State` carries neither marker and was shed, yet shed_core = %v — the payload dropped live state and reported no core loss, which is the exact silence budget.shed_core exists to break", shedCoreOf(live))
		}
	})
	t.Run("every un-pinned section declared disposable is not", func(t *testing.T) {
		declared = run(t, body(resumezone.ResumeDisposableMarker))
		if slices.Contains(shedCoreOf(declared), shedResumePinned) {
			t.Errorf("every un-pinned section is declared disposable, so shedding the resume costs a lookup and not a rule — reporting it as a core loss is the alarm that cries wolf: %v", shedCoreOf(declared))
		}
	})

	// 🔴 REPORTING ONLY, PROVED SIDE BY SIDE. The two documents differ by one
	// marker line inside the dropped section, so the ladder must produce the same
	// rungs and the byte-identical resume for both; only shed_core may differ.
	if live.Budget == nil || declared.Budget == nil {
		return // a subtest already failed on its premise; nothing to compare
	}
	if live.Resume != declared.Resume {
		t.Errorf("the served resume differs between the two tiers — the tier changed the PAYLOAD, not just the report:\n  core-tier run: %q\n  context-tier run: %q",
			live.Resume, declared.Resume)
	}
	if !slices.Equal(live.Budget.Shed, declared.Budget.Shed) {
		t.Errorf("the shed ladder acted differently for the two tiers: %v vs %v — the tier must not reach the mechanism",
			live.Budget.Shed, declared.Budget.Shed)
	}
}

// shedCoreOf reads ShedCore without assuming a budget was reported, so a
// diagnostic can never panic on the very payload it is trying to describe.
func shedCoreOf(br BootstrapResult) []string {
	if br.Budget == nil {
		return nil
	}
	return br.Budget.ShedCore
}

// A context-only shed reports NO shed_core: the field appearing at all means
// safety surface was dropped, so it must stay silent when only context went.
func TestBootstrapShedCoreAbsentWhenOnlyContextSheds(t *testing.T) {
	vault, resolver := testSetup(t)
	for range 5 {
		if _, err := vault.WriteSession("test-proj", storage.SessionMeta{
			Date:    "2026-04-07",
			Title:   "session with a long title to inflate size",
			Summary: strings.Repeat("detail ", 50),
			Tag:     "implementation",
		}, "body"); err != nil {
			t.Fatal(err)
		}
	}

	tool := BootstrapContextTool(resolver, vault)
	// Budget just above the no-sessions size (+60 covers the budget report the
	// payload now honestly measures itself carrying): sessions shed, core stays.
	fullResult, _ := tool.Handler(context.Background(), json.RawMessage(`{"project":"test-proj","max_tokens":1000000}`))
	fullBR := fullResult.(BootstrapResult)
	withoutSessions := fullBR
	withoutSessions.RecentSessions = nil
	noSessionJSON, _ := json.Marshal(withoutSessions)
	br := bootstrapResult(t, tool, string(mustParams(t, "test-proj", len(noSessionJSON)/4+60)))

	if br.Budget == nil || len(br.Budget.Shed) == 0 {
		t.Fatalf("test premise broken: nothing was shed: %+v", br.Budget)
	}
	for _, r := range br.Budget.Shed {
		// The resume rung is checked by name rather than through the map: its
		// tier is no longer static, and the scaffolded resume this fixture uses
		// leaves live sections undeclared, so shedding it here would be a real
		// core loss and would break the premise rather than the assertion.
		if r == shedResumePinned {
			t.Fatalf("test premise broken: the resume rung was shed at this budget")
		}
		if shedRungTier[r] != shedTierContext {
			t.Fatalf("test premise broken: core rung %q was shed at this budget", r)
		}
	}
	if len(br.Budget.ShedCore) != 0 {
		t.Errorf("shed_core = %v on a context-only shed — the tier report is crying wolf", br.Budget.ShedCore)
	}
}

// 🔴 TestBootstrapOverBudgetReportIsHonest is the regression pin for the stale
// verdict: the ladder used to measure the payload WITHOUT the budget field it
// attaches afterward (and without the post-ladder alerts), freeze over_budget
// on that under-measurement, and ship the live vault at 8060 tokens against a
// budget of 8000 with over=false. The verdict must be a function of the FINAL
// payload: sweep budgets across every shed boundary and re-measure what came
// back. The ±1 slack is the report's own digits — estimated_tokens is part of
// the bytes being measured.
func TestBootstrapOverBudgetReportIsHonest(t *testing.T) {
	vault, resolver := testSetup(t)
	// An un-markered resume is unsheddable, so mid-range budgets genuinely
	// cannot be met; tasks and sessions give the ladder context rungs — and the
	// post-ladder task alert — to hit boundaries with.
	resume := "# Resume\n\n## State\n\n" + strings.Repeat("state line for the sweep\n", 300)
	if err := vault.WriteResume("test-proj", resume, ""); err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		if err := vault.CreateTask("test-proj", storage.TaskSpec{
			Slug:     "task-" + string(rune('a'+i)),
			Title:    "a task title long enough to carry weight in the payload",
			Priority: "high",
		}); err != nil {
			t.Fatal(err)
		}
	}
	for range 5 {
		if _, err := vault.WriteSession("test-proj", storage.SessionMeta{
			Date:    "2026-04-07",
			Title:   "session",
			Summary: strings.Repeat("detail ", 40),
			Tag:     "implementation",
		}, "body"); err != nil {
			t.Fatal(err)
		}
	}

	tool := BootstrapContextTool(resolver, vault)
	full := bootstrapResult(t, tool, `{"project":"test-proj","max_tokens":1000000}`)
	fullRaw, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	fullTokens := len(fullRaw) / 4

	budgets := []int{50, 120, 250, 500, 900, 1300}
	// A fine sweep across the top, where "the ladder thinks it fits but the
	// attached report and alerts push the real payload over" lives.
	for b := fullTokens - 90; b <= fullTokens+20; b += 3 {
		budgets = append(budgets, b)
	}
	for _, max := range budgets {
		br := bootstrapResult(t, tool, string(mustParams(t, "test-proj", max)))
		raw, err := json.Marshal(br)
		if err != nil {
			t.Fatal(err)
		}
		measured := len(raw) / 4

		if br.Budget == nil {
			if measured > max {
				t.Errorf("max_tokens=%d: no budget report on a payload measuring %d tokens — the silent overrun", max, measured)
			}
			continue
		}
		if br.Budget.OverBudget != (br.Budget.EstimatedTokens > max) {
			t.Errorf("max_tokens=%d: over=%v contradicts its own estimated_tokens=%d", max, br.Budget.OverBudget, br.Budget.EstimatedTokens)
		}
		if d := measured - br.Budget.EstimatedTokens; d < -1 || d > 1 {
			t.Errorf("max_tokens=%d: estimated_tokens=%d but the returned payload measures %d — the report describes some other payload", max, br.Budget.EstimatedTokens, measured)
		}
		if measured > max+1 && !br.Budget.OverBudget {
			t.Errorf("max_tokens=%d: over=false on a payload measuring %d tokens — the stale verdict, back again", max, measured)
		}
		if measured < max && br.Budget.OverBudget {
			t.Errorf("max_tokens=%d: over=true on a payload measuring %d tokens — crying wolf trains readers to skim", max, measured)
		}
		if br.Budget.OverBudget && !strings.Contains(br.PostBootstrapInstructions, "over its own token budget") {
			t.Errorf("max_tokens=%d: over=true but the alert never reached the directive the agent reads", max)
		}
	}
}

// TestBootstrapResumeSha256MatchesDisk pins that resume_sha256 is the SHA-256 of
// the vault's resume.md as it sits on disk — the value a Phase-1 compare-and-set
// write is keyed on.
func TestBootstrapResumeSha256MatchesDisk(t *testing.T) {
	vault, resolver := testSetup(t)
	const body = "# Resume\n\nSmall enough to stay fully inline.\n"
	if err := vault.WriteResume("test-proj", body, ""); err != nil {
		t.Fatal(err)
	}
	path, err := vault.ResumeFile("test-proj")
	if err != nil {
		t.Fatal(err)
	}

	br := bootstrapResult(t, BootstrapContextTool(resolver, vault), `{"project":"test-proj"}`)
	if br.Resume != body {
		t.Fatalf("resume not inline: %q", br.Resume)
	}
	if want := onDiskSha(t, path); br.ResumeSha256 != want {
		t.Errorf("resume_sha256 = %q, want on-disk %q", br.ResumeSha256, want)
	}
}

// TestBootstrapShedResumeSha256IsOfFullBody is the regression that would
// otherwise ship silently: when the ladder replaces the resume body with its
// pinned zone, resume_sha256 must still describe the FULL file. Hashing the
// delivered body would make a wrap that pages the full body via resume_uri and
// writes it back conflict with itself on every attempt.
//
// MIGRATED, not deleted: this pinned the same invariant on the byte-axis `slim`
// excerpt until that path was removed. The invariant did not go with it — the
// ladder still substitutes a reduced body — so the test moved to the surviving
// reduction path rather than being dropped for being red.
func TestBootstrapShedResumeSha256IsOfFullBody(t *testing.T) {
	vault, resolver := testSetup(t)
	// Pinned zone + a large un-pinned remainder, so the ladder has something to
	// shed and the delivered body is genuinely shorter than the file.
	bigResume := "# Resume\n\n## Notes\n<!-- vp:pin -->\n\nkeep me\n\n## Diary\n\n" +
		strings.Repeat("state line for test-proj\n", 400)
	if err := vault.WriteResume("test-proj", bigResume, ""); err != nil {
		t.Fatal(err)
	}
	path, err := vault.ResumeFile("test-proj")
	if err != nil {
		t.Fatal(err)
	}

	br := bootstrapResult(t, BootstrapContextTool(resolver, vault), `{"project":"test-proj","max_tokens":400}`)
	if !strings.HasPrefix(br.Resume, "⚠ pinned sections only") {
		t.Fatalf("fixture was not shed to its pinned zone, test proves nothing; got prefix %q",
			br.Resume[:min(80, len(br.Resume))])
	}

	if want := onDiskSha(t, path); br.ResumeSha256 != want {
		t.Errorf("resume_sha256 = %q, want sha of FULL body %q", br.ResumeSha256, want)
	}
	delivered := sha256.Sum256([]byte(br.Resume))
	if br.ResumeSha256 == hex.EncodeToString(delivered[:]) {
		t.Error("resume_sha256 is the sha of the DELIVERED body — a CAS write of the full body would conflict with itself")
	}
}

// TestBootstrapResumeSha256EmptyWithoutProjectFile pins the assert-absent signal:
// with no Projects/<slug>/resume.md the body comes from the embedded default, so
// there is no file to compare against and the sha must be empty.
func TestBootstrapResumeSha256EmptyWithoutProjectFile(t *testing.T) {
	vault, resolver := testSetup(t)

	br := bootstrapResult(t, BootstrapContextTool(resolver, vault), `{"project":"test-proj"}`)
	if br.Resume == "" {
		t.Fatal("expected the embedded default resume")
	}
	if br.ResumeSha256 != "" {
		t.Errorf("resume_sha256 = %q, want empty when no project resume.md exists", br.ResumeSha256)
	}
}
