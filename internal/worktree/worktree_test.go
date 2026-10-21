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
// git config (default branch, hooks, signing) cannot influence a test, strips
// any repo-pointing variables this test process itself inherited, and bounds
// git's upward repository discovery to this test's own temp tree so a bare
// non-repo temp dir can never resolve to some enclosing repository (e.g. when
// TMPDIR itself sits inside a git repo or worktree). This must run before any
// other git subprocess in the test — every test in this file already calls it
// first.
func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	// Strip any repo-pointing variables this test process itself inherited
	// (e.g. from a git hook that ran `go test`), so a bare `git -C <dir>` in
	// this package always answers for <dir>, never for whatever GIT_DIR /
	// GIT_WORK_TREE / GIT_INDEX_FILE named. An explicit GIT_DIR bypasses
	// discovery entirely, so GIT_CEILING_DIRECTORIES alone cannot stop it —
	// this closes that gap. Mirrors the rationale of internal/storage's
	// withoutRepoLocalGitEnv / TrackedPalaceLocalFiles, scoped down to the
	// variables that matter for a plain `-C` invocation.
	for _, k := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}

	// Bound upward discovery to this test's own temp tree: git will not
	// search into or above this directory looking for a repository. Every
	// t.TempDir() call within one test shares the same immediate parent
	// (Go's testing package creates that parent once per test and reuses
	// it for every subsequent numbered subdir), so computing the ceiling
	// from any t.TempDir() call in the test covers every temp dir the
	// test creates — including a TMPDIR that itself sits inside a real
	// repository or worktree, which is the 2026-09-10 incident this closes.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
}

// runGitT runs a git command in dir for tests, failing on error and returning
// the trimmed combined output.
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir,
		"-c", "user.email=test@example.com", "-c", "user.name=test"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// tryGitT runs a git command in dir for tests WITHOUT failing on error; it
// returns the trimmed combined output and the error, for cases git may
// legitimately reject (e.g. a nested worktree add).
func tryGitT(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"-C", dir,
		"-c", "user.email=test@example.com", "-c", "user.name=test"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// initRepo builds a temp git repo with one commit on `main` and returns its
// root. The repo is a `repo` subdir of a fresh temp dir, so the under-root `.wt/`
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

	res, err := m.Create("my-plan", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.Branch != "my-plan" {
		t.Errorf("branch = %q, want my-plan", res.Branch)
	}
	if res.Base != DefaultBase {
		t.Errorf("base = %q, want %q", res.Base, DefaultBase)
	}
	wantPath := filepath.Join(root, ".wt", "my-plan")
	if res.Path != wantPath {
		t.Errorf("path = %q, want %q", res.Path, wantPath)
	}
	if _, err := os.Stat(filepath.Join(res.Path, "README.md")); err != nil {
		t.Errorf("worktree not checked out: %v", err)
	}
	if !m.branchExists("my-plan") {
		t.Error("branch my-plan should exist after Create")
	}
}

func TestCreateCustomBase(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)

	// An alternate base branch to cut from (the --branch override is gone; base
	// is the only remaining knob).
	runGitT(t, root, "branch", "dev")
	res, err := m.Create("feature-x", "dev")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.Branch != "feature-x" {
		t.Errorf("branch = %q, want feature-x", res.Branch)
	}
	if res.Base != "dev" {
		t.Errorf("base = %q, want dev", res.Base)
	}
}

func TestCreateRejectsBadSlug(t *testing.T) {
	isolateGit(t)
	m := New(initRepo(t))
	for _, bad := range []string{"", "Has Space", "../escape", "UPPER"} {
		if _, err := m.Create(bad, ""); err == nil {
			t.Errorf("Create(%q) should have failed", bad)
		}
	}
}

func TestCreateRejectsDuplicatePath(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	if _, err := m.Create("dup", ""); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := m.Create("dup", "")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second Create err = %v, want already-exists", err)
	}
}

