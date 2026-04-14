// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testResolver(t *testing.T) (*Resolver, string) {
	t.Helper()
	root := t.TempDir()
	return NewResolver(root), root
}

// writeFile creates a file at the given path with the given content,
// creating parent directories as needed.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// --- Resolve tests ---

func TestResolveEmbeddedDefault(t *testing.T) {
	r, _ := testResolver(t)

	content, source, err := r.Resolve("workflow", "test-proj")
	if err != nil {
		t.Fatalf("Resolve(workflow): %v", err)
	}
	if source != "embedded" {
		t.Errorf("source = %q, want %q", source, "embedded")
	}
	if !strings.Contains(content, "Pair Programming") {
		t.Error("embedded workflow.md should contain 'Pair Programming'")
	}
}

func TestResolveVaultOverride(t *testing.T) {
	r, root := testResolver(t)

	vaultWF := filepath.Join(root, "Templates", "workflow.md")
	writeFile(t, vaultWF, "vault workflow override")

	content, source, err := r.Resolve("workflow", "test-proj")
	if err != nil {
		t.Fatalf("Resolve(workflow): %v", err)
	}
	if source != "vault" {
		t.Errorf("source = %q, want %q", source, "vault")
	}
	if content != "vault workflow override" {
		t.Errorf("content = %q, want vault override", content)
	}
}

func TestResolveProjectOverride(t *testing.T) {
	r, root := testResolver(t)

	// Write both vault and project — project should win.
	writeFile(t, filepath.Join(root, "Templates", "workflow.md"), "vault")
	writeFile(t, filepath.Join(root, "Projects", "myproj", "workflow.md"), "project")

	content, source, err := r.Resolve("workflow", "myproj")
	if err != nil {
		t.Fatalf("Resolve(workflow): %v", err)
	}
	if source != "project" {
		t.Errorf("source = %q, want %q", source, "project")
	}
	if content != "project" {
		t.Errorf("content = %q, want %q", content, "project")
	}
}

func TestResolveFallthrough(t *testing.T) {
	r, root := testResolver(t)

	// Write vault template but no project override.
	writeFile(t, filepath.Join(root, "Templates", "resume.md"), "vault resume")

	content, source, err := r.Resolve("resume", "myproj")
	if err != nil {
		t.Fatalf("Resolve(resume): %v", err)
	}
	if source != "vault" {
		t.Errorf("source = %q, want %q", source, "vault")
	}
	if content != "vault resume" {
		t.Errorf("content = %q, want %q", content, "vault resume")
	}
}

func TestResolveEmptyProject(t *testing.T) {
	r, _ := testResolver(t)

	// Empty project should skip project tier, still resolve from embedded.
	_, source, err := r.Resolve("workflow", "")
	if err != nil {
		t.Fatalf("Resolve(workflow, empty project): %v", err)
	}
	if source != "embedded" {
		t.Errorf("source = %q, want %q", source, "embedded")
	}
}

func TestResolveCommand(t *testing.T) {
	r, _ := testResolver(t)

	content, source, err := r.Resolve("command:restart", "test-proj")
	if err != nil {
		t.Fatalf("Resolve(command:restart): %v", err)
	}
	if source != "embedded" {
		t.Errorf("source = %q, want %q", source, "embedded")
	}
	if !strings.Contains(content, "Context Restoration") {
		t.Error("restart command should contain 'Context Restoration'")
	}
}

func TestResolveCommandVaultOverride(t *testing.T) {
	r, root := testResolver(t)

	writeFile(t, filepath.Join(root, "Templates", "commands", "restart.md"), "custom restart")

	content, source, err := r.Resolve("command:restart", "test-proj")
	if err != nil {
		t.Fatalf("Resolve(command:restart): %v", err)
	}
	if source != "vault" {
		t.Errorf("source = %q, want %q", source, "vault")
	}
	if content != "custom restart" {
		t.Errorf("content = %q, want %q", content, "custom restart")
	}
}

func TestResolveSkill(t *testing.T) {
	r, root := testResolver(t)

	// Directory-form skill at vault tier.
	writeFile(t, filepath.Join(root, "Templates", "skills", "analyze", "SKILL.md"), "analyze skill")

	content, source, err := r.Resolve("skill:analyze", "test-proj")
	if err != nil {
		t.Fatalf("Resolve(skill:analyze): %v", err)
	}
	if source != "vault" {
		t.Errorf("source = %q, want %q", source, "vault")
	}
	if content != "analyze skill" {
		t.Errorf("content = %q, want %q", content, "analyze skill")
	}
}

