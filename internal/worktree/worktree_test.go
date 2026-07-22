// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// isolateGit points git at a throwaway config so the developer's global/system
// git config (default branch, hooks, signing) cannot influence a test.
func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

// initRepo builds a temp git repo with one commit on `main` and returns its
// root. The repo is a `repo` subdir of a fresh temp dir, so the sibling `wt/`
// the Manager writes to is isolated per test.
func initRepo(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", root,
			"-c", "user.email=test@example.com", "-c", "user.name=test"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-m", "seed")
	return root
}

// commitInWorktree makes a commit inside a worktree so its branch diverges from
// main — used to exercise the unmerged-branch protection in Remove.
func commitInWorktree(t *testing.T, wtPath string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wtPath, "extra.txt"), []byte("more\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", wtPath,
			"-c", "user.email=test@example.com", "-c", "user.name=test"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in worktree: %v\n%s", args, err, out)
		}
	}
	git("add", "-A")
	git("commit", "-m", "diverge")
}

func TestCreate(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)

	res, err := m.Create("my-plan", "", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.Branch != "plan/my-plan" {
		t.Errorf("branch = %q, want plan/my-plan", res.Branch)
	}
	if res.Base != DefaultBase {
		t.Errorf("base = %q, want %q", res.Base, DefaultBase)
	}
	wantPath := filepath.Join(filepath.Dir(root), "wt", "my-plan")
	if res.Path != wantPath {
		t.Errorf("path = %q, want %q", res.Path, wantPath)
	}
	if _, err := os.Stat(filepath.Join(res.Path, "README.md")); err != nil {
		t.Errorf("worktree not checked out: %v", err)
	}
	if !m.branchExists("plan/my-plan") {
		t.Error("branch plan/my-plan should exist after Create")
	}
}

func TestCreateCustomBranchAndBase(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)

	res, err := m.Create("feature-x", "main", "wip/feature-x")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.Branch != "wip/feature-x" {
		t.Errorf("branch = %q, want wip/feature-x", res.Branch)
	}
	if res.Base != "main" {
		t.Errorf("base = %q, want main", res.Base)
	}
}

func TestCreateRejectsBadSlug(t *testing.T) {
	isolateGit(t)
	m := New(initRepo(t))
	for _, bad := range []string{"", "Has Space", "../escape", "UPPER"} {
		if _, err := m.Create(bad, "", ""); err == nil {
			t.Errorf("Create(%q) should have failed", bad)
		}
	}
}

func TestCreateRejectsDuplicatePath(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	if _, err := m.Create("dup", "", ""); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := m.Create("dup", "", "")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second Create err = %v, want already-exists", err)
	}
}

func TestCreateRejectsExistingBranch(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	// A worktree on plan/taken, removed, leaves the branch behind; a fresh
	// Create for the same slug must refuse rather than collide on the branch.
	if _, err := m.Create("taken", "", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := m.Remove("taken", RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	_, err := m.Create("taken", "", "")
	if err == nil || !strings.Contains(err.Error(), "branch already exists") {
		t.Errorf("Create over surviving branch err = %v, want branch-already-exists", err)
	}
}

func TestCreateNonRepo(t *testing.T) {
	isolateGit(t)
	m := New(t.TempDir()) // a dir, but not a git repo
	_, err := m.Create("x", "", "")
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("err = %v, want not-a-git-repository", err)
	}
}

func TestRemove(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	res, err := m.Create("gone", "", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := m.Remove("gone", RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(res.Path); !os.IsNotExist(err) {
		t.Errorf("worktree path still present after Remove: %v", err)
	}
	// The merged branch (still at base) survives — DeleteBranch was not set.
	if !m.branchExists("plan/gone") {
		t.Error("branch should survive a Remove without DeleteBranch")
	}
}

func TestRemoveDeleteBranch(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	if _, err := m.Create("clean", "", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Branch is at base (merged), so a safe delete succeeds.
	if err := m.Remove("clean", RemoveOptions{DeleteBranch: true}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if m.branchExists("plan/clean") {
		t.Error("branch should be gone after DeleteBranch")
	}
}

func TestRemoveDeleteBranchRefusesUnmerged(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	res, err := m.Create("wip", "", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	commitInWorktree(t, res.Path) // branch now has a commit not in main
	err = m.Remove("wip", RemoveOptions{DeleteBranch: true})
	if err == nil || !strings.Contains(err.Error(), "not deleted") {
		t.Errorf("Remove unmerged err = %v, want not-deleted", err)
	}
	// The worktree is gone but the unmerged branch is preserved.
	if _, statErr := os.Stat(res.Path); !os.IsNotExist(statErr) {
		t.Errorf("worktree should still be removed: %v", statErr)
	}
	if !m.branchExists("plan/wip") {
		t.Error("unmerged branch must be preserved")
	}
}

func TestRemoveForce(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	res, err := m.Create("dirty", "", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Leave an uncommitted change; a plain remove refuses, --force clears it.
	if werr := os.WriteFile(filepath.Join(res.Path, "scratch.txt"), []byte("x\n"), 0o644); werr != nil {
		t.Fatal(werr)
	}
	if err := m.Remove("dirty", RemoveOptions{}); err == nil {
		t.Error("Remove of a dirty worktree without Force should fail")
	}
	if err := m.Remove("dirty", RemoveOptions{Force: true}); err != nil {
		t.Errorf("Remove with Force: %v", err)
	}
}

func TestList(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	if _, err := m.Create("alpha", "", ""); err != nil {
		t.Fatalf("Create alpha: %v", err)
	}
	if _, err := m.Create("beta", "", ""); err != nil {
		t.Fatalf("Create beta: %v", err)
	}

	plans, err := m.List(false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("plan worktrees = %d, want 2: %+v", len(plans), plans)
	}
	seen := map[string]bool{}
	for _, e := range plans {
		seen[e.Branch] = true
		if e.Head == "" {
			t.Errorf("entry %q missing HEAD", e.Branch)
		}
	}
	if !seen["plan/alpha"] || !seen["plan/beta"] {
		t.Errorf("missing expected branches: %+v", plans)
	}

	all, err := m.List(true)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 3 { // main checkout + two plan worktrees
		t.Errorf("all worktrees = %d, want 3: %+v", len(all), all)
	}
}

func TestListNonRepo(t *testing.T) {
	isolateGit(t)
	m := New(t.TempDir())
	if _, err := m.List(false); err == nil {
		t.Error("List on a non-repo should fail")
	}
}

func TestParseWorktreeList(t *testing.T) {
	out := strings.Join([]string{
		"worktree /home/u/code/repo",
		"HEAD 1111111111111111111111111111111111111111",
		"branch refs/heads/main",
		"",
		"worktree /home/u/code/wt/alpha",
		"HEAD 2222222222222222222222222222222222222222",
		"branch refs/heads/plan/alpha",
		"",
		"worktree /home/u/code/wt/detached",
		"HEAD 3333333333333333333333333333333333333333",
		"detached",
		"",
	}, "\n")
	entries := parseWorktreeList(out)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[0].Branch != "main" {
		t.Errorf("entries[0].Branch = %q, want main", entries[0].Branch)
	}
	if entries[1].Branch != "plan/alpha" {
		t.Errorf("entries[1].Branch = %q, want plan/alpha", entries[1].Branch)
	}
	if entries[2].Branch != "(detached)" {
		t.Errorf("entries[2].Branch = %q, want (detached)", entries[2].Branch)
	}
	if entries[1].Path != "/home/u/code/wt/alpha" {
		t.Errorf("entries[1].Path = %q", entries[1].Path)
	}
}
