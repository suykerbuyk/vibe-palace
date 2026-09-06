// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import "slices"

// UnknownDimensions returns the baseline's dimension keys that THIS BINARY'S
// registry does not know, sorted.
//
// A baseline key absent from DimensionNames() is PROOF THE RUNNING BINARY IS BEHIND
// THE VAULT. The baseline is written by whichever binary last ran `vp audit vault
// --accept`; the registry is compiled in. So a key with no registry entry can only
// mean the vault has seen a newer binary than this one. No digest, no hashing, no
// build-time step and no second package is needed to know that — the two artifacts
// are already in the same room.
//
// THE KNOWN SET IS DERIVED IN HERE, NOT PASSED IN, and that is deliberate. A
// `known []string` parameter would let a caller hand in the wrong set, which makes
// the caller a SECOND DEFINITION of "what this binary knows" — and a second
// definition can only ever drift out of agreement with the first (the
// stored-derived-value defect ADR-007 is about). This package owns the registry;
// the rule belongs beside it.
//
// It is a READER-SIDE signal. Nothing here refuses, gates or exits: the dispatch-seam
// surface gate is where a stale binary gets stopped, and a second gate location is
// two answers to one question. This reports what the audit could not see.
func (b Baseline) UnknownDimensions() []string {
	known := make(map[string]bool, len(dimensions))
	for _, name := range DimensionNames() {
		known[name] = true
	}
	out := make([]string, 0, len(b.Dimensions))
	for name := range b.Dimensions {
		if !known[name] {
			out = append(out, name)
		}
	}
	// Map iteration order is randomised, and this list reaches a COMMITTED report.
	// An unsorted one would churn `git log -p Audits/` on every run and destroy the
	// week-over-week drift signal the report's fixed ordering exists to protect.
	slices.Sort(out)
	return out
}