func TestResolveNotFound(t *testing.T) {
	r, _ := testResolver(t)

	_, _, err := r.Resolve("skill:nonexistent", "test-proj")
	if err == nil {
		t.Fatal("expected error for nonexistent skill, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found' message", err.Error())
	}
}

func TestResolveUnknownResourceType(t *testing.T) {
	r, _ := testResolver(t)

	_, _, err := r.Resolve("bogus:thing", "proj")
	if err == nil {
		t.Fatal("expected error for unknown resource type, got nil")
	}
	if !strings.Contains(err.Error(), "unknown resource type") {
		t.Errorf("error = %q, want 'unknown resource type' message", err.Error())
	}
}

// --- Expansion tests ---

func TestExpandProjectAndDate(t *testing.T) {
	r, _ := testResolver(t)

	content, _, err := r.Resolve("resume", "my-project")
	if err != nil {
		t.Fatalf("Resolve(resume): %v", err)
	}
	if strings.Contains(content, "{{PROJECT}}") {
		t.Error("{{PROJECT}} was not expanded")
	}
	if !strings.Contains(content, "my-project") {
		t.Error("project name not found in expanded content")
	}
	today := time.Now().Format("2006-01-02")
	if !strings.Contains(content, today) {
		t.Errorf("today's date %s not found in expanded content", today)
	}
}

// --- Path traversal tests ---

func TestResolvePathTraversal(t *testing.T) {
	r, _ := testResolver(t)

	tests := []struct {
		name     string
		resource string
	}{
		{"dotdot", "command:../../etc/passwd"},
		{"slash", "command:foo/bar"},
		{"backslash", "skill:foo\\bar"},
		{"empty name", "command:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := r.Resolve(tt.resource, "proj")
			if err == nil {
				t.Fatalf("Resolve(%q) should have returned an error", tt.resource)
			}
		})
	}
}

// --- resourceToPath tests ---

func TestResourceToPath(t *testing.T) {
	tests := []struct {
		resource string
		want     string
		wantErr  bool
	}{
		{"workflow", "workflow.md", false},
		{"resume", "resume.md", false},
		{"command:restart", filepath.Join("commands", "restart.md"), false},
		{"command:review-plan", filepath.Join("commands", "review-plan.md"), false},
		{"skill:analyze", filepath.Join("skills", "analyze", "SKILL.md"), false},
		{"command:", "", true},
		{"skill:", "", true},
		{"unknown", "", true},
		{"command:../escape", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.resource, func(t *testing.T) {
			got, err := resourceToPath(tt.resource)
			if (err != nil) != tt.wantErr {
				t.Errorf("resourceToPath(%q) error = %v, wantErr %v", tt.resource, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("resourceToPath(%q) = %q, want %q", tt.resource, got, tt.want)
			}
		})
	}
}

// --- ListResources tests ---

func TestListCommandsEmbedded(t *testing.T) {
	r, _ := testResolver(t)

	resources, err := r.ListResources("command", "")
	if err != nil {
		t.Fatalf("ListResources(command): %v", err)
	}
	// Embedded commands: cancel-plan, capture, restart, review-plan, wrap (sorted).
	if len(resources) != 5 {
		t.Fatalf("got %d commands, want 5: %v", len(resources), resources)
	}
	wantNames := []string{"cancel-plan", "capture", "restart", "review-plan", "wrap"}
	for i, want := range wantNames {
		if resources[i].Name != want {
			t.Errorf("resources[%d].Name = %q, want %q", i, resources[i].Name, want)
		}
		if resources[i].Source != "embedded" {
			t.Errorf("resources[%d].Source = %q, want %q", i, resources[i].Source, "embedded")
		}
	}
}

func TestListCommandsMergedNoDuplicates(t *testing.T) {
	r, root := testResolver(t)

	// Add a vault-level restart override and a new vault command.
	writeFile(t, filepath.Join(root, "Templates", "commands", "restart.md"), "override")
	writeFile(t, filepath.Join(root, "Templates", "commands", "deploy.md"), "deploy cmd")

	// Add a project-level command.
	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "custom.md"), "custom cmd")

	resources, err := r.ListResources("command", "proj")
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}

	// Expect: cancel-plan(embedded), capture(embedded), custom(project), deploy(vault),
	//         restart(vault — shadows embedded), review-plan(embedded), wrap(embedded)
	if len(resources) != 7 {
		t.Fatalf("got %d resources, want 7: %v", len(resources), resources)
	}

	// Check restart comes from vault (override), not embedded.
	for _, ri := range resources {
		if ri.Name == "restart" && ri.Source != "vault" {
			t.Errorf("restart source = %q, want %q (vault should shadow embedded)", ri.Source, "vault")
		}
		if ri.Name == "custom" && ri.Source != "project" {
			t.Errorf("custom source = %q, want %q", ri.Source, "project")
		}
	}
}

