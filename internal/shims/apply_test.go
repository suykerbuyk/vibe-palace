// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"os"
	"path/filepath"
	"testing"
)

// applyProject seeds a fixture project with cmdDir/.claude/commands/
// populated per the provided writes map (filename -> contents) and returns
// the project root.
func applyProject(t *testing.T, writes map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ShimDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range writes {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestApplyAddsNewShimsAndSkipsUnchanged(t *testing.T) {
	root := applyProject(t, map[string]string{
		Filename("restart"): Render("restart", "same", "", ""),
	})
	changes, err := Plan(sums("restart", "same", "wrap", "w"), root)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	rep, err := Apply(changes, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.Added != 1 {
		t.Errorf("Added = %d, want 1", rep.Added)
	}
	if rep.Unchanged != 1 {
		t.Errorf("Unchanged = %d, want 1", rep.Unchanged)
	}
	// Verify the wrap shim actually landed on disk with the right sha.
	wrapPath := filepath.Join(root, ShimDir, Filename("wrap"))
	scan, err := ScanShim(wrapPath)
	if err != nil || !scan.HasMarker {
		t.Fatalf("wrap shim not written correctly: err=%v scan=%+v", err, scan)
	}
	if scan.Sha != ExpectedSha("wrap", "w", "", "") {
		t.Errorf("wrap sha = %q, want %q", scan.Sha, ExpectedSha("wrap", "w", "", ""))
	}
}

func TestApplyUpdatesModifiedShim(t *testing.T) {
	root := applyProject(t, map[string]string{
		Filename("restart"): Render("restart", "old", "", ""),
	})
	changes, err := Plan(sums("restart", "new"), root)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Apply(changes, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.Updated != 1 {
		t.Errorf("Updated = %d, want 1", rep.Updated)
	}
	path := filepath.Join(root, ShimDir, Filename("restart"))
	scan, _ := ScanShim(path)
	if scan.Sha != ExpectedSha("restart", "new", "", "") {
		t.Errorf("sha after update = %q, want %q", scan.Sha, ExpectedSha("restart", "new", "", ""))
	}
}

func TestApplyLeavesCustomFilesUntouched(t *testing.T) {
	orig := "# do not touch\n"
	root := applyProject(t, map[string]string{
		Filename("mine"): orig,
	})
	changes, err := Plan(sums("restart", "r"), root)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Apply(changes, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.Custom != 1 {
		t.Errorf("Custom = %d, want 1", rep.Custom)
	}
	data, _ := os.ReadFile(filepath.Join(root, ShimDir, Filename("mine")))
	if string(data) != orig {
		t.Errorf("custom file modified:\nbefore=%q\nafter =%q", orig, data)
	}
}

func TestApplyStaleDefaultRetains(t *testing.T) {
	// Default options: Stale entries are counted but not removed. This is
	// the init-flow contract; removal only happens via interactive upgrade.
	root := applyProject(t, map[string]string{
		Filename("retired"): Render("retired", "gone", "", ""),
	})
	changes, err := Plan(sums("restart", "r"), root) // retired absent from desired
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Apply(changes, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.Stale != 1 {
		t.Errorf("Stale = %d, want 1", rep.Stale)
	}
	if rep.Removed != 0 {
		t.Errorf("Removed = %d, want 0 without opt-in", rep.Removed)
	}
	if _, err := os.Stat(filepath.Join(root, ShimDir, Filename("retired"))); err != nil {
		t.Errorf("retired shim disappeared without consent: %v", err)
	}
}

func TestApplyStaleWithAllowStaleRemoval(t *testing.T) {
	root := applyProject(t, map[string]string{
		Filename("retired"): Render("retired", "gone", "", ""),
	})
	changes, err := Plan(sums("restart", "r"), root)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Apply(changes, ApplyOptions{AllowStaleRemoval: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.Removed != 1 {
		t.Errorf("Removed = %d, want 1", rep.Removed)
	}
	if _, err := os.Stat(filepath.Join(root, ShimDir, Filename("retired"))); !os.IsNotExist(err) {
		t.Errorf("retired shim still present after removal: %v", err)
	}
}

func TestApplyOutcomesAlignWithPlanOrder(t *testing.T) {
	root := applyProject(t, map[string]string{})
	changes, err := Plan(sums("a", "aa", "b", "bb"), root)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Apply(changes, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Outcomes) != len(changes) {
		t.Fatalf("outcome count = %d, want %d", len(rep.Outcomes), len(changes))
	}
	for i := range changes {
		if rep.Outcomes[i].Change.Path != changes[i].Path {
			t.Errorf("outcome[%d].Path = %s, want %s",
				i, rep.Outcomes[i].Change.Path, changes[i].Path)
		}
	}
}

func TestApplyRoundTripIdempotent(t *testing.T) {
	// Run Apply twice; the second pass should be pure Unchanged.
	root := t.TempDir()
	s := sums("restart", "r", "wrap", "w")
	changes1, err := Plan(s, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(changes1, ApplyOptions{}); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	changes2, err := Plan(s, root)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Apply(changes2, ApplyOptions{})
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if rep.Added != 0 || rep.Updated != 0 {
		t.Errorf("second pass wrote files: %+v", rep)
	}
	if rep.Unchanged != 2 {
		t.Errorf("Unchanged = %d, want 2", rep.Unchanged)
	}
}
