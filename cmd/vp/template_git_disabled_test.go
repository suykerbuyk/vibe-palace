// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// disableGitInTestConfig is enableGitInTestConfig's inverse: the operator
// setting under test.
func disableGitInTestConfig(t *testing.T) {
	t.Helper()
	p, err := storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read test host config: %v", err)
	}
	s := strings.Replace(string(data), "git_enabled = true", "git_enabled = false", 1)
	if !strings.Contains(s, "git_enabled = false") {
		s = "git_enabled = false\n" + s
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repoState is HEAD plus a hash of the index: a staged deletion or a commit
// both move it.
func repoState(t *testing.T, repo string) string {
	t.Helper()
	index, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(index)
	return strings.TrimSpace(gitInVault(t, repo, "rev-parse", "HEAD")) + " " + hex.EncodeToString(sum[:])
}

// TestTemplateResetRefusesUpFrontWhenGitIsDisabled is acceptance item 5 for
// template reset: a reset that would commit is refused BEFORE any file is
// removed, so every file is still present and byte-identical and nothing is
// staged — the dry run refuses the same way, with no "would commit" line.
func TestTemplateResetRefusesUpFrontWhenGitIsDisabled(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		name := "real run"
		if dryRun {
			name = "dry run"
		}
		t.Run(name, func(t *testing.T) {
			vaultPath, _, _ := gitResetVault(t)
			disableGitInTestConfig(t)
			target := filepath.Join(vaultPath, "Templates", "commands", "wrap.md")
			before := repoState(t, vaultPath)

			out, errOut, code := runReset(t, vaultPath, "command", dryRun, "wrap")
			if code != cli.ExitUser {
				t.Errorf("exit %d, want %d\nstdout: %s\nstderr: %s", code, cli.ExitUser, out, errOut)
			}
			if !strings.Contains(errOut, "git is disabled") || !strings.Contains(errOut, "nothing was changed") {
				t.Errorf("stderr %q lacks the refusal", errOut)
			}
			if strings.Contains(out, "would commit") {
				t.Errorf("the dry run printed a commit it would refuse:\n%s", out)
			}
			assertFileBytes(t, target, myWrap)
			if after := repoState(t, vaultPath); after != before {
				t.Errorf("HEAD or the index moved: %s -> %s", before, after)
			}
		})
	}
}

// TestConfigSyncSkipsThePruneWholeWhenGitIsDisabled is acceptance item 5 for
// the config prune on a vault that is its own repository: one skip row per
// candidate, and nothing verified, removed, restored or committed.
func TestConfigSyncSkipsThePruneWholeWhenGitIsDisabled(t *testing.T) {
	restart := string(embeddedTemplateBytes(t, "commands/restart.md"))
	wrapMirror := string(embeddedTemplateBytes(t, "commands/wrap.md"))
	vaultPath, projDir, _ := canonicalGitVault(t, func(v string) {
		putVaultFile(t, v, "Templates/commands/restart.md", restart)
		putVaultFile(t, v, "Templates/commands/wrap.md", myWrap)
	})
	seedCommittedOverrideUnderMirror(t, vaultPath, "commands/wrap.md")
	disableGitInTestConfig(t)
	before := repoState(t, vaultPath)

	out := syncVault(t, projDir, "", "--yes")

	for _, rel := range []string{"Templates/commands/restart.md", "Templates/commands/wrap.md"} {
		if !strings.Contains(out, rel+" — skipped: git is disabled (git_enabled = false) — not verified, removed or restored") {
			t.Errorf("no skip row for %s:\n%s", rel, out)
		}
	}
	assertFileBytes(t, filepath.Join(vaultPath, "Templates", "commands", "restart.md"), restart)
	assertFileBytes(t, filepath.Join(vaultPath, "Templates", "commands", "wrap.md"), wrapMirror)
	if after := repoState(t, vaultPath); after != before {
		t.Errorf("HEAD or the index moved: %s -> %s", before, after)
	}
}

// TestConfigSyncSkipsTheNestedPruneWholeWhenGitIsDisabled: a vault nested in
// another repository goes through PruneMirrorsInEnclosingRepo, which never
// commits but deletes an untracked mirror and restores one over committed
// operator content on git's verdict. Under git_enabled = false it does
// neither: both files stay byte-identical, and the enclosing repository's
// HEAD and index do not move.
func TestConfigSyncSkipsTheNestedPruneWholeWhenGitIsDisabled(t *testing.T) {
	_, _ = initTestEnv(t, false)
	projDir := t.TempDir()
	markProjectDir(t, projDir)
	parent := t.TempDir()
	gitInVault(t, parent, "init", "-q", "-b", "main")
	gitInVault(t, parent, "config", "user.email", "test@test.com")
	gitInVault(t, parent, "config", "user.name", "Test")
	vaultPath := filepath.Join(parent, "notes", "vault")
	putVaultFile(t, vaultPath, "Templates/commands/wrap.md", myWrap)
	putVaultFile(t, parent, ".gitignore", ".vp-locks/\n*.bak\n")
	gitInVault(t, parent, "add", "-A")
	gitInVault(t, parent, "commit", "-qm", "project")

	// --no-git writes git_enabled = false: the setting under test.
	if code := cmdInit(cli.BuildInfo{Version: "test"}).Run(
		[]string{projDir, "--name", "ovr", "--vault-path", vaultPath, "--no-git"}); code != cli.ExitOK {
		t.Fatalf("init exit code = %d", code)
	}
	cwd, _ := os.Getwd()
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	wrapMirror := string(embeddedTemplateBytes(t, "commands/wrap.md"))
	restart := string(embeddedTemplateBytes(t, "commands/restart.md"))
	seedCommittedOverrideUnderMirror(t, vaultPath, "commands/wrap.md")   // over committed operator content
	putVaultFile(t, vaultPath, "Templates/commands/restart.md", restart) // untracked mirror
	before := repoState(t, parent)

	out := syncVault(t, projDir, "", "--yes")

	if !strings.Contains(out, "skipped: git is disabled") {
		t.Errorf("no skip rows:\n%s", out)
	}
	assertFileBytes(t, filepath.Join(vaultPath, "Templates", "commands", "restart.md"), restart)
	assertFileBytes(t, filepath.Join(vaultPath, "Templates", "commands", "wrap.md"), wrapMirror)
	if after := repoState(t, parent); after != before {
		t.Errorf("the enclosing repository moved: %s -> %s", before, after)
	}
}