func TestListSkillsEmbedded(t *testing.T) {
	r, _ := testResolver(t)

	resources, err := r.ListResources("skill", "")
	if err != nil {
		t.Fatalf("ListResources(skill): %v", err)
	}
	// Embedded skills: startup-analyst (directory-form).
	if len(resources) != 1 {
		t.Fatalf("got %d skills, want 1: %v", len(resources), resources)
	}
	if resources[0].Name != "startup-analyst" {
		t.Errorf("resources[0].Name = %q, want startup-analyst", resources[0].Name)
	}
	if resources[0].Source != "embedded" {
		t.Errorf("resources[0].Source = %q, want embedded", resources[0].Source)
	}
}

func TestListResourcesInvalidType(t *testing.T) {
	r, _ := testResolver(t)

	_, err := r.ListResources("bogus", "")
	if err == nil {
		t.Fatal("expected error for unsupported resource type, got nil")
	}
}

// --- Scoped Resolution tests ---

func TestResolveScopedRoomOverridesWing(t *testing.T) {
	r, root := testResolver(t)

	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "backend", ".wing", "lint.md"), "wing lint")
	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "backend", "api", "lint.md"), "room lint")

	content, source, err := r.ResolveScoped("command:lint", "proj", "backend", "api")
	if err != nil {
		t.Fatalf("ResolveScoped: %v", err)
	}
	if source != "room" {
		t.Errorf("source = %q, want %q", source, "room")
	}
	if content != "room lint" {
		t.Errorf("content = %q, want %q", content, "room lint")
	}
}

func TestResolveScopedWingOverridesProject(t *testing.T) {
	r, root := testResolver(t)

	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "lint.md"), "project lint")
	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "backend", ".wing", "lint.md"), "wing lint")

	content, source, err := r.ResolveScoped("command:lint", "proj", "backend", "")
	if err != nil {
		t.Fatalf("ResolveScoped: %v", err)
	}
	if source != "wing" {
		t.Errorf("source = %q, want %q", source, "wing")
	}
	if content != "wing lint" {
		t.Errorf("content = %q, want %q", content, "wing lint")
	}
}

func TestResolveScopedWingOnly(t *testing.T) {
	r, root := testResolver(t)

	// Wing without room: 4-tier resolution (wing > project > vault > embedded).
	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "backend", ".wing", "deploy.md"), "wing deploy")

	content, source, err := r.ResolveScoped("command:deploy", "proj", "backend", "")
	if err != nil {
		t.Fatalf("ResolveScoped: %v", err)
	}
	if source != "wing" {
		t.Errorf("source = %q, want %q", source, "wing")
	}
	if content != "wing deploy" {
		t.Errorf("content = %q, want %q", content, "wing deploy")
	}
}

func TestResolveScopedNoScope(t *testing.T) {
	r, _ := testResolver(t)

	// Empty wing/room: existing 3-tier behavior unchanged.
	content, source, err := r.ResolveScoped("command:restart", "proj", "", "")
	if err != nil {
		t.Fatalf("ResolveScoped: %v", err)
	}
	if source != "embedded" {
		t.Errorf("source = %q, want %q", source, "embedded")
	}
	if !strings.Contains(content, "Context Restoration") {
		t.Error("expected embedded restart content")
	}
}

func TestResolveScopedRoomWithoutWingErrors(t *testing.T) {
	r, _ := testResolver(t)

	_, _, err := r.ResolveScoped("command:lint", "proj", "", "api")
	if err == nil {
		t.Fatal("expected error when room is set without wing")
	}
	if !strings.Contains(err.Error(), "requires a wing") {
		t.Errorf("error = %q, want 'requires a wing' message", err.Error())
	}
}

func TestResolveScopedInvalidSlug(t *testing.T) {
	r, _ := testResolver(t)

	_, _, err := r.ResolveScoped("command:lint", "proj", "Bad Wing", "")
	if err == nil {
		t.Fatal("expected error for invalid wing slug")
	}
	if !strings.Contains(err.Error(), "invalid wing") {
		t.Errorf("error = %q, want 'invalid wing' message", err.Error())
	}

	_, _, err = r.ResolveScoped("command:lint", "proj", "backend", "Bad Room")
	if err == nil {
		t.Fatal("expected error for invalid room slug")
	}
	if !strings.Contains(err.Error(), "invalid room") {
		t.Errorf("error = %q, want 'invalid room' message", err.Error())
	}
}

