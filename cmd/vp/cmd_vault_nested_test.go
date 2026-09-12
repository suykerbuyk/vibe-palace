// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

// nestedVaultProbeLabels names the six fingerprint probes, in order, that
// buildNestedVaultCLIFixture's state() takes of the enclosing repository —
// the same six TestConfigSyncNeverWritesAnEnclosingRepo uses
// (cmd_config_override_test.go). The for-each-ref probe is the one a
// read-only `git fetch` may legitimately change (it can create or refresh
// remote-tracking refs); every other probe must never change, refused or not.
var nestedVaultProbeLabels = []string{"HEAD", "symbolic HEAD", "index (ls-files -s)", "for-each-ref", "origin main", "reflog"}

const nestedVaultForEachRefProbe = 3

// buildNestedVaultCLIFixture builds the task's own reproduction end-to-end: a
// clean enclosing repository with a remote and one unrelated, unpushed local
// commit, and a vault subdirectory nested inside it with NO .git entry of its
// own — the shape that makes storage.InspectVaultGit report VaultGitNested.
// It also wires the global vault config (setupTestVaultEnvAt) to point at the
// nested vault, so every `vp vault <cmd>` / `vp migrate kg-filenames` CLI
// entry point resolves to it exactly as it would for a real operator.
//
// Mirrors TestConfigSyncNeverWritesAnEnclosingRepo's fixture
// (cmd_config_override_test.go), for the `vp vault <cmd>` family instead of
// `vp config sync`.
//
// Returns the vault path, the enclosing repo's own path (parent), its bare
// origin, and a state() closure returning the six ordered fingerprint probes
// named by nestedVaultProbeLabels.
func buildNestedVaultCLIFixture(t *testing.T) (vaultPath, parent, origin string, state func() []string) {
	t.Helper()
	parent = t.TempDir()
	gitInVault(t, parent, "init", "-q", "-b", "main")
	gitInVault(t, parent, "config", "user.email", "test@test.com")
	gitInVault(t, parent, "config", "user.name", "Test")
	putVaultFile(t, parent, "README.md", "project\n")
	gitInVault(t, parent, "add", "-A")
	gitInVault(t, parent, "commit", "-qm", "project")

	origin = filepath.Join(t.TempDir(), "origin.git")
	gitInVault(t, parent, "init", "-q", "--bare", "-b", "main", origin)
	gitInVault(t, parent, "remote", "add", "origin", origin)
	gitInVault(t, parent, "push", "-q", "-u", "origin", "main")

	// An unrelated, unpushed local commit — the "someone else's commits" this
	// task exists to protect.
	putVaultFile(t, parent, "WIP.md", "not for push\n")
	gitInVault(t, parent, "add", "WIP.md")
	gitInVault(t, parent, "commit", "-qm", "LOCAL WIP - not for push")

	vaultPath = filepath.Join(parent, "notes", "vault")
	if err := os.MkdirAll(vaultPath, 0o755); err != nil {
		t.Fatal(err)
	}
	setupTestVaultEnvAt(t, vaultPath)

	state = func() []string {
		return []string{
			gitInVault(t, parent, "rev-parse", "HEAD"),
			gitInVault(t, parent, "symbolic-ref", "HEAD"),
			gitInVault(t, parent, "ls-files", "-s"),
			gitInVault(t, parent, "for-each-ref"),
			gitInVault(t, origin, "rev-parse", "main"),
			gitInVault(t, parent, "reflog", "-n", "5"),
		}
	}
	return vaultPath, parent, origin, state
}

