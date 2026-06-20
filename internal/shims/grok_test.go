// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTargetKindStringGrok(t *testing.T) {
	if got := GrokSkill.String(); got != "grok-skill" {
		t.Errorf("GrokSkill.String() = %q, want grok-skill", got)
	}
}

func TestTargetFileGrokPaths(t *testing.T) {
	root := "/proj"
	// vps-* skill gets its own subdir.
	if got := TargetFile(GrokSkill, root, "pairing"); got != filepath.Join(root, ".grok/skills", "vps-pairing", "SKILL.md") {
		t.Errorf("GrokSkill vps file = %q", got)
	}
	// The hub uses the literal "vpc" dir (no vps- prefix).
	if got := TargetFile(GrokSkill, root, "vpc"); got != filepath.Join(root, ".grok/skills", "vpc", "SKILL.md") {
		t.Errorf("GrokSkill hub file = %q", got)
	}
	if got := TargetDir(GrokSkill, root, "vpc"); got != filepath.Join(root, ".grok/skills", "vpc") {
		t.Errorf("GrokSkill hub dir = %q", got)
	}
}

func TestRenderGrokSkillGolden(t *testing.T) {
	out := RenderSkill(GrokSkill, sampleItem())
	mustContain := []string{
		"---\n",
		"name: vps-pairing\n",
		"description: Pair-programming persona\n",
		"metadata:\n",
		"  short-description: \"Vibe-palace skill: pairing\"\n",
		"---\n\n",
		"<!-- vibe-palace:shim v=1 sha=",
		" -->\n",
		"Call `vp_skill` with `name=\"pairing\"`",
		"stack\nadditively",
		"`vps-clear` drops all",
		"If MCP tools are not available",
		"/vault/Templates/skills/pairing/SKILL.md",
		shimCloseDelim,
	}
	for _, m := range mustContain {
		if !strings.Contains(out, m) {
			t.Errorf("GrokSkill render missing %q in:\n%s", m, out)
		}
	}
	// Must NOT carry the Claude-only frontmatter keys.
	for _, banned := range []string{"user-invocable", "argument-hint"} {
		if strings.Contains(out, banned) {
			t.Errorf("GrokSkill render unexpectedly contains %q:\n%s", banned, out)
		}
	}
}

func TestRenderGrokSkillEmptyVaultFallback(t *testing.T) {
	it := skillItem("focus", "Focus persona")
	it.VaultPath = ""
	out := RenderSkill(GrokSkill, it)
	if !strings.Contains(out, "{vault}/Templates/skills/focus/SKILL.md") {
		t.Errorf("missing default vault fallback when VaultPath empty:\n%s", out)
	}
}

func TestRenderGrokSkillDeterministic(t *testing.T) {
	it := sampleItem()
	first := RenderSkill(GrokSkill, it)
	second := RenderSkill(GrokSkill, it)
	if first != second {
		t.Error("renderGrokSkill not deterministic")
	}
}

func TestRenderedGrokShaMatchesExpected(t *testing.T) {
	it := sampleItem()
	expected := ExpectedSkillSha(GrokSkill, it)
	if !strings.Contains(RenderSkill(GrokSkill, it), "sha="+expected+" ") {
		t.Errorf("GrokSkill rendered sha %q not in output", expected)
	}
	// VaultPath must key the GrokSkill hash (vault fallback is in the bytes).
	it2 := it
	it2.VaultPath = "/different"
	if ExpectedSkillSha(GrokSkill, it2) == expected {
		t.Error("VaultPath change did not alter GrokSkill hash")
	}
}

func TestRenderGrokHubGolden(t *testing.T) {
	out := RenderSkill(GrokSkill, GrokHubItem())
	mustContain := []string{
		"name: vpc\n",
		"  short-description: \"Vibe-palace command hub (/vpc)\"\n",
		"<!-- vibe-palace:shim v=1 sha=",
		// Naked listing via empty input, no project.
		"empty input `{}`",
		"Do NOT pass `project`",
		// Dispatch.
		"`vp_cmd` with `name=\"<cmd>\"`",
		"follow the returned\ninstructions verbatim",
		// The task-reading discipline — the whole point.
		"review-plan",
		"cancel-plan",
		"execute-plan",
		"call `vp_get_task`",
		"NEVER grep or scan the filesystem for task files",
		"repo-relative\n`tasks/` path",
		// Large-body paging via the always-callable read tool.
		"include_content=false",
		"`vp_read_resource(uri, offset, limit)`",
		"`offset+length`",
		"until `eof`",
		// Session start passes slim=true; resume excerpt + resume_uri.
		"call `vp_bootstrap_context`",
		"with `slim=true`",
		"`resume_uri`",
		// Schema-loading guidance.
		"load its schema first",
		shimCloseDelim,
	}
	for _, m := range mustContain {
		if !strings.Contains(out, m) {
			t.Errorf("GrokHub render missing %q in:\n%s", m, out)
		}
	}
	// The hub must not hardcode a project anywhere, nor the Claude-only keys.
	for _, banned := range []string{"user-invocable", "argument-hint", "grok-first-class-citizen"} {
		if strings.Contains(out, banned) {
			t.Errorf("GrokHub render unexpectedly contains %q", banned)
		}
	}
}