func TestResolveScopedFallthroughToEmbedded(t *testing.T) {
	r, _ := testResolver(t)

	// Even with wing/room set, should fall through to embedded if no overrides exist.
	content, source, err := r.ResolveScoped("command:restart", "proj", "backend", "api")
	if err != nil {
		t.Fatalf("ResolveScoped: %v", err)
	}
	if source != "embedded" {
		t.Errorf("source = %q, want %q", source, "embedded")
	}
	if !strings.Contains(content, "Context Restoration") {
		t.Error("expected embedded restart content")
	}
}

func TestListResourcesScopedFullMerge(t *testing.T) {
	r, root := testResolver(t)

	// Create commands at all 5 tiers.
	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "backend", "api", "gen.md"), "room gen")
	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "backend", ".wing", "lint.md"), "wing lint")
	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "custom.md"), "project custom")
	writeFile(t, filepath.Join(root, "Templates", "commands", "deploy.md"), "vault deploy")
	// Embedded: cancel-plan, capture, restart, review-plan, wrap

	resources, err := r.ListResourcesScoped("command", "proj", "backend", "api")
	if err != nil {
		t.Fatalf("ListResourcesScoped: %v", err)
	}

	// Expect: cancel-plan(embedded), capture(embedded), custom(project),
	//         deploy(vault), gen(room), lint(wing), restart(embedded),
	//         review-plan(embedded), wrap(embedded) = 9 total
	if len(resources) != 9 {
		names := make([]string, len(resources))
		for i, ri := range resources {
			names[i] = ri.Name + "(" + ri.Source + ")"
		}
		t.Fatalf("got %d resources %v, want 9", len(resources), names)
	}

	// Verify specific sources.
	sourceMap := make(map[string]string)
	for _, ri := range resources {
		sourceMap[ri.Name] = ri.Source
	}
	checks := map[string]string{
		"gen":    "room",
		"lint":   "wing",
		"custom": "project",
		"deploy": "vault",
		"restart": "embedded",
	}
	for name, wantSource := range checks {
		if got := sourceMap[name]; got != wantSource {
			t.Errorf("%s source = %q, want %q", name, got, wantSource)
		}
	}
}

func TestListResourcesScopedWingOnly(t *testing.T) {
	r, root := testResolver(t)

	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "backend", ".wing", "lint.md"), "wing lint")
	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "custom.md"), "project custom")

	resources, err := r.ListResourcesScoped("command", "proj", "backend", "")
	if err != nil {
		t.Fatalf("ListResourcesScoped: %v", err)
	}

	// Wing + project + embedded (5 embedded commands) = 7
	if len(resources) != 7 {
		names := make([]string, len(resources))
		for i, ri := range resources {
			names[i] = ri.Name + "(" + ri.Source + ")"
		}
		t.Fatalf("got %d resources %v, want 7", len(resources), names)
	}

	sourceMap := make(map[string]string)
	for _, ri := range resources {
		sourceMap[ri.Name] = ri.Source
	}
	if sourceMap["lint"] != "wing" {
		t.Errorf("lint source = %q, want %q", sourceMap["lint"], "wing")
	}
	if sourceMap["custom"] != "project" {
		t.Errorf("custom source = %q, want %q", sourceMap["custom"], "project")
	}
}

func TestListResourcesScopedProjectDirSkipsSubdirs(t *testing.T) {
	r, root := testResolver(t)

	// Wing subdirectories under commands/ must not appear as project-level commands.
	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "custom.md"), "project cmd")
	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "backend", ".wing", "lint.md"), "wing cmd")

	// List without scope — should only see project-level "custom" plus embedded.
	resources, err := r.ListResources("command", "proj")
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}

	for _, ri := range resources {
		if ri.Name == "lint" {
			t.Error("wing-scoped 'lint' leaked into unscoped listing")
		}
		if ri.Name == "backend" {
			t.Error("wing directory 'backend' appeared as a command")
		}
	}
}

func TestExpandWingAndRoom(t *testing.T) {
	r, root := testResolver(t)

	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "backend", "api", "info.md"),
		"Project: {{PROJECT}}, Wing: {{WING}}, Room: {{ROOM}}")

	content, source, err := r.ResolveScoped("command:info", "proj", "backend", "api")
	if err != nil {
		t.Fatalf("ResolveScoped: %v", err)
	}
	if source != "room" {
		t.Errorf("source = %q, want %q", source, "room")
	}
	if content != "Project: proj, Wing: backend, Room: api" {
		t.Errorf("content = %q, want expanded placeholders", content)
	}
}

