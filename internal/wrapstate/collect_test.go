// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// fakeGit installs a gitCmdRunner stub for the duration of the test. The stub
// dispatches on the first git subcommand argument.
func fakeGit(t *testing.T, fn func(args []string) (string, error)) {
	t.Helper()
	old := gitCmdRunner
	gitCmdRunner = func(_ context.Context, _ string, args ...string) (string, error) {
		return fn(args)
	}
	t.Cleanup(func() { gitCmdRunner = old })
}

// mkGitDir returns a fresh temp dir containing an empty .git marker so the
// stat-guarded probes proceed to the (stubbed) git invocation.
func mkGitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveProjectRoot(t *testing.T) {
	root := mkGitDir(t)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ResolveProjectRoot(sub); got != root {
		t.Errorf("ResolveProjectRoot(%q) = %q, want %q", sub, got, root)
	}
	// No .git anywhere ⇒ returns start unchanged.
	nogit := t.TempDir()
	if got := ResolveProjectRoot(nogit); got != nogit {
		t.Errorf("no-git ResolveProjectRoot = %q, want %q", got, nogit)
	}
	if got := ResolveProjectRoot(""); got != "" {
		t.Errorf("empty start = %q, want empty", got)
	}
}

func TestCommitsSinceAnchor(t *testing.T) {
	fakeGit(t, func(args []string) (string, error) {
		if args[0] != "log" {
			t.Fatalf("unexpected git args: %v", args)
		}
		return "abc123 first subject with spaces\ndef456 second\n", nil
	})
	commits, err := CommitsSinceAnchor(context.Background(), "/repo", "anchor")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("want 2 commits, got %d", len(commits))
	}
	if commits[0].SHA != "abc123" || commits[0].Subject != "first subject with spaces" {
		t.Errorf("commit[0] = %+v", commits[0])
	}
}

func TestCommitsSinceAnchor_EmptyAnchor(t *testing.T) {
	commits, err := CommitsSinceAnchor(context.Background(), "/repo", "")
	if err != nil || commits != nil {
		t.Errorf("empty anchor: got (%v, %v), want (nil, nil)", commits, err)
	}
}

func TestFilesChangedSinceAnchor(t *testing.T) {
	fakeGit(t, func(args []string) (string, error) {
		if args[0] != "diff" {
			t.Fatalf("unexpected git args: %v", args)
		}
		return "a.go\nb.go\n\n", nil
	})
	files, err := FilesChangedSinceAnchor(context.Background(), "/repo", "anchor")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0] != "a.go" || files[1] != "b.go" {
		t.Errorf("files = %v", files)
	}
}

func TestOldestRootCommit(t *testing.T) {
	fakeGit(t, func(args []string) (string, error) {
		if args[0] != "rev-list" {
			t.Fatalf("unexpected git args: %v", args)
		}
		// newest-first; last line is the oldest root.
		return "newroot\noldroot\n", nil
	})
	sha, err := OldestRootCommit(context.Background(), "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if sha != "oldroot" {
		t.Errorf("oldest root = %q, want oldroot", sha)
	}
}

func TestDirtyProbes(t *testing.T) {
	fakeGit(t, func(args []string) (string, error) {
		return " M somefile\n", nil
	})
	repo := mkGitDir(t)
	dirty, err := ProjectHasUncommittedWrites(repo)
	if err != nil || !dirty {
		t.Errorf("project dirty: got (%v, %v), want (true, nil)", dirty, err)
	}
	vdirty, err := VaultHasUncommittedWrites(repo, "demo")
	if err != nil || !vdirty {
		t.Errorf("vault dirty: got (%v, %v), want (true, nil)", vdirty, err)
	}
	// Non-git dir degrades to clean.
	clean, err := ProjectHasUncommittedWrites(t.TempDir())
	if err != nil || clean {
		t.Errorf("non-git project: got (%v, %v), want (false, nil)", clean, err)
	}
}

