// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
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

// ---------------------------------------------------------------------------
// Glued fence delimiter.
// ---------------------------------------------------------------------------

// b2GluedFenceBefore reproduces the corpus shape: a fence opens, and the line
// that LOOKS like its closing delimiter carries an info string containing a
// backtick — so it can neither close that fence nor open a new one, and
// everything below renders as code.
const b2GluedFenceBefore = "# T\n" +
	"\n" +
	"**Status:** retired\n" +
	"**Priority:** high\n" +
	"\n" +
	"## Context\n" +
	"\n" +
	"```\n" +
	"some code\n" +
	"```//then referencing `${BOOT0_SERIAL}` in the args.\n" +
	"\n" +
	"## Results\n" +
	"\n" +
	"body\n"

// TestRepairGluedFenceDelimiter_SplitsAndRevealsStructure pins that the split
// restores downstream pairing — the H2 below the glued delimiter is swallowed
// before the repair and visible after it.
func TestRepairGluedFenceDelimiter_SplitsAndRevealsStructure(t *testing.T) {
	if err := ValidateWholeTaskFile(b2GluedFenceBefore); err == nil {
		t.Fatal("precondition: the fixture must FAIL the validator on the unterminated fence")
	}
	got, err := RepairGluedFenceDelimiter(b2GluedFenceBefore)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if verr := ValidateWholeTaskFile(got); verr != nil {
		t.Fatalf("repaired file does not validate: %v", verr)
	}
	if !strings.Contains(got, "```\n//then referencing `${BOOT0_SERIAL}` in the args.\n") {
		t.Errorf("the delimiter was not split into a bare delimiter plus its prose:\n%s", got)
	}
	// The whole point of the repair: structure below the bad delimiter re-emerges.
	var h2 int
	for _, l := range mdfence.OutsideFences(got) {
		if isH2Line(l.Text) {
			h2++
		}
	}
	if h2 != 2 {
		t.Errorf("found %d unfenced H2 headings after the repair, want 2 — the downstream pairing was not restored", h2)
	}
	// No byte of the info string is lost.
	if !strings.Contains(got, "//then referencing `${BOOT0_SERIAL}` in the args.") {
		t.Error("the glued prose was not preserved verbatim")
	}
}

// TestRepairGluedFenceDelimiter_IsIdempotent pins the property the detector's
// info-string keying buys. A second application must select nothing.
//
// 🔴 This matters more here than elsewhere: the second application would ALSO
// validate, so the writer's post-condition cannot catch a non-idempotent detector
// on this class.
func TestRepairGluedFenceDelimiter_IsIdempotent(t *testing.T) {
	once, err := RepairGluedFenceDelimiter(b2GluedFenceBefore)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if _, err := RepairGluedFenceDelimiter(once); err == nil {
		t.Fatal("a second application selected the file again: the detector is not keyed on the info string")
	}
}

// ---------------------------------------------------------------------------
// Bare legacy status line: construct Priority AND relocate the prose.
// ---------------------------------------------------------------------------

const b2BareLegacyBefore = "# Plan: Phase D\n" +
	"Status: Closed — operator accepted retrospective 2026-06-06; advancing to Phase E. AC2 carried forward.\n" +
	"\n" +
	"**Status:** retired\n" +
	"**Source:** doc/RESUMPTION-PLAN.md\n" +
	"\n" +
	"## Objective\n" +
	"\n" +
	"body\n"

// TestRepairBareLegacyStatusLine_NeitherHalfAloneValidates is the multi-transform
// pin: the class exists because one fix per file is not enough here.
func TestRepairBareLegacyStatusLine_NeitherHalfAloneValidates(t *testing.T) {
	before := ValidateWholeTaskFile(b2BareLegacyBefore)
	if before == nil {
		t.Fatal("precondition: the fixture must FAIL the validator")
	}
	got, err := RepairBareLegacyStatusLine(b2BareLegacyBefore)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if verr := ValidateWholeTaskFile(got); verr != nil {
		t.Fatalf("repaired file does not validate: %v", verr)
	}
	if !strings.Contains(got, "**Priority:** "+LegacyPriorityDefault) {
		t.Errorf("the constructed Priority is missing:\n%s", got)
	}
	if !strings.Contains(got, legacyHeaderSectionHeading) {
		t.Errorf("the relocated prose did not land in the legacy-header body section:\n%s", got)
	}
	if !strings.Contains(got, "Status: Closed — operator accepted retrospective 2026-06-06; advancing to Phase E. AC2 carried forward.") {
		t.Error("the bare legacy line was not preserved verbatim")
	}
}

