// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/shims"
	"github.com/suykerbuyk/vibe-palace/internal/skills"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// TestEmbeddedSkillPathsReachCursorGlobs closes the note_path trap for
// SkillFrontmatter.Paths: instead of hand-seeding the field, it drives a
// SHIPPED template end-to-end and proves the `paths:` declared in
// internal/templates/templates/skills/*/SKILL.md actually reach the
// rendered Cursor rule's `globs:` line. If any skill template that
// declares `paths:` regresses to an empty glob list, this fails.
func TestEmbeddedSkillPathsReachCursorGlobs(t *testing.T) {
	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded: %v", err)
	}
	var checkedPathBearing bool
	for _, r := range resources {
		// Only interested in top-level skill SKILL.md files.
		if !strings.HasPrefix(r.RelPath, "skills/") || !strings.HasSuffix(r.RelPath, "/SKILL.md") {
			continue
		}
		if strings.Count(r.RelPath, "/") != 2 {
			continue // skip references/*.md nested under the skill dir
		}
		fm, _, err := skills.Parse(r.Bytes)
		if err != nil {
			t.Fatalf("parse %s: %v", r.RelPath, err)
		}
		item := shims.SkillItem{Name: fm.Name, Frontmatter: fm}
		out := shims.RenderSkill(shims.CursorRule, item)
		if len(fm.Paths) == 0 {
			// A path-less skill is valid: it must still render, with an
			// empty glob list, never an error.
			if !strings.Contains(out, "globs: []\n") {
				t.Errorf("%s: path-less skill should render globs: [], got:\n%s", r.RelPath, out)
			}
			continue
		}
		checkedPathBearing = true
		if strings.Contains(out, "globs: []\n") {
			t.Errorf("%s: declares %d paths but rendered empty globs:\n%s", r.RelPath, len(fm.Paths), out)
		}
		for _, p := range fm.Paths {
			if !strings.Contains(out, p) {
				t.Errorf("%s: declared path %q absent from rendered globs:\n%s", r.RelPath, p, out)
			}
		}
	}
	if !checkedPathBearing {
		t.Error("no shipped skill template declares paths: — the populate half of the fix is unproven")
	}
}

// TestIntegrationSkillShimsLifecycle drives the Phase-4 skill-shim
// surfaces (ClaudeSkill + CursorRule) end-to-end against a fixture
// project. Because Phase 5 owns the `vp init` wiring, this test calls
// the shim planner/applier directly with a hand-built SkillItem list —
// which still exercises the full stack from the shim package down
// through file I/O and readback.
func TestIntegrationSkillShimsLifecycle(t *testing.T) {
	items := []shims.SkillItem{
		{
			Name: "pairing",
			Frontmatter: skills.SkillFrontmatter{
				Name:        "pairing",
				Description: "Pair-programming persona",
				Paths:       []string{"**/*.go"},
				Lifetime:    "postural",
			},
			VaultPath: "/vault/Templates/skills/pairing/SKILL.md",
		},
		{
			Name: "focus",
			Frontmatter: skills.SkillFrontmatter{
				Name:        "focus",
				Description: "Deep-focus persona",
				Lifetime:    "postural",
			},
			VaultPath: "/vault/Templates/skills/focus/SKILL.md",
		},
	}

	t.Run("claude_only", func(t *testing.T) {
		root := t.TempDir()
		// Simulate a Claude-only project: .claude/ exists, no .cursor/.
		if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if shims.CursorPresent(root) {
			t.Fatal("Cursor should not be detected for .claude-only project")
		}
		// Apply ClaudeSkill target only.
		plan, err := shims.PlanSkills(shims.ClaudeSkill, items, root)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if len(plan) != 2 {
			t.Fatalf("len(plan) = %d, want 2", len(plan))
		}
		rep, _, err := shims.ApplySkills(plan, shims.ApplyOptions{})
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if rep.Added != 2 {
			t.Errorf("Added = %d, want 2", rep.Added)
		}

		// Cursor rules dir must NOT have appeared.
		if _, err := os.Stat(filepath.Join(root, ".cursor", "rules")); !os.IsNotExist(err) {
			t.Errorf("cursor rules dir present in claude-only project: %v", err)
		}
		// Verify SKILL.md contents.
		skillPath := shims.TargetFile(shims.ClaudeSkill, root, "pairing")
		data, err := os.ReadFile(skillPath)
		if err != nil {
			t.Fatalf("read skill: %v", err)
		}
		if !strings.Contains(string(data), "name: vps-pairing") {
			t.Errorf("missing skill name in frontmatter:\n%s", data)
		}
		if !strings.Contains(string(data), "Pair-programming persona") {
			t.Errorf("missing description:\n%s", data)
		}

		// Second pass is all Unchanged.
		plan2, err := shims.PlanSkills(shims.ClaudeSkill, items, root)
		if err != nil {
			t.Fatal(err)
		}
		rep2, _, err := shims.ApplySkills(plan2, shims.ApplyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if rep2.Added != 0 || rep2.Updated != 0 {
			t.Errorf("second apply wrote files: %+v", rep2)
		}
		if rep2.Unchanged != 2 {
			t.Errorf("Unchanged = %d, want 2", rep2.Unchanged)
		}
	})

	t.Run("claude_and_cursor", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, ".cursor", "rules"), 0o755); err != nil {
			t.Fatal(err)
		}
		if !shims.CursorPresent(root) {
			t.Fatal("Cursor should be detected when .cursor/rules/ exists")
		}
		// Apply both targets.
		targets := []shims.TargetKind{shims.ClaudeSkill, shims.CursorRule}
		for _, tgt := range targets {
			plan, err := shims.PlanSkills(tgt, items, root)
			if err != nil {
				t.Fatalf("%s plan: %v", tgt, err)
			}
			rep, _, err := shims.ApplySkills(plan, shims.ApplyOptions{})
			if err != nil {
				t.Fatalf("%s apply: %v", tgt, err)
			}
			if rep.Added != 2 {
				t.Errorf("%s Added = %d, want 2", tgt, rep.Added)
			}
		}
		// Verify cursor rule on disk.
		rulePath := shims.TargetFile(shims.CursorRule, root, "pairing")
		data, err := os.ReadFile(rulePath)
		if err != nil {
			t.Fatalf("read rule: %v", err)
		}
		body := string(data)
		for _, want := range []string{
			"description: Pair-programming persona",
			"globs: [\"**/*.go\"]",
			"alwaysApply: false",
			"vp_skill",
			"/vault/Templates/skills/pairing/SKILL.md",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("cursor rule missing %q:\n%s", want, body)
			}
		}
		// Idempotent re-apply across both targets.
		for _, tgt := range targets {
			plan, err := shims.PlanSkills(tgt, items, root)
			if err != nil {
				t.Fatal(err)
			}
			rep, _, err := shims.ApplySkills(plan, shims.ApplyOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if rep.Added != 0 || rep.Updated != 0 {
				t.Errorf("%s second apply wrote: %+v", tgt, rep)
			}
			if rep.Unchanged != 2 {
				t.Errorf("%s Unchanged = %d, want 2", tgt, rep.Unchanged)
			}
		}
	})

	t.Run("cursorrules_flat_file_alone_does_not_trigger_cursor", func(t *testing.T) {
		// Strict rule: a flat `.cursorrules` file is not a signal for
		// emitting `.cursor/rules/` shims.
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".cursorrules"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if shims.CursorPresent(root) {
			t.Error(".cursorrules flat file should not trigger CursorPresent")
		}
	})
}
