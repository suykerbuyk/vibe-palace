// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"strings"
	"testing"
)

// Unit B2 — duplicate header-field repair.
//
// Every assertion here is an EXACT STRING or a byte comparison. None is a
// terminal-status predicate: IsTerminalStatus admits only "done"/"cancelled",
// and 15 of B2's 18 files carry "**Status:** retired", so an assertion phrased
// "the Status is still the terminal token" is FALSE on correct code — and the
// obvious way to make it pass is to widen IsTerminalStatus, which
// TestIsTerminalStatusDoesNotAdmitTheLegacyValue exists to forbid.

const b2DupPriorityBefore = "# T\n" +
	"\n" +
	"**Status:** retired\n" +
	"**Priority:** medium\n" +
	"\n" +
	"## Context\n" +
	"\n" +
	"body\n" +
	"\n" +
	"**Priority:** high\n"

const b2DupPriorityAfter = "# T\n" +
	"\n" +
	"**Status:** retired\n" +
	"**Priority:** medium\n" +
	"\n" +
	"## Context\n" +
	"\n" +
	"body\n" +
	"\n" +
	"**Legacy priority:** high\n"

// TestRepairDuplicateHeaderField_ByteExact is the per-class before/after fixture.
//
// Break: make the transform return its input unchanged. The got != before arm
// fails with a message naming THAT break rather than a diff.
func TestRepairDuplicateHeaderField_ByteExact(t *testing.T) {
	if err := ValidateWholeTaskFile(b2DupPriorityBefore); err == nil {
		t.Fatal("precondition: the fixture must FAIL the validator, or there is nothing to repair")
	}
	got, err := RepairDuplicateHeaderField(b2DupPriorityBefore, fieldPriority)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if got == b2DupPriorityBefore {
		t.Fatal("the transform was a NO-OP: it returned its input unchanged")
	}
	if got != b2DupPriorityAfter {
		t.Errorf("repaired bytes differ from the literal expectation:\n got %q\nwant %q", got, b2DupPriorityAfter)
	}
	if err := ValidateWholeTaskFile(got); err != nil {
		t.Errorf("repaired file does not validate: %v", err)
	}
}

// TestRepairDuplicateHeaderField_PreservesEveryOtherLine is the line-count plus
// per-line assertion. It subsumes "delete the duplicate instead of relabelling"
// and every adjacent lossy variant — stripping the "**" markers, or truncating
// the value at the first colon — which a strings.Contains(after, value) check
// would let through.
func TestRepairDuplicateHeaderField_PreservesEveryOtherLine(t *testing.T) {
	got, err := RepairDuplicateHeaderField(b2DupPriorityBefore, fieldPriority)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	lb := strings.Split(b2DupPriorityBefore, "\n")
	la := strings.Split(got, "\n")
	if len(la) != len(lb) {
		t.Fatalf("relabel changed the line count %d -> %d: a line was deleted or inserted", len(lb), len(la))
	}
	changed := 0
	for i := range lb {
		if la[i] == lb[i] {
			continue
		}
		changed++
		want := strings.Replace(lb[i], "**Priority:**", "**Legacy priority:**", 1)
		if la[i] != want {
			t.Errorf("line %d changed by something other than the label rename:\n  before %q\n  after  %q",
				i+1, lb[i], la[i])
		}
	}
	if changed != 1 {
		t.Errorf("changed %d lines, want exactly 1", changed)
	}
}

// TestRepairDuplicateHeaderField_InBlockLinesAreByteIdentical is the positional
// directionality assertion — the one the plan originally called "the
// orphaned-fields assertion", a phrase that named nothing in this tree.
//
// Break: target the IN-BLOCK occurrence. This test fails, and so does the
// refusal test below.
func TestRepairDuplicateHeaderField_InBlockLinesAreByteIdentical(t *testing.T) {
	got, err := RepairDuplicateHeaderField(b2DupPriorityBefore, fieldPriority)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	lb := strings.Split(b2DupPriorityBefore, "\n")
	la := strings.Split(got, "\n")
	start, end := headerBlock(lb)
	if start == end {
		t.Fatal("precondition: the fixture must have a non-empty header block")
	}
	for i := start; i < end; i++ {
		if la[i] != lb[i] {
			t.Errorf("header-block line %d was rewritten: before %q, after %q", i+1, lb[i], la[i])
		}
	}
}

