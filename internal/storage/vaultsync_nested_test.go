// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nestedVaultFixture builds a parent repository — with a bare origin and an
// unrelated, unpushed local commit — and nests a vault subdirectory under it
// that has NO .git entry of its own: the shape that makes InspectVaultGit
// report VaultGitNested (the vault's real top level, per `git
// rev-parse --show-toplevel`, is the parent, not the vault path).
//
// Mirrors the fixture in TestConfigSyncNeverWritesAnEnclosingRepo
// (cmd/vp/cmd_config_override_test.go): a clean enclosing repo with a remote
// and one unrelated, unpushed local commit is exactly the reproduction the
// task was filed from. state() fingerprints the parent with the same six
// probes that test uses, so a caller can assert the enclosing repository is
// byte-for-byte untouched by a refused operation.
func nestedVaultFixture(t *testing.T) (vaultPath string, parent string, state func() string) {
	t.Helper()
	parent = t.TempDir()
	gitRun(t, parent, "init", "-b", "main")
	gitRun(t, parent, "config", "user.email", "test@example.com")
	gitRun(t, parent, "config", "user.name", "Test User")
	writeFile(t, parent, "README.md", "project\n")
	gitRun(t, parent, "add", "-A")
	gitRun(t, parent, "commit", "-m", "project")

	bare := initBareRemote(t)
	gitRun(t, parent, "remote", "add", "origin", bare)
	gitRun(t, parent, "push", "-u", "origin", "main")

	// An unrelated, unpushed local commit — the "someone else's commits" this
	// task exists to protect.
	writeFile(t, parent, "WIP.md", "not for push\n")
	gitRun(t, parent, "add", "WIP.md")
	gitRun(t, parent, "commit", "-m", "LOCAL WIP - not for push")

	vaultPath = filepath.Join(parent, "notes", "vault")
	if err := os.MkdirAll(vaultPath, 0o755); err != nil {
		t.Fatal(err)
	}

	state = func() string {
		return strings.Join([]string{
			gitRun(t, parent, "rev-parse", "HEAD"),
			gitRun(t, parent, "symbolic-ref", "HEAD"),
			gitRun(t, parent, "ls-files", "-s"),
			gitRun(t, parent, "for-each-ref"),
			gitRun(t, bare, "rev-parse", "main"),
			gitRun(t, parent, "reflog", "-n", "5"),
		}, "\n")
	}
	return vaultPath, parent, state
}

// assertRefusesNested is the shared assertion body for every "refuses on a
// nested vault" test below: the returned error must name the shape ("inside
// another repository") and the enclosing repo's own path, and the fingerprint
// taken before the call must be byte-identical to one taken after.
func assertRefusesNested(t *testing.T, err error, parent string, before, after string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a refusal error for a nested vault, got nil")
	}
	if !strings.Contains(err.Error(), "inside another repository") {
		t.Errorf("error %q does not say \"inside another repository\"", err.Error())
	}
	resolvedParent, rerr := filepath.EvalSymlinks(parent)
	if rerr != nil {
		resolvedParent = parent
	}
	if !strings.Contains(err.Error(), parent) && !strings.Contains(err.Error(), resolvedParent) {
		t.Errorf("error %q does not name the enclosing repository %q", err.Error(), parent)
	}
	if after != before {
		t.Errorf("the enclosing repository changed:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

// TestCommitAndPushPathsRefusesNestedVault pins the CommitAndPushPaths choke
// point: a vault nested inside another repository's work tree must never be
// staged into or committed, and the enclosing repository must be left exactly
// as it was.
func TestCommitAndPushPathsRefusesNestedVault(t *testing.T) {
	vaultPath, parent, state := nestedVaultFixture(t)
	before := state()

	res, err := CommitAndPushPaths(vaultPath, "should never land", []string{"whatever.txt"}, false)
	assertRefusesNested(t, err, parent, before, state())
	if res != nil {
		t.Errorf("expected nil result on refusal, got %#v", res)
	}
}

// TestPushPlainRefusesNestedVault pins the PushPlain choke point: a nested
// vault must never be pushed, and the returned result stays non-nil (the
// documented "always non-nil" contract) even on refusal.
func TestPushPlainRefusesNestedVault(t *testing.T) {
	vaultPath, parent, state := nestedVaultFixture(t)
	before := state()

	res, err := PushPlain(vaultPath, []string{"origin"})
	assertRefusesNested(t, err, parent, before, state())
	if res == nil {
		t.Fatal("expected non-nil PlainPushResult even on refusal")
	}
	if len(res.RemoteResults) != 0 {
		t.Errorf("expected no remotes attempted, got %#v", res.RemoteResults)
	}
}
