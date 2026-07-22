// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

// worktreeTestRepo builds a temp git repo with one commit on main, chdirs into
// it (so the Run closures resolve it as the project root), and isolates git from
// the developer's global config. Returns the repo root.
func worktreeTestRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
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
	t.Chdir(root)
	return root
}

func TestCmdWorktreeCreateAndList(t *testing.T) {
	root := worktreeTestRepo(t)

	if code := cmdWorktreeCreate().Run([]string{"my-plan"}); code != cli.ExitOK {
		t.Fatalf("create exit = %d, want ExitOK", code)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "wt", "my-plan")); err != nil {
		t.Errorf("worktree not created: %v", err)
	}

	if code := cmdWorktreeList().Run([]string{"--json"}); code != cli.ExitOK {
		t.Errorf("list exit = %d, want ExitOK", code)
	}
	if code := cmdWorktreeRemove().Run([]string{"my-plan", "--delete-branch"}); code != cli.ExitOK {
		t.Errorf("remove exit = %d, want ExitOK", code)
	}
}

func TestCmdWorktreeCreateArgErrors(t *testing.T) {
	worktreeTestRepo(t)
	if code := cmdWorktreeCreate().Run(nil); code != cli.ExitUser {
		t.Errorf("no-arg create exit = %d, want ExitUser", code)
	}
	if code := cmdWorktreeCreate().Run([]string{"a", "b"}); code != cli.ExitUser {
		t.Errorf("two-arg create exit = %d, want ExitUser", code)
	}
	if code := cmdWorktreeCreate().Run([]string{"--bogus", "x"}); code != cli.ExitUser {
		t.Errorf("bad-flag create exit = %d, want ExitUser", code)
	}
	if code := cmdWorktreeCreate().Run([]string{"Bad Slug"}); code != cli.ExitUser {
		t.Errorf("bad-slug create exit = %d, want ExitUser", code)
	}
}

func TestCmdWorktreeRemoveArgError(t *testing.T) {
	worktreeTestRepo(t)
	if code := cmdWorktreeRemove().Run(nil); code != cli.ExitUser {
		t.Errorf("no-arg remove exit = %d, want ExitUser", code)
	}
}

func TestCmdWorktreeParentHasSubcommands(t *testing.T) {
	c := cmdWorktree()
	if c.Run != nil {
		t.Error("parent worktree command should have no Run")
	}
	if len(c.Subcommands) != 3 {
		t.Errorf("subcommands = %d, want 3", len(c.Subcommands))
	}
}
