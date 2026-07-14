// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
)

// ResumePinMarker declares a resume.md H2 section as ALWAYS-INLINE: the token
// shed ladder in AssembleBootstrap may drop every unmarked section, but never a
// marked one.
//
// It is a MARKER IN THE ARTIFACT, and it is deliberately not a list of heading
// names in this file. A code-side allowlist would look simpler and would fail
// silently: rename "Project-Specific Behavioral Notes" in a resume and the
// section carrying the stale-binary rule, the CAS/expansion trap and the NTFS
// constraint stops being pinned — with nothing anywhere reporting that it did.
// A resume is edited far more often than this package is, so the rule has to
// live where the editing happens.
//
// The marker is an HTML comment so it renders as nothing in Obsidian and in
// GitHub, and it is read fence-aware (see pinnedResumeZone), so a marker quoted
// inside a code fence is documentation, not a declaration.
const ResumePinMarker = "<!-- vp:pin -->"

// pinnedResumeZone returns the always-inline zone of a resume body: the
// preamble (frontmatter, H1, any leading comment) followed by every H2 section
// that carries ResumePinMarker, in file order.
//
// declared reports whether the document pinned at least one H2 SECTION. It is
// the load-bearing half of this function, because of what the caller does with
// false: a resume that declares no pin zone is NOT SHEDDABLE AT ALL — bootstrap
// keeps it fully inline and reports over-budget rather than guessing which half
// of an undeclared document was safe to drop. Guessing wrong drops the
// correctness notes silently, which is the exact failure class this whole epic
// exists to delete. Degrading to "too big, and here is why" is safe; degrading
// to "quietly smaller" is not.
//
// A marker sitting in the preamble alone does NOT count as a declaration: it
// pins nothing that was not already inline, and honoring it would shed the
// entire body down to the frontmatter on the strength of one stray line.
func pinnedResumeZone(content string) (zone string, declared bool) {
	lines := strings.Split(content, "\n")

	// Fence-awareness is not decoration. A heading or a marker inside a code
	// fence is sample text, and two separate parser bugs in this project (191's
	// iteration headings, 204's task header block) were exactly this mistake.
	// mdfence is the one definition of a fence; do not hand-roll a second.
	heading := make(map[int]bool) // 1-indexed line numbers of real H2 headings
	marker := make(map[int]bool)  // 1-indexed line numbers of real pin markers
	for _, l := range mdfence.OutsideFences(content) {
		switch t := strings.TrimSpace(l.Text); {
		case strings.HasPrefix(t, "## "):
			heading[l.Num] = true
		case t == ResumePinMarker:
			marker[l.Num] = true
		}
	}

	// Walk the H2 section boundaries in order. A section runs from its heading
	// to the line before the next H2, or to EOF.
	var starts []int
	for i := 1; i <= len(lines); i++ {
		if heading[i] {
			starts = append(starts, i)
		}
	}
	if len(starts) == 0 {
		return "", false
	}

	// The preamble — everything above the first H2 — is always kept. It carries
	// the YAML frontmatter (without which the body is not a resume) and the H1.
	var out []string
	out = append(out, lines[:starts[0]-1]...)

	for i, start := range starts {
		end := len(lines) + 1 // 1-indexed, exclusive
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		pinned := false
		for n := start; n < end; n++ {
			if marker[n] {
				pinned = true
				break
			}
		}
		if !pinned {
			continue
		}
		declared = true
		out = append(out, lines[start-1:end-1]...)
	}

	if !declared {
		return "", false
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n", true
}
