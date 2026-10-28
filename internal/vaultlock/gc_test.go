// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultlock

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shortGCTimeout is the bounded .gc exclusive-poll budget the abort tests use, so
// they do not wait the production two seconds.
const shortGCTimeout = 150 * time.Millisecond

// seedDeadSidecar acquires and immediately releases a lock for target, leaving an
// UNHELD sidecar file behind (exactly the litter GC exists to remove). It returns
// the sidecar's base name.
func seedDeadSidecar(t *testing.T, root, target string) string {
	t.Helper()
	release, err := Acquire(root, target)
	if err != nil {
		t.Fatalf("Acquire(%s): %v", target, err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	// Name the sidecar deterministically from the key (ReadDir order is lexical,
	// not creation order, so "the last one" is unreliable).
	name := filepath.Base(sidecarPath(root, canonicalKey(target)))
	if !sidecarExists(t, root, name) {
		t.Fatalf("no sidecar created for %s", target)
	}
	return name
}

// TestFlockSharedCompatibility pins the new shared primitive's semantics: shared
// is compatible with shared, and mutually exclusive with exclusive.
func TestFlockSharedCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coord")

	open := func() *os.File {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return f
	}

	a, b := open(), open()
	defer a.Close()
	defer b.Close()

	if err := flockShared(a); err != nil {
		t.Fatalf("flockShared a: %v", err)
	}
	// A second shared lock on a DIFFERENT fd must be granted.
	if err := flockShared(b); err != nil {
		t.Fatalf("flockShared b (shared+shared must be compatible): %v", err)
	}
	// An exclusive TRY while shared is held must fail (ok=false), not error.
	c := open()
	defer c.Close()
	if ok, err := flockTryExclusive(c); err != nil || ok {
		t.Fatalf("flockTryExclusive while shared held: ok=%v err=%v; want ok=false err=nil", ok, err)
	}
	// Drop both shared holders; now exclusive must succeed.
	_ = funlock(a)
	_ = funlock(b)
	if ok, err := flockTryExclusive(c); err != nil || !ok {
		t.Fatalf("flockTryExclusive after shared released: ok=%v err=%v; want ok=true", ok, err)
	}
	// A shared TRY while exclusive is held must fail.
	d := open()
	defer d.Close()
	if ok, err := flockTryShared(d); err != nil || ok {
		t.Fatalf("flockTryShared while exclusive held: ok=%v err=%v; want ok=false", ok, err)
	}
}

// TestFlockTryExclusiveFileNoCreate pins the GC sweep primitive: it does NOT
// create a missing file (which would resurrect a just-unlinked name), reports a
// vanished file as fs.ErrNotExist, locks an unheld file, and refuses a held one.
func TestFlockTryExclusiveFileNoCreate(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone.lock")

	if _, _, err := flockTryExclusiveFile(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("flockTryExclusiveFile(missing): err=%v; want fs.ErrNotExist", err)
	}
	// It must NOT have created the file (no O_CREATE).
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("flockTryExclusiveFile created a file it should not have: %v", err)
	}

	existing := filepath.Join(dir, "present.lock")
	if err := os.WriteFile(existing, nil, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ok, closeFn, err := flockTryExclusiveFile(existing)
	if err != nil || !ok {
		t.Fatalf("flockTryExclusiveFile(unheld): ok=%v err=%v; want ok=true", ok, err)
	}

	// While we hold it, a second try must report ok=false (held), not error.
	ok2, _, err2 := flockTryExclusiveFile(existing)
	if err2 != nil || ok2 {
		t.Fatalf("flockTryExclusiveFile(held): ok=%v err=%v; want ok=false err=nil", ok2, err2)
	}
	if err := closeFn(); err != nil {
		t.Fatalf("closeFn: %v", err)
	}
}

// TestSweepDeadLocksRemovesUnheldAndDryRun covers the core functional contract:
// apply removes unheld litter; dry-run reports it without removing.
func TestSweepDeadLocksRemovesUnheldAndDryRun(t *testing.T) {
	root := t.TempDir()
	_ = seedDeadSidecar(t, root, filepath.Join(root, "a.md"))
	_ = seedDeadSidecar(t, root, filepath.Join(root, "b.md"))
	if got := len(lockFiles(t, root)); got != 2 {
		t.Fatalf("seeded %d sidecars, want 2", got)
	}

	// Dry run: reports would-remove, removes nothing.
	rep, err := sweepDeadLocks(root, true, shortGCTimeout)
	if err != nil {
		t.Fatalf("dry-run sweep: %v", err)
	}
	if !rep.Quiescent || !rep.DryRun {
		t.Fatalf("dry-run report: quiescent=%v dryRun=%v; want both true", rep.Quiescent, rep.DryRun)
	}
	if len(rep.Removed()) != 0 {
		t.Fatalf("dry-run removed %v; want nothing", rep.Removed())
	}
	if got := len(lockFiles(t, root)); got != 2 {
		t.Fatalf("dry-run left %d sidecars, want 2 (removed nothing)", got)
	}

	// Apply: removes both.
	rep, err = sweepDeadLocks(root, false, shortGCTimeout)
	if err != nil {
		t.Fatalf("apply sweep: %v", err)
	}
	if !rep.Quiescent || len(rep.Removed()) != 2 {
		t.Fatalf("apply report: quiescent=%v removed=%v; want quiescent + 2 removed", rep.Quiescent, rep.Removed())
	}
	if got := len(lockFiles(t, root)); got != 0 {
		t.Fatalf("apply left %d sidecars, want 0", got)
	}
}

// TestSweepDeadLocksSkipsHeld proves a LIVE lock is never swept while a dead one is.
func TestSweepDeadLocksSkipsHeld(t *testing.T) {
	root := t.TempDir()

	// A held lock (kept for the duration).
	heldRelease, err := Acquire(root, filepath.Join(root, "live.md"))
	if err != nil {
		t.Fatalf("Acquire live: %v", err)
	}
	defer heldRelease()

	// A dead sidecar.
	dead := seedDeadSidecar(t, root, filepath.Join(root, "dead.md"))

	rep, err := sweepDeadLocks(root, false, shortGCTimeout)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !rep.Quiescent {
		t.Fatalf("expected quiescent: the held per-path lock does NOT hold .gc (it is released after the flock)")
	}
	removed := rep.Removed()
	if len(removed) != 1 || removed[0] != dead {
		t.Fatalf("removed %v; want exactly the dead sidecar %q", removed, dead)
	}
	// The held lock's sidecar must survive and be reported kept-held.
	var sawKeptHeld bool
	for _, e := range rep.Entries {
		if e.Verdict == "kept-held" {
			sawKeptHeld = true
		}
	}
	if !sawKeptHeld {
		t.Fatalf("no kept-held entry; the live lock was not reported skipped: %+v", rep.Entries)
	}
}

// TestSweepDeadLocksAbortsWhileGCShared is the QUIESCENCE test: while any .gc
// shared holder exists (a writer mid open→flock, or a nested acquirer in its
// inner window), GC's bounded non-blocking poll times out, aborts, and removes
// NOTHING.
func TestSweepDeadLocksAbortsWhileGCShared(t *testing.T) {
	root := t.TempDir()
	dead := seedDeadSidecar(t, root, filepath.Join(root, "dead.md"))

	// Simulate a writer in its open→flock window: it holds .gc SHARED.
	gcShared, err := acquireGCShared(root)
	if err != nil {
		t.Fatalf("acquireGCShared: %v", err)
	}

	rep, err := sweepDeadLocks(root, false, shortGCTimeout)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if rep.Quiescent {
		t.Fatalf("sweep reported quiescent while a .gc shared holder exists; it must abort")
	}
	if len(rep.Removed()) != 0 {
		t.Fatalf("sweep removed %v while non-quiescent; must remove nothing", rep.Removed())
	}
	if !sidecarExists(t, root, dead) {
		t.Fatalf("dead sidecar was removed during a non-quiescent sweep")
	}

	// Nested-acquire variant: hold an OUTER per-path lock too, then the inner
	// window's .gc shared — still a no-op.
	outer, err := Acquire(root, filepath.Join(root, "outer.md"))
	if err != nil {
		t.Fatalf("Acquire outer: %v", err)
	}
	rep, err = sweepDeadLocks(root, false, shortGCTimeout)
	if err != nil {
		t.Fatalf("nested sweep: %v", err)
	}
	if rep.Quiescent || len(rep.Removed()) != 0 {
		t.Fatalf("nested-acquire holder: quiescent=%v removed=%v; want abort + nothing removed", rep.Quiescent, rep.Removed())
	}
	_ = outer()

	// Once the shared holder releases, a sweep succeeds and removes the dead one.
	if err := gcShared(); err != nil {
		t.Fatalf("release gc shared: %v", err)
	}
	rep, err = sweepDeadLocks(root, false, shortGCTimeout)
	if err != nil {
		t.Fatalf("post-release sweep: %v", err)
	}
	if !rep.Quiescent {
		t.Fatalf("post-release sweep still non-quiescent")
	}
	if sidecarExists(t, root, dead) {
		t.Fatalf("dead sidecar survived a quiescent sweep")
	}
}

// TestSweepDeadLocksSplitRevert is the REVERT test. It proves the .gc
// coordination is what prevents the unlink/create split, by running the same
// scenario WITH coordination (writer holds .gc shared ⇒ GC aborts ⇒ no split) and
// WITHOUT it (writer opens its sidecar without .gc ⇒ GC unlinks it ⇒ a fresh
// O_CREATE yields a SECOND inode for one key, and both holders' exclusive flocks
// succeed at once — mutual exclusion lost). The two observations (two inodes vs
// lost mutual exclusion) are asserted SEPARATELY.
func TestSweepDeadLocksSplitRevert(t *testing.T) {
	t.Run("coordination present: no split", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "x.md")
		key := canonicalKey(target)

		// Writer enters its open→flock window: holds .gc SHARED and has the sidecar
		// open but not yet flocked.
		gcShared, err := acquireGCShared(root)
		if err != nil {
			t.Fatalf("acquireGCShared: %v", err)
		}
		wf, err := openLockFileNoGC(root, key)
		if err != nil {
			t.Fatalf("openLockFileNoGC: %v", err)
		}
		defer wf.Close()

		// GC runs: it must ABORT (shared holder present) and remove nothing.
		rep, err := sweepDeadLocks(root, false, shortGCTimeout)
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if rep.Quiescent {
			t.Fatalf("GC did not abort while the writer held .gc shared — the split is NOT prevented")
		}

		// Writer completes its acquire; the sidecar on disk is the SAME inode it holds.
		if err := flockExclusive(wf); err != nil {
			t.Fatalf("flockExclusive wf: %v", err)
		}
		defer funlock(wf)
		wfi, err := wf.Stat()
		if err != nil {
			t.Fatalf("stat wf: %v", err)
		}
		diskPath := sidecarPath(root, key)
		dfi, err := os.Stat(diskPath)
		if err != nil {
			t.Fatalf("stat disk sidecar: %v", err)
		}
		if !os.SameFile(wfi, dfi) {
			t.Fatalf("coordination present but the on-disk sidecar is a DIFFERENT inode than the writer's — unexpected split")
		}
		_ = gcShared()
	})

	t.Run("coordination absent: split observed", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "x.md")
		key := canonicalKey(target)

		// Reverted writer: it opens its sidecar WITHOUT taking .gc (the coordination
		// this amendment adds). This is exactly the window the hazard lives in.
		wf, err := openLockFileNoGC(root, key)
		if err != nil {
			t.Fatalf("openLockFileNoGC: %v", err)
		}
		defer wf.Close()

		// GC runs with nothing holding .gc shared: it acquires .gc exclusive and
		// unlinks the (currently unheld) sidecar.
		rep, err := sweepDeadLocks(root, false, shortGCTimeout)
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if !rep.Quiescent || len(rep.Removed()) != 1 {
			t.Fatalf("reverted sweep: quiescent=%v removed=%v; want it to unlink the uncoordinated sidecar", rep.Quiescent, rep.Removed())
		}

		// The writer flocks its now-ORPHAN inode and believes it holds the lock.
		if err := flockExclusive(wf); err != nil {
			t.Fatalf("flockExclusive orphan: %v", err)
		}
		defer funlock(wf)

		// A fresh acquirer O_CREATEs a NEW inode at the same key and flocks it.
		nf, err := openLockFileNoGC(root, key)
		if err != nil {
			t.Fatalf("openLockFileNoGC fresh: %v", err)
		}
		defer nf.Close()

		// Observation 1 (two inodes for one key): the orphan and the fresh inode differ.
		wfi, err := wf.Stat()
		if err != nil {
			t.Fatalf("stat wf: %v", err)
		}
		nfi, err := nf.Stat()
		if err != nil {
			t.Fatalf("stat nf: %v", err)
		}
		if os.SameFile(wfi, nfi) {
			t.Fatalf("expected a split (two inodes for one key) after an uncoordinated unlink, got one inode")
		}

		// Observation 2 (SEPARATE: mutual exclusion lost): both exclusive flocks
		// succeed AT THE SAME TIME, which the lock must never permit.
		okFresh, ferr := flockTryExclusive(nf)
		if ferr != nil {
			t.Fatalf("flockTryExclusive nf: %v", ferr)
		}
		if !okFresh {
			t.Fatalf("fresh inode's exclusive flock was refused; expected the split to let BOTH holders lock (mutual exclusion lost)")
		}
		funlock(nf)
	})
}

