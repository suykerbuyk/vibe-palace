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

// ---------------------------------------------------------------------------
// Extra titles and interleaved header prose.
// ---------------------------------------------------------------------------

const b2TwoTitlesBefore = "# First title\n" +
	"\n" +
	"**Status:** retired\n" +
	"**Priority:** medium\n" +
	"\n" +
	"# Second wording of the same title\n" +
	"\n" +
	"## Problem\n" +
	"\n" +
	"body\n"

// TestRepairExtraTitles_IsFlat pins that existing H2s and below are untouched:
// a section's range terminates at the next H1 or H2, so flat keeps every existing
// section range byte-identical while cascading would destroy every section key.
//
// Break: also demote H2 to H3. This test fails on "## Problem".
func TestRepairExtraTitles_IsFlat(t *testing.T) {
	got, err := RepairExtraTitles(b2TwoTitlesBefore)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	want := strings.Replace(b2TwoTitlesBefore, "# Second wording", "## Second wording", 1)
	if got != want {
		t.Errorf("flat demotion produced unexpected bytes:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(got, "\n## Problem\n") {
		t.Error("an existing H2 was demoted: the transform must be FLAT")
	}
	if strings.Count(got, "\n# ") != 0 || !strings.HasPrefix(got, "# First title") {
		t.Errorf("wrong number of surviving H1 titles:\n%s", got)
	}
}

const b2WedgeBefore = "# T\n" +
	"\n" +
	"**Status:** retired\n" +
	"a sentence that is not a field line.\n" +
	"**Priority:** high\n" +
	"\n" +
	"**Epoch:** E1\n" +
	"\n" +
	"## Context\n" +
	"\n" +
	"body\n"

// TestRepairInterleavedHeaderProse_IsMinimal pins that only the lines INSIDE the
// run move. A greedy rule would also hoist the **Epoch:** field below the blank,
// extending headerBlock over a name that was previously outside it — which the
// strict writer refuses.
//
// Break: extend the wedge past the blank line. The **Epoch:** assertion fails.
func TestRepairInterleavedHeaderProse_IsMinimal(t *testing.T) {
	if err := ValidateWholeTaskFile(b2WedgeBefore); err == nil {
		t.Fatal("precondition: the fixture must FAIL the validator")
	}
	got, err := RepairInterleavedHeaderProse(b2WedgeBefore)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if verr := ValidateWholeTaskFile(got); verr != nil {
		t.Fatalf("repaired file does not validate: %v", verr)
	}
	want := "# T\n" +
		"\n" +
		"**Status:** retired\n" +
		"**Priority:** high\n" +
		"a sentence that is not a field line.\n" +
		"\n" +
		"**Epoch:** E1\n" +
		"\n" +
		"## Context\n" +
		"\n" +
		"body\n"
	if got != want {
		t.Errorf("relocation produced unexpected bytes:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(got, "\n\n**Epoch:** E1\n") {
		t.Error("the **Epoch:** field below the blank was hoisted: the relocation must be MINIMAL, not greedy")
	}
	if len(strings.Split(got, "\n")) != len(strings.Split(b2WedgeBefore, "\n")) {
		t.Error("the relocation changed the line count")
	}
}

// TestRepairInterleavedHeaderProse_RefusesWrappedFieldValues is the guard that
// stands between this transform and the live corpus file whose four header values
// are hard-wrapped.
//
// 🔴 THE REFUSED OUTPUT WOULD HAVE VALIDATED. That is the whole point: a
// mechanical relocate on wrapped values severs each value from its continuation
// and stacks the remainders under whichever field follows, and
// ValidateWholeTaskFile returns nil on the result. No validator, audit dimension
// or existing test reports it, so this refusal is the only thing that can.
//
// Break: delete the wedge count. This test fails, and nothing else does.
func TestRepairInterleavedHeaderProse_RefusesWrappedFieldValues(t *testing.T) {
	const wrapped = "# T\n" +
		"\n" +
		"**Status:** retired\n" +
		"but deferred 2026-06-07: a continuation of the STATUS value.\n" +
		"**Priority:** Low — speculative hardening\n" +
		"(a continuation of the PRIORITY value).\n" +
		"**Filed:** 2026-05-13 — discovered during recovery\n" +
		"(a continuation of the FILED value).\n" +
		"\n" +
		"## Problem\n" +
		"\n" +
		"body\n"
	if err := ValidateWholeTaskFile(wrapped); err == nil {
		t.Fatal("precondition: the fixture must FAIL the validator")
	}
	_, err := RepairInterleavedHeaderProse(wrapped)
	if err == nil {
		t.Fatal("a header region of WRAPPED field values was mechanically relocated; that output validates " +
			"while every value is severed from its own continuation")
	}
	if !strings.Contains(err.Error(), "prose wedges") {
		t.Errorf("refusal cites the wrong cause: %v", err)
	}
}

// TestRepairInterleavedHeaderProse_DoesNotMergeOntoStatus pins that the wedge is
// relocated, never joined onto the field above it.
//
// A merged value would be non-terminal, dropping the file into the whole-line
// replacing population of the status and board-fields migrations — destroying the
// prose by way of the migration this repair exists to unblock.
//
// The Status is asserted as an EXACT STRING. Deliberately not IsTerminalStatus:
// "retired" is not terminal, so that predicate is false here on correct code.
func TestRepairInterleavedHeaderProse_DoesNotMergeOntoStatus(t *testing.T) {
	got, err := RepairInterleavedHeaderProse(b2WedgeBefore)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	meta := ParseTaskMetaFromContent("t", got, true)
	if meta.Status != "retired" {
		t.Errorf("Status = %q, want the exact string %q", meta.Status, "retired")
	}
	if strings.Contains(got, "**Status:** retired a sentence") {
		t.Error("the wedge was MERGED onto the Status line")
	}
	if !strings.Contains(got, "a sentence that is not a field line.") {
		t.Error("the wedge text was lost")
	}
}

// TestRepairInterleavedHeaderProse_DoesNotReachPastABlank is the discriminating
// fixture for GREEDY-versus-MINIMAL, and it exists because a break that removed
// the blank-line stop left every other test in this file GREEN.
//
// The earlier minimal fixture cannot see that break: its wedge is followed
// IMMEDIATELY by a field line, so greedy and minimal agree on it. The difference
// only shows where a blank separates the wedge from the fields below — there,
// minimal REFUSES (the run genuinely ended and there is nothing interleaved),
// while greedy swallows the blank and hoists fields that were never in the run,
// extending headerBlock over names the strict writer then refuses.
//
// Break: drop the strings.TrimSpace(lines[w]) != "" condition from the wedge scan.
// This test fails; without it, nothing does.
func TestRepairInterleavedHeaderProse_DoesNotReachPastABlank(t *testing.T) {
	const blankSeparated = "# T\n" +
		"\n" +
		"**Status:** retired\n" +
		"a wedge sentence.\n" +
		"\n" +
		"**Priority:** high\n" +
		"\n" +
		"## Context\n" +
		"\n" +
		"body\n"
	if ValidateWholeTaskFile(blankSeparated) == nil {
		t.Fatal("precondition: the fixture must FAIL the validator")
	}
	got, err := RepairInterleavedHeaderProse(blankSeparated)
	if err == nil {
		t.Fatalf("the wedge scan reached PAST a blank line and hoisted fields that were never in the "+
			"header run; the relocation must be MINIMAL:\n%s", got)
	}
	if !strings.Contains(err.Error(), "no header field line after the wedge") {
		t.Errorf("refusal cites the wrong cause: %v", err)
	}
}
