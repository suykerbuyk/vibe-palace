// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
)

// PreambleOutcome is what MovePreambleUnderContext decided about one task file.
type PreambleOutcome int

const (
	// PreambleEmpty: nothing sits between the header block and the first H2.
	// A no-op, not an error — this is the shape CreateTask produces today and
	// the structural zero the whole migration exists to reach.
	PreambleEmpty PreambleOutcome = iota
	// PreambleMovedIntoExistingContext: the first H2 was already the
	// conventional heading, so the preamble text was prepended to that
	// section's body.
	PreambleMovedIntoExistingContext
	// PreambleMovedIntoNewContext: the first H2 was something else, so a
	// conventional heading was INSERTED carrying the former preamble, and the
	// rest of the file follows unchanged.
	PreambleMovedIntoNewContext
	// PreambleSkippedNoH2: the file has no "## " heading at all outside code
	// fences, so "everything above the first H2" is the ENTIRE body.
	//
	// 🔴 This branch exists because the region definition degenerates here, not
	// because these files are fine. Rewriting a whole task body end-to-end is
	// not what "move the preamble" means, and a migration that silently did it
	// would be indistinguishable from a bug. Skip, report as its own class,
	// leave the bytes alone.
	//
	// The separate defect is NOT that "every amend appends". upsertSection
	// appends only when sectionBounds misses, so the FIRST amend of a given
	// section name appends that H2 after the unsectioned prose and every LATER
	// amend of the same name replaces it — pinned by
	// TestAmendTaskAppendsWhenSectionAbsent and TestAmendTaskIsIdempotent. What
	// actually persists is that the ORIGINAL unsectioned prose stays
	// permanently unreachable: no section name addresses it, so no amend can
	// ever revise it. Only a whole-file overwrite can.
	//
	// That is also the only path that can reintroduce the class — CreateTask
	// emits ConventionalFirstHeading unconditionally, so no task is born
	// without an H2 — and validateWholeTaskFile now refuses a whole-file write
	// with no "## " heading outside code fences. The files this branch skips
	// are the pre-existing ones; nothing new joins them.
	PreambleSkippedNoH2
)

// MovePreambleUnderContext moves ALL text between the end of a task file's
// header block and its first H2 heading down under the conventional first
// heading (ConventionalFirstHeading), returning the rewritten file.
//
// # Why ALL of it, with no classifier
//
// The tempting version moves only "claims" and leaves provenance in place. That
// needs a rule separating the two, and no such rule survives contact with real
// files: two active tasks open with nearly identical words — "Filed 2026-07-30
// from the host-parity Option C decision" and "Filed 2026-07-30 from the
// review-plan pass ... Do not absorb this into <slug>" — and only the second
// carries a standing directive. Any regex admitting the first admits the second.
//
// Moving everything dissolves that problem instead of solving it: afterwards a
// NON-EMPTY preamble is itself the finding, which is mechanical, and CreateTask
// already guarantees the same zero for new tasks.
//
// # What it does not touch
//
// The header block is copied through byte-for-byte. Title, status, priority,
// parent and depends each have their own writer, and this is not one of them.
// Everything from the first H2 onward is likewise preserved verbatim except for
// the heading insertion or the prepend.
//
// Fence-aware: a "## " inside a code fence is sample text, not the first
// heading. Task bodies routinely quote markdown, and a local re-implementation
// of fence detection is the 191/204 defect.
func MovePreambleUnderContext(content string) (string, PreambleOutcome) {
	lines := strings.Split(content, "\n")

	// End of the contiguous header-field run: the preamble starts here.
	_, hdrEnd := headerBlock(lines)

	// First H2 OUTSIDE fences. mdfence.Line.Num is 1-indexed.
	firstH2 := -1
	for _, l := range mdfence.OutsideFences(content) {
		if isH2Line(strings.TrimSpace(l.Text)) {
			firstH2 = l.Num - 1
			break
		}
	}
	if firstH2 < 0 {
		return content, PreambleSkippedNoH2
	}
	if firstH2 < hdrEnd {
		// Degenerate: a heading before the header block ended. Leave it alone
		// rather than guessing what the file meant.
		return content, PreambleSkippedNoH2
	}

	preamble := strings.Join(lines[hdrEnd:firstH2], "\n")
	if strings.TrimSpace(preamble) == "" {
		return content, PreambleEmpty
	}
	moved := strings.TrimSpace(preamble)

	header := lines[:hdrEnd]
	rest := lines[firstH2:]

	var b strings.Builder
	b.WriteString(strings.Join(header, "\n"))
	b.WriteString("\n\n")

	wantHeading := "## " + ConventionalFirstHeading
	if strings.TrimSpace(rest[0]) == wantHeading {
		// The conventional heading is already first: the moved text becomes the
		// head of its body, ahead of whatever that section already said.
		b.WriteString(wantHeading)
		b.WriteString("\n\n")
		b.WriteString(moved)
		body := strings.TrimLeft(strings.Join(rest[1:], "\n"), "\n")
		if strings.TrimSpace(body) != "" {
			b.WriteString("\n\n")
			b.WriteString(body)
		} else {
			b.WriteString("\n")
		}
		return normalizeTrailingNewline(b.String()), PreambleMovedIntoExistingContext
	}

	// The first H2 is something else: insert the conventional heading carrying
	// the former preamble, then the rest of the file unchanged.
	b.WriteString(wantHeading)
	b.WriteString("\n\n")
	b.WriteString(moved)
	b.WriteString("\n\n")
	b.WriteString(strings.Join(rest, "\n"))
	return normalizeTrailingNewline(b.String()), PreambleMovedIntoNewContext
}

// normalizeTrailingNewline guarantees exactly one trailing newline, matching
// what CreateTask and the amend path already produce.
func normalizeTrailingNewline(s string) string {
	return strings.TrimRight(s, "\n") + "\n"
}