// TestRepairDuplicateHeaderField_RelabelledLineIsNotAField pins the SPACE.
//
// Break: spell the constant "**Legacy_priority:**". The file still VALIDATES, so
// only this assertion catches it.
func TestRepairDuplicateHeaderField_RelabelledLineIsNotAField(t *testing.T) {
	got, err := RepairDuplicateHeaderField(b2DupPriorityBefore, fieldPriority)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, legacyPriorityRelabel) {
			continue
		}
		if isHeaderFieldLine(line) {
			t.Errorf("the relabelled line is still a header field line (%q): the label must not be a bare word", line)
		}
		if _, ok := headerFieldName(line); ok {
			t.Errorf("headerFieldName still recognises the relabelled line (%q)", line)
		}
		return
	}
	t.Fatalf("no relabelled line found in:\n%s", got)
}

// TestRepairDuplicateHeaderField_BothInBlockIsRefused is the fixture B1's
// reviews twice found missing: a duplicate INSIDE the block must be REFUSED,
// not silently relabelled, because relabelling either one truncates the block.
func TestRepairDuplicateHeaderField_BothInBlockIsRefused(t *testing.T) {
	const bothInBlock = "# T\n\n**Status:** retired\n**Priority:** medium\n**Priority:** high\n\n## Context\n\nbody\n"
	if err := ValidateWholeTaskFile(bothInBlock); err == nil {
		t.Fatal("precondition: the fixture must FAIL the validator")
	}
	_, err := RepairDuplicateHeaderField(bothInBlock, fieldPriority)
	if err == nil {
		t.Fatal("BOTH occurrences are inside the header block and the repair did not refuse")
	}
	if !strings.Contains(err.Error(), "inside the contiguous header block") {
		t.Errorf("refusal cites the wrong cause: %v", err)
	}
}

// TestRepairDuplicateHeaderField_FencedOccurrenceIsNotCounted is the
// fence-awareness fixture. A "**Status:**" inside a code fence is sample text.
//
// The assertion is BYTE-EXACT on the fenced region, not on validity: a
// line-prefix scan produces a VALID file here, so a validity-only assertion is
// green under that break.
func TestRepairDuplicateHeaderField_FencedOccurrenceIsNotCounted(t *testing.T) {
	const fenced = "# T\n" +
		"\n" +
		"**Status:** retired\n" +
		"**Priority:** medium\n" +
		"\n" +
		"## Context\n" +
		"\n" +
		"```\n" +
		"**Status:** sample\n" +
		"```\n" +
		"\n" +
		"**Status:** later prose\n"
	if err := ValidateWholeTaskFile(fenced); err == nil {
		t.Fatal("precondition: the fixture must FAIL the validator, or the transform is never reached")
	}
	got, err := RepairDuplicateHeaderField(fenced, fieldStatus)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if !strings.Contains(got, "```\n**Status:** sample\n```") {
		t.Errorf("the transform reached inside a code fence; it is not using mdfence.OutsideFences:\n%s", got)
	}
	if !strings.Contains(got, legacyStatusRelabel+" later prose") {
		t.Errorf("the real out-of-block duplicate was not relabelled:\n%s", got)
	}
}

// TestRepairDuplicateHeaderField_StatusValueIsUnchanged asserts the resolved
// Status by EXACT STRING, equal to the before value.
//
// 🔴 Deliberately not IsTerminalStatus: "retired" is not terminal, so that
// predicate is false here on correct code. A predicate would also pass under a
// substitution to a different terminal value; only byte equality catches that.
func TestRepairDuplicateHeaderField_StatusValueIsUnchanged(t *testing.T) {
	const dupStatus = "# T\n" +
		"\n" +
		"**Status:** retired\n" +
		"**Priority:** medium\n" +
		"\n" +
		"**Status:** Planned 2026-06-18. No code written. A whole paragraph of provenance.\n" +
		"\n" +
		"## Context\n" +
		"\n" +
		"body\n"
	before := ParseTaskMetaFromContent("t", dupStatus, true)
	if before.Status != "retired" {
		t.Fatalf("precondition: before Status = %q, want %q", before.Status, "retired")
	}
	got, err := RepairDuplicateHeaderField(dupStatus, fieldStatus)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	after := ParseTaskMetaFromContent("t", got, true)
	if after.Status != "retired" {
		t.Errorf("Status = %q, want the exact string %q — the prose paragraph must never become the bound value",
			after.Status, "retired")
	}
	if after.Status != before.Status {
		t.Errorf("Status moved %q -> %q", before.Status, after.Status)
	}
	if n := strings.Count(got, "**Status:**"); n != 1 {
		t.Errorf("found %d \"**Status:**\" lines after the repair, want exactly 1", n)
	}
	if !strings.Contains(got, "Planned 2026-06-18. No code written. A whole paragraph of provenance.") {
		t.Error("the provenance paragraph was not preserved verbatim")
	}
}
