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
	writeFile(t, filepath.Join(root, "Projects", "myproj", "agentctx", "workflow.md"), "project")

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

	content, source, err := r.Resolve("command:vp-restart", "test-proj")
	if err != nil {
		t.Fatalf("Resolve(command:vp-restart): %v", err)
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

	writeFile(t, filepath.Join(root, "Templates", "commands", "vp-restart.md"), "custom restart")

	content, source, err := r.Resolve("command:vp-restart", "test-proj")
	if err != nil {
		t.Fatalf("Resolve(command:vp-restart): %v", err)
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

	// No embedded skills, so write one to vault.
	writeFile(t, filepath.Join(root, "Templates", "skills", "analyze.md"), "analyze skill")

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
		{"command:vp-restart", filepath.Join("commands", "vp-restart.md"), false},
		{"command:review-plan", filepath.Join("commands", "review-plan.md"), false},
		{"skill:analyze", filepath.Join("skills", "analyze.md"), false},
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
	// Embedded commands: vp-cancel-plan, vp-capture, vp-restart, vp-review-plan, vp-wrap (sorted).
	if len(resources) != 5 {
		t.Fatalf("got %d commands, want 5: %v", len(resources), resources)
	}
	wantNames := []string{"vp-cancel-plan", "vp-capture", "vp-restart", "vp-review-plan", "vp-wrap"}
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

	// Add a vault-level vp-restart override and a new vault command.
	writeFile(t, filepath.Join(root, "Templates", "commands", "vp-restart.md"), "override")
	writeFile(t, filepath.Join(root, "Templates", "commands", "deploy.md"), "deploy cmd")

	// Add a project-level command.
	writeFile(t, filepath.Join(root, "Projects", "proj", "agentctx", "commands", "custom.md"), "custom cmd")

	resources, err := r.ListResources("command", "proj")
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}

	// Expect: custom(project), deploy(vault), vp-cancel-plan(embedded), vp-capture(embedded),
	//         vp-restart(vault — shadows embedded), vp-review-plan(embedded), vp-wrap(embedded)
	if len(resources) != 7 {
		t.Fatalf("got %d resources, want 7: %v", len(resources), resources)
	}

	// Check vp-restart comes from vault (override), not embedded.
	for _, ri := range resources {
		if ri.Name == "vp-restart" && ri.Source != "vault" {
			t.Errorf("vp-restart source = %q, want %q (vault should shadow embedded)", ri.Source, "vault")
		}
		if ri.Name == "custom" && ri.Source != "project" {
			t.Errorf("custom source = %q, want %q", ri.Source, "project")
		}
	}
}

func TestListSkillsEmpty(t *testing.T) {
	r, _ := testResolver(t)

	resources, err := r.ListResources("skill", "")
	if err != nil {
		t.Fatalf("ListResources(skill): %v", err)
	}
	if len(resources) != 0 {
		t.Errorf("expected 0 skills (no embedded skills), got %d", len(resources))
	}
}

func TestListResourcesInvalidType(t *testing.T) {
	r, _ := testResolver(t)

	_, err := r.ListResources("bogus", "")
	if err == nil {
		t.Fatal("expected error for unsupported resource type, got nil")
	}
}
