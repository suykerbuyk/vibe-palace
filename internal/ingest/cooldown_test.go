// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
)

// --- helpers ----------------------------------------------------------------

// spawnRec records the projects a RebuildSpawner was asked to rebuild.
type spawnRec struct {
	mu   sync.Mutex
	seen []string
}

func (s *spawnRec) spawner() func(string) {
	return func(p string) {
		s.mu.Lock()
		s.seen = append(s.seen, p)
		s.mu.Unlock()
	}
}

func (s *spawnRec) projects() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// cooldownDeps is f.deps() with the U4 seams and an injected clock wired.
func (f *fixture) cooldownDeps(now func() time.Time, spawn func(string), alive func(int) bool) Deps {
	d := f.deps()
	d.Now = now
	d.RebuildSpawner = spawn
	d.HolderAlive = alive
	return d
}

// countRunTry counts index-run try-lock events while f runs.
func countRunTry(t *testing.T, fn func()) int {
	t.Helper()
	var n int
	restore := indexstore.ObserveRunLocks(func(ev indexstore.RunLockEvent) {
		if ev == indexstore.RunTry {
			n++
		}
	})
	defer restore()
	fn()
	return n
}

// --- site (a): the drain defers within the cooldown, spaced not back-to-back --

// TestDrainDefersWithinCooldown (U4 site a): an automatic drain that embedded
// arms the cooldown; the next drain within the window DEFERS — it takes no run
// lock (zero RunTry), reports Deferred, and commits nothing — and a drain past
// the window acquires and commits the archive that arrived meanwhile.
func TestDrainDefersWithinCooldown(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	a := f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))

	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	clock := base
	d := f.cooldownDeps(func() time.Time { return clock }, nil, nil)
	cd := 10 * time.Minute

	// Run 1: acquires, commits A, stamps the cooldown at base.
	r1, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha", First: a.Manifest.SourceSHA256, Cooldown: cd, Budget: RunBudget{Archives: 10}})
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if r1.Committed == 0 {
		t.Fatalf("run 1 should have committed A, got %+v", r1)
	}
	if _, ok, _ := indexstore.ReadRunCompleted(f.v); !ok {
		t.Fatal("run 1 embedded but did not arm the cooldown marker")
	}

	// Run 2 at base+5m (< cooldown): must defer, take NO run lock, commit nothing.
	clock = base.Add(5 * time.Minute)
	var r2 RunResult
	tries := countRunTry(t, func() {
		var e error
		r2, e = Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha", Cooldown: cd})
		if e != nil {
			t.Fatalf("run 2: %v", e)
		}
	})
	if !r2.Deferred {
		t.Fatalf("run 2 within the cooldown must be Deferred, got %+v", r2)
	}
	if tries != 0 {
		t.Fatalf("a deferred run must not try the run lock, got %d RunTry events", tries)
	}
	if r2.Committed != 0 {
		t.Fatalf("a deferred run commits nothing, got %+v", r2)
	}

	// A new archive arrives during the window.
	b := f.archiveOf("alpha", "B", transcript(day(2), "b", 2), day(2))

	// Run 3 at base+15m (> cooldown): acquires (RunTry fires) and commits B.
	clock = base.Add(15 * time.Minute)
	var r3 RunResult
	tries = countRunTry(t, func() {
		var e error
		r3, e = Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha", First: b.Manifest.SourceSHA256, Cooldown: cd, Budget: RunBudget{Archives: 10}})
		if e != nil {
			t.Fatalf("run 3: %v", e)
		}
	})
	if r3.Deferred {
		t.Fatalf("run 3 past the cooldown must not defer, got %+v", r3)
	}
	if tries == 0 {
		t.Fatal("run 3 past the cooldown must acquire the run lock (a RunTry event)")
	}
	if r3.Committed == 0 {
		t.Fatalf("run 3 must commit the archive that arrived during the window, got %+v", r3)
	}
}

