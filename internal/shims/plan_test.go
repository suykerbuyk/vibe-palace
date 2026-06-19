// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
)

func sums(nb ...string) []commands.Summary {
	// nb is pairs: name, brief, name, brief, ...
	out := make([]commands.Summary, 0, len(nb)/2)
	for i := 0; i+1 < len(nb); i += 2 {
		out = append(out, commands.Summary{Name: nb[i], Brief: nb[i+1]})
	}
	return out
}

func TestPlanAllNewOnEmptyProject(t *testing.T) {
	root := t.TempDir()
	changes, err := Plan(sums("restart", "r", "wrap", "w"), root)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("len(changes) = %d, want 2", len(changes))
	}
	for _, c := range changes {
		if c.Kind != New {
			t.Errorf("%s: Kind = %s, want New", c.Name, c.Kind)
		}
	}
}

func TestPlanClassifiesMixedState(t *testing.T) {
	root := t.TempDir()
	cmdDir := filepath.Join(root, ShimDir)
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// restart exists and is up-to-date.
	restartPath := filepath.Join(cmdDir, Filename("restart"))
	if err := os.WriteFile(restartPath, []byte(Render("restart", "current", "", "")), 0o644); err != nil {
		t.Fatal(err)
	}
	// wrap exists but has a stale brief — should be Modified.
	wrapPath := filepath.Join(cmdDir, Filename("wrap"))
	if err := os.WriteFile(wrapPath, []byte(Render("wrap", "OLD BRIEF", "", "")), 0o644); err != nil {
		t.Fatal(err)
	}
	// doctor is in the desired set but no file yet — New.
	//
	// retired: a file we wrote previously for a command that no longer
	// exists — Stale.
	retiredPath := filepath.Join(cmdDir, Filename("retired"))
	if err := os.WriteFile(retiredPath, []byte(Render("retired", "gone", "", "")), 0o644); err != nil {
		t.Fatal(err)
	}
	// mine: a vpc-*.md without our marker — Custom.
	minePath := filepath.Join(cmdDir, Filename("mine"))
	if err := os.WriteFile(minePath, []byte("# my own\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// README.md — non-vpc file, should be ignored entirely.
	if err := os.WriteFile(filepath.Join(cmdDir, "README.md"), []byte("readme"), 0o644); err != nil {
		t.Fatal(err)
	}

	changes, err := Plan(sums("restart", "current", "wrap", "NEW BRIEF", "doctor", "d"), root)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	byName := map[string]Change{}
	var custom []Change
	for _, c := range changes {
		if c.Kind == CustomChange {
			custom = append(custom, c)
			continue
		}
		byName[c.Name] = c
	}

	if got := byName["restart"].Kind; got != UnchangedChange {
		t.Errorf("restart Kind = %s, want Unchanged", got)
	}
	if got := byName["wrap"].Kind; got != Modified {
		t.Errorf("wrap Kind = %s, want Modified", got)
	}
	if byName["wrap"].PrevSha != ExpectedSha("wrap", "OLD BRIEF", "", "") {
		t.Errorf("wrap PrevSha = %q, want old sha", byName["wrap"].PrevSha)
	}
	if got := byName["doctor"].Kind; got != New {
		t.Errorf("doctor Kind = %s, want New", got)
	}
	if got := byName["retired"].Kind; got != Stale {
		t.Errorf("retired Kind = %s, want Stale", got)
	}
	if len(custom) != 1 {
		t.Fatalf("custom count = %d, want 1", len(custom))
	}
	if custom[0].Path != minePath {
		t.Errorf("custom path = %s, want %s", custom[0].Path, minePath)
	}
}

func TestPlanSortOrder(t *testing.T) {
	root := t.TempDir()
	cmdDir := filepath.Join(root, ShimDir)
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Populate: one Unchanged, one Modified, one Stale, one Custom.
	os.WriteFile(filepath.Join(cmdDir, Filename("u")), []byte(Render("u", "same", "", "")), 0o644)
	os.WriteFile(filepath.Join(cmdDir, Filename("m")), []byte(Render("m", "old", "", "")), 0o644)
	os.WriteFile(filepath.Join(cmdDir, Filename("s")), []byte(Render("s", "x", "", "")), 0o644)
	os.WriteFile(filepath.Join(cmdDir, Filename("c")), []byte("# custom\n"), 0o644)

	changes, err := Plan(sums("u", "same", "m", "new", "n", "brief"), root)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []ChangeKind{New, Modified, UnchangedChange, Stale, CustomChange}
	if len(changes) != len(wantOrder) {
		t.Fatalf("len(changes) = %d, want %d; got: %+v", len(changes), len(wantOrder), changes)
	}
	for i, c := range changes {
		if c.Kind != wantOrder[i] {
			t.Errorf("changes[%d].Kind = %s, want %s", i, c.Kind, wantOrder[i])
		}
	}
}

func TestPlanMissingShimDirIsAllNew(t *testing.T) {
	// Explicit coverage: when .claude/commands/ does not exist at all,
	// Plan must not error and must return pure New entries.
	root := t.TempDir()
	changes, err := Plan(sums("a", "aa"), root)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 1 || changes[0].Kind != New {
		t.Errorf("want single New, got %+v", changes)
	}
}

// TestPlanProjectScopedCarriesProjectAndHint verifies a project-tier Summary
// produces a New Change carrying the project slug and argument-hint.
func TestPlanProjectScopedCarriesProjectAndHint(t *testing.T) {
	root := t.TempDir()
	changes, err := Plan([]commands.Summary{
		{Name: "res_build", Brief: "b", Source: "project", Project: "rezbldrvault", ArgHint: "[x]"},
	}, root)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 1 || changes[0].Kind != New {
		t.Fatalf("want 1 New, got %+v", changes)
	}
	if changes[0].Project != "rezbldrvault" {
		t.Errorf("Project = %q, want rezbldrvault", changes[0].Project)
	}
	if changes[0].ArgHint != "[x]" {
		t.Errorf("ArgHint = %q, want [x]", changes[0].ArgHint)
	}
}

// TestPlanProjectShimDriftOnSlugChange verifies an on-disk project shim is
// classified Modified when its slug changes, since the slug folds into the hash.
func TestPlanProjectShimDriftOnSlugChange(t *testing.T) {
	root := t.TempDir()
	cmdDir := filepath.Join(root, ShimDir)
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(cmdDir, Filename("res_build"))
	if err := os.WriteFile(p, []byte(Render("res_build", "b", "old", "")), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := Plan([]commands.Summary{
		{Name: "res_build", Brief: "b", Source: "project", Project: "new"},
	}, root)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 1 || changes[0].Kind != Modified {
		t.Fatalf("want 1 Modified on slug change, got %+v", changes)
	}
}

// TestPlanSkipsNewWhenCustomFileCollides verifies a hand-written markerless file
// occupying a desired command's path is reported as CustomChange only — no
// duplicate New (Wire refuses to clobber it; the user removes it to adopt).
func TestPlanSkipsNewWhenCustomFileCollides(t *testing.T) {
	root := t.TempDir()
	cmdDir := filepath.Join(root, ShimDir)
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, Filename("res_build")),
		[]byte("# hand made, no marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := Plan([]commands.Summary{
		{Name: "res_build", Brief: "b", Source: "project", Project: "p"},
	}, root)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("want exactly 1 change (CustomChange), got %+v", changes)
	}
	if changes[0].Kind != CustomChange {
		t.Errorf("Kind = %s, want CustomChange", changes[0].Kind)
	}
}

func TestChangeKindString(t *testing.T) {
	cases := map[ChangeKind]string{
		New: "new", Modified: "modified", UnchangedChange: "unchanged",
		Stale: "stale", CustomChange: "custom",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("%d: got %q want %q", k, got, want)
		}
	}
}
