// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// gateFixture builds a vault in which every gated operation would change
// something: the remote is one commit ahead (pull moves HEAD), the local branch
// is one commit ahead (push moves the remote), and a capture artifact is
// uncommitted (tidy and commit move HEAD).
func gateFixture(t *testing.T) (dir, bare string) {
	t.Helper()
	dir, bare = syncSeedRemote(t)

	other := t.TempDir()
	gitRun(t, other, "clone", "-q", bare, ".")
	gitRun(t, other, "config", "user.email", "o@example.com")
	gitRun(t, other, "config", "user.name", "Other")
	writeFile(t, other, "REMOTE.md", "from elsewhere\n")
	gitRun(t, other, "add", "REMOTE.md")
	gitRun(t, other, "commit", "-q", "-m", "remote advance")
	gitRun(t, other, "push", "-q", "origin", "main")

	writeFile(t, dir, "LOCAL.md", "local\n")
	gitRun(t, dir, "add", "LOCAL.md")
	gitRun(t, dir, "commit", "-q", "-m", "local advance")

	writeFile(t, dir, "Projects/foo/sessions/x.md", "session body\n")
	return dir, bare
}

// fingerprint is everything a vault git operation can move: HEAD, every ref
// including remote-tracking refs, the index, and the bare remote's refs.
func fingerprint(t *testing.T, dir, bare string) string {
	t.Helper()
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(index)
	return strings.Join([]string{
		gitRun(t, dir, "rev-parse", "HEAD"),
		gitRun(t, dir, "for-each-ref"),
		hex.EncodeToString(sum[:]),
		gitRun(t, bare, "for-each-ref"),
	}, "\n")
}

