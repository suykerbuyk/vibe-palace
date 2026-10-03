// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
)

// mustTryRunLock(t)(TryRunLock(...)) returns the lock or fails the test.
func mustTryRunLock(t *testing.T) func(*RunLock, bool, error) *RunLock {
	t.Helper()
	return func(rl *RunLock, ok bool, err error) *RunLock {
		t.Helper()
		if err != nil || !ok {
			t.Fatalf("TryRunLock: ok=%v err=%v", ok, err)
		}
		return rl
	}
}

// The run lock is non-blocking and names its holder: while a helper holds it,
// TryRunLock returns ok=false at once, and ReadHolder names the helper.
func TestRunLockIsNonBlockingAndNamesItsHolder(t *testing.T) {
	v := newVault(t)
	h := startHelper(t, v, "run-hold")
	h.ready(t)

	within(t, "TryRunLock against a held lock", func() {
		if rl, ok, err := TryRunLock(v, KindRebuild, "beta"); err != nil || ok {
			if rl != nil {
				_ = rl.Release()
			}
			t.Errorf("TryRunLock while held: ok=%v err=%v, want ok=false", ok, err)
		}
	})
	got, err := ReadHolder(v)
	if err != nil {
		t.Fatalf("ReadHolder: %v", err)
	}
	if got.PID != h.cmd.Process.Pid || got.Kind != KindIngest || got.Project != "alpha" || got.StartTime.IsZero() {
		t.Fatalf("holder = %+v, want pid %d, ingest, alpha, a start time", got, h.cmd.Process.Pid)
	}
	h.finish(t)
}

// Liveness is the OS lock, not the holder record: once the holder is killed,
// the next TryRunLock succeeds although the record still names the dead pid.
func TestRunLockLivenessIsTheOSLock(t *testing.T) {
	v := newVault(t)
	h := startHelper(t, v, "run-hold")
	h.ready(t)
	pid := h.cmd.Process.Pid
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = h.cmd.Wait()

	if got, err := ReadHolder(v); err != nil || got.PID != pid {
		t.Fatalf("after the kill the record should still name the dead pid %d: %+v, %v", pid, got, err)
	}
	rl := mustTryRunLock(t)(TryRunLock(v, KindIngest, "alpha"))
	defer rl.Release()
	if got, err := ReadHolder(v); err != nil || got.PID != os.Getpid() {
		t.Fatalf("the new holder did not rewrite the record: %+v, %v", got, err)
	}
}

// The holder record is advisory in both directions: deleting it or corrupting
// it does not free a held lock, and a record naming a live foreign pid does not
// stop a free lock from being taken.
func TestHolderRecordIsAdvisory(t *testing.T) {
	v := newVault(t)
	h := startHelper(t, v, "run-hold")
	h.ready(t)

	for _, corrupt := range []func(){
		func() { _ = os.Remove(v.IndexRunHolderPath()) },
		func() { _ = os.WriteFile(v.IndexRunHolderPath(), []byte("{not json"), 0o644) },
	} {
		corrupt()
		if rl, ok, err := TryRunLock(v, KindIngest, "alpha"); err != nil || ok {
			if rl != nil {
				_ = rl.Release()
			}
			t.Fatalf("TryRunLock with a missing or corrupt record: ok=%v err=%v, want ok=false", ok, err)
		}
		if _, err := ReadHolder(v); !errors.Is(err, ErrHolderUnknown) {
			t.Fatalf("ReadHolder = %v, want ErrHolderUnknown", err)
		}
	}
	h.finish(t)

	// The lock is free; a record names pid 1, which is alive and not ours.
	stale, _ := json.Marshal(Holder{PID: 1, Kind: KindRebuild, Project: "beta"})
	if err := os.WriteFile(v.IndexRunHolderPath(), stale, 0o644); err != nil {
		t.Fatal(err)
	}
	rl := mustTryRunLock(t)(TryRunLock(v, KindIngest, "alpha"))
	_ = rl.Release()
}

// The holder reports progress through the record; Release removes the record
// BEFORE it releases the lock and leaves none behind; ReadHolder never touches
// the lock.
func TestHolderRecordLifecycleAndProgress(t *testing.T) {
	v := newVault(t)
	h := startHelper(t, v, "run-hold", "VP_INDEXSTORE_PROGRESS=1")
	h.ready(t)

	var events []lockEvent
	var mu sync.Mutex
	recordLocks(t, func(ev lockEvent) { mu.Lock(); events = append(events, ev); mu.Unlock() })
	got, err := ReadHolder(v)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress == nil || *got.Progress != (Progress{Done: 3, Total: 10}) {
		t.Fatalf("progress = %+v, want {3 10}", got.Progress)
	}
	mu.Lock()
	if len(events) != 0 {
		t.Fatalf("ReadHolder performed lock operations: %+v", events)
	}
	mu.Unlock()
	h.finish(t)

	rl := mustTryRunLock(t)(TryRunLock(v, KindRebuild, "alpha"))
	inner := rl.release
	recordAtUnlock := ""
	rl.release = func() error {
		if _, err := os.Stat(v.IndexRunHolderPath()); err == nil {
			recordAtUnlock = "present"
		} else {
			recordAtUnlock = "absent"
		}
		return inner()
	}
	if err := rl.Release(); err != nil {
		t.Fatal(err)
	}
	if recordAtUnlock != "absent" {
		t.Fatalf("the holder record was %s when the lock was released; it must be removed first", recordAtUnlock)
	}
	if _, err := os.Stat(v.IndexRunHolderPath()); !os.IsNotExist(err) {
		t.Fatalf("a holder record survived a clean release: %v", err)
	}
	if err := rl.Release(); err != nil {
		t.Fatalf("a second Release is not a no-op: %v", err)
	}
}

