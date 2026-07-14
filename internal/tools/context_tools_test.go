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

	// Budget that sheds sessions but keeps memory: just above the no-sessions size.
	withoutSessions := fullBR
	withoutSessions.RecentSessions = nil
	noSessionJSON, _ := json.Marshal(withoutSessions)
	budgetKeepMem := len(noSessionJSON)/4 + 10
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
	budgetShedMem := len(noSessMemJSON)/4 + 10
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

// TestBootstrapSlimExcerptsResume verifies the byte-axis slim path: resume is
// replaced by a banner-led, rune-safe excerpt + URI while workflow stays inline.
func TestBootstrapSlimExcerptsResume(t *testing.T) {
	vault, resolver := testSetup(t)
	// A resume larger than bootstrapExcerptCap so slim must excerpt it.
	bigResume := "# Resume\n\n" + strings.Repeat("state line for test-proj\n", 400)
	if err := vault.WriteResume("test-proj", bigResume, ""); err != nil {
		t.Fatal(err)
	}
	tool := BootstrapContextTool(resolver, vault)

	br := bootstrapResult(t, tool, `{"project":"test-proj","slim":true}`)
	if !strings.HasPrefix(br.Resume, "⚠ excerpt — full content at vibe-palace://resume/test-proj") {
		t.Errorf("slim resume missing banner; got prefix %q", br.Resume[:min(80, len(br.Resume))])
	}
	if len(br.Resume) >= len(bigResume) {
		t.Errorf("slim resume not excerpted: len %d >= full %d", len(br.Resume), len(bigResume))
	}
	// Workflow is the behavioral contract — it stays inline (not banner-led)
	// since the embedded default is well under bootstrapWorkflowInlineCap.
	if strings.HasPrefix(br.Workflow, "⚠ excerpt") {
		t.Error("workflow should stay inline under slim, not be excerpted")
	}
}

