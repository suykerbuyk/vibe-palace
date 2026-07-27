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

// ResumeDisposableMarker declares a resume.md H2 section as SAFE TO DROP under
// budget pressure — reference material the author has consciously decided the
// ladder may shed.
//
// It exists so that ABSENCE STOPS BEING A VALUE. With only ResumePinMarker there
// are two states in the artifact but three in the author's head: pinned,
// deliberately sheddable, and "nobody has looked at this section yet". The first
// two are decisions; the third is an omission, and collapsing it into "sheddable"
// is exactly how live state gets dropped silently. Requiring a positive
// declaration on the drop side makes the omission VISIBLE — see
// UndeclaredLiveSections, which reports the sections carrying neither marker so
// the gap can be surfaced instead of guessed at.
//
// Like the pin marker it is an HTML comment (invisible in Obsidian and GitHub)
// and it is read fence-aware, so a marker quoted inside a code fence is
// documentation rather than a declaration.
const ResumeDisposableMarker = "<!-- vp:disposable -->"

// resumeH2 is one H2 section of a resume body, located and classified.
type resumeH2 struct {
	// heading is the H2's text with the leading "## " removed, as written.
	heading string
	// start is the 1-indexed line number of the heading line; end is the
	// 1-indexed line number one past the section's last line.
	start, end int
	pinned     bool
	disposable bool
}

// scanResumeH2 splits a resume body into its H2 sections and records which
// markers each one carries.
//
// It is the ONE fence-aware walk of a resume: pinnedResumeZone and
// UndeclaredLiveSections must agree on what a section is and where it ends, or
// the reduced document and the report about what was reduced describe different
// documents. That divergence is precisely the failure mode this package already
// paid for twice (191's iteration headings, 204's task header block) — two
// private parsers of one concept, disagreeing where nobody looked. Add callers
// here rather than a third walk elsewhere.
//
// The returned lines are the split of content, so callers can slice sections out
// of it using the 1-indexed bounds. Everything above the first heading is the
// preamble and is deliberately NOT a section: it is unconditionally inline, it
// has no heading to report, and a marker stranded there declares nothing.
func scanResumeH2(content string) (lines []string, sections []resumeH2) {
	lines = strings.Split(content, "\n")

	// Fence-awareness is not decoration. A heading or a marker inside a code
	// fence is sample text. mdfence is the one definition of a fence; do not
	// hand-roll a second.
	type mark struct{ heading, pin, disposable bool }
	marks := make(map[int]mark) // keyed by 1-indexed line number
	var starts []int
	for _, l := range mdfence.OutsideFences(content) {
		switch t := strings.TrimSpace(l.Text); {
		case strings.HasPrefix(t, "## "):
			marks[l.Num] = mark{heading: true}
			starts = append(starts, l.Num)
		case t == ResumePinMarker:
			marks[l.Num] = mark{pin: true}
		case t == ResumeDisposableMarker:
			marks[l.Num] = mark{disposable: true}
		}
	}
	if len(starts) == 0 {
		return lines, nil
	}

	// A section runs from its heading to the line before the next H2, or to EOF.
	for i, start := range starts {
		end := len(lines) + 1 // 1-indexed, exclusive
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		sec := resumeH2{
			heading: strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[start-1]), "## ")),
			start:   start,
			end:     end,
		}
		for n := start; n < end; n++ {
			m := marks[n]
			sec.pinned = sec.pinned || m.pin
			sec.disposable = sec.disposable || m.disposable
		}
		sections = append(sections, sec)
	}
	return lines, sections
}

// UndeclaredLiveSections returns the H2 heading texts, in file order, of every
// section of a resume body that carries NEITHER ResumePinMarker NOR
// ResumeDisposableMarker.
//
// These are the sections nobody has ruled on, and the rule this project has
// settled on is that an unruled section is LIVE STATE, not reference material.
// Absence is not a value: "no marker" means the author has not yet said whether
// the content survives budget pressure, and treating silence as consent to drop
// is how correctness notes disappear without anyone being told. Reporting the
// gap by NAME is what makes the omission fixable — a bare count would say a
// resume is under-declared without saying where.
//
// A section carrying both markers is reported as neither: the pin wins, because
// the conservative reading of a contradictory declaration is the one that keeps
// content. It is not an undeclared section — someone did decide, twice — so
// flagging it here would send the author to a section that already has a ruling.
//
// The preamble is never reported: it is not a section, it is always inline, and
// it has no heading of its own to name.
func UndeclaredLiveSections(content string) []string {
	_, sections := scanResumeH2(content)
	var out []string
	for _, s := range sections {
		if s.pinned || s.disposable {
			continue
		}
		out = append(out, s.heading)
	}
	return out
}

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
	lines, sections := scanResumeH2(content)
	if len(sections) == 0 {
		return "", false
	}

	// The preamble — everything above the first H2 — is always kept. It carries
	// the YAML frontmatter (without which the body is not a resume) and the H1.
	var out []string
	out = append(out, lines[:sections[0].start-1]...)

	for _, s := range sections {
		// The pin wins over a co-located disposable marker: a contradictory
		// declaration is resolved toward keeping content, never toward dropping
		// it.
		if !s.pinned {
			continue
		}
		declared = true
		out = append(out, lines[s.start-1:s.end-1]...)
	}

	if !declared {
		return "", false
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n", true
}