func TestVaultDirtyByCategory(t *testing.T) {
	repo := mkGitDir(t)
	cases := []struct {
		name          string
		porcelain     string
		wantNonMemory bool
		wantMemory    bool
	}{
		{"clean", "", false, false},
		{"memory only", " M Projects/demo/memory/MEMORY.md\n", false, true},
		{"non-memory only", " M Projects/demo/resume.md\n", true, false},
		{
			"both",
			" M Projects/demo/memory/MEMORY.md\n M Projects/demo/tasks/x.md\n",
			true, true,
		},
		{"added memory file", "A  Projects/demo/memory/new.md\n", false, true},
		{"untracked non-memory", "?? Projects/demo/notes.md\n", true, false},
		{
			"rename into memory classifies by destination",
			"R  Projects/demo/old.md -> Projects/demo/memory/new.md\n",
			false, true,
		},
		{
			"rename out of memory classifies by destination",
			"R  Projects/demo/memory/old.md -> Projects/demo/resume.md\n",
			true, false,
		},
		{
			"quoted memory path",
			" M \"Projects/demo/memory/a b.md\"\n",
			false, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeGit(t, func([]string) (string, error) { return tc.porcelain, nil })
			nonMem, mem, err := VaultDirtyByCategory(repo, "demo")
			if err != nil {
				t.Fatal(err)
			}
			if nonMem != tc.wantNonMemory || mem != tc.wantMemory {
				t.Errorf("got (nonMemory=%v, memory=%v), want (nonMemory=%v, memory=%v)",
					nonMem, mem, tc.wantNonMemory, tc.wantMemory)
			}
		})
	}

	// Non-repo degrades to (false, false).
	fakeGit(t, func([]string) (string, error) { return " M Projects/demo/memory/x.md\n", nil })
	nonMem, mem, err := VaultDirtyByCategory(t.TempDir(), "demo")
	if err != nil || nonMem || mem {
		t.Errorf("non-repo: got (%v, %v, %v), want (false, false, nil)", nonMem, mem, err)
	}
}