// TestBootstrapSlimSmallResumeStaysInline pins the fix for the unconditional
// banner: a resume that already fits within bootstrapExcerptCap must be returned
// whole under slim, NOT labeled as a truncated excerpt (which would lure the
// agent into a wasted vp_read_resource fetch for content it already holds).
func TestBootstrapSlimSmallResumeStaysInline(t *testing.T) {
	vault, resolver := testSetup(t)
	smallResume := "# Resume\n\nA short resume for test-proj, well under the cap.\n"
	if len(smallResume) > bootstrapExcerptCap {
		t.Fatalf("test fixture too large: %d > cap %d", len(smallResume), bootstrapExcerptCap)
	}
	if err := vault.WriteResume("test-proj", smallResume, ""); err != nil {
		t.Fatal(err)
	}
	tool := BootstrapContextTool(resolver, vault, true) // HTTP default slim=true

	br := bootstrapResult(t, tool, `{"project":"test-proj"}`)
	if strings.HasPrefix(br.Resume, "⚠ excerpt") {
		t.Errorf("small resume wrongly banner-labeled as excerpt: %q", br.Resume)
	}
	if br.Resume != smallResume {
		t.Errorf("small resume not returned whole under slim:\n got %q\nwant %q", br.Resume, smallResume)
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

// TestBootstrapSlimPerTransportDefault pins the tri-state default: with the
// param omitted, the per-transport default (threaded via BootstrapContextTool's
// slimDefault) decides; an explicit slim overrides it both ways.
func TestBootstrapSlimPerTransportDefault(t *testing.T) {
	vault, resolver := testSetup(t)
	bigResume := "# Resume\n\n" + strings.Repeat("state line for test-proj\n", 400)
	if err := vault.WriteResume("test-proj", bigResume, ""); err != nil {
		t.Fatal(err)
	}

	isExcerpt := func(br BootstrapResult) bool {
		return strings.HasPrefix(br.Resume, "⚠ excerpt")
	}

	// HTTP transport defaults slim=true (param omitted).
	httpTool := BootstrapContextTool(resolver, vault, true)
	if !isExcerpt(bootstrapResult(t, httpTool, `{"project":"test-proj"}`)) {
		t.Error("HTTP default (slimDefault=true), param omitted: expected excerpt")
	}
	// stdio transport defaults slim=false (param omitted).
	stdioTool := BootstrapContextTool(resolver, vault) // slimDefault defaults false
	if isExcerpt(bootstrapResult(t, stdioTool, `{"project":"test-proj"}`)) {
		t.Error("stdio default (slimDefault=false), param omitted: expected full body")
	}
	// Explicit param overrides the transport default both ways.
	if isExcerpt(bootstrapResult(t, httpTool, `{"project":"test-proj","slim":false}`)) {
		t.Error("explicit slim=false over HTTP: expected full body")
	}
	if !isExcerpt(bootstrapResult(t, stdioTool, `{"project":"test-proj","slim":true}`)) {
		t.Error("explicit slim=true over stdio: expected excerpt")
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
	br := bootstrapResult(t, tool, `{"project":"test-proj","slim":false,"max_tokens":50}`)

	if br.Resume != wantResume {
		t.Errorf("resume with no %s marker was shed anyway (len %d, want %d) — the server guessed", ResumePinMarker, len(br.Resume), len(wantResume))
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
	if !strings.Contains(br.Budget.Reason, ResumePinMarker) {
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
		"## Project-Specific Behavioral Notes\n" + ResumePinMarker + "\n\n- " + notes + "\n\n" +
		"## Current State\n\n" + strings.Repeat("- "+diary+"\n", 500)
	if err := vault.WriteResume("test-proj", resume, ""); err != nil {
		t.Fatal(err)
	}

	tool := BootstrapContextTool(resolver, vault)
	br := bootstrapResult(t, tool, `{"project":"test-proj","slim":false,"max_tokens":2000}`)

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
	resume := "# Resume\n\n## Notes\n" + ResumePinMarker + "\n\nterse.\n\n## Current State\n\n" +
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
	br := bootstrapResult(t, tool, `{"project":"test-proj","slim":false,"max_tokens":400}`)

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
	resume := "# Resume\n\n## Behavioral Notes\n" + ResumePinMarker + "\n\n- never do the bad thing\n\n" +
		"## Current State\n\n" + strings.Repeat("- narrative line that belongs in iterations.md\n", 1100)
	if len(resume) < 50_000 {
		t.Fatalf("test resume is %d bytes — too small to reproduce the defect", len(resume))
	}
	if err := vault.WriteResume("test-proj", resume, ""); err != nil {
		t.Fatal(err)
	}

	tool := BootstrapContextTool(resolver, vault)
	// No max_tokens ⇒ the DEFAULT (8000). No slim ⇒ stdio's default (false).
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
	if tokens := len(raw) / 4; tokens > 8000 {
		t.Errorf("payload is %d tokens against the default budget of 8000 — the alerts are riding in a tail a host will truncate", tokens)
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
	resume := "# Resume\n\n## Notes\n" + ResumePinMarker + "\n\n- terse\n\n## Current State\n\n" +
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

// TestBootstrapSlimResumeSha256IsOfFullBody is the regression that would
// otherwise ship silently: under slim the resume body is replaced by a banner-led
// excerpt, but resume_sha256 must still describe the FULL file. Hashing the
// excerpt would make a wrap that pages the full body via resume_uri and writes it
// back conflict with itself on every attempt.
func TestBootstrapSlimResumeSha256IsOfFullBody(t *testing.T) {
	vault, resolver := testSetup(t)
	bigResume := "# Resume\n\n" + strings.Repeat("state line for test-proj\n", 400)
	if len(bigResume) <= bootstrapExcerptCap {
		t.Fatalf("fixture too small to be excerpted: %d <= cap %d", len(bigResume), bootstrapExcerptCap)
	}
	if err := vault.WriteResume("test-proj", bigResume, ""); err != nil {
		t.Fatal(err)
	}
	path, err := vault.ResumeFile("test-proj")
	if err != nil {
		t.Fatal(err)
	}

	br := bootstrapResult(t, BootstrapContextTool(resolver, vault), `{"project":"test-proj","slim":true}`)
	if !strings.HasPrefix(br.Resume, "⚠ excerpt") {
		t.Fatalf("fixture was not excerpted, test proves nothing")
	}

	if want := onDiskSha(t, path); br.ResumeSha256 != want {
		t.Errorf("resume_sha256 = %q, want sha of FULL body %q", br.ResumeSha256, want)
	}
	excerpt := sha256.Sum256([]byte(br.Resume))
	if br.ResumeSha256 == hex.EncodeToString(excerpt[:]) {
		t.Error("resume_sha256 is the sha of the EXCERPT — a CAS write of the full body would conflict with itself")
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
