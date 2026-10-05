// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import "sync/atomic"

// lockEventKind names one lock operation, for the test-only recorder.
type lockEventKind int

const (
	evRunTry lockEventKind = iota
	evRunAcquired
	evRunReleased
	evCommitWait
	evCommitAcquired
	evCommitReleased
)

type lockEvent struct {
	kind    lockEventKind
	project string
}

// lockRecorder, when a test sets it, sees every lock operation this package
// performs. Production leaves it nil. It is how the tests check that a probe
// never takes the run lock and that the index commit lock is a leaf.
var lockRecorder func(lockEvent)

func record(ev lockEvent) {
	if r := lockRecorder; r != nil {
		r(ev)
	}
	if o := commitObserver.Load(); o != nil {
		switch ev.kind {
		case evCommitWait:
			(*o)(ev.project, CommitWait)
		case evCommitAcquired:
			(*o)(ev.project, CommitAcquired)
		case evCommitReleased:
			(*o)(ev.project, CommitReleased)
		}
	}
	if o := runObserver.Load(); o != nil {
		switch ev.kind {
		case evRunTry:
			(*o)(RunTry)
		case evRunAcquired:
			(*o)(RunAcquired)
		case evRunReleased:
			(*o)(RunReleased)
		}
	}
}

// RunLockEvent is what ObserveRunLocks reports.
type RunLockEvent string

const (
	// RunTry: a run lock try-acquire is about to happen (the only authoritative
	// check; a status probe must never cause one).
	RunTry RunLockEvent = "try"
	// RunAcquired: the run lock is held.
	RunAcquired RunLockEvent = "acquired"
	// RunReleased: the run lock is released.
	RunReleased RunLockEvent = "released"
)

// runObserver is ObserveRunLocks' observer.
var runObserver atomic.Pointer[func(RunLockEvent)]

// ObserveRunLocks installs f to see every run-lock try, acquire and release
// this process performs, and returns a function that removes it. It is a TEST
// SEAM, declared in non-test code because tests in other packages need it: the
// rebuild driver pins that it try-locks the run lock exactly once per run and
// that vp_refresh_index's probe never try-locks it. Production never sets it.
func ObserveRunLocks(f func(RunLockEvent)) (restore func()) {
	runObserver.Store(&f)
	return func() { runObserver.Store(nil) }
}

// CommitLockEvent is what ObserveCommitLocks reports.
type CommitLockEvent string

const (
	// CommitWait: about to wait for (or try) a project's commit lock.
	CommitWait CommitLockEvent = "wait"
	// CommitAcquired: the commit lock is held.
	CommitAcquired CommitLockEvent = "acquired"
	// CommitReleased: the commit lock is released.
	CommitReleased CommitLockEvent = "released"
)

// commitObserver is ObserveCommitLocks' observer.
var commitObserver atomic.Pointer[func(project string, ev CommitLockEvent)]

// ObserveCommitLocks installs f to see every index commit lock this process
// waits for, takes and releases, and returns a function that removes it. It is a TEST SEAM, declared in non-test code because tests in
// other packages need it: internal/search pins that its index sweep never runs
// while the engine holds a commit lock, which nothing else on disk would show.
// Production never sets it.
func ObserveCommitLocks(f func(project string, ev CommitLockEvent)) (restore func()) {
	commitObserver.Store(&f)
	return func() { commitObserver.Store(nil) }
}
