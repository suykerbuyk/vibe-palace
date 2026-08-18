// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// errRenameFake is the sentinel the injected rename funcs below fail with. Using
// one value for every case keeps the errors.Is assertions unambiguous.
var errRenameFake = errors.New("fake rename failure")

// stubRename replaces the rename/classify/sleep/backoff seam for one test and
// restores it afterwards, so the cases cannot leak into each other or into the
// real Write tests in this package. The returned pointer counts attempts.
//
// The backoff schedule is replaced with the same NUMBER of zero-length waits as
// production uses, so the tests exercise the real attempt bound at zero wall
// cost and stay deterministic (no timing assertions anywhere).
func stubRename(t *testing.T, rename func(string, string) error, retryable func(error) bool) *int {
	t.Helper()

	oldRename, oldRetryable, oldSleep, oldBackoff :=
		renameFn, isRetryableRenameErr, sleepFn, renameRetryBackoff
	t.Cleanup(func() {
		renameFn, isRetryableRenameErr, sleepFn, renameRetryBackoff =
			oldRename, oldRetryable, oldSleep, oldBackoff
	})

	calls := 0
	renameFn = func(oldpath, newpath string) error {
		calls++
		return rename(oldpath, newpath)
	}
	isRetryableRenameErr = retryable
	sleepFn = func(time.Duration) {}
	renameRetryBackoff = make([]time.Duration, len(oldBackoff))

	return &calls
}

// wantAttempts is the documented bound: one initial attempt plus one per entry
// in the backoff schedule.
const wantAttempts = 7

func TestRenameRetryBound(t *testing.T) {
	if got := len(renameRetryBackoff) + 1; got != wantAttempts {
		t.Fatalf("attempt bound = %d, want %d", got, wantAttempts)
	}
	var total time.Duration
	for _, d := range renameRetryBackoff {
		total += d
	}
	if total > time.Second {
		t.Fatalf("worst-case backoff = %s, want <= 1s", total)
	}
}

func TestRenameWithRetry_RetriesThenSucceeds(t *testing.T) {
	const failures = 3
	attempt := 0
	calls := stubRename(t,
		func(string, string) error {
			attempt++
			if attempt <= failures {
				return errRenameFake
			}
			return nil
		},
		func(error) bool { return true },
	)

	if err := renameWithRetry("old", "new"); err != nil {
		t.Fatalf("renameWithRetry = %v, want nil", err)
	}
	if *calls != failures+1 {
		t.Fatalf("rename attempts = %d, want %d", *calls, failures+1)
	}
}

func TestRenameWithRetry_NoRetryOnNonRetryable(t *testing.T) {
	calls := stubRename(t,
		func(string, string) error { return errRenameFake },
		func(error) bool { return false },
	)

	err := renameWithRetry("old", "new")
	if !errors.Is(err, errRenameFake) {
		t.Fatalf("err = %v, want errors.Is(err, errRenameFake)", err)
	}
	if *calls != 1 {
		t.Fatalf("rename attempts = %d, want exactly 1", *calls)
	}
}

func TestRenameWithRetry_ExhaustsBound(t *testing.T) {
	calls := stubRename(t,
		func(string, string) error { return errRenameFake },
		func(error) bool { return true },
	)

	err := renameWithRetry("old", "new")
	if !errors.Is(err, errRenameFake) {
		t.Fatalf("err = %v, want the last error wrapped so errors.Is reaches it", err)
	}
	if *calls != wantAttempts {
		t.Fatalf("rename attempts = %d, want the documented bound %d", *calls, wantAttempts)
	}
}

// TestWrite_RetriesTransientRename drives the retry through the public Write
// entry point, proving Write is wired to renameWithRetry and not to a bare
// os.Rename: the first two renames fail transiently, the third is the real one
// and the file lands on disk.
func TestWrite_RetriesTransientRename(t *testing.T) {
	const failures = 2
	attempt := 0
	calls := stubRename(t,
		func(oldpath, newpath string) error {
			attempt++
			if attempt <= failures {
				return errRenameFake
			}
			return os.Rename(oldpath, newpath)
		},
		func(err error) bool { return errors.Is(err, errRenameFake) },
	)

	dir := t.TempDir()
	p := filepath.Join(dir, "file.txt")
	if err := Write("", p, []byte("payload")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if *calls != failures+1 {
		t.Fatalf("rename attempts = %d, want %d", *calls, failures+1)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "payload" {
		t.Fatalf("content = %q, want %q", got, "payload")
	}
}

// TestWrite_PropagatesNonRetryableRename pins the caller-visible error shape:
// Write still wraps with "rename: %w" and the sentinel survives to the caller.
func TestWrite_PropagatesNonRetryableRename(t *testing.T) {
	calls := stubRename(t,
		func(string, string) error { return errRenameFake },
		func(error) bool { return false },
	)

	dir := t.TempDir()
	err := Write("", filepath.Join(dir, "file.txt"), []byte("x"))
	if !errors.Is(err, errRenameFake) {
		t.Fatalf("Write err = %v, want errors.Is(err, errRenameFake)", err)
	}
	if *calls != 1 {
		t.Fatalf("rename attempts = %d, want exactly 1", *calls)
	}
}
