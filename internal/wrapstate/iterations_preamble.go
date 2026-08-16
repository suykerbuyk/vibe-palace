// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import "strings"

// The false "newest first" preamble, and its removal.
//
// Two iterations.md files (vibe-palace and rezbldrvault) open with an HTML
// comment that says the narratives are ordered "newest first". They are not, and
// never were: AppendIterationOwned seeks to the END of the file and appends, so
// the order is OLDEST first — the order the narratives were written. The comment
// is not stale, it is BACKWARDS, which is the worse failure: a reader who trusts
// it and takes "the first entry" gets the oldest narrative in the archive while
// believing it has the newest, and nothing about the result looks wrong.
//
// A false comment in a data file is also uniquely durable. Nothing compiles it,
// nothing tests it, and every agent that reads the file reads it as fact. This
// one had survived long enough to be copied verbatim into a second project.
//
// # Why an exact literal, and not a "find the newest-first claim" rule
//
// Three other places in the vault say "newest first" TRUTHFULLY:
//
//   - rezbldr/iterations.md L5 — a prose sentence scoped to three preserved
//     pre-rename iterations that really are newest-first. It is CORRECT and must
//     be left exactly as it is.
//   - quantum-ng and vibe-palace narrative bodies — describing `git log`, which
//     really does print newest-first.
//
// A rule shaped like "delete the sentence that claims newest first" would have
// to be taught about all three, and would still be one archive away from
// deleting a true statement it had not been taught about. Matching the ONE known
// block byte-for-byte cannot: anything that is not that block, including a
// hand-edited variant of it, is reported and left alone.

// falseNewestFirstPreamble is the exact HTML comment block, byte-for-byte, that
// the two archives carry. Both copies are identical, which is unsurprising —
// the second was seeded from the first.
const falseNewestFirstPreamble = `<!-- Each iteration gets a section below, newest first.
     An "iteration" is a logical unit of work (feature, fix, refactor)
     that may span multiple sessions. -->`

// AccurateIterationsPreamble is the replacement: one sentence that is true of
// the file the writer actually produces. It states the three facts a reader
// needs and stops — append-only, file order is write order, and a narrative is
// reached by its NUMBER rather than by its position, which is what makes the
// ordering question stop mattering.
const AccurateIterationsPreamble = `<!-- This file is APPEND-ONLY: every iteration is added at the END, so file
     order is the order the narratives were written, and a narrative is
     addressed by its number with vp_get_iteration. -->`

// PreambleRepairState is what the preamble repair found in one archive.
type PreambleRepairState string

const (
	// PreambleRepaired — the exact false block was present once, in the
	// preamble, and was replaced.
	PreambleRepaired PreambleRepairState = "repaired"
	// PreambleAbsent — the archive does not carry the block (never did, or the
	// repair already ran). The idempotent, uninteresting case.
	PreambleAbsent PreambleRepairState = "absent"
	// PreambleAmbiguous — the block appears more than once. Refused: a second
	// copy is not something this repair was reasoned about, and one of them may
	// be quoted inside a narrative as evidence rather than stated as fact.
	PreambleAmbiguous PreambleRepairState = "ambiguous"
	// PreambleNotInPreamble — the block appears only AFTER the first iteration
	// heading, so it is narrative body (someone quoting the defect), not the
	// file's own preamble. Refused.
	PreambleNotInPreamble PreambleRepairState = "not-in-preamble"
)

// RepairIterationsPreamble replaces the false "newest first" preamble comment
// with an accurate one, and returns the new content plus what it found.
//
// It is a pure function and it changes NOTHING else: the replacement is a
// single literal-for-literal substitution, so a file without the exact block is
// returned identical. Every non-repaired state leaves content untouched.
func RepairIterationsPreamble(content string) (string, PreambleRepairState) {
	count := strings.Count(content, falseNewestFirstPreamble)
	switch {
	case count == 0:
		return content, PreambleAbsent
	case count > 1:
		return content, PreambleAmbiguous
	}
	// The block must sit in the PREAMBLE — ahead of the first entry heading.
	// Below that line it is body text: a narrative quoting the defect it was
	// filed about, which must survive verbatim as evidence.
	idx := strings.Index(content, falseNewestFirstPreamble)
	if heads := ScanIterHeadings(content); len(heads) > 0 {
		if firstHeadingOffset(content, heads[0].Line) <= idx {
			return content, PreambleNotInPreamble
		}
	}
	return strings.Replace(content, falseNewestFirstPreamble, AccurateIterationsPreamble, 1), PreambleRepaired
}

// firstHeadingOffset returns the byte offset of 1-indexed line num within
// content, or len(content) when the line is past the end.
func firstHeadingOffset(content string, num int) int {
	off := 0
	for i := 1; i < num; i++ {
		nl := strings.IndexByte(content[off:], '\n')
		if nl < 0 {
			return len(content)
		}
		off += nl + 1
	}
	return off
}
