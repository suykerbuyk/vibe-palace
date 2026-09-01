// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

// TestPrintVaultRoot pins the one definition of the line. Every surface routes
// here, so a change to this string is a change to all of them at once.
func TestPrintVaultRoot(t *testing.T) {
	var buf bytes.Buffer
	printVaultRoot(&buf, "/srv/palace-vault")
	if got, want := buf.String(), "Vault: /srv/palace-vault\n"; got != want {
		t.Errorf("printVaultRoot() = %q, want %q", got, want)
	}
}

// assertAbove is the ORDER assertion this whole task turns on. A presence-only
// check passes on the broken arrangement — a root printed BELOW the relative
// paths it qualifies does not close the reporting gap — so every caller here
// compares positions rather than asking whether both strings exist.
func assertAbove(t *testing.T, out, above, below string) {
	t.Helper()
	i := strings.Index(out, above)
	if i < 0 {
		t.Fatalf("output missing %q; got:\n%s", above, out)
	}
	j := strings.Index(out, below)
	if j < 0 {
		t.Fatalf("output missing %q; got:\n%s", below, out)
	}
	if i > j {
		t.Errorf("%q must appear ABOVE %q, but it is below it; got:\n%s", above, below, out)
	}
}

// pvrGitVault builds a real temp vault bound through the environment, git-inits
// it, and seeds one commit. Tests act on the vault they built rather than on a
// canned string, so the root they assert is the root the command actually
// resolved.
func pvrGitVault(t *testing.T, remote bool) string {
	t.Helper()
	vaultDir := setupTestVaultEnv(t)
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", vaultDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable or failed (%v): %s", err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	if remote {
		run("remote", "add", "origin", "https://example.com/repo.git")
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	run("add", "seed.txt")
	run("commit", "-m", "seed")
	return vaultDir
}

// seedSweepAndDirt writes one sweepable capture artifact and one piece of
// non-artifact dirt, so both halves of the sweep/report split are non-empty and
// the root has real relative paths to sit above.
func seedSweepAndDirt(t *testing.T, vaultDir string) (artifact, dirt string) {
	t.Helper()
	artifact = "Projects/vibe-palace/sessions/2026-08-31.md"
	dirt = "Projects/vibe-palace/resume.md"
	mkfile(t, vaultDir, artifact, "session\n")
	mkfile(t, vaultDir, dirt, "resume\n")
	return artifact, dirt
}

func TestVaultTidyDryRunPrintsRootAboveTheDirt(t *testing.T) {
	vaultDir := pvrGitVault(t, false)
	artifact, dirt := seedSweepAndDirt(t, vaultDir)

	var code int
	out := captureStdout(t, func() {
		code = cmdVaultTidy().Run([]string{"--dry-run"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d; out:\n%s", code, cli.ExitOK, out)
	}

	root := "Vault: " + vaultDir
	if !strings.Contains(out, root) {
		t.Fatalf("output missing %q; got:\n%s", root, out)
	}
	// The root qualifies every relative path below it, both halves of the split.
	assertAbove(t, out, root, "Would sweep")
	assertAbove(t, out, root, artifact)
	assertAbove(t, out, root, "Reported")
	assertAbove(t, out, root, dirt)
}

func TestVaultTidyApplyPrintsRootAboveTheDirt(t *testing.T) {
	vaultDir := pvrGitVault(t, false)
	_, dirt := seedSweepAndDirt(t, vaultDir)

	// --no-push keeps the apply path off the network; it still commits the
	// sweep and still prints the Reported block the root has to sit above.
	var code int
	out := captureStdout(t, func() {
		code = cmdVaultTidy().Run([]string{"--no-push"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d; out:\n%s", code, cli.ExitOK, out)
	}

	root := "Vault: " + vaultDir
	if !strings.Contains(out, root) {
		t.Fatalf("output missing %q; got:\n%s", root, out)
	}
	// On the apply path the sweep line comes first, so the root must precede
	// that too — it is the first line of the command's output, not merely the
	// line above the Reported block.
	assertAbove(t, out, root, "Swept")
	assertAbove(t, out, root, "Reported")
	assertAbove(t, out, root, dirt)
}

func TestVaultSyncDryRunPrintsRootAboveTheDirt(t *testing.T) {
	vaultDir := pvrGitVault(t, true)
	artifact, _ := seedSweepAndDirt(t, vaultDir)

	var code int
	out := captureStdout(t, func() {
		code = cmdVaultSync().Run([]string{"--dry-run"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, want %d; out:\n%s", code, cli.ExitOK, out)
	}

	root := "Vault: " + vaultDir
	if !strings.Contains(out, root) {
		t.Fatalf("output missing %q; got:\n%s", root, out)
	}
	assertAbove(t, out, root, "Would sweep")
	assertAbove(t, out, root, artifact)
}

// TestVaultStatusPrintsExactlyOneRootLine guards the conversion of the
// `vault status` site: it already printed this line, and unifying the spelling
// must not have added a second copy.
func TestVaultStatusPrintsExactlyOneRootLine(t *testing.T) {
	vaultDir := pvrGitVault(t, false)

	out := captureStdout(t, func() {
		cmdVaultStatus().Run([]string{"--no-fetch"})
	})
	if n := strings.Count(out, "Vault: "); n != 1 {
		t.Errorf("vault status printed %d root lines, want exactly 1; got:\n%s", n, out)
	}
	if !strings.Contains(out, "Vault: "+vaultDir) {
		t.Errorf("output missing %q; got:\n%s", "Vault: "+vaultDir, out)
	}
}
