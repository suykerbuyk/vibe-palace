// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultlock

import (
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
