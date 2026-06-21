// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntegration_VaultCommitTolerateMissingPath proves the full `vault commit`
// stack (CLI → storage.CommitAndPushPaths → git) tolerates an absent path in the
// supplied --paths list against a REAL `vp` binary and a REAL git vault. This is
// the iter-134 wrap regression: a never-written `Projects/<slug>/memory/` dir
// listed unconditionally by /wrap used to make `git add` exit 128 and abort the
// whole commit. The absent path must be SKIPPED (reported on stderr), and the
// real file (resume.md) must still land in a commit.
func TestIntegration_VaultCommitTolerateMissingPath(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the real vp binary against a git vault; skipped under -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bin := buildVPBinary(t)

	// Isolated HOME + XDG so the child `vp` resolves our temp vault via the
	// global config, exactly as a real install would.
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	if err := os.MkdirAll(filepath.Join(xdg, "vibe-palace"), 0o755); err != nil {
		t.Fatal(err)
	}
	vaultRoot := filepath.Join(home, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `vault_path = "` + vaultRoot + `"` + "\ngit_enabled = true\n"
	if err := os.WriteFile(filepath.Join(xdg, "vibe-palace", "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	// Real git vault with a committer identity.
	run(t, vaultRoot, "git", "init", "-q")
	run(t, vaultRoot, "git", "config", "user.email", "test@example.com")
	run(t, vaultRoot, "git", "config", "user.name", "Test")

	// Write the real file in the worktree; commit nothing yet. The memory dir is
	// deliberately NEVER created — it is the absent path that used to fatal.
	writeFile(t, filepath.Join(vaultRoot, "Projects/demo/resume.md"), "resume\n")

	childEnv := append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+xdg)

	// Run the real binary exactly as /wrap would: a real file plus a never-written
	// absent dir in the same --paths list.
	cmd := exec.Command(bin, "vault", "commit",
		"--paths", "Projects/demo/resume.md,Projects/demo/memory",
		"--message", "wrap demo")
	cmd.Dir = home
	cmd.Env = childEnv
	out, err := cmd.CombinedOutput()

	// 1) Must exit 0. Before the fix this failed with `git add: exit status 128`.
	if err != nil {
		t.Fatalf("vault commit must exit 0 with an absent path in --paths, got: %v\n%s", err, out)
	}

	output := string(out)

	// 2) The absent path is reported as skipped on stderr.
	if !strings.Contains(output, "Skipped (absent): Projects/demo/memory") {
		t.Errorf("expected skipped-path notice for the absent memory dir, output:\n%s", output)
	}

	// 3) resume.md is committed: tracked in the index, and a "wrap demo" commit
	//    sits at HEAD.
	tracked := gitOutput(t, vaultRoot, "ls-files", "Projects/demo/resume.md")
	if tracked == "" {
		t.Errorf("resume.md should be committed/tracked, ls-files empty; output:\n%s", output)
	}
	if subj := gitOutput(t, vaultRoot, "log", "-1", "--pretty=%s"); !strings.Contains(subj, "wrap demo") {
		t.Errorf("expected a 'wrap demo' commit at HEAD, got subject %q", subj)
	}

	// 4) The absent memory path was neither created on disk nor committed.
	if _, statErr := os.Lstat(filepath.Join(vaultRoot, "Projects/demo/memory")); !os.IsNotExist(statErr) {
		t.Errorf("absent memory path must not be created on disk, stat err = %v", statErr)
	}
	if memTracked := gitOutput(t, vaultRoot, "ls-files", "Projects/demo/memory"); memTracked != "" {
		t.Errorf("absent memory path must not be committed, ls-files = %q", memTracked)
	}
}

// gitOutput runs a git command against the vault and returns its trimmed
// stdout, failing the test on a git error.
func gitOutput(t *testing.T, vault string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", vault}, args...)
	out, err := exec.Command("git", full...).Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}
