// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testinfra

import (
	"fmt"
	"sync"
	"testing"
)

// TestNewRecordingLaunchRecordsInOrder proves the recording fake records each
// call's (binary, args, logPath) in call order, returns distinct
// monotonically-increasing pids, and copies args rather than aliasing the
// caller's slice.
func TestNewRecordingLaunchRecordsInOrder(t *testing.T) {
	launch, snapshot := NewRecordingLaunch()

	args := []string{"drain", "summaries"}
	pid1, err := launch("vp", args, "/tmp/one.log")
	if err != nil {
		t.Fatalf("launch #1: %v", err)
	}
	pid2, err := launch("vp", []string{"other"}, "/tmp/two.log")
	if err != nil {
		t.Fatalf("launch #2: %v", err)
	}

	if pid1 == pid2 {
		t.Fatalf("pids not distinct: %d == %d", pid1, pid2)
	}
	if pid2 <= pid1 {
		t.Fatalf("pid2 (%d) not > pid1 (%d), want monotonically increasing", pid2, pid1)
	}

	calls := snapshot()
	if len(calls) != 2 {
		t.Fatalf("recorded %d calls, want 2", len(calls))
	}
	if calls[0].Binary != "vp" || calls[0].LogPath != "/tmp/one.log" {
		t.Fatalf("call 0 = %+v, want binary vp, logPath /tmp/one.log", calls[0])
	}
	if len(calls[0].Args) != 2 || calls[0].Args[0] != "drain" || calls[0].Args[1] != "summaries" {
		t.Fatalf("call 0 args = %v, want [drain summaries]", calls[0].Args)
	}
	if calls[1].LogPath != "/tmp/two.log" {
		t.Fatalf("call 1 = %+v, want logPath /tmp/two.log", calls[1])
	}

	// Mutating the caller's slice after the call must not retroactively
	// change the recorded call: NewRecordingLaunch must have copied it.
	args[0] = "mutated"
	if snapshot()[0].Args[0] != "drain" {
		t.Fatalf("recorded args aliased caller's slice: got %q, want \"drain\" (unaffected by later mutation)", snapshot()[0].Args[0])
	}
}

// TestNewRecordingLaunchConcurrentSafe drives many goroutines through one
// recording fake at once and asserts every call was recorded exactly once
// with a unique pid — proving the mutex-guarded append and pid counter are
// race-free under concurrent use. Run with -race.
func TestNewRecordingLaunchConcurrentSafe(t *testing.T) {
	launch, snapshot := NewRecordingLaunch()

	const n = 50
	var wg sync.WaitGroup
	pids := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pid, err := launch(fmt.Sprintf("bin-%d", i), []string{"arg"}, "log")
			if err != nil {
				t.Errorf("launch: %v", err)
				return
			}
			pids[i] = pid
		}(i)
	}
	wg.Wait()

	calls := snapshot()
	if len(calls) != n {
		t.Fatalf("recorded %d calls, want %d", len(calls), n)
	}

	seen := make(map[int]bool, n)
	for _, pid := range pids {
		if seen[pid] {
			t.Fatalf("duplicate pid %d recorded", pid)
		}
		seen[pid] = true
	}
}

// TestNewRecordingLaunchSnapshotRaceFreeAgainstConcurrentLaunch pins the fix
// for a real gap: snapshot must be safe to call WHILE launch calls are still
// in flight from other goroutines (e.g. TestHarness.RecordedLaunches called
// concurrently with an in-flight tool dispatch) — the two must share the
// same synchronization, not just each be individually safe under concurrent
// calls to themselves. Run with -race; a shared-mutex bug here shows up as a
// race report, not a wrong-count assertion failure.
func TestNewRecordingLaunchSnapshotRaceFreeAgainstConcurrentLaunch(t *testing.T) {
	launch, snapshot := NewRecordingLaunch()

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = launch(fmt.Sprintf("bin-%d", i), []string{"arg"}, "log")
		}(i)
	}
	// Read concurrently with the writers above — the assertion is only that
	// this whole test is race-free (see -race), not any particular count
	// snapshot happens to observe mid-flight.
	for range 20 {
		_ = snapshot()
	}
	wg.Wait()

	if got := len(snapshot()); got != n {
		t.Fatalf("recorded %d calls after Wait, want %d", got, n)
	}
}