// assertFingerprintUnchanged compares every probe of before/after, failing
// loudly on any that differ; skip names zero-based probe indices to exclude
// (e.g. for-each-ref, which a read-only fetch may legitimately touch).
func assertFingerprintUnchanged(t *testing.T, before, after []string, skip ...int) {
	t.Helper()
	skipSet := map[int]bool{}
	for _, i := range skip {
		skipSet[i] = true
	}
	if len(before) != len(after) {
		t.Fatalf("fingerprint shape changed: before has %d probes, after has %d", len(before), len(after))
	}
	for i, label := range nestedVaultProbeLabels {
		if skipSet[i] {
			continue
		}
		if before[i] != after[i] {
			t.Errorf("%s changed:\n--- before\n%s\n--- after\n%s", label, before[i], after[i])
		}
	}
}

// assertCLIRefusedNested is the shared assertion body for the subtests below:
// non-OK exit, output naming the enclosing repository, and every fingerprint
// probe (the enclosing repository's HEAD, index, refs, bare origin, and
// reflog) byte-for-byte unchanged — refused or not, this is never a read-only
// fetch, so even for-each-ref must not move.
func assertCLIRefusedNested(t *testing.T, code int, out string, parent string, before, after []string) {
	t.Helper()
	if code == cli.ExitOK {
		t.Errorf("exit code = %d, want non-OK\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "inside another repository") {
		t.Errorf("output does not say \"inside another repository\":\n%s", out)
	}
	if !strings.Contains(out, parent) {
		t.Errorf("output does not name the enclosing repository %q:\n%s", parent, out)
	}
	assertFingerprintUnchanged(t, before, after)
}

// TestVaultSyncNeverWritesAnEnclosingRepo is the CLI-level pin for the direct
// fix: `vp vault sync` on a nested vault must refuse instead of merging and
// pushing the enclosing repository's branch.
func TestVaultSyncNeverWritesAnEnclosingRepo(t *testing.T) {
	_, parent, _, state := buildNestedVaultCLIFixture(t)
	before := state()

	var code int
	out := captureStderr(t, func() {
		code = cmdVaultSync().Run(nil)
	})
	assertCLIRefusedNested(t, code, out, parent, before, state())
}

// TestVaultSyncNoTidyNeverWritesAnEnclosingRepo pins the `--no-tidy` bypass
// path: it skips SyncVault entirely and calls pullAll/pushAll directly, so it
// is the case most likely to regress silently if a future change adds a new
// bypass around the gated storage functions.
func TestVaultSyncNoTidyNeverWritesAnEnclosingRepo(t *testing.T) {
	_, parent, _, state := buildNestedVaultCLIFixture(t)
	before := state()

	var code int
	out := captureStderr(t, func() {
		code = cmdVaultSync().Run([]string{"--no-tidy"})
	})
	assertCLIRefusedNested(t, code, out, parent, before, state())
}

// TestVaultPullNeverWritesAnEnclosingRepo pins `vp vault pull`.
func TestVaultPullNeverWritesAnEnclosingRepo(t *testing.T) {
	_, parent, _, state := buildNestedVaultCLIFixture(t)
	before := state()

	var code int
	out := captureStderr(t, func() {
		code = cmdVaultPull().Run(nil)
	})
	assertCLIRefusedNested(t, code, out, parent, before, state())
}

// TestVaultPushNeverWritesAnEnclosingRepo pins `vp vault push`.
func TestVaultPushNeverWritesAnEnclosingRepo(t *testing.T) {
	_, parent, _, state := buildNestedVaultCLIFixture(t)
	before := state()

	var code int
	out := captureStderr(t, func() {
		code = cmdVaultPush().Run(nil)
	})
	assertCLIRefusedNested(t, code, out, parent, before, state())
}

// TestVaultCommitNeverWritesAnEnclosingRepo pins `vp vault commit`.
func TestVaultCommitNeverWritesAnEnclosingRepo(t *testing.T) {
	vaultPath, parent, _, state := buildNestedVaultCLIFixture(t)
	putVaultFile(t, vaultPath, "notes.txt", "scratch\n")
	before := state()

	var code int
	out := captureStderr(t, func() {
		code = cmdVaultCommit().Run([]string{"--paths", "notes.txt", "--message", "should never land"})
	})
	assertCLIRefusedNested(t, code, out, parent, before, state())
}

// TestVaultTidyNeverWritesAnEnclosingRepo pins `vp vault tidy`, with a
// sweepable capture artifact present so it would otherwise commit.
//
// The artifact is written at the ENCLOSING repo's root (parent), not under
// the vault: `git status --porcelain`, run via `-C <vaultPath>` against a
// vault with no .git of its own, reports on the WHOLE enclosing repository
// with paths relative to ITS root — not the vault. A path there shaped like a
// sweep rule (as verified directly against real git output while writing this
// test) is exactly the trap this task closes: without the gate, tidy would
// classify and stage/commit content that belongs to the enclosing repository,
// not the vault.
func TestVaultTidyNeverWritesAnEnclosingRepo(t *testing.T) {
	_, parent, _, state := buildNestedVaultCLIFixture(t)
	putVaultFile(t, parent, "Projects/vibe-palace/sessions/2026-07-19.md", "session\n")
	before := state()

	var code int
	var stdout, stderr string
	stdout = captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			code = cmdVaultTidy().Run(nil)
		})
	})
	assertCLIRefusedNested(t, code, stdout+stderr, parent, before, state())
}

