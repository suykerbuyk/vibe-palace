// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// The lock-held variants run under a caller's root lock without taking it
// again (vaultlock.Acquire is not reentrant, so a re-acquire hangs), and
// refuse a token that is not the live root lock of the vault it names.

// withinDeadline runs fn and fails the test if it has not returned in time: a
// re-acquire of the caller's own lock blocks forever, not with an error.
func withinDeadline(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("%s did not return under a held root lock: it re-acquired the lock and deadlocked", what)
	}
}

func holdRoot(t *testing.T, dir string) *vaultlock.Held {
	t.Helper()
	held, err := vaultlock.AcquireHeld(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })
	return held
}

// assertStillHeld fails unless the root lock is still held after the callee
// returned: a callee that released the caller's lock would unserialise the
// rest of the caller's run.
func assertStillHeld(t *testing.T, dir string) {
	t.Helper()
	rel, ok, err := vaultlock.TryAcquire(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		_ = rel()
		t.Fatal("the caller's root lock was released by the callee")
	}
}

func TestCommitPathsLockedCommitsUnderAHeldRootLock(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "Projects/alpha/resume.md", "alpha\n")
	before := gitRun(t, dir, "rev-parse", "HEAD")
	held := holdRoot(t, dir)

	var res *PushResult
	var err error
	withinDeadline(t, "commitPathsLocked", func() {
		res, err = commitPathsLocked(held, "copy alpha", "", []string{"Projects/alpha"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CommitSHA == "" || gitRun(t, dir, "rev-parse", "HEAD") == before {
		t.Fatalf("no commit landed (result %+v)", res)
	}
	if got := gitRun(t, dir, "log", "-1", "--format=%s"); got != "copy alpha" {
		t.Fatalf("HEAD subject = %q", got)
	}
	if got := gitRun(t, dir, "show", "--name-only", "--format=", "HEAD"); got != "Projects/alpha/resume.md" {
		t.Fatalf("HEAD files = %q", got)
	}
	assertStillHeld(t, dir)
}

func TestCommitSplitPurgeLockedCommitsUnderAHeldRootLock(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "Projects/alpha/resume.md", "alpha\n")
	writeFile(t, dir, "palace/alpha/kg/entities.jsonl", "{}\n")
	writeFile(t, dir, "Projects/keep/resume.md", "keep\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	head := gitRun(t, dir, "rev-parse", "HEAD")
	held := holdRoot(t, dir)

	var res *SplitPurgeCommitResult
	var err error
	withinDeadline(t, "CommitSplitPurgeLocked", func() {
		res, err = CommitSplitPurgeLocked(held, SplitPurgeCommit{Slugs: []string{"alpha"}, Message: "delete alpha", ExpectHead: head})
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TrackedRemoved != 2 {
		t.Fatalf("TrackedRemoved = %d, want 2", res.TrackedRemoved)
	}
	if out := gitRun(t, dir, "ls-files", "--", "Projects/alpha", "palace/alpha"); out != "" {
		t.Fatalf("alpha still tracked: %q", out)
	}
	assertStillHeld(t, dir)
}

// A token that is not the root lock of the vault it names refuses before any
// git runs: nothing is staged and HEAD does not move.
func TestLockedVariantsRefuseATokenThatIsNotTheRootLock(t *testing.T) {
	dir := initTestRepo(t)
	other := t.TempDir()
	writeFile(t, dir, "Projects/alpha/resume.md", "alpha\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	writeFile(t, dir, "Projects/alpha/resume.md", "edited\n")
	head := gitRun(t, dir, "rev-parse", "HEAD")

	released, err := vaultlock.AcquireHeld(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = released.Release()

	tokens := map[string]func() *vaultlock.Held{
		"per-path lock": func() *vaultlock.Held {
			return holdLock(t, dir, filepath.Join(dir, "Projects", "alpha", "resume.md"))
		},
		"another vault's root key": func() *vaultlock.Held { return holdLock(t, dir, other) },
		"released root lock":       func() *vaultlock.Held { return released },
		"nil":                      func() *vaultlock.Held { return nil },
	}
	for name, tok := range tokens {
		held := tok()
		var cerr, perr error
		withinDeadline(t, name, func() {
			_, cerr = commitPathsLocked(held, "copy alpha", "", []string{"Projects/alpha"})
			_, perr = CommitSplitPurgeLocked(held, SplitPurgeCommit{Slugs: []string{"alpha"}, Message: "delete alpha", ExpectHead: head})
		})
		if !errors.Is(cerr, vaultlock.ErrNotRootLock) {
			t.Errorf("%s: commitPathsLocked err = %v, want ErrNotRootLock", name, cerr)
		}
		if !errors.Is(perr, vaultlock.ErrNotRootLock) {
			t.Errorf("%s: CommitSplitPurgeLocked err = %v, want ErrNotRootLock", name, perr)
		}
	}
	if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved from %s to %s", head, got)
	}
	if staged := gitRun(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("staged after a refusal: %q", staged)
	}
}

func holdLock(t *testing.T, root, target string) *vaultlock.Held {
	t.Helper()
	held, err := vaultlock.AcquireHeld(root, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })
	return held
}

// While a departure record is pending, commitPathsLocked refuses before it
// stages, like every committer: no commit, and the index exactly as it was —
// including someone else's staged edit under a path it was given. The
// commitOnlyPaths backstop alone would refuse too, but only after staging, and
// its unstage would drop that edit.
func TestCommitPathsLockedRefusesWhileADepartureIsPending(t *testing.T) {
	dir := crashedPurgeVault(t, false)
	writeFile(t, dir, "Projects/keep/memory/new.md", "a typed write meanwhile\n")
	head := gitRun(t, dir, "rev-parse", "HEAD")
	stagedBefore := gitRun(t, dir, "diff", "--cached", "--name-only")
	held := holdRoot(t, dir)

	var err error
	withinDeadline(t, "commitPathsLocked", func() {
		_, err = commitPathsLocked(held, "commit keep", "", []string{"Projects/keep"})
	})
	if !errors.Is(err, ErrPendingDeparture) {
		t.Fatalf("err = %v, want ErrPendingDeparture", err)
	}
	if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved from %s to %s", head, got)
	}
	if got := gitRun(t, dir, "diff", "--cached", "--name-only"); got != stagedBefore {
		t.Fatalf("the index changed under a refusal: staged %q, was %q", got, stagedBefore)
	}
}