func TestCollect_Integration(t *testing.T) {
	projectRoot := mkGitDir(t)
	vaultRoot := t.TempDir()

	// iterations.md in the vault.
	iterPath := filepath.Join(vaultRoot, "Projects", "demo", "iterations.md")
	if err := os.MkdirAll(filepath.Dir(iterPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(iterPath, []byte("### Iteration 5 — prior\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// vault tasks dir with one active task.
	tasksDir := filepath.Join(vaultRoot, "Projects", "demo", "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "new-task.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// doc/TESTING.md with a parseable headline.
	docDir := filepath.Join(projectRoot, "doc")
	if err := os.MkdirAll(docDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docDir, "TESTING.md"), []byte("**100 tests** total\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeGit(t, func(args []string) (string, error) {
		switch args[0] {
		case "rev-parse":
			return "feature-branch\n", nil
		case "log":
			// LastIterAnchorSha (log -n 1 -- .vibe-palace/last-iter): no anchor.
			if len(args) > 1 && args[1] == "-n" {
				return "", nil
			}
			// CommitsSinceAnchor.
			return "sha1 a commit\n", nil
		case "rev-list":
			return "rootsha\n", nil
		case "diff":
			return "x.go\n", nil
		case "status":
			return "", nil
		}
		return "", nil
	})

	res, err := Collect(context.Background(), CollectInput{
		VaultRoot:      vaultRoot,
		Project:        "demo",
		IterationsPath: iterPath,
		TasksDir:       tasksDir,
		ProjectRoot:    projectRoot,
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.IterN != 6 {
		t.Errorf("IterN = %d, want 6", res.IterN)
	}
	if res.Branch != "feature-branch" {
		t.Errorf("Branch = %q", res.Branch)
	}
	if len(res.CommitsSinceLastIter) != 1 {
		t.Errorf("commits = %v", res.CommitsSinceLastIter)
	}
	if !reflect.DeepEqual(res.TaskDeltas.Added, []string{"new-task"}) {
		t.Errorf("added = %v, want [new-task]", res.TaskDeltas.Added)
	}
	if res.TestCounts.Unit != 100 {
		t.Errorf("unit count = %d, want 100", res.TestCounts.Unit)
	}
	// commits present ⇒ fresh-feature.
	if res.Shape != ShapeFreshFeature {
		t.Errorf("shape = %q, want fresh-feature", res.Shape)
	}
}

func TestCollect_MemoryDirtNotNagWorthy(t *testing.T) {
	projectRoot := mkGitDir(t)
	vaultRoot := mkGitDir(t)

	iterPath := filepath.Join(vaultRoot, "Projects", "demo", "iterations.md")
	if err := os.MkdirAll(filepath.Dir(iterPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(iterPath, []byte("### Iteration 1 — x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tasksDir := filepath.Join(vaultRoot, "Projects", "demo", "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	fakeGit(t, func(args []string) (string, error) {
		switch args[0] {
		case "status":
			// Only a memory file is dirty in the vault.
			return " M Projects/demo/memory/MEMORY.md\n", nil
		default:
			return "", nil
		}
	})

	res, err := Collect(context.Background(), CollectInput{
		VaultRoot:      vaultRoot,
		Project:        "demo",
		IterationsPath: iterPath,
		TasksDir:       tasksDir,
		ProjectRoot:    projectRoot,
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.VaultHasUncommittedWrites {
		t.Error("memory-only dirt must not set VaultHasUncommittedWrites")
	}
	if !res.MemoryHasUncommittedWrites {
		t.Error("memory-only dirt must set MemoryHasUncommittedWrites")
	}
}

func TestPreflight_MemoryDirt(t *testing.T) {
	oldChk := surfaceCheckCompatible
	surfaceCheckCompatible = func(string) error { return nil }
	t.Cleanup(func() { surfaceCheckCompatible = oldChk })

	// Memory-only vault dirt, clean project: no vault_dirty warning, note
	// present, ok stays true.
	t.Run("memory only", func(t *testing.T) {
		fakeGit(t, func(args []string) (string, error) {
			if args[0] == "status" {
				// project dir status is empty; the vault scoped status carries
				// the memory dirt. The stub can't distinguish dirs, so both
				// calls return the same — project_dirty would also fire here,
				// so scope the memory assertion to vault_dirty only.
				return " M Projects/demo/memory/MEMORY.md\n", nil
			}
			return "", nil
		})
		res := Preflight(mkGitDir(t), mkGitDir(t), "demo")
		if !res.OK {
			t.Error("memory dirt must not flip ok off")
		}
		for _, w := range res.Warnings {
			if w.Check == "vault_dirty" {
				t.Errorf("memory-only dirt must not emit vault_dirty warning; warnings=%+v", res.Warnings)
			}
		}
		found := false
		for _, n := range res.Notes {
			if n.Check == "memory_dirty" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected memory_dirty note, got notes=%+v", res.Notes)
		}
	})

	// Non-memory vault dirt: vault_dirty warning present.
	t.Run("non-memory dirt", func(t *testing.T) {
		fakeGit(t, func(args []string) (string, error) {
			if args[0] == "status" {
				return " M Projects/demo/resume.md\n", nil
			}
			return "", nil
		})
		res := Preflight(mkGitDir(t), mkGitDir(t), "demo")
		found := false
		for _, w := range res.Warnings {
			if w.Check == "vault_dirty" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected vault_dirty warning, got warnings=%+v", res.Warnings)
		}
	})
}

func TestPreflight_Matrix(t *testing.T) {
	// Clean surface + clean trees ⇒ ok, no errors/warnings.
	t.Run("all clean", func(t *testing.T) {
		oldChk := surfaceCheckCompatible
		surfaceCheckCompatible = func(string) error { return nil }
		t.Cleanup(func() { surfaceCheckCompatible = oldChk })
		fakeGit(t, func([]string) (string, error) { return "", nil })

		res := Preflight(t.TempDir(), mkGitDir(t), "demo")
		if !res.OK || len(res.Errors) != 0 || len(res.Warnings) != 0 {
			t.Errorf("clean preflight = %+v", res)
		}
	})

	// Surface incompatible ⇒ error, ok=false.
	t.Run("surface incompatible", func(t *testing.T) {
		oldChk := surfaceCheckCompatible
		surfaceCheckCompatible = func(string) error {
			return &surface.IncompatibleError{BinarySurface: 1, VaultSurface: 9, StampDir: "/v/Projects/demo"}
		}
		t.Cleanup(func() { surfaceCheckCompatible = oldChk })
		fakeGit(t, func([]string) (string, error) { return "", nil })

		res := Preflight(t.TempDir(), mkGitDir(t), "demo")
		if res.OK {
			t.Error("incompatible surface should set ok=false")
		}
		if len(res.Errors) != 1 || res.Errors[0].Check != "surface" {
			t.Errorf("errors = %+v", res.Errors)
		}
		if !strings.Contains(res.Errors[0].Detail, "v1 < vault v9") {
			t.Errorf("detail = %q", res.Errors[0].Detail)
		}
	})

	// Dirty vault + dirty project ⇒ warnings only, ok stays true.
	t.Run("dirty warns but ok", func(t *testing.T) {
		oldChk := surfaceCheckCompatible
		surfaceCheckCompatible = func(string) error { return nil }
		t.Cleanup(func() { surfaceCheckCompatible = oldChk })
		fakeGit(t, func([]string) (string, error) { return " M f\n", nil })

		vault := mkGitDir(t)
		res := Preflight(vault, mkGitDir(t), "demo")
		if !res.OK {
			t.Error("dirty trees must not flip ok off")
		}
		checks := map[string]bool{}
		for _, w := range res.Warnings {
			checks[w.Check] = true
		}
		if !checks["vault_dirty"] || !checks["project_dirty"] {
			t.Errorf("expected vault_dirty + project_dirty warnings, got %+v", res.Warnings)
		}
	})
}

// realGitRepo builds an actual git repo in a fresh temp dir (init + one empty
// commit) so the unstubbed gitCmdRunner probes real git. When dirty, an
// untracked file is left behind — `git status --porcelain` reports untracked
// files, which is exactly the "any dirt at all" signal ProjectGitState wants.
func realGitRepo(t *testing.T, dirty bool) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"git", "-C", dir, "init"},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
		{"git", "-C", dir, "commit", "--allow-empty", "-m", "root"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	if dirty {
		if err := os.WriteFile(filepath.Join(dir, "dirt.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// mkSubdir creates (and returns) a nested subdirectory of dir.
func mkSubdir(t *testing.T, dir string) string {
	t.Helper()
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	return sub
}

// gitWorktree adds a linked worktree of repo whose `.git` is a FILE, not a
// directory — the case an info.IsDir() check would wrongly reject.
func gitWorktree(t *testing.T, repo string) string {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "wt")
	cmd := exec.Command("git", "-C", repo, "worktree", "add", "-b", "wt-branch", wt)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	info, err := os.Lstat(filepath.Join(wt, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		t.Fatalf("precondition failed: worktree .git is a dir, expected a file")
	}
	return wt
}

// TestProjectGitState exercises the three-valued probe against real git repos.
// Every case first passes through ResolveProjectRoot, mirroring what the
// vp_ingest_commit_msg handler does, so subdirectory inputs must land on the
// repo root and report the root's state.
func TestProjectGitState(t *testing.T) {
	cases := []struct {
		name string
		dir  func(t *testing.T) string
		want GitState
	}{
		{"not a repo", func(t *testing.T) string { return t.TempDir() }, GitNotARepo},
		{"not a repo, subdir", func(t *testing.T) string { return mkSubdir(t, t.TempDir()) }, GitNotARepo},
		{"clean repo", func(t *testing.T) string { return realGitRepo(t, false) }, GitClean},
		{"clean repo, subdir", func(t *testing.T) string { return mkSubdir(t, realGitRepo(t, false)) }, GitClean},
		{"dirty repo", func(t *testing.T) string { return realGitRepo(t, true) }, GitDirty},
		{"dirty repo, subdir", func(t *testing.T) string { return mkSubdir(t, realGitRepo(t, true)) }, GitDirty},
		{"worktree (.git is a file)", func(t *testing.T) string { return gitWorktree(t, realGitRepo(t, false)) }, GitClean},
		{"worktree subdir", func(t *testing.T) string { return mkSubdir(t, gitWorktree(t, realGitRepo(t, false))) }, GitClean},
		{"empty dir arg", func(t *testing.T) string { return "" }, GitNotARepo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := ResolveProjectRoot(tc.dir(t))
			got, err := ProjectGitState(root)
			if err != nil {
				t.Fatalf("ProjectGitState(%q): %v", root, err)
			}
			if got != tc.want {
				t.Errorf("ProjectGitState(%q) = %v, want %v", root, got, tc.want)
			}
		})
	}
}

// TestProjectGitState_ShallowOnSubdir locks in why callers MUST resolve the
// root first: the probe stats `.git` in dir itself and does not walk upward.
func TestProjectGitState_ShallowOnSubdir(t *testing.T) {
	sub := mkSubdir(t, realGitRepo(t, true))
	got, err := ProjectGitState(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != GitNotARepo {
		t.Errorf("unresolved subdir = %v, want %v (probe is deliberately shallow)", got, GitNotARepo)
	}
}

// TestProjectGitState_ProbeError checks a failing git invocation surfaces the
// error rather than a confident state — ingest permits on error.
func TestProjectGitState_ProbeError(t *testing.T) {
	fakeGit(t, func([]string) (string, error) {
		return "", errors.New("boom")
	})
	got, err := ProjectGitState(mkGitDir(t))
	if err == nil {
		t.Fatal("expected probe error")
	}
	if got != GitNotARepo {
		t.Errorf("state on error = %v, want indeterminate %v", got, GitNotARepo)
	}
	// The delegating wrapper must keep its old (false, err) contract.
	dirty, err := ProjectHasUncommittedWrites(mkGitDir(t))
	if err == nil || dirty {
		t.Errorf("ProjectHasUncommittedWrites on error = (%v, %v), want (false, err)", dirty, err)
	}
}

// TestProjectHasUncommittedWrites_DelegatesToGitState pins the flattening: the
// wrapper must still collapse not-a-repo and clean alike into false.
func TestProjectHasUncommittedWrites_DelegatesToGitState(t *testing.T) {
	cases := []struct {
		name string
		dir  string
		want bool
	}{
		{"not a repo", t.TempDir(), false},
		{"clean repo", realGitRepo(t, false), false},
		{"dirty repo", realGitRepo(t, true), true},
		{"empty path", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ProjectHasUncommittedWrites(tc.dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("ProjectHasUncommittedWrites(%q) = %v, want %v", tc.dir, got, tc.want)
			}
		})
	}
}

func TestGitStateString(t *testing.T) {
	for state, want := range map[GitState]string{
		GitNotARepo: "not-a-repo",
		GitClean:    "clean",
		GitDirty:    "dirty",
		GitState(9): "unknown",
	} {
		if got := state.String(); got != want {
			t.Errorf("GitState(%d).String() = %q, want %q", state, got, want)
		}
	}
}
