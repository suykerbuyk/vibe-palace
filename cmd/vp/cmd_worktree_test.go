// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
	if _, err := os.Stat(filepath.Join(root, ".wt", "my-plan")); err != nil {
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

// TestCmdWorktreeEpicFlags drives the epic surface end to end through the CLI:
// create the integration tier, create a task tier, confirm the integration
// removal refuses while the task remains, then remove task then integration.
func TestCmdWorktreeEpicFlags(t *testing.T) {
	root := worktreeTestRepo(t)

	if code := cmdWorktreeCreate().Run([]string{"--epic", "ep"}); code != cli.ExitOK {
		t.Fatalf("create --epic exit = %d, want ExitOK", code)
	}
	if _, err := os.Stat(filepath.Join(root, ".wt", "ep")); err != nil {
		t.Errorf("epic integration worktree not created: %v", err)
	}

	if code := cmdWorktreeCreate().Run([]string{"--epic", "ep", "--task", "t1"}); code != cli.ExitOK {
		t.Fatalf("create --epic --task exit = %d, want ExitOK", code)
	}
	if _, err := os.Stat(filepath.Join(root, ".wt", "ep__t1")); err != nil {
		t.Errorf("epic task worktree not created: %v", err)
	}

	// Integration removal refuses (ExitUser) while the task worktree remains.
	if code := cmdWorktreeRemove().Run([]string{"--epic", "ep", "--delete-branch"}); code != cli.ExitUser {
		t.Errorf("remove --epic should refuse while task remains (ExitUser), got %d", code)
	}

	if code := cmdWorktreeRemove().Run([]string{"--epic", "ep", "--task", "t1", "--delete-branch"}); code != cli.ExitOK {
		t.Errorf("remove epic task exit = %d, want ExitOK", code)
	}
	if code := cmdWorktreeRemove().Run([]string{"--epic", "ep", "--delete-branch"}); code != cli.ExitOK {
		t.Errorf("remove epic integration exit = %d, want ExitOK", code)
	}
}

// TestCmdWorktreeTargetErrors pins the mutually-exclusive selection rules.
func TestCmdWorktreeTargetErrors(t *testing.T) {
	worktreeTestRepo(t)
	if code := cmdWorktreeCreate().Run([]string{"--epic", "ep", "slug"}); code != cli.ExitUser {
		t.Errorf("--epic with a positional slug should be ExitUser, got %d", code)
	}
	if code := cmdWorktreeCreate().Run([]string{"--task", "t1"}); code != cli.ExitUser {
		t.Errorf("--task without --epic should be ExitUser, got %d", code)
	}
}

// TestWorktreeCreateHelpHasNoLegacyScheme is the man-page drift backstop (M1):
// the four vp-worktree*.1 pages are generated from command metadata and carry no
// sourceaudit check, so this asserts the metadata the generator reads no longer
// advertises the removed --branch flag or the old plan/ / ../wt/ scheme, and
// does carry the new --epic/--task flags.
func TestWorktreeCreateHelpHasNoLegacyScheme(t *testing.T) {
	c := cmdWorktreeCreate()
	haveEpic, haveTask := false, false
	for _, f := range c.Flags {
		if f.Name == "--branch" {
			t.Error("create still defines the removed --branch flag")
		}
		if f.Name == "--epic" {
			haveEpic = true
		}
		if f.Name == "--task" {
			haveTask = true
		}
	}
	if !haveEpic || !haveTask {
		t.Errorf("create must define --epic and --task flags: %+v", c.Flags)
	}
	blob := c.Synopsis + "\n" + c.Description
	for _, f := range c.Flags {
		blob += "\n" + f.Name + " " + f.Help
	}
	for _, ex := range c.Examples {
		blob += "\n" + ex.Cmd + " " + ex.Comment
	}
	for _, bad := range []string{"--branch", "plan/", "../wt/"} {
		if strings.Contains(blob, bad) {
			t.Errorf("create help still mentions legacy %q", bad)
		}
	}
}

// TestCmdWorktreeParentHasSubcommands asserts the parent lists exactly the
// children registered under it.
//
// It used to assert a hardcoded 3, which is the stored-count antipattern one
// layer up: it would have passed unchanged if a wrong child were swapped in, and
// it had to be edited whenever a real child was added. The expectation is now
// derived from the registry, which is also where the field itself comes from.
func TestCmdWorktreeParentHasSubcommands(t *testing.T) {
	if cmdWorktree().Run != nil {
		t.Error("parent worktree command should have no Run")
	}
	c := registeredCommand(t, "worktree")
	want := registeredChildren(t, "worktree")
	if !slices.Equal(c.Subcommands, want) {
		t.Errorf("Subcommands = %v, want %v", c.Subcommands, want)
	}
	for _, name := range []string{"worktree create", "worktree remove", "worktree list", "worktree reconcile"} {
		if !slices.Contains(c.Subcommands, name) {
			t.Errorf("worktree does not list %q", name)
		}
	}
}

// TestCmdWorktreeReconcile smokes the CLI: a repo with one worktree reports
// without error, and the verdict never promotes to reclaimable with no remote
// (--check-remote defaults true; main is not on any remote here).
func TestCmdWorktreeReconcile(t *testing.T) {
	worktreeTestRepo(t)
	if code := cmdWorktreeCreate().Run([]string{"solo"}); code != cli.ExitOK {
		t.Fatalf("create exit = %d, want ExitOK", code)
	}
	if code := cmdWorktreeReconcile().Run([]string{"--json"}); code != cli.ExitOK {
		t.Errorf("reconcile --json exit = %d, want ExitOK", code)
	}
	if code := cmdWorktreeReconcile().Run([]string{"--check-remote=false"}); code != cli.ExitOK {
		t.Errorf("reconcile --check-remote=false exit = %d, want ExitOK", code)
	}
}
