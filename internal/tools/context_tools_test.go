// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/project"
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
}

// TestCommandInvocationUsesCanonicalToolNames was DELETED, not silently lost,
// and this note is here so nobody re-derives it from scratch on seeing the gap.
//
// It guarded commandInvocationDirective — the per-call copy of the vpc-/vps-
// dispatch rule that rode in every bootstrap payload. That field is gone: the
// same sentence already reaches a client twice before any tool call, via
// mcp.ServerInstructions at initialize and via the agentfile block, and the
// per-call third copy was the one a host preview ate.
//
// Its falsifiable half — no stale tool name — moved to
// mcp.TestServerInstructionsConst, onto the copy that is now load-bearing, in
// the same commit. Its other half did NOT move: asserting that a string built
// from agentfile.CommandToolName contains agentfile.CommandToolName is a
// tautology that cannot go red, and it was mutation-tested to confirm exactly
// that before being dropped.

func TestBootstrapIncludesSkills(t *testing.T) {
	// Seed vault-level skills. Embedded skills also exist now; the
	// assertion is that the seeded names appear, not that they are alone.
	// analyze carries a YAML fence — the same shape as every embedded
	// SKILL.md — so a brief of "---" is a FAIL, not a missing description.
	root := t.TempDir()
	writeVaultFile(t, root, "Templates/skills/analyze/SKILL.md", "---\nname: analyze\ndescription: >\n  Deep analysis.\n---\n\n# Analyze\n\nPerform deep analysis of the codebase.")
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
	if s := names["analyze"]; s.Brief == "" || s.Brief == "---" {
		t.Errorf("seeded skill 'analyze' brief = %q, want the body sentence not the YAML fence", s.Brief)
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
	}
}

// TestBootstrapRejectsInvalidProject pins that the handler refuses a non-slug
// project up front, rather than succeeding and advertising resume_uri/
// workflow_uri that the vp_read_resource path would later reject.
func TestBootstrapRejectsInvalidProject(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)

	// Empty string is handled by resolveBootstrapProject (cwd default / loud
	// error), not slug.Validate — covered by the defaulting tests below.
	for _, bad := range []string{"MyApp", "has space", "../escape"} {
		params := json.RawMessage(`{"project":"` + bad + `"}`)
		if _, err := tool.Handler(context.Background(), params); err == nil {
			t.Errorf("project %q: expected validation error, got nil", bad)
		}
	}
}