// setHostGitEnabled writes the host config the gate reads. initTestRepo has
// already pointed XDG_CONFIG_HOME at a per-test directory.
func setHostGitEnabled(t *testing.T, enabled bool) {
	t.Helper()
	p, err := VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "git_enabled = false\n"
	if enabled {
		body = "git_enabled = true\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitStubPATH replaces PATH with a directory holding only a `git` that records
// its arguments in the returned marker file and fails. gitCmd and every raw
// exec resolve "git" through PATH on each call, so any spawn lands in the
// marker.
func gitStubPATH(t *testing.T) (marker string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the git stub is a shell script")
	}
	bin := t.TempDir()
	marker = filepath.Join(t.TempDir(), "git-was-spawned")
	script := "#!/bin/sh\necho \"$@\" >> '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return marker
}

// TestVaultGitEntryPointsRefuseBeforeAnyGit is the storage half of the parity
// guarantee, and the backstop for every caller that has no command-top
// preflight: under git_enabled = false each gated entry point returns an error
// wrapping ErrGitDisabled, starts no git process at all, and leaves the vault
// fingerprint unchanged. SyncVault keeps its non-nil-result contract.
func TestVaultGitEntryPointsRefuseBeforeAnyGit(t *testing.T) {
	dir, bare := gateFixture(t)
	before := fingerprint(t, dir, bare)
	setHostGitEnabled(t, false)
	realPATH := os.Getenv("PATH")
	marker := gitStubPATH(t)

	calls := map[string]func() error{
		"Pull": func() error {
			res, err := Pull(dir, []string{"origin"})
			if res == nil {
				t.Error("Pull returned a nil result on refusal")
			}
			return err
		},
		"PushPlain": func() error {
			res, err := PushPlain(dir, []string{"origin"})
			if res == nil {
				t.Error("PushPlain returned a nil result on refusal")
			}
			return err
		},
		"SyncVault": func() error {
			res, err := SyncVault(dir, []string{"origin"})
			if res == nil {
				t.Error("SyncVault returned a nil result on refusal; both front-ends read res.Committed before err")
			}
			return err
		},
		"TidyVault":          func() error { _, err := TidyVault(dir, true); return err },
		"CommitAndPushPaths": func() error { _, err := CommitAndPushPaths(dir, "m", []string{"Projects/foo"}, true); return err },
		"CommitAndPushPathsWithDowngrade": func() error {
			_, _, err := CommitAndPushPathsWithDowngrade(dir, "m", []string{"Projects/foo"}, true)
			return err
		},
		"BuildStatusReport fetch":    func() error { _, err := BuildStatusReport(dir, true); return err },
		"BuildStatusReport no-fetch": func() error { _, err := BuildStatusReport(dir, false); return err },
		"CommitRemovals":             func() error { _, err := CommitRemovals(dir, "m", []string{"README.md"}); return err },
		"GitAdd":                     func() error { return GitAdd(dir, "Projects") },
		"GitAddForce":                func() error { return GitAddForce(dir, "Projects") },
		"GitStatusClean":             func() error { _, err := GitStatusClean(dir); return err },
		"PruneMirrorsVerifiedWithDowngrade": func() error {
			_, _, _, err := PruneMirrorsVerifiedWithDowngrade(dir, []string{"README.md"}, true, PruneVerifier{})
			return err
		},
		"PruneMirrorsInEnclosingRepo": func() error {
			_, err := PruneMirrorsInEnclosingRepo(dir, []string{"README.md"}, PruneVerifier{})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrGitDisabled) {
				t.Errorf("%s = %v, want an error wrapping ErrGitDisabled", name, err)
			}
		})
	}

	if spawned, err := os.ReadFile(marker); err == nil {
		t.Errorf("a gated entry point started git on a disabled host:\n%s", spawned)
	}
	t.Setenv("PATH", realPATH)
	if after := fingerprint(t, dir, bare); after != before {
		t.Errorf("the vault moved under git_enabled = false:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestSyncVaultMovesTheFixtureWhenEnabled is the enabled control for the gate
// test's fixture: with git_enabled = true the same sync commits the artifact,
// merges the remote and pushes, so the fingerprint the refusal must hold still
// is one that can move.
func TestSyncVaultMovesTheFixtureWhenEnabled(t *testing.T) {
	dir, bare := gateFixture(t)
	before := fingerprint(t, dir, bare)
	setHostGitEnabled(t, true)
	res, err := SyncVault(dir, []string{"origin"})
	if err != nil {
		t.Fatalf("SyncVault: %v", err)
	}
	if !res.Committed {
		t.Error("SyncVault did not commit the capture artifact")
	}
	if after := fingerprint(t, dir, bare); after == before {
		t.Error("an enabled sync left the fingerprint unchanged, so the refusal test proves nothing")
	}
}

// TestRetiredTemplatesLockRefusesBeforeAnyGit is the gate on the one removal
// `vp config sync` makes that is neither a prune nor a commit: the retired
// .vibe-palace/templates.lock. Whether it may go is a git verdict — the index,
// HEAD and check-ignore — and the caller deletes the file on that verdict, so
// git_enabled = false must refuse before the first of those runs and leave the
// lock exactly where it is.
//
// The gate is not this function's first statement (a path check and a file
// read come first; neither is git), so this asserts the property that matters
// instead: zero git processes, and the file still on disk.
func TestRetiredTemplatesLockRefusesBeforeAnyGit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  string
		wantErr error
	}{
		{"disabled", "git_enabled = false\n", ErrGitDisabled},
		{"unreadable", "git_enabled = \"no\"\n", ErrGitConfigUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := initTestRepo(t)
			writeFile(t, dir, RetiredTemplatesLockRel, "stale lock\n")
			lock := filepath.Join(dir, filepath.FromSlash(RetiredTemplatesLockRel))
			cfg, err := VaultConfigFilePath()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfg, []byte(tc.config), 0o644); err != nil {
				t.Fatal(err)
			}
			marker := gitStubPATH(t)

			content, removable, err := RetiredTemplatesLock(dir)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("RetiredTemplatesLock err = %v, want one wrapping %v", err, tc.wantErr)
			}
			if removable {
				t.Error("removable = true on a host where no git may run: the caller would delete the lock")
			}
			if string(content) != "stale lock\n" {
				t.Errorf("content = %q, want the bytes that were read before the gate", content)
			}
			if spawned, rerr := os.ReadFile(marker); rerr == nil {
				t.Errorf("the retired-lock check started git on a disabled host:\n%s", spawned)
			}
			if _, serr := os.Stat(lock); serr != nil {
				t.Errorf("the retired lock is gone: %v", serr)
			}
		})
	}
}

// TestRetiredTemplatesLockIsRemovableWhenEnabled is the enabled control for the
// gate above: the same untracked, un-ignored lock on the same vault is
// removable when the host config permits git, so the refusal is holding back
// an operation that would otherwise happen.
func TestRetiredTemplatesLockIsRemovableWhenEnabled(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, RetiredTemplatesLockRel, "stale lock\n")
	setHostGitEnabled(t, true)

	content, removable, err := RetiredTemplatesLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !removable {
		t.Error("an untracked, un-ignored retired lock is not removable with git enabled")
	}
	if string(content) != "stale lock\n" {
		t.Errorf("content = %q", content)
	}
}