// TestVaultMigrateKGFilenamesNeverWritesAnEnclosingRepo is the CLI-level pin
// for the 5th choke point (Review H1): `vp migrate kg-filenames --yes` on a
// nested vault must refuse with the nesting message, not the generic
// "not a git repository" message, and must leave the enclosing repository
// untouched.
func TestVaultMigrateKGFilenamesNeverWritesAnEnclosingRepo(t *testing.T) {
	vaultPath, parent, _, state := buildNestedVaultCLIFixture(t)
	writeOldTriple(t, vaultPath, "proj", filepath.Join("src", "main.rs--m--s1.json"), "src/main.rs", "m", "s1")
	before := state()

	var code int
	out := captureStderr(t, func() {
		code = cmdMigrateKGFilenames().Run([]string{"--yes"})
	})
	if code == cli.ExitOK {
		t.Errorf("exit code = %d, want non-OK\noutput:\n%s", code, out)
	}
	if strings.Contains(out, "is not a git repository") {
		t.Errorf("got the generic non-git message instead of the nesting refusal:\n%s", out)
	}
	if !strings.Contains(out, "inside another repository") {
		t.Errorf("output does not say \"inside another repository\":\n%s", out)
	}
	if !strings.Contains(out, parent) {
		t.Errorf("output does not name the enclosing repository %q:\n%s", parent, out)
	}
	assertFingerprintUnchanged(t, before, state())
	// Nothing migrated: the triple is still at its old path.
	if _, err := os.Stat(filepath.Join(vaultPath, "palace", "proj", "kg", "triples", "src", "main.rs--m--s1.json")); err != nil {
		t.Errorf("triple missing at old path — apply mutated despite refusal: %v", err)
	}
}

// TestVaultStatusStillWorksOnNestedVault is the negative control (also the
// pin for the M1 decision): the read-only `vp vault status` fetch carve-out
// must still work on a nested vault — it is bounded, ref-only, and not the
// harm this task's reproduction demonstrated. Only for-each-ref may move (a
// fetch can create/refresh remote-tracking refs); HEAD, the index, and the
// reflog must not.
func TestVaultStatusStillWorksOnNestedVault(t *testing.T) {
	_, _, _, state := buildNestedVaultCLIFixture(t)
	before := state()

	var code int
	out := captureStdout(t, func() {
		code = cmdVaultStatus().Run(nil)
	})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d\noutput:\n%s", code, cli.ExitOK, out)
	}
	if out == "" {
		t.Error("expected a status report on stdout, got empty output")
	}
	assertFingerprintUnchanged(t, before, state(), nestedVaultForEachRefProbe)
}