func TestRenderGrokHubDeterministic(t *testing.T) {
	first := RenderSkill(GrokSkill, GrokHubItem())
	second := RenderSkill(GrokSkill, GrokHubItem())
	if first != second {
		t.Error("renderGrokHub not deterministic")
	}
}

func TestPlanSkillsGrokAllNew(t *testing.T) {
	root := t.TempDir()
	items := []SkillItem{skillItem("pairing", "a"), skillItem("focus", "b")}
	changes, err := PlanSkills(GrokSkill, items, root)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("got %d changes, want 2", len(changes))
	}
	for _, c := range changes {
		if c.Kind != New || c.Target != GrokSkill {
			t.Errorf("%s: Kind=%s Target=%s", c.Name, c.Kind, c.Target)
		}
	}
}

func TestApplySkillsGrokRoundTrip(t *testing.T) {
	root := t.TempDir()
	items := []SkillItem{skillItem("pairing", "Pair"), skillItem("focus", "Focus")}

	// New → files created at .grok/skills/vps-<n>/SKILL.md.
	changes, err := PlanSkills(GrokSkill, items, root)
	if err != nil {
		t.Fatal(err)
	}
	rep, _, err := ApplySkills(changes, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Added != 2 {
		t.Errorf("Added = %d, want 2", rep.Added)
	}
	for _, it := range items {
		p := TargetFile(GrokSkill, root, it.Name)
		want := filepath.Join(root, ".grok/skills", SkillDirName(it.Name), "SKILL.md")
		if p != want {
			t.Errorf("path = %q, want %q", p, want)
		}
		scan, err := ScanShim(p)
		if err != nil || !scan.HasMarker {
			t.Errorf("%s missing marker: %v", p, err)
		}
	}

	// Idempotent re-plan → all Unchanged.
	changes2, err := PlanSkills(GrokSkill, items, root)
	if err != nil {
		t.Fatal(err)
	}
	rep2, _, err := ApplySkills(changes2, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Added != 0 || rep2.Updated != 0 || rep2.Unchanged != 2 {
		t.Errorf("re-plan not idempotent: %+v", rep2)
	}

	// Remove "focus" from items + AllowStaleRemoval → Stale removed + dir pruned.
	smaller := []SkillItem{skillItem("pairing", "Pair")}
	changes3, err := PlanSkills(GrokSkill, smaller, root)
	if err != nil {
		t.Fatal(err)
	}
	rep3, _, err := ApplySkills(changes3, ApplyOptions{AllowStaleRemoval: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep3.Removed != 1 {
		t.Errorf("Removed = %d, want 1", rep3.Removed)
	}
	prunedDir := filepath.Join(root, ".grok/skills", SkillDirName("focus"))
	if _, err := os.Stat(prunedDir); !os.IsNotExist(err) {
		t.Errorf("stale vps-focus/ dir not pruned: %v", err)
	}
}

func TestPlanGrokHubRoundTrip(t *testing.T) {
	root := t.TempDir()
	hubPath := TargetFile(GrokSkill, root, GrokHubName)

	// New.
	ch, err := PlanGrokHub(root)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Kind != New || ch.Path != hubPath {
		t.Fatalf("first plan: Kind=%s Path=%s", ch.Kind, ch.Path)
	}
	if _, _, err := ApplySkills([]SkillChange{ch}, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(hubPath); err != nil {
		t.Fatalf("hub SKILL.md not created: %v", err)
	}

	// Unchanged on re-plan.
	ch2, err := PlanGrokHub(root)
	if err != nil {
		t.Fatal(err)
	}
	if ch2.Kind != UnchangedChange {
		t.Errorf("re-plan Kind = %s, want Unchanged", ch2.Kind)
	}

	// Tamper the sha in the marker → Modified.
	data, err := os.ReadFile(hubPath)
	if err != nil {
		t.Fatal(err)
	}
	realSha := ExpectedSkillSha(GrokSkill, GrokHubItem())
	tampered := strings.Replace(string(data), "sha="+realSha, "sha=deadbee", 1)
	if tampered == string(data) {
		t.Fatalf("could not find sha token %q to tamper in:\n%s", realSha, data)
	}
	if err := os.WriteFile(hubPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	ch3, err := PlanGrokHub(root)
	if err != nil {
		t.Fatal(err)
	}
	if ch3.Kind != Modified {
		t.Errorf("tampered Kind = %s, want Modified", ch3.Kind)
	}
}

func TestPlanGrokHubCustomWhenMarkerless(t *testing.T) {
	root := t.TempDir()
	hubPath := TargetFile(GrokSkill, root, GrokHubName)
	if err := os.MkdirAll(filepath.Dir(hubPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hubPath, []byte("# my own hub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch, err := PlanGrokHub(root)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Kind != CustomChange {
		t.Errorf("markerless hub Kind = %s, want Custom", ch.Kind)
	}
}

func TestPlanSkillsGrokIgnoresHubDir(t *testing.T) {
	// A vpc/ hub dir present alongside vps-* skills must not be reported by
	// PlanSkills (it lacks the vps- prefix) — so it is never flagged Stale.
	root := t.TempDir()
	hub, err := PlanGrokHub(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApplySkills([]SkillChange{hub}, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	changes, err := PlanSkills(GrokSkill, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		if c.Name == GrokHubName || strings.Contains(c.Path, filepath.Join("skills", "vpc")) {
			t.Errorf("PlanSkills surfaced the hub: %+v", c)
		}
	}
}
