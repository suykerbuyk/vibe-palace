// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// RecordSkip names one record a collection reader could not parse.
//
// It is a REPORT, not an error, and the distinction is the whole of the fix it
// belongs to. Two malformed notes out of 2360 once returned the entire session
// index of the two largest projects as empty, because ListSessions was
// fail-closed and every caller either bailed on the error or dropped the
// results — so vp_bootstrap_context reported zero sessions and the absence read
// as normal.
//
// 🔴 IT IS RETURNED, NOT LOGGED, AND THERE IS NO VARIANT THAT DISCARDS IT. A
// wrapper that dropped skips would be shorter, so it is what every future call
// site would reach for — and a silently skipped record is the same defect as a
// fail-closed read, moved down a layer. Every caller takes the extra return
// value and decides how to surface it; the compiler is what makes that decision
// unavoidable.
//
// # ONE TYPE, DELIBERATELY, ACROSS EVERY COLLECTION
//
// It was born as SessionSkip and generalized the moment a second reader needed
// it (ListEntities). A near-identical SessionSkip beside an EntitySkip is how
// two copies of one lesson come to disagree — one grows a field, one grows a
// different reason format, and the surface that renders both has to know which
// it is holding. The cost of generalizing was one rename across seven files,
// paid while the type was two commits old.
//
// 🔴 A SKIP IS NOT AVAILABLE TO EVERY READER, and adding this type to a reader
// is not the same as that reader being allowed to use it. Tolerance is correct
// only where the write path can genuinely produce a half-written record:
// appended JSONL (sessions, entities, drawers, the ingest ledger) has a torn
// final line as a normal outcome of an interrupted append. A collection whose
// writer is atomicfile.Write cannot produce a partial record at all, so a
// malformed one there means corruption, and skipping it would turn "something
// is wrong" into "that record does not exist". ListTriples is the live example
// and it stays fail-closed on purpose — see TestListTriplesStaysFailClosed.
type RecordSkip struct {
	// Path is vault-relative, because that is what an operator acts on and what
	// is stable across hosts. For a record INSIDE a file it carries the line as
	// "<path>:<line>", so the report names the record rather than only the file.
	Path   string
	Reason string
}