// TestRepairBareLegacyStatusLine_DoesNotMergeOntoTheBoldStatus is the assertion
// that stands between this file and the destruction the whole unit sequences
// around.
//
// The Status is asserted as an EXACT STRING. Deliberately NOT IsTerminalStatus:
// "retired" is not terminal, so that predicate is false here on correct code, and
// the obvious way to make it pass is to widen IsTerminalStatus.
func TestRepairBareLegacyStatusLine_DoesNotMergeOntoTheBoldStatus(t *testing.T) {
	got, err := RepairBareLegacyStatusLine(b2BareLegacyBefore)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	meta := ParseTaskMetaFromContent("phase-d", got, true)
	if meta.Status != "retired" {
		t.Errorf("Status = %q, want the exact string %q — the prose must never become the bound value", meta.Status, "retired")
	}
	if strings.Count(got, "**Status:**") != 1 {
		t.Errorf("found %d \"**Status:**\" lines, want exactly 1", strings.Count(got, "**Status:**"))
	}
	if strings.Contains(got, "**Status:** Closed") {
		t.Error("the bare line was MERGED onto the bold Status field")
	}
	// Relocating DISARMS the sibling repair rather than merely avoiding it.
	if scan := ScanLegacyHeader(got); scan.BareLine != 0 {
		t.Errorf("a bare legacy line remains at %d: the legacy-header repair would still plan a "+
			"destructive merge on this file", scan.BareLine)
	}
}

// TestRepairGluedFenceDelimiter_LeavesLegitimateInfoStringsAlone is the fixture
// that distinguishes "keyed on the info string" from "keyed on HAVING an info
// string", and it exists because a break that dropped the OpensFence check left
// every other test in this file GREEN.
//
// A fence opened with a language tag — ```go — has a non-empty info string and is
// entirely correct. Splitting it would strip the tag onto its own line and turn a
// working fence into two, changing how the block renders and, on a file whose
// fences carry structure, what the validator counts.
//
// Break: drop the mdfence.OpensFence check from the detector. This test fails;
// without it, nothing does.
func TestRepairGluedFenceDelimiter_LeavesLegitimateInfoStringsAlone(t *testing.T) {
	const withLangTag = "# T\n" +
		"\n" +
		"**Status:** retired\n" +
		"**Priority:** high\n" +
		"\n" +
		"## Context\n" +
		"\n" +
		"```go\n" +
		"func main() {}\n" +
		"```\n" +
		"\n" +
		"```\n" +
		"plain\n" +
		"```//then referencing `${X}` in the args.\n" +
		"\n" +
		"## Results\n" +
		"\n" +
		"body\n"
	got, err := RepairGluedFenceDelimiter(withLangTag)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if !strings.Contains(got, "```go\nfunc main() {}\n```\n") {
		t.Errorf("a LEGITIMATE ```go fence was split; the detector must key on whether the info string "+
			"PREVENTS the delimiter from opening a fence, not merely on its presence:\n%s", got)
	}
	if !strings.Contains(got, "```\n//then referencing `${X}` in the args.\n") {
		t.Errorf("the glued delimiter was not split:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// The header-run wedge: a REFUSAL predicate, with no repair beside it.
// ---------------------------------------------------------------------------

// TestHeaderRunProseWedges_FindsASingleWedge pins the n=1 case, which is the one
// with no structural signal: a lone non-field line inside the run is reported so
// the caller can refuse, because nothing in the bytes says whether it is prose or
// the continuation of the value above it.
func TestHeaderRunProseWedges_FindsASingleWedge(t *testing.T) {
	const one = "# T\n\n**Status:** retired\nfor `/vpc-execute-plan` pending human sign-off.\n**Priority:** high\n\n## Context\n\nbody\n"
	got := HeaderRunProseWedges(one)
	if len(got) != 1 || got[0] != 4 {
		t.Errorf("wedges = %v, want exactly [4] — a single wedge must still be reported, or the caller "+
			"cannot refuse the case where a wrapped value's continuation would be re-attributed", got)
	}
}

// TestHeaderRunProseWedges_FindsEveryWedge pins the n>1 case, where the COUNT is
// structural proof that the field VALUES wrap.
func TestHeaderRunProseWedges_FindsEveryWedge(t *testing.T) {
	const many = "# T\n\n**Status:** retired\ncontinuation one.\n**Priority:** Low — a value\ncontinuation two.\n**Filed:** 2026-05-13\ncontinuation three.\n\n## Problem\n\nbody\n"
	got := HeaderRunProseWedges(many)
	if len(got) != 3 {
		t.Errorf("wedges = %v, want 3", got)
	}
}

// TestHeaderRunProseWedges_ContiguousRunHasNone pins the negative: a clean header
// block must not be reported, or every valid file becomes a refusal.
func TestHeaderRunProseWedges_ContiguousRunHasNone(t *testing.T) {
	const clean = "# T\n\n**Status:** retired\n**Priority:** high\n\n## Context\n\nbody\n"
	if got := HeaderRunProseWedges(clean); len(got) != 0 {
		t.Errorf("wedges = %v, want none on a contiguous field run", got)
	}
}

// TestHeaderRunProseWedges_StopsAtTheBlankLine pins that the scan does not reach
// past the header region into the body, which would report every prose paragraph
// in the file as a wedge.
func TestHeaderRunProseWedges_StopsAtTheBlankLine(t *testing.T) {
	const bodyProse = "# T\n\n**Status:** retired\n**Priority:** high\n\nordinary body prose\n\n## Context\n\nbody\n"
	if got := HeaderRunProseWedges(bodyProse); len(got) != 0 {
		t.Errorf("wedges = %v, want none — the scan must stop at the blank line that ends the header region", got)
	}
}
