// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultlock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// lockFiles returns the names of every .lock file under <root>/.vp-locks.
func lockFiles(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".vp-locks"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read lock dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".lock" {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestAcquireCreatesLockDirAndFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "notes", "a.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	release, err := Acquire(root, target)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	info, err := os.Stat(filepath.Join(root, ".vp-locks"))
	if err != nil {
		t.Fatalf("lock dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf(".vp-locks is not a directory")
	}

	names := lockFiles(t, root)
	if len(names) != 1 {
		t.Fatalf("expected exactly 1 lock file, got %d: %v", len(names), names)
	}
}

func TestSameTargetSameLockFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "notes", "a.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Acquire and release for the same path twice, plus a lexically equivalent
	// but non-cleaned spelling. All must map to the same single lock file.
	for _, p := range []string{
		target,
		target,
		filepath.Join(root, "notes", ".", "a.md"),
		filepath.Join(root, "notes", "x", "..", "a.md"),
	} {
		release, err := Acquire(root, p)
		if err != nil {
			t.Fatalf("Acquire(%q): %v", p, err)
		}
		if err := release(); err != nil {
			t.Fatalf("release(%q): %v", p, err)
		}
	}

	names := lockFiles(t, root)
	if len(names) != 1 {
		t.Fatalf("expected 1 lock file for equivalent paths, got %d: %v", len(names), names)
	}
}

func TestSymlinkedRootSameLockFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "notes", "a.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}

	// A symlink alias for the root: <link> -> <root>. A path reaching the file
	// through the link must canonicalize to the same key (mirrors vaultfs
	// passing an EvalSymlinks-resolved path while storage passes a lexical join).
	link := filepath.Join(t.TempDir(), "vault-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	// Lock through the real path.
	rel1, err := Acquire(root, target)
	if err != nil {
		t.Fatalf("Acquire real: %v", err)
	}
	if err := rel1(); err != nil {
		t.Fatalf("release real: %v", err)
	}

	// Lock through the symlinked alias (still rooted at the real vault root for
	// the lock dir, since callers always hand vaultlock the real root).
	rel2, err := Acquire(root, filepath.Join(link, "notes", "a.md"))
	if err != nil {
		t.Fatalf("Acquire via link: %v", err)
	}
	if err := rel2(); err != nil {
		t.Fatalf("release via link: %v", err)
	}

	names := lockFiles(t, root)
	if len(names) != 1 {
		t.Fatalf("expected 1 lock file across symlinked spellings, got %d: %v", len(names), names)
	}
}

func TestSerialization(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "counter.txt")

	const n = 50
	var (
		counter int
		busy    bool
		mu      sync.Mutex // guards busy/overlap detection only (NOT the lock under test)
		overlap bool
		wg      sync.WaitGroup
	)

	for range n {
		wg.Go(func() {
			release, err := Acquire(root, target)
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			defer release()

			// Critical section: detect any overlap.
			mu.Lock()
			if busy {
				overlap = true
			}
			busy = true
			mu.Unlock()

			counter++
			time.Sleep(200 * time.Microsecond)

			mu.Lock()
			busy = false
			mu.Unlock()
		})
	}
	wg.Wait()

	if overlap {
		t.Fatalf("critical sections overlapped: lock did not serialize")
	}
	if counter != n {
		t.Fatalf("counter = %d, want %d", counter, n)
	}
}

func TestDifferentTargetsDifferentLocks(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.md")
	b := filepath.Join(root, "b.md")

	relA, err := Acquire(root, a)
	if err != nil {
		t.Fatalf("Acquire a: %v", err)
	}
	defer relA()

	// Different target must not block while a is held; do it on a watchdog.
	done := make(chan error, 1)
	go func() {
		relB, err := Acquire(root, b)
		if err != nil {
			done <- err
			return
		}
		done <- relB()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Acquire/release b: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Acquire(b) blocked while a held: locks are not independent")
	}

	names := lockFiles(t, root)
	if len(names) != 2 {
		t.Fatalf("expected 2 distinct lock files, got %d: %v", len(names), names)
	}
}

func TestReleaseIdempotent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.md")

	release, err := Acquire(root, target)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("second release should be no-op nil, got: %v", err)
	}
}

