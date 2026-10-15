// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"os"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// recordingLaunch is a detachlaunch.LaunchFunc that records the args it was
// asked to spawn instead of forking a process. It also records, at the moment of
// the spawn, whether the vault's rebuild-corpus marker was already set — so a
// test can pin that the want is recorded BEFORE the spawn (n1).
type recordingLaunch struct {
	mu          sync.Mutex
	v           *storage.Vault
	args        [][]string
	wantAtSpawn []bool
}

func (r *recordingLaunch) fn(_ string, args []string, _ string) (int, error) {
	want, _ := indexstore.ReadRebuildWanted(r.v, "proj")
	r.mu.Lock()
	r.args = append(r.args, append([]string(nil), args...))
	r.wantAtSpawn = append(r.wantAtSpawn, want)
	r.mu.Unlock()
	return 4321, nil
}

func (r *recordingLaunch) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.args)
}

// TestRebuildLauncherRecordsWantThenSpawns (U2 / ADR-016 decisions 4 and 5): the
// spawn seam records the durable rebuild-corpus want FIRST, then spawns the
// detached `vp index rebuild` when no live run holds the lock.
func TestRebuildLauncherRecordsWantThenSpawns(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	testutil.InitProject(t, v.Root, "proj")
	rl := &recordingLaunch{v: v}

	NewRebuildLauncher(v, rl.fn)("proj")

	if want, err := indexstore.ReadRebuildWanted(v, "proj"); err != nil || !want {
		t.Fatalf("the durable want was not recorded: want=%v err=%v", want, err)
	}
	if rl.calls() != 1 {
		t.Fatalf("spawned %d times, want 1", rl.calls())
	}
	if got := rl.args[0]; len(got) < 3 || got[0] != "index" || got[1] != "rebuild" || got[2] != "proj" {
		t.Fatalf("spawn args = %v, want [index rebuild proj ...]", got)
	}
	// n1: the want was recorded BEFORE the spawn, so a spawn that loses the lock
	// loses nothing.
	if !rl.wantAtSpawn[0] {
		t.Fatal("the launcher spawned before recording the want; a lost spawn would then lose the want")
	}
}

// TestRebuildLauncherRecordsWantButDoesNotSpawnWhileHeld: while a live run holds
// the advisory run-lock holder record, the launcher still records the want but
// does NOT add to the spawn burst — it never try-locks; the holder record is read
// advisorily.
func TestRebuildLauncherRecordsWantButDoesNotSpawnWhileHeld(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	testutil.InitProject(t, v.Root, "proj")

	held, ok, err := indexstore.TryRunLock(v, indexstore.KindRebuild, "proj")
	if err != nil || !ok {
		t.Fatalf("acquire run lock: ok=%v err=%v", ok, err)
	}
	t.Cleanup(func() { _ = held.Release() })

	rl := &recordingLaunch{v: v}
	NewRebuildLauncher(v, rl.fn)("proj")

	if want, err := indexstore.ReadRebuildWanted(v, "proj"); err != nil || !want {
		t.Fatalf("the want must be recorded even while the lock is held: want=%v err=%v", want, err)
	}
	if rl.calls() != 0 {
		t.Fatalf("spawned %d times while a live run held the lock, want 0", rl.calls())
	}

	// A paranoia check that the want write did not try-lock the run lock: the
	// holder is still the one we took (its pid is this process).
	h, herr := indexstore.ReadHolder(v)
	if herr != nil || h.PID != os.Getpid() {
		t.Fatalf("the launcher disturbed the run-lock holder: holder=%+v err=%v", h, herr)
	}
}