// TestSweepBlockingAcquireStaysBlocked is the Linux blocking-revert leg: a
// BLOCKING .gc exclusive (the design GC does NOT use) stays blocked for as long as
// a shared holder exists, so a blocking GC would hang past its timeout and remove
// nothing. This is why the production acquire is a bounded NON-BLOCKING poll.
func TestSweepBlockingAcquireStaysBlocked(t *testing.T) {
	root := t.TempDir()
	dead := seedDeadSidecar(t, root, filepath.Join(root, "dead.md"))

	// A writer holds .gc shared.
	gcShared, err := acquireGCShared(root)
	if err != nil {
		t.Fatalf("acquireGCShared: %v", err)
	}

	// The REVERTED GC acquire: a blocking exclusive on .gc.
	bf, err := os.OpenFile(gcLockPath(root), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open .gc: %v", err)
	}
	defer bf.Close()
	done := make(chan error, 1)
	go func() { done <- flockExclusive(bf) }()

	// Well past the production bounded-poll budget, the blocking acquire is STILL
	// blocked (a non-blocking poll would have returned "busy" by now), and nothing
	// has been swept.
	select {
	case <-done:
		t.Fatalf("blocking .gc exclusive acquired while a shared holder exists — a blocking GC would wrongly proceed")
	case <-time.After(gcAcquireTimeout + 300*time.Millisecond):
		// still blocked == correct
	}
	if !sidecarExists(t, root, dead) {
		t.Fatalf("dead sidecar was removed; the blocking acquire must have removed nothing while blocked")
	}

	// Release the shared holder so the goroutine can finish and the fd unlocks.
	if err := gcShared(); err != nil {
		t.Fatalf("release gc shared: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("blocking acquire after release: %v", err)
	}
	_ = funlock(bf)
}

// sidecarPath returns the on-disk path of the sidecar for a canonical key.
func sidecarPath(root, key string) string {
	// Mirror openLockFileNoGC's naming.
	f, err := openLockFileNoGC(root, key)
	if err != nil {
		panic(err)
	}
	p := f.Name()
	_ = f.Close()
	return p
}

// sidecarExists reports whether the named sidecar still exists under .vp-locks.
func sidecarExists(t *testing.T, root, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, ".vp-locks", name))
	if err == nil {
		return true
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	t.Fatalf("stat sidecar %s: %v", name, err)
	return false
}
