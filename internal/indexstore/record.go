// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

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
}
