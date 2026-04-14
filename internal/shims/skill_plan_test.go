// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/skills"
)

func skillItem(name, desc string, paths ...string) SkillItem {
	return SkillItem{
		Name: name,
		Frontmatter: skills.SkillFrontmatter{
			Name:        name,
			Description: desc,
			Paths:       paths,
			Lifetime:    "postural",
		},
		VaultPath: "/vault/Templates/skills/" + name + "/SKILL.md",
	}
}

func TestPlanSkillsUnsupportedTarget(t *testing.T) {
	if _, err := PlanSkills(ClaudeCommand, nil, t.TempDir()); err == nil {
		t.Error("expected error for ClaudeCommand target")
	}
}

func TestPlanSkillsAllNewOnEmptyProject(t *testing.T) {
	root := t.TempDir()
	items := []SkillItem{skillItem("pairing", "a"), skillItem("focus", "b")}
	for _, tgt := range []TargetKind{ClaudeSkill, CursorRule} {
		changes, err := PlanSkills(tgt, items, root)
		if err != nil {
			t.Fatalf("%s: %v", tgt, err)
		}
		if len(changes) != 2 {
			t.Fatalf("%s: got %d changes, want 2", tgt, len(changes))
		}
		for _, c := range changes {
			if c.Kind != New {
				t.Errorf("%s/%s: Kind = %s, want New", tgt, c.Name, c.Kind)
			}
			if c.Target != tgt {
				t.Errorf("%s/%s: Target = %s", tgt, c.Name, c.Target)
			}
		}
	}
}