// A trigger that arrives while the holder is finishing is never lost. Both
// orders are forced: (a) the trigger tries the lock before the holder releases
// and exits, so the holder's recheck must re-acquire and serve it; (b) the
// trigger tries after the release and takes the lock itself, so the holder's
// recheck must not. In both, the archive is committed exactly once.
func TestNoLostTrigger(t *testing.T) {
	for _, order := range []string{"trigger-before-release", "trigger-after-release"} {
		t.Run(order, func(t *testing.T) {
			v := newVault(t)
			var mu sync.Mutex
			durable := map[string]bool{} // archives written, keyed by sha
			committed := map[string]int{}
			pending := func() ([]string, error) {
				mu.Lock()
				defer mu.Unlock()
				var out []string
				for sha := range durable {
					if committed[sha] == 0 {
						out = append(out, sha)
					}
				}
				return out, nil
			}
			// run is one holder's run: commit everything pending, then
			// release with the recheck, and go again while it re-acquires.
			run := func(rl *RunLock) {
				for {
					shas, _ := pending()
					seen := map[string]struct{}{}
					mu.Lock()
					for _, s := range shas {
						committed[s]++
					}
					mu.Unlock()
					again, err := rl.ReleaseAndRecheck(seen, pending)
					if err != nil {
						t.Fatal(err)
					}
					if !again {
						return
					}
				}
			}
			// trigger writes archive X durably, then tries the lock; if it
			// gets it, it runs; otherwise it exits.
			trigger := func() {
				mu.Lock()
				durable["X"] = true
				mu.Unlock()
				rl, ok, err := TryRunLock(v, KindIngest, "alpha")
				if err != nil {
					t.Fatal(err)
				}
				if ok {
					run(rl)
				}
			}

			holder := mustTryRunLock(t)(TryRunLock(v, KindIngest, "alpha"))
			// The holder's final rescan found nothing.
			if order == "trigger-before-release" {
				trigger() // finds the lock held and exits
				again, err := holder.ReleaseAndRecheck(map[string]struct{}{}, pending)
				if err != nil {
					t.Fatal(err)
				}
				if !again {
					t.Fatal("the holder did not re-acquire for X, which arrived after its last rescan")
				}
				run(holder)
			} else {
				fired := false
				again, err := holder.ReleaseAndRecheck(map[string]struct{}{}, func() ([]string, error) {
					if !fired {
						fired = true
						trigger() // the lock is free now: the trigger takes it and runs
					}
					return pending()
				})
				if err != nil {
					t.Fatal(err)
				}
				if again {
					t.Fatal("the holder re-acquired although the trigger had taken the lock and served X")
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if committed["X"] != 1 {
				t.Fatalf("archive X committed %d times, want exactly 1", committed["X"])
			}
			if rl, ok, err := TryRunLock(v, KindIngest, "alpha"); err != nil || !ok {
				t.Fatalf("the run lock was left held: ok=%v err=%v", ok, err)
			} else {
				_ = rl.Release()
			}
		})
	}
}

// The recheck ignores what the holder's last rescan saw: an archive left over
// by the budget (L) and a failing one (F) do not make it re-acquire; only an
// archive the rescan did not see (N) does.
func TestRecheckSkipsWhatTheLastRescanSaw(t *testing.T) {
	v := newVault(t)
	seen := map[string]struct{}{"L": {}, "F": {}}

	rl := mustTryRunLock(t)(TryRunLock(v, KindIngest, "alpha"))
	again, err := rl.ReleaseAndRecheck(seen, func() ([]string, error) { return []string{"L", "F"}, nil })
	if err != nil || again {
		t.Fatalf("recheck with only seen archives pending: again=%v err=%v, want false", again, err)
	}
	free := mustTryRunLock(t)(TryRunLock(v, KindIngest, "alpha"))
	_ = free.Release()

	rl = mustTryRunLock(t)(TryRunLock(v, KindIngest, "alpha"))
	again, err = rl.ReleaseAndRecheck(seen, func() ([]string, error) { return []string{"L", "F", "N"}, nil })
	if err != nil || !again {
		t.Fatalf("recheck with an unseen archive pending: again=%v err=%v, want true", again, err)
	}
	if got, err := ReadHolder(v); err != nil || got.PID != os.Getpid() || got.Kind != KindIngest {
		t.Fatalf("the re-acquired lock has no holder record: %+v, %v", got, err)
	}
	_ = rl.Release()
}
