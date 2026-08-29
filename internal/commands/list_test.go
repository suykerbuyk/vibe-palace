// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package commands_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// writeProjectCommand seeds a project-tier command file at
// <vault>/Projects/<slug>/commands/<name>.md.
func writeProjectCommand(t *testing.T, vault, slug, name, content string) {
	t.Helper()
	dir := filepath.Join(vault, "Projects", slug, "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAlias(t *testing.T) {
	if got := commands.Alias("restart"); got != "vpc-restart" {
		t.Fatalf("Alias: got %q, want %q", got, "vpc-restart")
	}
}

func TestExtractBrief(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"empty", "", 40, "(no description)"},
		{"only headings", "# Title\n## Sub\n", 40, "(no description)"},
		{"first body line", "# Title\n\nDo the thing.\n", 40, "Do the thing."},
		{"truncates at word", "# T\n" + strings.Repeat("word ", 20), 20, "word word word…"},
		// Skills (and a few commands) open with a YAML fence. The brief is
		// the first body sentence, never the opening "---".
		{"skill frontmatter", "---\nname: pair-reviewer\ndescription: >\n  Dual-agent pairing.\n---\n\n# Pair Reviewer\n\nYou sit in the **review chair**.\n", 60, "You sit in the **review chair**."},
		{"command frontmatter", "---\nargument-hint: <epic-slug>\n---\n# Tasks: Epic\n\nShow the subtree of tasks rooted at an epic.\n", 60, "Show the subtree of tasks rooted at an epic."},
		{"no frontmatter unchanged", "# Herdr\n\nHerdr is a terminal workspace manager.\n", 60, "Herdr is a terminal workspace manager."},
		// No closing fence: parseFrontmatter leaves the bytes intact, so the
		// brief is still the opening line. The body sentence must not win —
		// that would mean an unclosed fence was treated as frontmatter.
		{"unclosed fence is content", "---\nname: broken\n# Title\n\nBody after an unclosed fence.\n", 60, "---"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commands.ExtractBrief(tc.input, tc.maxLen)
			if tc.name == "truncates at word" {
				if !strings.HasSuffix(got, "…") || len(got) > tc.maxLen+3 {
					t.Fatalf("ExtractBrief truncation: got %q (len=%d)", got, len(got))
				}
				return
			}
			if got != tc.want {
				t.Fatalf("ExtractBrief: got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestList_EmbeddedCommands(t *testing.T) {
	// No vault → everything resolves via embedded tier.
	r := vpctx.NewResolver(t.TempDir())
	summaries, err := commands.List(r, "command", "", "", "", 60)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) == 0 {
		t.Fatal("expected at least one embedded command")
	}
	seen := map[string]bool{}
	for _, s := range summaries {
		seen[s.Name] = true
		if s.Source != "embedded" {
			t.Errorf("%s: source=%q, want embedded", s.Name, s.Source)
		}
		if s.Alias != "vpc-"+s.Name {
			t.Errorf("%s: alias=%q, want vpc-%s", s.Name, s.Alias, s.Name)
		}
		if s.Brief == "" {
			t.Errorf("%s: brief is empty", s.Name)
		}
		if s.Brief == "---" {
			t.Errorf("%s: brief is the YAML fence", s.Name)
		}
	}
	for _, want := range []string{"restart", "wrap", "capture", "stage", "herdr"} {
		if !seen[want] {
			t.Errorf("missing expected embedded command %q", want)
		}
	}
}

// TestList_EmbeddedSkillsBriefIsNotFence pins the skill manifest agents read
// after bootstrap: every embedded skill has a real brief, and pair-reviewer
// is among them. ExtractBrief used to return "---" (the opening YAML fence)
// for every SKILL.md, so the list looked empty of descriptions.
func TestList_EmbeddedSkillsBriefIsNotFence(t *testing.T) {
	r := vpctx.NewResolver(t.TempDir())
	summaries, err := commands.List(r, "skill", "", "", "", 60)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seen := map[string]bool{}
	for _, s := range summaries {
		seen[s.Name] = true
		if s.Source != "embedded" {
			t.Errorf("%s: source=%q, want embedded", s.Name, s.Source)
		}
		if s.Brief == "" || s.Brief == "---" {
			t.Errorf("%s: brief=%q, want a body sentence not the YAML fence", s.Name, s.Brief)
		}
	}
	for _, want := range []string{"chair", "code-digger", "epic-orchestrator", "pair-reviewer", "second-opinion", "startup-analyst"} {
		if !seen[want] {
			t.Errorf("missing expected embedded skill %q", want)
		}
	}
}

// TestList_ProjectScopedCommand verifies a project-tier command surfaces with
// Source="project", carries its slug in Project, lifts argument-hint from
// frontmatter, and extracts the Brief from the body (skipping the YAML fence).
func TestList_ProjectScopedCommand(t *testing.T) {
	vault := t.TempDir()
	writeProjectCommand(t, vault, "rezbldrvault", "res_build",
		"---\nargument-hint: [job file path] [--cover-letter]\n---\n\nGenerate a targeted resume.\n")
	r := vpctx.NewResolver(vault)

	summaries, err := commands.List(r, "command", "rezbldrvault", "", "", 60)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var got *commands.Summary
	for i := range summaries {
		if summaries[i].Name == "res_build" {
			got = &summaries[i]
		}
	}
	if got == nil {
		t.Fatal("project command res_build not listed")
	}
	if got.Source != "project" {
		t.Errorf("Source = %q, want project", got.Source)
	}
	if got.Project != "rezbldrvault" {
		t.Errorf("Project = %q, want rezbldrvault", got.Project)
	}
	if got.ArgHint != "[job file path] [--cover-letter]" {
		t.Errorf("ArgHint = %q", got.ArgHint)
	}
	if got.Brief != "Generate a targeted resume." {
		t.Errorf("Brief = %q, want the body line (fence must be skipped)", got.Brief)
	}
}

// TestList_GlobalCommandHasNoProject verifies global (embedded/vault) commands
// never carry a Project slug even when a project is in scope.
func TestList_GlobalCommandHasNoProject(t *testing.T) {
	vault := t.TempDir()
	writeProjectCommand(t, vault, "p", "res_build", "Body.\n")
	r := vpctx.NewResolver(vault)
	summaries, err := commands.List(r, "command", "p", "", "", 60)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, s := range summaries {
		if s.Name == "restart" { // embedded global
			if s.Project != "" {
				t.Errorf("global command restart carries Project=%q, want empty", s.Project)
			}
			if s.Source == "project" {
				t.Errorf("global command restart Source=project")
			}
		}
	}
}