func TestApplySkillsRoundTripClaudeSkill(t *testing.T) {
	root := t.TempDir()
	items := []SkillItem{skillItem("pairing", "Pair programming"), skillItem("focus", "Focus mode")}
	changes, err := PlanSkills(ClaudeSkill, items, root)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	rep, _, err := ApplySkills(changes, ApplyOptions{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if rep.Added != 2 {
		t.Errorf("Added = %d, want 2", rep.Added)
	}
	// Verify files landed with markers.
	for _, it := range items {
		p := TargetFile(ClaudeSkill, root, it.Name)
		scan, err := ScanShim(p)
		if err != nil {
			t.Fatalf("scan %s: %v", p, err)
		}
		if !scan.HasMarker {
			t.Errorf("%s has no marker", p)
		}
		if scan.Sha != ExpectedSkillSha(ClaudeSkill, it) {
			t.Errorf("%s sha mismatch", p)
		}
	}
	// Second pass is all Unchanged.
	changes2, err := PlanSkills(ClaudeSkill, items, root)
	if err != nil {
		t.Fatal(err)
	}
	rep2, _, err := ApplySkills(changes2, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Added != 0 || rep2.Updated != 0 {
		t.Errorf("second pass wrote: %+v", rep2)
	}
	if rep2.Unchanged != 2 {
		t.Errorf("Unchanged = %d, want 2", rep2.Unchanged)
	}
}

func TestPlanSkillsClassifiesMixedState(t *testing.T) {
	root := t.TempDir()
	// Seed: pairing exists up-to-date; focus exists stale; retired exists
	// but no longer desired; mine is a custom subdir with SKILL.md lacking
	// our marker.
	good := skillItem("pairing", "current")
	stale := skillItem("focus", "OLD")
	retired := skillItem("retired", "old")

	for _, it := range []SkillItem{good, stale, retired} {
		p := TargetFile(ClaudeSkill, root, it.Name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(RenderSkill(ClaudeSkill, it)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Custom: a vps-mine/ dir with a SKILL.md we did not write.
	minePath := filepath.Join(root, ClaudeSkillsDir, SkillDirName("mine"), "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(minePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(minePath, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	desired := []SkillItem{
		skillItem("pairing", "current"), // unchanged
		skillItem("focus", "NEW"),       // modified
		skillItem("doctor", "d"),        // new
	}
	changes, err := PlanSkills(ClaudeSkill, desired, root)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	byName := map[string]SkillChange{}
	var customs []SkillChange
	for _, c := range changes {
		if c.Kind == CustomChange {
			customs = append(customs, c)
			continue
		}
		byName[c.Name] = c
	}
	if k := byName["pairing"].Kind; k != UnchangedChange {
		t.Errorf("pairing = %s, want Unchanged", k)
	}
	if k := byName["focus"].Kind; k != Modified {
		t.Errorf("focus = %s, want Modified", k)
	}
	if byName["focus"].PrevSha != ExpectedSkillSha(ClaudeSkill, stale) {
		t.Errorf("focus PrevSha mismatch")
	}
	if k := byName["doctor"].Kind; k != New {
		t.Errorf("doctor = %s, want New", k)
	}
	if k := byName["retired"].Kind; k != Stale {
		t.Errorf("retired = %s, want Stale", k)
	}
	if len(customs) != 1 {
		t.Fatalf("custom count = %d, want 1", len(customs))
	}
	if customs[0].Path != minePath {
		t.Errorf("custom path = %s, want %s", customs[0].Path, minePath)
	}
}

func TestPlanSkillsCursorRuleCustomAndStale(t *testing.T) {
	root := t.TempDir()
	// Seed a marker-bearing rule for "retired" and a custom rule "mine".
	retired := skillItem("retired", "gone")
	retiredPath := TargetFile(CursorRule, root, retired.Name)
	if err := os.MkdirAll(filepath.Dir(retiredPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retiredPath, []byte(RenderSkill(CursorRule, retired)), 0o644); err != nil {
		t.Fatal(err)
	}
	minePath := filepath.Join(root, CursorRulesDir, "vps-mine.mdc")
	if err := os.WriteFile(minePath, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Non-vps file (other tools' territory) must be ignored entirely.
	if err := os.WriteFile(filepath.Join(root, CursorRulesDir, "other.mdc"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	desired := []SkillItem{skillItem("pairing", "pp")}
	changes, err := PlanSkills(CursorRule, desired, root)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	kinds := map[ChangeKind]int{}
	for _, c := range changes {
		kinds[c.Kind]++
	}
	if kinds[New] != 1 || kinds[Stale] != 1 || kinds[CustomChange] != 1 {
		t.Errorf("unexpected kinds: %+v", kinds)
	}
}

func TestApplySkillsStaleRemovalClaudeSkillPrunesDir(t *testing.T) {
	root := t.TempDir()
	retired := skillItem("retired", "gone")
	p := TargetFile(ClaudeSkill, root, retired.Name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(RenderSkill(ClaudeSkill, retired)), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := PlanSkills(ClaudeSkill, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	rep, _, err := ApplySkills(changes, ApplyOptions{AllowStaleRemoval: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Removed != 1 {
		t.Errorf("Removed = %d, want 1", rep.Removed)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("SKILL.md still present: %v", err)
	}
	parent := filepath.Dir(p)
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Errorf("parent vps-*/ dir not pruned: %v", err)
	}
}

func TestApplySkillsStaleDefaultRetains(t *testing.T) {
	root := t.TempDir()
	retired := skillItem("retired", "gone")
	p := TargetFile(CursorRule, root, retired.Name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(RenderSkill(CursorRule, retired)), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := PlanSkills(CursorRule, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	rep, _, err := ApplySkills(changes, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Stale != 1 {
		t.Errorf("Stale = %d, want 1", rep.Stale)
	}
	if rep.Removed != 0 {
		t.Errorf("Removed = %d, want 0 without opt-in", rep.Removed)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("retired rule disappeared without consent: %v", err)
	}
}

func TestApplySkillsLeavesCustomAlone(t *testing.T) {
	root := t.TempDir()
	// Custom .mdc at vps-mine.mdc (wrong-prefix files are ignored; same
	// prefix without marker must be left alone).
	minePath := filepath.Join(root, CursorRulesDir, "vps-mine.mdc")
	if err := os.MkdirAll(filepath.Dir(minePath), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := "# mine\n"
	if err := os.WriteFile(minePath, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := PlanSkills(CursorRule, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApplySkills(changes, ApplyOptions{AllowStaleRemoval: true}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(minePath)
	if string(data) != orig {
		t.Errorf("custom file modified:\nbefore=%q\nafter =%q", orig, data)
	}
}

func TestWireSkillAddUpdateUnchangedCustom(t *testing.T) {
	root := t.TempDir()
	it := skillItem("pairing", "v1")
	p := TargetFile(ClaudeSkill, root, it.Name)

	// Add.
	res, err := WireSkill(p, ClaudeSkill, it)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if res.Kind != Added {
		t.Errorf("add Kind = %v, want Added", res.Kind)
	}

	// Unchanged.
	res, err = WireSkill(p, ClaudeSkill, it)
	if err != nil {
		t.Fatalf("unchanged: %v", err)
	}
	if res.Kind != Unchanged {
		t.Errorf("Kind = %v, want Unchanged", res.Kind)
	}

	// Update.
	it2 := skillItem("pairing", "v2")
	res, err = WireSkill(p, ClaudeSkill, it2)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if res.Kind != Updated {
		t.Errorf("Kind = %v, want Updated", res.Kind)
	}

	// Custom: overwrite file with marker-less content; next Wire leaves
	// it alone.
	if err := os.WriteFile(p, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = WireSkill(p, ClaudeSkill, it2)
	if err != nil {
		t.Fatalf("custom: %v", err)
	}
	if res.Kind != Custom {
		t.Errorf("Kind = %v, want Custom", res.Kind)
	}
	data, _ := os.ReadFile(p)
	if !strings.Contains(string(data), "# mine") {
		t.Error("custom content was overwritten")
	}
}

func TestPlanSkillsSortOrder(t *testing.T) {
	root := t.TempDir()
	// Populate one of each kind for CursorRule:
	u := skillItem("u", "same")
	m := skillItem("m", "old")
	s := skillItem("s", "x")
	for _, it := range []SkillItem{u, m, s} {
		p := TargetFile(CursorRule, root, it.Name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(RenderSkill(CursorRule, it)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, CursorRulesDir, "vps-c.mdc"), []byte("# custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	desired := []SkillItem{
		skillItem("u", "same"),
		skillItem("m", "new"),
		skillItem("n", "brief"),
	}
	changes, err := PlanSkills(CursorRule, desired, root)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []ChangeKind{New, Modified, UnchangedChange, Stale, CustomChange}
	if len(changes) != len(wantOrder) {
		t.Fatalf("len = %d, want %d: %+v", len(changes), len(wantOrder), changes)
	}
	for i, c := range changes {
		if c.Kind != wantOrder[i] {
			t.Errorf("changes[%d] = %s, want %s", i, c.Kind, wantOrder[i])
		}
	}
}

func TestScanShimDetectsDriftOnSkillTargets(t *testing.T) {
	root := t.TempDir()
	it := skillItem("pairing", "v1")
	p := TargetFile(ClaudeSkill, root, it.Name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(RenderSkill(ClaudeSkill, it)), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err := ScanShim(p)
	if err != nil {
		t.Fatal(err)
	}
	if !scan.HasMarker {
		t.Error("HasMarker = false on a rendered ClaudeSkill")
	}
	if scan.Version != skillShimVersion {
		t.Errorf("Version = %d, want %d", scan.Version, skillShimVersion)
	}
	if scan.Sha != ExpectedSkillSha(ClaudeSkill, it) {
		t.Errorf("Sha mismatch")
	}
}
