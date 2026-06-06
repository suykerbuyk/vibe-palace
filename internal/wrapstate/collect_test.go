// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"context"
	"os"
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
