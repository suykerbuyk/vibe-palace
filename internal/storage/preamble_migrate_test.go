// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"strings"
	"testing"
)

// headerOf returns everything through the last contiguous header-field line —
// the region the migration must copy through byte-for-byte.
func headerOf(t *testing.T, content string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	_, end := headerBlock(lines)
	return strings.Join(lines[:end], "\n")
}

// preambleOf returns the text between the header block and the first H2. After
// the migration this must be empty for every migrated file — that structural
// zero is the whole point of the pass.
func preambleOf(t *testing.T, content string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	_, start := headerBlock(lines)
	for i := start; i < len(lines); i++ {
		if isH2Line(strings.TrimSpace(lines[i])) {
			return strings.TrimSpace(strings.Join(lines[start:i], "\n"))
		}
	}
	return strings.TrimSpace(strings.Join(lines[start:], "\n"))
}

const migFixtureHeader = "# A Task\n\n**Status:** pending\n**Priority:** high\n"

func TestMovePreambleInsertsContextWhenFirstH2IsSomethingElse(t *testing.T) {
	const claim = "Head of the queue. Do these phases IN ORDER, and before any other work."
	before := migFixtureHeader + "\n" + claim + "\n\n## The evidence\n\nBody text.\n"

	after, outcome := MovePreambleUnderContext(before)
	if outcome != PreambleMovedIntoNewContext {
		t.Fatalf("outcome = %v, want PreambleMovedIntoNewContext", outcome)
	}

	// POSITIONAL: the preamble region is now EMPTY.
	if got := preambleOf(t, after); got != "" {
		t.Errorf("preamble is not empty after migration: %q", got)
	}
	// And the text sits under Context, above the original first heading.
	ci := strings.Index(after, "## "+ConventionalFirstHeading)
	ti := strings.Index(after, claim)
	ei := strings.Index(after, "## The evidence")
	switch {
	case ci < 0:
		t.Fatalf("no %q heading inserted:\n%s", ConventionalFirstHeading, after)
	case ti < ci:
		t.Errorf("moved text is ABOVE the Context heading (%d < %d) — still a preamble", ti, ci)
	case ti > ei:
		t.Errorf("moved text landed below the original first heading (%d > %d)", ti, ei)
	}
	// Header byte-identical.
	if got, want := headerOf(t, after), headerOf(t, before); got != want {
		t.Errorf("header block changed\n got: %q\nwant: %q", got, want)
	}
	// The original section survives.
	if !strings.Contains(after, "## The evidence\n\nBody text.") {
		t.Errorf("original section was damaged:\n%s", after)
	}
}

func TestMovePreamblePrependsWhenContextIsAlreadyFirst(t *testing.T) {
	const claim = "Filed out of a sweep. Also: do not absorb this into the sibling."
	before := migFixtureHeader + "\n" + claim + "\n\n## " + ConventionalFirstHeading + "\n\nExisting context body.\n"

	after, outcome := MovePreambleUnderContext(before)
	if outcome != PreambleMovedIntoExistingContext {
		t.Fatalf("outcome = %v, want PreambleMovedIntoExistingContext", outcome)
	}
	if got := preambleOf(t, after); got != "" {
		t.Errorf("preamble is not empty after migration: %q", got)
	}
	// No second Context heading.
	if n := strings.Count(after, "## "+ConventionalFirstHeading); n != 1 {
		t.Errorf("Context heading appears %d times, want 1:\n%s", n, after)
	}
	// Moved text leads the section, existing body follows.
	ci := strings.Index(after, "## "+ConventionalFirstHeading)
	ti := strings.Index(after, claim)
	bi := strings.Index(after, "Existing context body.")
	if !(ci < ti && ti < bi) {
		t.Errorf("order is wrong (heading %d, moved %d, existing %d):\n%s", ci, ti, bi, after)
	}
	if got, want := headerOf(t, after), headerOf(t, before); got != want {
		t.Errorf("header block changed")
	}
}

func TestMovePreambleEmptyIsANoOp(t *testing.T) {
	before := migFixtureHeader + "\n## " + ConventionalFirstHeading + "\n\nBody.\n"
	after, outcome := MovePreambleUnderContext(before)
	if outcome != PreambleEmpty {
		t.Fatalf("outcome = %v, want PreambleEmpty", outcome)
	}
	if after != before {
		t.Errorf("an empty preamble must be a byte-for-byte no-op\n got: %q\nwant: %q", after, before)
	}
}

// TestMovePreambleSkipsAFileWithNoH2 pins the Critical-3 branch. With no "## "
// heading the region definition degenerates to "the entire body", and rewriting
// a whole task end-to-end is not what this migration means.
func TestMovePreambleSkipsAFileWithNoH2(t *testing.T) {
	before := migFixtureHeader + "\nSome framing.\n\n### A sub-heading\n\nMore body.\n"
	after, outcome := MovePreambleUnderContext(before)
	if outcome != PreambleSkippedNoH2 {
		t.Fatalf("outcome = %v, want PreambleSkippedNoH2", outcome)
	}
	if after != before {
		t.Errorf("a skipped file must be byte-for-byte unchanged\n got: %q", after)
	}
}

// TestMovePreambleIsFenceAware: a "## " inside a code fence is sample text, not
// the first heading. Task bodies routinely quote markdown.
func TestMovePreambleIsFenceAware(t *testing.T) {
	before := migFixtureHeader + "\nFraming above.\n\n```md\n## Not A Real Heading\n```\n\n## Real Heading\n\nBody.\n"
	after, outcome := MovePreambleUnderContext(before)
	if outcome != PreambleMovedIntoNewContext {
		t.Fatalf("outcome = %v, want PreambleMovedIntoNewContext", outcome)
	}
	// The fenced sample must still be inside the fence, below Context, and the
	// real heading must remain the section after it.
	if !strings.Contains(after, "```md\n## Not A Real Heading\n```") {
		t.Errorf("fenced sample was damaged:\n%s", after)
	}
	ci := strings.Index(after, "## "+ConventionalFirstHeading)
	fi := strings.Index(after, "## Not A Real Heading")
	if ci > fi {
		t.Errorf("Context was inserted below the fenced sample — the fence was treated as a heading")
	}
	if got := preambleOf(t, after); got != "" {
		t.Errorf("preamble not empty: %q", got)
	}
}