// TestBootstrapDefaultsProjectFromHighConfidenceCwd pins L4: when project is
// omitted on the stdio tool, a high-confidence cwd signal plus an existing
// Projects/<slug>/ in the vault supplies the slug. Basename-only dirs must
// never default (see TestBootstrapRefusesBasenameDefault).
func TestBootstrapDefaultsProjectFromHighConfidenceCwd(t *testing.T) {
	vault, resolver := testSetup(t)
	// Seed the vault project tree the existence gate requires.
	if err := vault.WriteResume("hc-boot", "# Resume\n", ""); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	cfg := filepath.Join(dir, project.ConfigFileName)
	if err := os.WriteFile(cfg, []byte("[project]\nname = \"hc-boot\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	tool := BootstrapContextTool(resolver, vault)
	result, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	br, ok := result.(BootstrapResult)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if br.Project != "hc-boot" {
		t.Errorf("Project = %q, want hc-boot", br.Project)
	}
}

// TestBootstrapRefusesBasenameDefault is the ADR-006 pin: DetectProject would
// happily return the directory basename, but bootstrap must not — a wrong
// silent default returns a successful empty-ish payload and poisons the session.
func TestBootstrapRefusesBasenameDefault(t *testing.T) {
	vault, resolver := testSetup(t)
	parent := t.TempDir()
	dir := filepath.Join(parent, "basename-only-proj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Even if the vault happens to have that slug, basename is not high-confidence.
	if err := vault.WriteResume("basename-only-proj", "# R\n", ""); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	tool := BootstrapContextTool(resolver, vault)
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected error when only basename is available, got nil")
	}
}

// TestBootstrapDefaultRequiresVaultProject pins the second L4 gate: a
// high-confidence detect whose slug has no Projects/<slug>/ must fail loud
// rather than AssembleBootstrap's graceful empty success.
func TestBootstrapDefaultRequiresVaultProject(t *testing.T) {
	vault, resolver := testSetup(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.ConfigFileName), []byte("[project]\nname = \"no-vault-tree\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	tool := BootstrapContextTool(resolver, vault)
	_, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error when Projects/<slug>/ is absent")
	}
	if !strings.Contains(err.Error(), "no-vault-tree") {
		t.Errorf("error should name detected slug, got: %v", err)
	}
	if !strings.Contains(err.Error(), "vp_list_projects") {
		t.Errorf("error should tell caller how to recover, got: %v", err)
	}
}

// TestBootstrapExplicitProjectRequiredOnHTTPPath pins that the multiplexed
// serve tool refuses empty project even when cwd would high-confidence detect.
func TestBootstrapExplicitProjectRequiredOnHTTPPath(t *testing.T) {
	vault, resolver := testSetup(t)
	if err := vault.WriteResume("hc-boot", "# Resume\n", ""); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.ConfigFileName), []byte("[project]\nname = \"hc-boot\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	tool := BootstrapContextToolExplicit(resolver, vault)
	_, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("explicit tool must require project even with high-confidence cwd")
	}
	if !strings.Contains(err.Error(), "project is required") {
		t.Errorf("error = %v, want project is required", err)
	}
	// Explicit still works.
	result, err := tool.Handler(context.Background(), json.RawMessage(`{"project":"hc-boot"}`))
	if err != nil {
		t.Fatalf("explicit project: %v", err)
	}
	if br := result.(BootstrapResult); br.Project != "hc-boot" {
		t.Errorf("Project = %q", br.Project)
	}
}

// TestBootstrapSchemaProjectOptional pins the stdio schema shape: project is
// not in required[], so hosts may omit it and hit the handler default path.
func TestBootstrapSchemaProjectOptional(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)
	var schema struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	for _, r := range schema.Required {
		if r == "project" {
			t.Error("project must not be in stdio schema required[] after L4 (handler gates defaulting)")
		}
	}
	if _, ok := schema.Properties["project"]; !ok {
		t.Error("project property missing from schema")
	}
}

// TestBootstrapSchemaExplicitRequiresProject pins that the HTTP/explicit
// variant's machine-readable contract matches the runtime: project is required
// in schema, not only in a handler error string.
func TestBootstrapSchemaExplicitRequiresProject(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextToolExplicit(resolver, vault)
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if !slices.Contains(schema.Required, "project") {
		t.Error("BootstrapContextToolExplicit schema must require project — otherwise schema-driven clients omit it and only learn from a runtime string")
	}
	// Schemas must not be identical (stdio optional vs explicit required).
	stdio := BootstrapContextTool(resolver, vault)
	if string(stdio.Schema) == string(tool.Schema) {
		t.Error("stdio and explicit bootstrap schemas must differ — shared literal reopens the optional-schema/required-runtime split")
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
func TestBootstrapAlertsSurviveALiveSizedResume(t *testing.T) {
	vault, resolver := testSetup(t)
	resume := "# Resume\n\n## Behavioral Notes\n\n- never do the bad thing\n\n" +
		"## Current State\n\n" + strings.Repeat("- narrative line that belongs in iterations.md\n", 1100)
	if len(resume) < 50_000 {
		t.Fatalf("test resume is %d bytes — too small to reproduce the defect", len(resume))
	}
	if err := vault.WriteResume("test-proj", resume, ""); err != nil {
		t.Fatal(err)
	}

	tool := BootstrapContextTool(resolver, vault)
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

	if !strings.Contains(br.Resume, "never do the bad thing") {
		t.Error("the resume did not arrive whole")
	}
	if br.Resume != resume {
		t.Errorf("resume not delivered whole: got %d bytes, want %d", len(br.Resume), len(resume))
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
func TestBootstrapAuditStalenessNagReachesTheDirective(t *testing.T) {
	vault, resolver := testSetup(t)
	resume := "# Resume\n\n## Notes\n\n- terse\n\n## Current State\n\n" +
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
