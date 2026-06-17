// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func TestGitRemotes(t *testing.T) {
	// Create a temp git repo.
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("remote", "add", "origin", "https://example.com/repo.git")
	run("remote", "add", "backup", "https://backup.example.com/repo.git")

	remotes, err := gitRemotes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(remotes) != 2 {
		t.Errorf("expected 2 remotes, got %d: %v", len(remotes), remotes)
	}
}

func TestGitRemotesNoRemotes(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	_, err := gitRemotes(dir)
	if err == nil {
		t.Error("expected error for no remotes")
	}
}

func TestPullAllDryRun(t *testing.T) {
	code := pullAll("/nonexistent", []string{"origin"}, true)
	if code != cli.ExitOK {
		t.Errorf("dry run should succeed: exit code = %d", code)
	}
}

func TestPushAllDirtyState(t *testing.T) {
	// Create a git repo with uncommitted changes.
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")

	// Create a file and add it but don't commit → dirty state.
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644)
	run("add", "file.txt")

	code := pushAll(dir, []string{"origin"}, false)
	if code != cli.ExitUser {
		t.Errorf("expected ExitUser for dirty state, got %d", code)
	}
}

func TestPushAllDryRun(t *testing.T) {
	// Create clean git repo.
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data"), 0o644)
	run("add", "file.txt")
	run("commit", "-m", "init")

	code := pushAll(dir, []string{"origin"}, true)
	if code != cli.ExitOK {
		t.Errorf("dry run should succeed: exit code = %d", code)
	}
}

func TestVaultPullBadFlags(t *testing.T) {
	cmd := cmdVaultPull()
	code := cmd.Run([]string{"--unknown"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

func TestVaultPushBadFlags(t *testing.T) {
	cmd := cmdVaultPush()
	code := cmd.Run([]string{"--unknown"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

func TestVaultSyncBadFlags(t *testing.T) {
	cmd := cmdVaultSync()
	code := cmd.Run([]string{"--unknown"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

func TestVaultTidyBadFlags(t *testing.T) {
	cmd := cmdVaultTidy()
	code := cmd.Run([]string{"--unknown"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

// TestVaultTidyDryRun verifies --dry-run prints the sweep/report split, makes
// clear nothing was committed, and creates no commit.
func TestVaultTidyDryRun(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", vaultDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	os.WriteFile(filepath.Join(vaultDir, "seed.txt"), []byte("seed"), 0o644)
	run("add", "seed.txt")
	run("commit", "-m", "seed")

	headBefore := gitHead(t, vaultDir)

	// One sweepable artifact, one piece of non-artifact dirt.
	mkfile(t, vaultDir, "Projects/vibe-palace/sessions/2026-06-17.md", "session\n")
	mkfile(t, vaultDir, "Projects/vibe-palace/resume.md", "resume\n")

	var code int
	out := captureStdout(t, func() {
		code = cmdVaultTidy().Run([]string{"--dry-run"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d", code, cli.ExitOK)
	}
	for _, want := range []string{
		"Would sweep",
		"Projects/vibe-palace/sessions/2026-06-17.md",
		"Reported",
		"Projects/vibe-palace/resume.md",
		"nothing was committed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q; got:\n%s", want, out)
		}
	}
	if headAfter := gitHead(t, vaultDir); headAfter != headBefore {
		t.Errorf("dry-run created a commit: HEAD %s -> %s", headBefore, headAfter)
	}
}

// gitHead returns the current HEAD commit hash of dir.
func gitHead(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// mkfile writes rel (creating parent dirs) under dir.
func mkfile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVaultRoot(t *testing.T) {
	setupTestVaultEnv(t)
	root, err := vaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	if root == "" {
		t.Error("root is empty")
	}
}

func TestFullVaultPullDryRun(t *testing.T) {
	// Create a vault dir with git repo.
	vaultDir := setupTestVaultEnv(t)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", vaultDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("remote", "add", "origin", "https://example.com/repo.git")

	cmd := cmdVaultPull()
	code := cmd.Run([]string{"--dry-run"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
}

func TestFullVaultPushDryRun(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", vaultDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("remote", "add", "origin", "https://example.com/repo.git")
	os.WriteFile(filepath.Join(vaultDir, "f.txt"), []byte("data"), 0o644)
	run("add", "f.txt")
	run("commit", "-m", "init")

	cmd := cmdVaultPush()
	code := cmd.Run([]string{"--dry-run"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
}

func TestFullVaultSyncDryRun(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", vaultDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("remote", "add", "origin", "https://example.com/repo.git")
	os.WriteFile(filepath.Join(vaultDir, "f.txt"), []byte("data"), 0o644)
	run("add", "f.txt")
	run("commit", "-m", "init")

	cmd := cmdVaultSync()
	code := cmd.Run([]string{"--dry-run"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
}
