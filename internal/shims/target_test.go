// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/skills"
)

func sampleItem() SkillItem {
	return SkillItem{
		Name: "pairing",
		Frontmatter: skills.SkillFrontmatter{
			Name:        "pairing",
			Description: "Pair-programming persona",
			Triggers:    []string{"pair", "mob"},
			Paths:       []string{"**/*.go", "internal/**"},
			Lifetime:    "postural",
		},
		VaultPath: "/vault/Templates/skills/pairing/SKILL.md",
	}
}

func TestTargetKindString(t *testing.T) {
	cases := map[TargetKind]string{
		ClaudeCommand: "claude-command",
		ClaudeSkill:   "claude-skill",
		CursorRule:    "cursor-rule",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("%d: got %q want %q", k, got, want)
		}
	}
}

func TestTargetFilePaths(t *testing.T) {
	root := "/proj"
	cases := []struct {
		kind TargetKind
		name string
		want string
	}{
		{ClaudeCommand, "restart", filepath.Join(root, ".claude/commands", "vpc-restart.md")},
		{ClaudeSkill, "pairing", filepath.Join(root, ".claude/skills", "vps-pairing", "SKILL.md")},
		{CursorRule, "pairing", filepath.Join(root, ".cursor/rules", "vps-pairing.mdc")},
	}
	for _, c := range cases {
		if got := TargetFile(c.kind, root, c.name); got != c.want {
			t.Errorf("TargetFile(%s,%s) = %q, want %q", c.kind, c.name, got, c.want)
		}
	}
}

func TestSkillDirName(t *testing.T) {
	if got := SkillDirName("foo"); got != "vps-foo" {
		t.Errorf("SkillDirName(foo) = %q, want vps-foo", got)
	}
	if got := CursorRuleFilename("foo"); got != "vps-foo.mdc" {
		t.Errorf("CursorRuleFilename(foo) = %q, want vps-foo.mdc", got)
	}
}

func TestRenderClaudeSkillGolden(t *testing.T) {
	out := RenderSkill(ClaudeSkill, sampleItem())
	mustContain := []string{
		"---\n",
		"name: vps-pairing\n",
		"description: Pair-programming persona\n",
		"---\n\n",
		"<!-- vibe-palace:shim v=1 sha=",
		" -->\n",
		"Call `vp_skill` with `name=\"pairing\"`",
		"stack\nadditively",
		"`vps-clear` drops all",
		shimCloseDelim,
	}
	for _, m := range mustContain {
		if !strings.Contains(out, m) {
			t.Errorf("ClaudeSkill render missing %q in:\n%s", m, out)
		}
	}
}

func TestRenderCursorRuleGolden(t *testing.T) {
	out := RenderSkill(CursorRule, sampleItem())
	mustContain := []string{
		"---\n",
		"description: Pair-programming persona\n",
		"globs: [\"**/*.go\", \"internal/**\"]\n",
		"alwaysApply: false\n",
		"---\n\n",
		"<!-- vibe-palace:shim v=1 sha=",
		"Call `vp_skill` with `name=\"pairing\"`",
		"If MCP tools are not available",
		"/vault/Templates/skills/pairing/SKILL.md",
		shimCloseDelim,
	}
	for _, m := range mustContain {
		if !strings.Contains(out, m) {
			t.Errorf("CursorRule render missing %q in:\n%s", m, out)
		}
	}
}

func TestRenderCursorRuleEmptyPathsAndVault(t *testing.T) {
	it := SkillItem{
		Name: "focus",
		Frontmatter: skills.SkillFrontmatter{
			Description: "Focus persona",
			Lifetime:    "postural",
		},
	}
	out := RenderSkill(CursorRule, it)
	if !strings.Contains(out, "globs: []\n") {
		t.Errorf("empty Paths should render [], got:\n%s", out)
	}
	if !strings.Contains(out, "{vault}/Templates/skills/focus/SKILL.md") {
		t.Errorf("missing fallback placeholder when VaultPath empty:\n%s", out)
	}
}

