// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testinfra

import (
	"sync"

	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
)

// RecordedLaunch captures the arguments of one call to a recording fake
// detachlaunch.LaunchFunc, for tests to assert against.
type RecordedLaunch struct {
	Binary  string
	Args    []string
	LogPath string
}

// NewRecordingLaunch returns a detachlaunch.LaunchFunc that records every
// call it receives (append order = call order) and returns a fabricated,
// monotonically-increasing pid without spawning any process, plus a
// snapshot function that safely reads back everything recorded so far. The
// LaunchFunc never calls exec.Command or touches the OS process table, so a
// test that dispatches a launch-triggering tool through it never spawns a
// real process.
//
// Both are backed by the SAME private mutex, so calling snapshot
// concurrently with an in-flight call to the LaunchFunc is race-free — unlike
// reading a plain slice the LaunchFunc appends to from the outside, which
// would race under -race. Callers must always read recorded calls through
// the returned snapshot function, never by reaching into whatever backing
// slice this closes over.
//
// The starting pid (90000) is a fake-looking value deliberately outside a
// real OS's ordinary pid range, chosen only so a recorded pid is obviously
// synthetic in test output — it never corresponds to, and never touches, an
// actual process.
func NewRecordingLaunch() (launch detachlaunch.LaunchFunc, snapshot func() []RecordedLaunch) {
	var (
		mu      sync.Mutex
		calls   []RecordedLaunch
		nextPID = 90000
	)

	launch = func(binary string, args []string, logPath string) (int, error) {
		mu.Lock()
		defer mu.Unlock()

		// Copy args: callers of LaunchFunc may reuse/mutate their slice after
		// this call returns, and the recorded call must reflect what was
		// passed at call time.
		argsCopy := append([]string(nil), args...)
		calls = append(calls, RecordedLaunch{
			Binary:  binary,
			Args:    argsCopy,
			LogPath: logPath,
		})

		pid := nextPID
		nextPID++
		return pid, nil
	}

	snapshot = func() []RecordedLaunch {
		mu.Lock()
		defer mu.Unlock()
		return append([]RecordedLaunch(nil), calls...)
	}

	return launch, snapshot
}
