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

func TestVaultParentShowsHelp(t *testing.T) {
	cmd := cmdVault()
	code := cmd.Run(nil)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
}

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