func TestAcquireNonExistentTarget(t *testing.T) {
	root := t.TempDir()
	// Parent (root) exists, but the file does not yet — exercises the
	// EvalSymlinks parent-fallback branch of canonicalKey.
	target := filepath.Join(root, "does-not-exist-yet.md")

	release, err := Acquire(root, target)
	if err != nil {
		t.Fatalf("Acquire on missing target: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	names := lockFiles(t, root)
	if len(names) != 1 {
		t.Fatalf("expected 1 lock file, got %d: %v", len(names), names)
	}
}

func TestAcquireInvalidVaultRoot(t *testing.T) {
	if _, err := Acquire("", "/tmp/x"); err == nil {
		t.Fatalf("expected error for empty vaultRoot")
	}
	if _, err := Acquire("relative/root", "/tmp/x"); err == nil {
		t.Fatalf("expected error for relative vaultRoot")
	}
}

func TestAcquireWithTimeoutReturnsCleanErrorInsteadOfHanging(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "held")

	release, err := Acquire(root, target) // hold it for the whole test
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	start := time.Now()
	_, err = AcquireWithTimeout(root, target, 100*time.Millisecond)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrLockWaitTimeout) {
		t.Fatalf("AcquireWithTimeout error = %v, want ErrLockWaitTimeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("AcquireWithTimeout took %s, want ~100ms (proves it does not hang)", elapsed)
	}
}

// TestCanonicalKeyStableAcrossMissingDirectories is the EXCLUSION LOST probe from
// task retire-racing-a-cross-project-move-duplicates-the-task, kept as a
// regression test. With the vault reached through a symlink and a destination
// whose directory does not exist yet, the earlier canonicalKey keyed the file by
// its unresolved spelling, then by its resolved spelling once the directory was
// created: a second holder took the lock on the same file.
func TestCanonicalKeyStableAcrossMissingDirectories(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "vault-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("Projects", "q", "tasks", "x.md")
	storageSpelling := filepath.Join(link, rel) // storage: a lexical join of the configured (symlinked) root
	// vaultfs.ResolveSafePath roots a path in the EvalSymlinks-resolved vault
	// (internal/vaultfs/safety.go: absVault := filepath.EvalSymlinks(vaultPath);
	// joined := filepath.Join(absVault, relPath)). That formula is reproduced
	// here because vaultfs imports vaultlock and an in-package test cannot
	// import it back.
	vaultfsSpelling := filepath.Join(resolvedRoot, rel)

	before := canonicalKey(storageSpelling)
	if got := canonicalKey(vaultfsSpelling); got != before {
		t.Errorf("storage and vaultfs spellings of one missing-parent file get different keys:\n  storage %s\n  vaultfs %s", before, got)
	}
	// K1's companion: two different missing files must not share a key.
	if canonicalKey(filepath.Join(link, "Projects", "q", "tasks", "y.md")) == before {
		t.Errorf("two different missing files share one key %s", before)
	}

	release, err := Acquire(link, storageSpelling)
	if err != nil {
		t.Fatalf("Acquire with q/tasks missing: %v", err)
	}
	defer func() { _ = release() }()

	if err := os.MkdirAll(filepath.Join(real, "Projects", "q", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if after := canonicalKey(storageSpelling); after != before {
		t.Errorf("key moved when the directory was created:\n  before %s\n  after  %s", before, after)
	}
	rel2, ok, err := TryAcquire(link, storageSpelling)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if ok {
		_ = rel2()
		t.Fatal("EXCLUSION LOST: a second holder took the lock on the same destination file after its directory was created")
	}
}

// pairPaths returns two existing files under root whose canonical keys sort
// lo < hi.
func pairPaths(t *testing.T, root string) (loPath, hiPath string) {
	t.Helper()
	a, b := filepath.Join(root, "a.md"), filepath.Join(root, "b.md")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if canonicalKey(a) < canonicalKey(b) {
		return a, b
	}
	return b, a
}

// The pair takes the LOWER key first, whatever order its arguments come in: with
// the higher key held elsewhere, AcquirePair(hi, lo) must be holding lo while it
// waits.
func TestAcquirePairTakesTheLowerKeyFirst(t *testing.T) {
	root := t.TempDir()
	lo, hi := pairPaths(t, root)

	releaseHi, err := Acquire(root, hi)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan func() error, 1)
	go func() {
		r, err := AcquirePair(root, hi, lo) // arguments deliberately reversed
		if err != nil {
			t.Errorf("AcquirePair: %v", err)
			got <- nil
			return
		}
		got <- r
	}()
	time.Sleep(200 * time.Millisecond)

	relLo, ok, err := TryAcquire(root, lo)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		// Release at once: holding lo here would park the pair forever once hi
		// is released, and the test would hang instead of failing.
		_ = relLo()
		_ = releaseHi()
		<-got
		t.Fatal("while waiting for the higher key, AcquirePair was not holding the lower one: it acquired in argument order")
	}
	if err := releaseHi(); err != nil {
		t.Fatal(err)
	}
	var release func() error
	select {
	case release = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("AcquirePair did not complete after the higher key was released")
	}
	if release == nil {
		t.FailNow()
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{lo, hi} {
		r, ok, err := TryAcquire(root, p)
		if err != nil || !ok {
			t.Fatalf("%s still held after the pair's release (ok=%v err=%v)", p, ok, err)
		}
		_ = r()
	}
}

// Two paths with one key take ONE lock: a pair of the same file must not
// deadlock on itself.
func TestAcquirePairSameKeyIsOneLock(t *testing.T) {
	root := t.TempDir()
	p, _ := pairPaths(t, root)
	done := make(chan func() error, 1)
	go func() {
		r, err := AcquirePair(root, p, filepath.Join(filepath.Dir(p), ".", filepath.Base(p)))
		if err != nil {
			t.Errorf("AcquirePair: %v", err)
		}
		done <- r
	}()
	var release func() error
	select {
	case release = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AcquirePair of one file with itself did not return: self-deadlock")
	}
	if release == nil {
		t.FailNow()
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	r, ok, err := TryAcquire(root, p)
	if err != nil || !ok {
		t.Fatalf("lock still held after one release (ok=%v err=%v)", ok, err)
	}
	_ = r()
}

// If the second acquire fails, the first lock is released before the error
// returns, so a failed pair leaves nothing held.
func TestAcquirePairReleasesFirstOnSecondFailure(t *testing.T) {
	root := t.TempDir()
	lo, hi := pairPaths(t, root)
	// A DIRECTORY where the higher key's sidecar belongs makes its open fail.
	sum := sha256.Sum256([]byte(canonicalKey(hi)))
	if err := os.MkdirAll(filepath.Join(root, ".vp-locks", hex.EncodeToString(sum[:])+".lock"), 0o755); err != nil {
		t.Fatal(err)
	}
	if r, err := AcquirePair(root, lo, hi); err == nil {
		_ = r()
		t.Fatal("AcquirePair succeeded although the higher sidecar cannot be opened")
	}
	r, ok, err := TryAcquire(root, lo)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("the lower key is still held after AcquirePair failed on the higher one")
	}
	_ = r()
}
