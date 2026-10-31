// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderCodexCommandSkillWritesImplicitOptOut(t *testing.T) {
	item := skillItem("vpc-wrap", "Wrap the current work")
	rendered := RenderSkill(CodexSkill, item)
	if !strings.Contains(rendered, "$vpc-wrap") || !strings.Contains(rendered, "vp_cmd` with `name=\"wrap\"") {
		t.Fatalf("command skill lacks explicit invocation dispatch:\n%s", rendered)
	}
	if got := CodexSkillPolicy(); got != "policy:\n  allow_implicit_invocation: false\n" {
		t.Fatalf("Codex policy = %q", got)
	}
}

func TestRenderCodexPersonaSkillWritesImplicitOptOut(t *testing.T) {
	rendered := RenderSkill(CodexSkill, skillItem("vps-chair", "You chair the review"))
	if !strings.Contains(rendered, "$vps-chair") || !strings.Contains(rendered, "vp_skill` with `name=\"chair\"") {
		t.Fatalf("persona skill lacks explicit invocation dispatch:\n%s", rendered)
	}
	if !strings.Contains(CodexSkillPolicy(), "allow_implicit_invocation: false") {
		t.Fatal("Codex policy does not disable implicit invocation")
	}
}

func TestRenderEveryCodexSkillHasFalseImplicitPolicy(t *testing.T) {
	root := t.TempDir()
	items := []SkillItem{skillItem("vpc-wrap", "Wrap"), skillItem("vps-chair", "Chair")}
	changes, err := PlanSkills(CodexSkill, items, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApplySkills(changes, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		policyPath := filepath.Join(root, CodexSkillsDir, item.Name, "agents", "openai.yaml")
		policy, err := os.ReadFile(policyPath)
		if err != nil {
			t.Fatalf("%s policy missing: %v", item.Name, err)
		}
		if string(policy) != CodexSkillPolicy() || !strings.Contains(string(policy), "allow_implicit_invocation: false") {
			t.Fatalf("%s has unsafe implicit policy: %q", item.Name, policy)
		}
	}
}

func TestRenderCodexSkillKeepsThinLabelAndMCPDispatch(t *testing.T) {
	rendered := RenderSkill(CodexSkill, skillItem("vpc-restart", "Restore context"))
	if !strings.Contains(rendered, "description: \"Vibe-palace skill — Restore context\"") {
		t.Fatalf("short skill label missing:\n%s", rendered)
	}
	if strings.Contains(strings.ToLower(rendered), "automatically") || strings.Contains(rendered, "match this skill when") {
		t.Fatalf("description became implicit trigger text:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Call `vp_cmd` with `name=\"restart\"") {
		t.Fatalf("managed MCP dispatch missing:\n%s", rendered)
	}
}

func TestPlanCodexSkillsPreservesUnmarkedVPCustomSkill(t *testing.T) {
	root := t.TempDir()
	path := TargetFile(CodexSkill, root, "vpc-local")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("---\nname: vpc-local\n---\noperator-owned\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := PlanSkills(CodexSkill, []SkillItem{skillItem("vpc-local", "Local")}, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApplySkills(changes, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("unmarked custom skill was changed:\n%s", got)
	}
}

func TestApplyCodexSkillsIsByteIdempotent(t *testing.T) {
	root := t.TempDir()
	items := []SkillItem{skillItem("vpc-wrap", "Wrap"), skillItem("vps-chair", "Chair")}
	firstPlan, err := PlanSkills(CodexSkill, items, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApplySkills(firstPlan, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, item := range items {
		for _, rel := range []string{"SKILL.md", "agents/openai.yaml"} {
			path := filepath.Join(root, CodexSkillsDir, item.Name, rel)
			before[path], err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	secondPlan, err := PlanSkills(CodexSkill, items, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range secondPlan {
		if change.Kind != UnchangedChange {
			t.Fatalf("second plan has %s for %s", change.Kind, change.Path)
		}
	}
	if _, _, err := ApplySkills(secondPlan, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	for path, want := range before {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("second apply changed %s: err=%v", path, err)
		}
	}
}

func TestRemoveCodexSkillsLeavesUnmarkedGlobalSiblings(t *testing.T) {
	root := t.TempDir()
	managed := skillItem("vpc-retired", "Removed")
	managedPath := TargetFile(CodexSkill, root, managed.Name)
	if _, err := WireSkill(managedPath, CodexSkill, managed); err != nil {
		t.Fatal(err)
	}
	customPath := TargetFile(CodexSkill, root, "vpc-custom")
	if err := os.MkdirAll(filepath.Dir(customPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(customPath, []byte("operator custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveCodexUserSkills(filepath.Join(root, CodexSkillsDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(managedPath)); !os.IsNotExist(err) {
		t.Fatalf("stale managed skill directory remains (err=%v)", err)
	}
	got, err := os.ReadFile(customPath)
	if err != nil || string(got) != "operator custom" {
		t.Fatalf("unmarked sibling was removed or changed: %q err=%v", got, err)
	}
}