func TestListResourcesScopedShadowing(t *testing.T) {
	r, root := testResolver(t)

	// Same command at room and wing level — room should shadow.
	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "backend", ".wing", "lint.md"), "wing")
	writeFile(t, filepath.Join(root, "Projects", "proj", "commands", "backend", "api", "lint.md"), "room")

	resources, err := r.ListResourcesScoped("command", "proj", "backend", "api")
	if err != nil {
		t.Fatalf("ListResourcesScoped: %v", err)
	}

	for _, ri := range resources {
		if ri.Name == "lint" {
			if ri.Source != "room" {
				t.Errorf("lint source = %q, want %q (room should shadow wing)", ri.Source, "room")
			}
			return
		}
	}
	t.Error("lint not found in resource list")
}

func TestResolveScopedSkill(t *testing.T) {
	r, root := testResolver(t)

	writeFile(t, filepath.Join(root, "Projects", "proj", "skills", "backend", "api", "owasp", "SKILL.md"), "room owasp skill")

	content, source, err := r.ResolveScoped("skill:owasp", "proj", "backend", "api")
	if err != nil {
		t.Fatalf("ResolveScoped: %v", err)
	}
	if source != "room" {
		t.Errorf("source = %q, want %q", source, "room")
	}
	if content != "room owasp skill" {
		t.Errorf("content = %q, want %q", content, "room owasp skill")
	}
}

// TestListEmbeddedSkill_FansOutFiles locks in the Phase-5 contract:
// ListEmbedded("skill") returns one entry per file under skills/<name>/
// (SKILL.md + each reference), not just skill names. commands.Plan relies
// on this fan-out to emit per-file diffs.
func TestListEmbeddedSkill_FansOutFiles(t *testing.T) {
	r := NewResolver(t.TempDir())
	names, err := r.ListEmbedded("skill")
	if err != nil {
		t.Fatalf("ListEmbedded: %v", err)
	}
	want := []string{
		"startup-analyst/SKILL.md",
		"startup-analyst/references/capex-opex.md",
		"startup-analyst/references/competitive-landscape.md",
		"startup-analyst/references/funding-sources.md",
		"startup-analyst/references/reality-validation.md",
		"startup-analyst/references/strategic-partnerships.md",
	}
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("ListEmbedded(\"skill\") missing %q; got %v", w, names)
		}
	}
}

// TestEmbeddedContent_NestedSkillForm ensures "skill:<name>/<relpath>"
// resolves to the per-file embedded bytes (SKILL.md or a reference),
// while "skill:<name>" still resolves to the directory-form SKILL.md.
func TestEmbeddedContent_NestedSkillForm(t *testing.T) {
	r := NewResolver(t.TempDir())

	bodyDir, err := r.EmbeddedContent("skill:startup-analyst")
	if err != nil {
		t.Fatalf("directory form: %v", err)
	}
	bodyMD, err := r.EmbeddedContent("skill:startup-analyst/SKILL.md")
	if err != nil {
		t.Fatalf("nested SKILL.md: %v", err)
	}
	if bodyDir != bodyMD {
		t.Errorf("directory form and nested SKILL.md should resolve identically")
	}
	ref, err := r.EmbeddedContent("skill:startup-analyst/references/capex-opex.md")
	if err != nil {
		t.Fatalf("nested reference: %v", err)
	}
	if len(ref) == 0 {
		t.Errorf("nested reference body is empty")
	}
	// Path-traversal guard: ".." rejected.
	if _, err := r.EmbeddedContent("skill:startup-analyst/../workflow.md"); err == nil {
		t.Error("expected error for traversal identifier, got nil")
	}
}

// TestVaultPath_NestedSkillForm proves the nested identifier round-trips
// through VaultPath to the filesystem location a vault override would
// use.
func TestVaultPath_NestedSkillForm(t *testing.T) {
	root := t.TempDir()
	r := NewResolver(root)
	got, err := r.VaultPath("skill:startup-analyst/references/capex-opex.md")
	if err != nil {
		t.Fatalf("VaultPath: %v", err)
	}
	want := filepath.Join(root, "Templates", "skills", "startup-analyst", "references", "capex-opex.md")
	if got != want {
		t.Errorf("VaultPath = %q, want %q", got, want)
	}
}