// --- B2/B3: the drain is the guaranteed, holder-gated consumer of a want ------

// TestDrainConsumesRebuildWant (U4 B2/B3): a pending rebuild-corpus want is
// spawned by the drain when the cooldown is inactive and no LIVE holder is
// building, and is NOT spawned while the cooldown is active or a live holder
// holds the run lock — the fix for both the strand (B2) and the storm (B3).
func TestDrainConsumesRebuildWant(t *testing.T) {
	alive := func(int) bool { return true }
	dead := func(int) bool { return false }

	t.Run("inactive+no-holder spawns", func(t *testing.T) {
		f := newFixture(t)
		f.noteRebuild("alpha")
		rec := &spawnRec{}
		d := f.cooldownDeps(nil, rec.spawner(), dead)
		if _, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha", Cooldown: -1}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := rec.projects(); len(got) != 1 || got[0] != "alpha" {
			t.Fatalf("an inactive-window drain with no holder must spawn the rebuild for alpha, got %v", got)
		}
	})

	t.Run("cooldown active does not spawn", func(t *testing.T) {
		f := newFixture(t)
		f.noteRebuild("alpha")
		if err := indexstore.WriteRunCompleted(f.v, time.Now()); err != nil {
			t.Fatal(err)
		}
		rec := &spawnRec{}
		d := f.cooldownDeps(nil, rec.spawner(), dead)
		if _, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha", Cooldown: time.Hour}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := rec.projects(); len(got) != 0 {
			t.Fatalf("an active-cooldown drain must not spawn a rebuild, got %v", got)
		}
	})

	t.Run("live holder does not spawn (B3)", func(t *testing.T) {
		f := newFixture(t)
		f.noteRebuild("alpha")
		// A different run holds the one vault run lock with a holder record.
		held, ok, err := indexstore.TryRunLock(f.v, indexstore.KindRebuild, "alpha")
		if err != nil || !ok {
			t.Fatalf("pre-acquire run lock: ok=%v err=%v", ok, err)
		}
		defer held.Release()
		rec := &spawnRec{}
		d := f.cooldownDeps(nil, rec.spawner(), alive) // holder pid reported LIVE
		if _, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha", Cooldown: -1}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := rec.projects(); len(got) != 0 {
			t.Fatalf("a drain must NOT spawn while a live holder builds (would storm ErrRunLockHeld), got %v", got)
		}
	})

	t.Run("dead holder spawns (stale takeover)", func(t *testing.T) {
		f := newFixture(t)
		f.noteRebuild("alpha")
		held, ok, err := indexstore.TryRunLock(f.v, indexstore.KindRebuild, "alpha")
		if err != nil || !ok {
			t.Fatalf("pre-acquire run lock: ok=%v err=%v", ok, err)
		}
		defer held.Release()
		rec := &spawnRec{}
		d := f.cooldownDeps(nil, rec.spawner(), dead) // holder pid reported DEAD
		if _, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha", Cooldown: -1}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := rec.projects(); len(got) != 1 || got[0] != "alpha" {
			t.Fatalf("a drain must spawn when the holder names a dead pid (stale takeover), got %v", got)
		}
	})

	t.Run("nil seams do not spawn or panic", func(t *testing.T) {
		f := newFixture(t)
		f.noteRebuild("alpha")
		d := f.deps() // RebuildSpawner and HolderAlive both nil
		d.Now = func() time.Time { return time.Now() }
		if _, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha", Cooldown: -1}); err != nil {
			t.Fatalf("Run with nil seams: %v", err)
		}
	})
}

// --- correction 2: which runs arm the cooldown (stamp sites) ------------------

// stamped reports whether the cooldown marker exists for f's vault.
func (f *fixture) stamped() bool {
	_, ok, err := indexstore.ReadRunCompleted(f.v)
	if err != nil {
		f.t.Fatalf("ReadRunCompleted: %v", err)
	}
	return ok
}

