// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import "github.com/suykerbuyk/vibe-palace/internal/archive"

// SetCoverageListEntriesHook is a TEST SEAM for packages that drive coverage
// from outside this one (internal/tools counts how many coverage derivations a
// bootstrap performs): f replaces the archive-manifest listing coverageCounts
// calls, once per CoverageState derivation. It returns the function that
// restores the previous listing. Production code never calls it, the same
// convention as indexstore.SetCommitStepHook.
func SetCoverageListEntriesHook(f func(vaultRoot, project string) ([]*archive.Entry, error)) (restore func()) {
	old := coverageListEntries
	coverageListEntries = f
	return func() { coverageListEntries = old }
}