func TestRenderSkillDeterministic(t *testing.T) {
	it := sampleItem()
	for _, kind := range []TargetKind{ClaudeSkill, CursorRule} {
		a := RenderSkill(kind, it)
		b := RenderSkill(kind, it)
		if a != b {
			t.Errorf("%s: not deterministic", kind)
		}
	}
}

func TestExpectedSkillShaStable(t *testing.T) {
	it := sampleItem()
	a := ExpectedSkillSha(ClaudeSkill, it)
	b := ExpectedSkillSha(ClaudeSkill, it)
	if a != b || len(a) != 7 {
		t.Errorf("sha unstable or wrong length: a=%q b=%q", a, b)
	}
	// Changing description must change hash.
	it2 := it
	it2.Frontmatter.Description = "different"
	if ExpectedSkillSha(ClaudeSkill, it2) == a {
		t.Error("description change did not alter hash")
	}
	// Different targets must produce different hashes even on identical
	// items (prevents a skill shim file from pretending to be a cursor
	// shim and vice versa).
	if ExpectedSkillSha(CursorRule, it) == ExpectedSkillSha(ClaudeSkill, it) {
		t.Error("ClaudeSkill and CursorRule produced same sha")
	}
	// Changing vault path affects CursorRule but not ClaudeSkill.
	it3 := it
	it3.VaultPath = "/different"
	if ExpectedSkillSha(CursorRule, it3) == ExpectedSkillSha(CursorRule, it) {
		t.Error("VaultPath change did not alter CursorRule hash")
	}
	if ExpectedSkillSha(ClaudeSkill, it3) != ExpectedSkillSha(ClaudeSkill, it) {
		t.Error("VaultPath change altered ClaudeSkill hash (should not)")
	}
}

func TestRenderedShaMatchesExpected(t *testing.T) {
	it := sampleItem()
	for _, kind := range []TargetKind{ClaudeSkill, CursorRule} {
		expected := ExpectedSkillSha(kind, it)
		rendered := RenderSkill(kind, it)
		if !strings.Contains(rendered, "sha="+expected+" ") {
			t.Errorf("%s: rendered sha %q not in output:\n%s", kind, expected, rendered)
		}
	}
}

func TestRenderGlobsYAMLFlow(t *testing.T) {
	cases := map[string]string{
		"nil":      renderGlobsYAMLFlow(nil),
		"empty":    renderGlobsYAMLFlow([]string{}),
		"one":      renderGlobsYAMLFlow([]string{"a"}),
		"two":      renderGlobsYAMLFlow([]string{"a", "b"}),
		"quotes":   renderGlobsYAMLFlow([]string{"a\"b"}),
		"backslsh": renderGlobsYAMLFlow([]string{"a\\b"}),
	}
	want := map[string]string{
		"nil":      "[]",
		"empty":    "[]",
		"one":      "[\"a\"]",
		"two":      "[\"a\", \"b\"]",
		"quotes":   "[\"a\\\"b\"]",
		"backslsh": "[\"a\\\\b\"]",
	}
	for k, got := range cases {
		if got != want[k] {
			t.Errorf("%s: got %q want %q", k, got, want[k])
		}
	}
}

func TestTargetDirAndFileUnknownKind(t *testing.T) {
	// Defensive: unknown kinds yield empty string rather than a panic.
	if got := TargetDir(TargetKind(99), "/root", "x"); got != "" {
		t.Errorf("unknown kind TargetDir: got %q", got)
	}
	if got := TargetFile(TargetKind(99), "/root", "x"); got != "" {
		t.Errorf("unknown kind TargetFile: got %q", got)
	}
	if got := RenderSkill(TargetKind(99), SkillItem{}); got != "" {
		t.Errorf("unknown kind RenderSkill: got %q", got)
	}
}
