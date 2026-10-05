// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

// SetCommitStepHook is a TEST SEAM for packages that drive the store, such as
// the pending-archive ingester's crash tests: f is called after each durable
// step of a commit (the step names CommitArchive, Supersede and CommitBatch
// pass: "superseding", "vectors", "chunks", "kg", "ledger"), and a non-nil
// error stops the commit there, leaving the files exactly as a process killed
// at that point would. It returns the function that restores the previous
// hook. Production code never calls it.
func SetCommitStepHook(f func(step string) error) (restore func()) {
	old := commitStep
	commitStep = f
	return func() { commitStep = old }
}
