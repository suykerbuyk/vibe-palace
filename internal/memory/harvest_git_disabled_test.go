// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package memory

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/memorytestutil"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func harvestHostConfig(t *testing.T, body string) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	p := filepath.Join(xdg, "vibe-palace", "config.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// seededGitVault is newGitVault with one commit, so HEAD can be compared.
func seededGitVault(t *testing.T) (root, head string) {
	t.Helper()
	root = newGitVault(t)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "README.md")
	gitRun(t, root, "commit", "-m", "seed")
	return root, revParseHEAD(t, root)
}

func revParseHEAD(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// TestHarvest_GitDisabledRoutesAndSkipsTheCommit is acceptance item 4 for the
// harvest (and so the SessionEnd hook): the memory files are routed into the
// vault, HEAD does not move, the Result says skipped with no error, and no git
// process runs at all.
func TestHarvest_GitDisabledRoutesAndSkipsTheCommit(t *testing.T) {
	vaultRoot, head := seededGitVault(t)
	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	harvestHostConfig(t, "git_enabled = false\n")
	if runtime.GOOS == "windows" {
		t.Skip("the git stub is a shell script")
	}
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "git-was-spawned")
	if err := os.WriteFile(filepath.Join(bin, "git"),
		[]byte("#!/bin/sh\necho \"$@\" >> '"+marker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	realPATH := os.Getenv("PATH")
	t.Setenv("PATH", bin)

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir, Push: true})
	t.Setenv("PATH", realPATH)
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if res.CommitState != "skipped" || res.Committed {
		t.Errorf("CommitState = %q, Committed = %v; want skipped, false", res.CommitState, res.Committed)
	}
	if !strings.Contains(res.CommitDetail, "git is disabled") {
		t.Errorf("CommitDetail = %q, want the refusal", res.CommitDetail)
	}
	if len(res.Routed) != 3 {
		t.Errorf("routed = %v, want the three typed memories", res.Routed)
	}
	if spawned, err := os.ReadFile(marker); err == nil {
		t.Errorf("harvest started git on a disabled host:\n%s", spawned)
	}
	if got := revParseHEAD(t, vaultRoot); got != head {
		t.Errorf("HEAD moved under git_enabled = false")
	}
}

// TestHarvest_UnreadableGitSettingKeepsTheResult: the Result is returned
// together with the wrapped error, never nil — the SessionEnd hook logs the
// error and the routed files are still accounted for.
func TestHarvest_UnreadableGitSettingKeepsTheResult(t *testing.T) {
	vaultRoot, head := seededGitVault(t)
	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	cfgPath := harvestHostConfig(t, "GIT_ENABLED = false\n")

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir, Push: true})
	if !errors.Is(err, storage.ErrGitConfigUnreadable) {
		t.Fatalf("err = %v, want ErrGitConfigUnreadable", err)
	}
	if res == nil {
		t.Fatal("Harvest returned a nil Result with the error")
	}
	if res.CommitState != "config_unreadable" || !strings.Contains(res.CommitDetail, cfgPath) {
		t.Errorf("CommitState = %q, CommitDetail = %q; want config_unreadable naming %s", res.CommitState, res.CommitDetail, cfgPath)
	}
	if len(res.Routed) != 3 {
		t.Errorf("routed = %v, want the three typed memories", res.Routed)
	}
	if got := revParseHEAD(t, vaultRoot); got != head {
		t.Errorf("HEAD moved under an unreadable setting")
	}
}

// TestHarvest_GitEnabledControlCommits: the same fixture with git enabled
// moves HEAD, so the unchanged HEAD above is the refusal, not the fixture.
func TestHarvest_GitEnabledControlCommits(t *testing.T) {
	vaultRoot, head := seededGitVault(t)
	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	harvestHostConfig(t, "git_enabled = true\n")
	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir, Push: true})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if !res.Committed || res.CommitState != "" {
		t.Errorf("Committed = %v, CommitState = %q; want a commit", res.Committed, res.CommitState)
	}
	if got := revParseHEAD(t, vaultRoot); got == head {
		t.Error("an enabled harvest left HEAD unchanged")
	}
}