func TestCreateRejectsExistingBranch(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	// A worktree on branch taken, removed, leaves the branch behind; a fresh
	// Create for the same slug must refuse rather than collide on the branch.
	if _, err := m.Create("taken", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := m.Remove("taken", RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	_, err := m.Create("taken", "")
	if err == nil || !strings.Contains(err.Error(), "branch already exists") {
		t.Errorf("Create over surviving branch err = %v, want branch-already-exists", err)
	}
}

func TestCreateNonRepo(t *testing.T) {
	isolateGit(t)
	m := New(t.TempDir()) // a dir, but not a git repo
	_, err := m.Create("x", "")
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("err = %v, want not-a-git-repository", err)
	}
}

// TestIsolateGitDoesNotEscapeIntoAnEnclosingRepo is the regression pin for the
// 2026-09-10 incident: with TMPDIR nested inside a real repository, isolateGit
// must bound git's discovery so Create on a bare temp dir still fails with
// "not a git repository", and the enclosing repository is left untouched.
func TestIsolateGitDoesNotEscapeIntoAnEnclosingRepo(t *testing.T) {
	// Built directly (not via t.TempDir()), because it must NOT be affected by
	// the TMPDIR override below, and it must outlive that override's scope
	// long enough to be inspected after.
	enclosing, err := os.MkdirTemp("", "enclosing-repo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(enclosing) })
	git := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", enclosing,
			"-c", "user.email=test@example.com", "-c", "user.name=test"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(enclosing, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "seed")

	snapshot := func() string {
		wt, _ := exec.Command("git", "-C", enclosing, "worktree", "list", "--porcelain").CombinedOutput()
		br, _ := exec.Command("git", "-C", enclosing, "branch", "-a").CombinedOutput()
		return string(wt) + "\x00" + string(br)
	}
	before := snapshot()

	// Simulate TMPDIR pointed inside the enclosing repo (the actual incident
	// condition), nested a few levels down as the real one was.
	fakeTMPDIR := filepath.Join(enclosing, "cache", "tmp")
	if err := os.MkdirAll(fakeTMPDIR, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", fakeTMPDIR)

	isolateGit(t) // must run before any other git call, so its ceiling covers them
	m := New(t.TempDir())
	_, err = m.Create("x", "")
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("Create err = %v, want not-a-git-repository", err)
	}

	if after := snapshot(); before != after {
		t.Errorf("enclosing repo mutated by a test that should never touch it:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestIsolateGitStripsInheritedGitDir pins the gap a code review found in the
// GIT_CEILING_DIRECTORIES fix above: GIT_CEILING_DIRECTORIES only bounds
// upward discovery from a bare directory — it does nothing once GIT_DIR (or
// GIT_WORK_TREE/GIT_INDEX_FILE/...) is already present in the process
// environment, because an explicit GIT_DIR bypasses discovery entirely. Git
// exports these into every hook script's environment (and its children), so a
// test invoked from inside a git hook would reopen the exact incident class
// this package's tests exist to close, through a different vector. isolateGit
// must strip them before any other git subprocess runs.
func TestIsolateGitStripsInheritedGitDir(t *testing.T) {
	// Built directly (not via t.TempDir()), so it is unaffected by whatever
	// isolateGit does later and outlives this test's assertions.
	enclosing, err := os.MkdirTemp("", "enclosing-repo-gitdir")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(enclosing) })
	git := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", enclosing,
			"-c", "user.email=test@example.com", "-c", "user.name=test"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(enclosing, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "seed")

	snapshot := func() string {
		wt, _ := exec.Command("git", "-C", enclosing, "worktree", "list", "--porcelain").CombinedOutput()
		br, _ := exec.Command("git", "-C", enclosing, "branch", "-a").CombinedOutput()
		return string(wt) + "\x00" + string(br)
	}
	before := snapshot()

	// Simulate a git hook's environment: GIT_DIR pointing at the enclosing
	// scratch repo, set BEFORE isolateGit runs. An explicit GIT_DIR makes git
	// operate on that repository directly, regardless of -C or
	// GIT_CEILING_DIRECTORIES — the exact bypass this test pins.
	t.Setenv("GIT_DIR", filepath.Join(enclosing, ".git"))

	isolateGit(t) // must strip GIT_DIR before any other git call in the test
	m := New(t.TempDir())
	_, err = m.Create("x", "")
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("Create err = %v, want not-a-git-repository", err)
	}

	if after := snapshot(); before != after {
		t.Errorf("enclosing repo mutated by an inherited GIT_DIR that isolateGit should have stripped:\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestRemove(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	res, err := m.Create("gone", "")
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
	if !m.branchExists("gone") {
		t.Error("branch should survive a Remove without DeleteBranch")
	}
}

func TestRemoveDeleteBranch(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	if _, err := m.Create("clean", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Branch is at base (merged), so a safe delete succeeds.
	if err := m.Remove("clean", RemoveOptions{DeleteBranch: true}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if m.branchExists("clean") {
		t.Error("branch should be gone after DeleteBranch")
	}
}

func TestRemoveDeleteBranchRefusesUnmerged(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	res, err := m.Create("wip", "")
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
	if !m.branchExists("wip") {
		t.Error("unmerged branch must be preserved")
	}
}

func TestRemoveForce(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	res, err := m.Create("dirty", "")
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
	if _, err := m.Create("alpha", ""); err != nil {
		t.Fatalf("Create alpha: %v", err)
	}
	if _, err := m.Create("beta", ""); err != nil {
		t.Fatalf("Create beta: %v", err)
	}

	plans, err := m.List(false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf(".wt/ worktrees = %d, want 2: %+v", len(plans), plans)
	}
	seen := map[string]bool{}
	for _, e := range plans {
		seen[e.Branch] = true
		if e.Head == "" {
			t.Errorf("entry %q missing HEAD", e.Branch)
		}
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Errorf("missing expected branches: %+v", plans)
	}

	all, err := m.List(true)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 3 { // main checkout + two .wt/ worktrees
		t.Errorf("all worktrees = %d, want 3: %+v", len(all), all)
	}
}

// TestListIsPathDiscriminated proves List(false) selects by PARENT DIRECTORY
// (== <root>/.wt), not by a branch prefix or a loose substring match: it includes
// the single-task and both epic tiers, and EXCLUDES the main checkout, a
// harness-owned .claude/worktrees/* tree, and any worktree nested below .wt. The
// nested case is what a `strings.Contains(e.Path, ".wt")` matcher would wrongly
// admit.
func TestListIsPathDiscriminated(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	if _, err := m.Create("single", ""); err != nil {
		t.Fatalf("Create single: %v", err)
	}
	if _, err := m.CreateEpicIntegration("ep", ""); err != nil {
		t.Fatalf("CreateEpicIntegration: %v", err)
	}
	if _, err := m.CreateEpicTask("ep", "t1", ""); err != nil {
		t.Fatalf("CreateEpicTask: %v", err)
	}

	// A harness-owned worktree under .claude/worktrees/ must be excluded.
	harnessPath := filepath.Join(root, ".claude", "worktrees", "agent-x")
	runGitT(t, root, "worktree", "add", harnessPath, "-b", "agent-x")

	// A worktree nested BELOW .wt (parent != <root>/.wt) must be excluded. Use a
	// plain (non-worktree) subdir of .wt for a clean nested case; if git refuses
	// the add, skip the per-path assertion but still assert main + harness absent.
	nestedPath := filepath.Join(root, ".wt", "grp", "nested")
	if _, err := tryGitT(t, root, "worktree", "add", nestedPath, "-b", "nested"); err != nil {
		t.Logf("nested worktree add rejected by git (acceptable): %v", err)
		nestedPath = ""
	}

	ours, err := m.List(false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ours) != 3 {
		t.Fatalf(".wt/ worktrees = %d, want 3 (single, ep, ep__t1): %+v", len(ours), ours)
	}
	got := map[string]bool{}
	byPath := map[string]bool{}
	for _, e := range ours {
		got[e.Branch] = true
		byPath[e.Path] = true
	}
	for _, b := range []string{"single", "ep", "ep__t1"} {
		if !got[b] {
			t.Errorf("List(false) missing %q: %+v", b, ours)
		}
	}
	// EXCLUSION — the half a loosened matcher or an m.Root swap would miss.
	if got["agent-x"] {
		t.Error("List(false) wrongly included the harness .claude/worktrees/ branch")
	}
	if got["nested"] {
		t.Error("List(false) wrongly included a worktree nested below .wt")
	}
	excludedPaths := []string{root, harnessPath}
	if nestedPath != "" {
		excludedPaths = append(excludedPaths, nestedPath)
	}
	for _, p := range excludedPaths {
		if byPath[p] {
			t.Errorf("List(false) wrongly included excluded path %q: %+v", p, ours)
		}
	}

	all, err := m.List(true)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	wantAll := 5 // main + single + ep + ep__t1 + agent-x
	if nestedPath != "" {
		wantAll = 6
	}
	if len(all) != wantAll {
		t.Errorf("all worktrees = %d, want %d: %+v", len(all), wantAll, all)
	}
	if all[0].Path != root {
		t.Errorf("entries[0].Path = %q, want main checkout %q", all[0].Path, root)
	}
}

// TestListThroughSymlinkedRoot proves the discriminator uses git's canonical root
// (the first porcelain record), not the Manager's possibly-unresolved Root:
// constructing the Manager on a SYMLINK to the repo still returns the .wt
// children. This is the case an `m.Root`-for-`entries[0].Path` swap would fail.
func TestListThroughSymlinkedRoot(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	real := New(root)
	if _, err := real.Create("single", ""); err != nil {
		t.Fatalf("Create single: %v", err)
	}
	if _, err := real.CreateEpicTask("ep", "t1", ""); err != nil {
		t.Fatalf("CreateEpicTask: %v", err)
	}

	link := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}
	m := New(link)
	ours, err := m.List(false)
	if err != nil {
		t.Fatalf("List via symlink: %v", err)
	}
	got := map[string]bool{}
	for _, e := range ours {
		got[e.Branch] = true
	}
	for _, b := range []string{"single", "ep__t1"} {
		if !got[b] {
			t.Errorf("List(false) via symlinked root missing %q: %+v", b, ours)
		}
	}
	if len(ours) != 2 {
		t.Errorf(".wt/ worktrees via symlink = %d, want 2: %+v", len(ours), ours)
	}
}

// TestEpicTwoTierLifecycle proves the Fix-B naming: the integration branch
// <epic> and task branch <epic>__<task> are flat refs that COEXIST (no git D/F
// ref conflict), and RemoveEpicIntegration enforces decision 6 — it refuses
// while a task tree/branch remains, and succeeds only once they are gone.
func TestEpicTwoTierLifecycle(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)

	ires, err := m.CreateEpicIntegration("ep", "")
	if err != nil {
		t.Fatalf("CreateEpicIntegration: %v", err)
	}
	if ires.Branch != "ep" {
		t.Errorf("integration branch = %q, want ep", ires.Branch)
	}
	if want := filepath.Join(root, ".wt", "ep"); ires.Path != want {
		t.Errorf("integration path = %q, want %q", ires.Path, want)
	}

	tres, err := m.CreateEpicTask("ep", "t1", "")
	if err != nil {
		t.Fatalf("CreateEpicTask: %v", err)
	}
	if tres.Branch != "ep__t1" {
		t.Errorf("task branch = %q, want ep__t1", tres.Branch)
	}
	if want := filepath.Join(root, ".wt", "ep__t1"); tres.Path != want {
		t.Errorf("task path = %q, want %q", tres.Path, want)
	}

	// Both flat refs coexist — the Fix-B property that dodges the D/F conflict a
	// bare <epic> + <epic>/<task> pair would hit.
	if !m.branchExists("ep") || !m.branchExists("ep__t1") {
		t.Fatalf("both ep and ep__t1 branches must coexist")
	}

	// Integration removal refuses while the task tree/branch remains.
	if err := m.RemoveEpicIntegration("ep", RemoveOptions{DeleteBranch: true}); err == nil {
		t.Error("RemoveEpicIntegration should refuse while a task worktree remains")
	} else if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("refusal err = %v, want a refusing-to-remove message", err)
	}
	// The integration tree must still be present after the refusal.
	if _, statErr := os.Stat(ires.Path); statErr != nil {
		t.Errorf("integration worktree must survive a refused removal: %v", statErr)
	}

	// Remove the task, then the integration succeeds.
	if err := m.RemoveEpicTask("ep", "t1", RemoveOptions{DeleteBranch: true}); err != nil {
		t.Fatalf("RemoveEpicTask: %v", err)
	}
	if m.branchExists("ep__t1") {
		t.Error("task branch ep__t1 should be gone after RemoveEpicTask --delete-branch")
	}
	if err := m.RemoveEpicIntegration("ep", RemoveOptions{DeleteBranch: true}); err != nil {
		t.Fatalf("RemoveEpicIntegration after tasks gone: %v", err)
	}
	if m.branchExists("ep") {
		t.Error("integration branch ep should be gone after RemoveEpicIntegration --delete-branch")
	}
}

// TestIgnoredPathWorktree re-proves inside the package tests the STEP-0 finding:
// a worktree created at the gitignored under-root path does NOT dirty the main
// checkout, and the round-trip (create → remove --delete-branch) is clean.
func TestIgnoredPathWorktree(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/.wt/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, root, "add", ".gitignore")
	runGitT(t, root, "commit", "-m", "ignore /.wt/")

	m := New(root)
	if _, err := m.Create("iso", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if st := runGitT(t, root, "status", "--porcelain"); st != "" {
		t.Errorf("main checkout dirty after creating an ignored worktree: %q", st)
	}
	if err := m.Remove("iso", RemoveOptions{DeleteBranch: true}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if m.branchExists("iso") {
		t.Error("branch iso should be gone after remove --delete-branch")
	}
}

// TestLongComposite proves the composite name is EXEMPT from the 64-char
// per-component slug cap: each half validates as a bare slug, and the derived
// <epic>__<task> directory/branch (here > 64 bytes) is accepted by git and the
// filesystem.
func TestLongComposite(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)
	epic := "worktree-discipline-wt-dir-slug-convention-and-lifecycle" // this epic's real 54-char slug
	task := "core-code-move-rename-and-path-discrimination"
	want := epic + "__" + task
	if len(want) <= 64 {
		t.Fatalf("test composite is %d chars; expected > 64 to exercise the exemption", len(want))
	}
	res, err := m.CreateEpicTask(epic, task, "")
	if err != nil {
		t.Fatalf("CreateEpicTask long composite: %v", err)
	}
	if res.Branch != want {
		t.Errorf("branch = %q, want %q", res.Branch, want)
	}
	if !m.branchExists(want) {
		t.Error("long composite branch should exist")
	}
	if err := m.RemoveEpicTask(epic, task, RemoveOptions{DeleteBranch: true}); err != nil {
		t.Fatalf("RemoveEpicTask: %v", err)
	}
}

// TestCreateEpicTaskRejectsBadComponent proves the epic helpers validate each
// COMPONENT (never the composite): a bad epic or task is rejected with an error
// and leaves no .wt directory and no branch behind. Assertions are on the error
// and the absence of side effects — not on passing the composite through
// slug.Validate, which would wrongly reject the legal "__" name.
func TestCreateEpicTaskRejectsBadComponent(t *testing.T) {
	isolateGit(t)
	root := initRepo(t)
	m := New(root)

	cases := []struct {
		name string
		call func() error
	}{
		{"task has space", func() error { _, e := m.CreateEpicTask("ep", "Bad Task", ""); return e }},
		{"task has slash", func() error { _, e := m.CreateEpicTask("ep", "a/b", ""); return e }},
		{"task traversal", func() error { _, e := m.CreateEpicTask("ep", "..", ""); return e }},
		{"epic bad", func() error { _, e := m.CreateEpicTask("Bad", "t1", ""); return e }},
		{"integration epic bad", func() error { _, e := m.CreateEpicIntegration("Bad", ""); return e }},
	}
	for _, c := range cases {
		if err := c.call(); err == nil {
			t.Errorf("%s: expected an error, got nil", c.name)
		}
	}

	// No side effects: .wt holds nothing, and only main exists as a branch.
	if ents, err := os.ReadDir(filepath.Join(root, ".wt")); err == nil && len(ents) != 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf(".wt should be empty after rejected creates, found: %v", names)
	}
	if br := runGitT(t, root, "branch", "--format=%(refname:short)"); br != "main" {
		t.Errorf("branches after rejected creates = %q, want only main", br)
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
		"worktree /home/u/code/repo/.wt/alpha",
		"HEAD 2222222222222222222222222222222222222222",
		"branch refs/heads/alpha",
		"",
		"worktree /home/u/code/repo/.wt/detached",
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
	if entries[1].Branch != "alpha" {
		t.Errorf("entries[1].Branch = %q, want alpha", entries[1].Branch)
	}
	if entries[2].Branch != "(detached)" {
		t.Errorf("entries[2].Branch = %q, want (detached)", entries[2].Branch)
	}
	if entries[1].Path != "/home/u/code/repo/.wt/alpha" {
		t.Errorf("entries[1].Path = %q", entries[1].Path)
	}
}
