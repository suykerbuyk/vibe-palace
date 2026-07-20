// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// writeOldTriple writes a triple JSON file at an arbitrary (possibly nested,
// old-encoding) path under a project's triples dir, with the RAW subject/
// predicate/object in the body — exactly the shape the migration must read.
func writeOldTriple(t *testing.T, root, project, relName, subj, pred, obj string) {
	t.Helper()
	path := filepath.Join(root, "palace", project, "kg", "triples", relName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(storage.Triple{Subject: subj, Predicate: pred, Object: obj}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

func gitInitCommit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	gitCmd(t, dir, "init", "--quiet")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "baseline", "--quiet")
}

// TestRunKGFilenamePlanMutatesNothing: the plan-only preview reports what WOULD
// change and touches nothing (no rename, no stamp).
func TestRunKGFilenamePlanMutatesNothing(t *testing.T) {
	root := t.TempDir()
	vault := storage.NewVault(root)
	writeOldTriple(t, root, "proj", filepath.Join("src", "main.rs--m--s1.json"), "src/main.rs", "m", "s1")
	oldPath := filepath.Join(root, "palace", "proj", "kg", "triples", "src", "main.rs--m--s1.json")

	m, err := runKGFilenamePlan(vault)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if m.Renamed != 1 {
		t.Errorf("plan Renamed = %d, want 1", m.Renamed)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("plan moved the file: %v", err)
	}
	if f, _ := surface.ReadFormat(root); f != 0 {
		t.Errorf("plan advanced format to %d, want 0", f)
	}
}

// TestRunKGFilenameApplyRefusesNonGitVault: without a git repo there is no
// guaranteed rollback, so apply refuses.
func TestRunKGFilenameApplyRefusesNonGitVault(t *testing.T) {
	root := t.TempDir()
	vault := storage.NewVault(root)
	writeOldTriple(t, root, "proj", filepath.Join("src", "main.rs--m--s1.json"), "src/main.rs", "m", "s1")

	if _, _, err := runKGFilenameApply(vault); err == nil {
		t.Fatal("expected refusal on a non-git vault, got nil")
	}
}

// TestRunKGFilenameApplyRefusesDirtyTree: a dirty vault git tree makes
// `git checkout .` an unsafe rollback, so apply refuses.
func TestRunKGFilenameApplyRefusesDirtyTree(t *testing.T) {
	root := t.TempDir()
	vault := storage.NewVault(root)
	writeOldTriple(t, root, "proj", filepath.Join("src", "main.rs--m--s1.json"), "src/main.rs", "m", "s1")
	gitInitCommit(t, root)
	// Dirty the tree.
	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runKGFilenameApply(vault); err == nil {
		t.Fatal("expected refusal on a dirty tree, got nil")
	}
	// Nothing migrated.
	if _, err := os.Stat(filepath.Join(root, "palace", "proj", "kg", "triples", "src", "main.rs--m--s1.json")); err != nil {
		t.Errorf("apply mutated despite a dirty tree: %v", err)
	}
}

// TestRunKGFilenameApplyStampsAndStages: the happy path renames, stamps, and
// stages (without committing).
func TestRunKGFilenameApplyStampsAndStages(t *testing.T) {
	root := t.TempDir()
	vault := storage.NewVault(root)
	writeOldTriple(t, root, "proj", filepath.Join("src", "main.rs--m--s1.json"), "src/main.rs", "m", "s1")
	writeOldTriple(t, root, "proj", "kai--works_on--orion.json", "Kai", "works on", "Orion")
	gitInitCommit(t, root)

	m, staged, err := runKGFilenameApply(vault)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.Renamed != 2 {
		t.Errorf("Renamed = %d, want 2", m.Renamed)
	}
	if staged < 1 {
		t.Errorf("staged = %d, want >= 1", staged)
	}
	// Stamped current.
	if f, err := surface.ReadFormat(root); err != nil || f != surface.RequiredDataFormat {
		t.Errorf("format = %d (err %v), want %d", f, err, surface.RequiredDataFormat)
	}
	// The format stamp is STAGED (tracked) despite living under gitignored .vibe-palace/.
	out, err := exec.Command("git", "-C", root, "diff", "--staged", "--name-only").CombinedOutput()
	if err != nil {
		t.Fatalf("git diff --staged: %s: %v", out, err)
	}
	if !strings.Contains(string(out), ".vibe-palace/vault.toml") {
		t.Errorf("format stamp not staged; staged files:\n%s", out)
	}
	// Fully queryable both sides post-migration.
	in, err := vault.QueryEntity("proj", "s1", "", "in")
	if err != nil {
		t.Fatalf("QueryEntity: %v", err)
	}
	if len(in) != 1 || in[0].Subject != "src/main.rs" {
		t.Errorf("object-side lookup = %+v, want the src/main.rs triple", in)
	}
}