// TestRebuildStampSites (correction 2): a notes-only cold rebuild (zero
// archives — the case a Committed/Repaired count predicate would miss) ARMS the
// cooldown; a --no-embed rebuild does NOT; a stopped-but-embedded rebuild DOES;
// and an ErrRunLockHeld rebuild does NOT.
func TestRebuildStampSites(t *testing.T) {
	t.Run("notes-only cold rebuild arms", func(t *testing.T) {
		f := newFixture(t)
		f.writeNote("alpha", "s1", strings.Repeat("Alpha note on the ledger and chunk store. ", 20))
		if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
			t.Fatalf("rebuild: %v", err)
		}
		if !f.stamped() {
			t.Fatal("a notes-only cold rebuild embeds the note corpus and must arm the cooldown")
		}
	})

	t.Run("--no-embed does not arm", func(t *testing.T) {
		f := newFixture(t)
		f.writeNote("alpha", "s1", strings.Repeat("Alpha note on the ledger and chunk store. ", 20))
		if _, err := f.rebuild(RebuildOptions{Project: "alpha", NoEmbed: true, FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
			t.Fatalf("rebuild --no-embed: %v", err)
		}
		if f.stamped() {
			t.Fatal("a --no-embed rebuild embeds nothing and must not arm the cooldown")
		}
	})

	t.Run("stopped-but-embedded arms", func(t *testing.T) {
		f := newFixture(t)
		f.ensureLedger("alpha")
		f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
		f.archiveOf("alpha", "B", transcript(day(2), "b", 2), day(2))
		res, err := f.rebuild(RebuildOptions{Project: "alpha", MaxArchives: 1, FreeSpace: &fakeFree{ok: plentyFree}})
		if err != nil {
			t.Fatalf("rebuild: %v", err)
		}
		if res.Stopped != "max archives" {
			t.Fatalf("want a max-archives stop, got %q", res.Stopped)
		}
		if !f.stamped() {
			t.Fatal("a rebuild that embedded before a max-archives stop must arm the cooldown")
		}
	})

	t.Run("marker written only after release (NB-1)", func(t *testing.T) {
		f := newFixture(t)
		f.writeNote("alpha", "s1", strings.Repeat("Alpha note on the ledger and chunk store. ", 20))
		// On every RunReleased, the freshly-armed marker must NOT yet exist: the
		// single deferred func releases the flock and only THEN stamps. A second
		// defer registered after held.Release() (LIFO stamp-before-release) would
		// have written the marker before this event fires, and this flips.
		var sawMarkerWhileHeld bool
		restore := indexstore.ObserveRunLocks(func(ev indexstore.RunLockEvent) {
			if ev == indexstore.RunReleased {
				if _, ok, _ := indexstore.ReadRunCompleted(f.v); ok {
					sawMarkerWhileHeld = true
				}
			}
		})
		if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
			restore()
			t.Fatalf("rebuild: %v", err)
		}
		restore()
		if sawMarkerWhileHeld {
			t.Fatal("the cooldown marker was written while the run lock was still held (NB-1): stamp must follow Release")
		}
		if !f.stamped() {
			t.Fatal("the marker must be written after the rebuild embedded and released")
		}
	})

	t.Run("ErrRunLockHeld does not arm", func(t *testing.T) {
		f := newFixture(t)
		f.writeNote("alpha", "s1", strings.Repeat("Alpha note. ", 20))
		held, ok, err := indexstore.TryRunLock(f.v, indexstore.KindRebuild, "alpha")
		if err != nil || !ok {
			t.Fatalf("pre-acquire run lock: ok=%v err=%v", ok, err)
		}
		defer held.Release()
		if _, err := f.rebuildD(f.deps(), RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err == nil {
			t.Fatal("rebuild should have refused while the run lock is held")
		}
		if f.stamped() {
			t.Fatal("a rebuild that never acquired the run lock must not arm the cooldown")
		}
	})
}
