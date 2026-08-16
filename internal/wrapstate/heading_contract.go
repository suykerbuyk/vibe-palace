// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
)

// This file is the ONE definition of the iterations.md entry frame and of the
// ways a heading can fail to be the header the writer would have emitted. Do not
// add a second copy — the entire class of defect it exists to close is two
// private definitions of one concept disagreeing where nobody looked.
//
// The frame is DERIVED FROM THE WRITER, not guessed at from heading shapes.
// storage.(*Vault).AppendIterationOwned composes every entry as:
//
//	"\n---\n" + FormatIterationHeader(n, title) + "\n\n" + body + "\n"
//
// so "an entry begins" means exactly one thing: a bare "---" line whose next
// non-empty neighbour is an H2 heading. The reader half (iterHeadingRe) has
// always recognised only "## Iteration N", and that gap is what this file
// closes. Live archives carry H2 headings that sit on the writer's frame but are
// not "## Iteration N" — "## 2026-06-17 Wrap", "## Grok first-class native slash
// commands (...)", "## 146 — Review + ...", "## Iteration — P1.1 committed".
// ParseEntries could not see them, so each one was silently glued onto the END
// of the PREVIOUS numbered entry's body. vp_get_iteration for N=108, 110, 125,
// 128, 145 and 154 each returned that iteration PLUS the whole following orphan
// and reported success — a reader that over-returns while claiming to be exact
// is worse than one that fails, because nothing downstream can tell.

// FrameOrphan is an H2 heading sitting on the writer's entry frame that
// FormatIterationHeader would not have emitted. It is a real entry boundary in
// the file — the writer's own frame says so — that carries no iteration number,
// so it can never become an Entry. ParseEntries must still STOP at it.
type FrameOrphan struct {
	Line int    // 1-indexed line number within the source content
	Text string // trimmed heading line
}

// yamlKeyRe matches a YAML/front-matter-ish "key:" line. It is deliberately
// narrow: an ASCII-leading identifier of word characters, spaces and hyphens,
// then a colon. Narrative prose almost never takes that shape, and a heading
// never does (it starts with '#').
var yamlKeyRe = regexp.MustCompile(`^[A-Za-z_][\w -]*:`)

// yamlLookback is how far back from a candidate "---" the YAML-closer probe
// will search for the matching opener. Front matter blocks in this vault run a
// handful of keys; twelve lines is generous without reaching back far enough to
// collide with the previous entry's own frame.
const yamlLookback = 12

// ScanFrameOrphans returns every H2 heading that sits on the writer's entry
// frame but is not the header FormatIterationHeader would have emitted, in file
// order.
//
// The rules, in the order they are applied:
//
//   - Fence-aware, via internal/mdfence — the ONE fence definition in this
//     codebase. An inline ``` run is NOT a fence opener (see the mdfence package
//     doc; iterations.md carries the line that proves it). A heading is also
//     rejected outright if any line between it and its predecessor is fenced,
//     because then the "---" the naive walk would find lives on the far side of
//     a code block and is not this heading's frame.
//   - An indent of four or more spaces is an indented code block, not a heading
//     (CommonMark). scanIterHeadings already applies this rule; disagreeing with
//     it here would recreate the two-definitions defect one layer down.
//   - The heading must be H2. H3 is the level narratives use for their own
//     sub-sections ("### Phase 1 — ..."), and the writer never emits one as a
//     header, so an H3 on a frame is body content.
//   - Its last non-empty predecessor line must be exactly "---".
//   - A "---" that CLOSES a YAML/front-matter block is not the writer's frame.
//     Live counterexample from recmeet: "---\nshape: ...\nsummary: ...\n---\n##
//     Session arc". Without this exclusion the heading below a front-matter
//     block reads as an entry boundary and splits a document that has no
//     entries at all.
//   - A canonical header is by definition not an orphan.
func ScanFrameOrphans(content string) []FrameOrphan {
	lines := strings.Split(content, "\n")
	// outside[i] reports whether 1-indexed line i lies outside every code fence.
	// Fence DELIMITER lines are outside-false too, which is what makes the
	// "no fenced line in between" guard above work.
	outside := make([]bool, len(lines)+2)
	for _, l := range mdfence.OutsideFences(content) {
		if l.Num < len(outside) {
			outside[l.Num] = true
		}
	}

	var out []FrameOrphan
	for _, l := range mdfence.OutsideFences(content) {
		if !isH2Heading(l.Text) {
			continue
		}
		p := lastNonEmptyPredecessor(lines, outside, l.Num)
		if p == 0 || strings.TrimSpace(lines[p-1]) != "---" {
			continue
		}
		if isYAMLCloser(lines, outside, p) {
			continue
		}
		trimmed := strings.TrimSpace(l.Text)
		if IsCanonicalHeader(trimmed) {
			continue
		}
		out = append(out, FrameOrphan{Line: l.Num, Text: trimmed})
	}
	return out
}

// isH2Heading reports whether line is an ATX H2 heading outside an indented code
// block. The "## " prefix test excludes H3 for free: a third '#' is not a space.
func isH2Heading(line string) bool {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent >= 4 || indent >= len(line) {
		return false
	}
	s := line[indent:]
	return strings.HasPrefix(s, "## ") || strings.HasPrefix(s, "##\t")
}

// lastNonEmptyPredecessor returns the 1-indexed line number of the last
// non-empty line before num, skipping blank lines. It returns 0 when it runs off
// the top of the file or when it meets a line that is inside (or delimits) a
// code fence — in that case whatever lies further back is on the far side of a
// code block and cannot be this heading's frame.
func lastNonEmptyPredecessor(lines []string, outside []bool, num int) int {
	for i := num - 1; i >= 1; i-- {
		if !outside[i] {
			return 0
		}
		if strings.TrimSpace(lines[i-1]) == "" {
			continue
		}
		return i
	}
	return 0
}

// isYAMLCloser reports whether the "---" at 1-indexed line p closes a
// YAML/front-matter-ish block rather than opening the writer's entry frame.
//
// It walks backwards from p looking for an opening "---" within yamlLookback
// lines and requires every intervening non-empty line to be a "key:" line. Both
// halves matter: without the opener the probe would classify any "---" preceded
// by prose-shaped metadata as front matter, and without the key test the
// previous entry's own frame (which is always within a dozen lines of a short
// body) would look like an opener.
func isYAMLCloser(lines []string, outside []bool, p int) bool {
	for i := p - 1; i >= 1 && p-i <= yamlLookback; i-- {
		if !outside[i] {
			return false
		}
		t := strings.TrimSpace(lines[i-1])
		if t == "---" {
			// Every line between the two markers already passed the key test on
			// the way here, so this is front matter.
			return true
		}
		if t == "" {
			continue
		}
		if !yamlKeyRe.MatchString(t) {
			return false
		}
	}
	return false
}

// IsCanonicalHeader reports whether text is byte-identical to what
// FormatIterationHeader would emit for the number it names.
//
// It is implemented as a ROUND TRIP — parse N and the title back out, re-emit,
// compare — rather than as a second regex, so it cannot drift away from the
// writer. Note what that buys and what it does not: see HasDoubledPrefix for a
// corruption this oracle is structurally incapable of seeing.
func IsCanonicalHeader(text string) bool {
	trimmed := strings.TrimSpace(text)
	m := iterHeadingRe.FindStringSubmatch(trimmed)
	if m == nil {
		return false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return false
	}
	return trimmed == FormatIterationHeader(n, titleFromHeader(trimmed))
}

// doubledPrefixRe matches a title that itself begins with another
// "Iteration N —" prefix. The dash class carries em dash, en dash and hyphen
// because all three appear in the archive.
var doubledPrefixRe = regexp.MustCompile(`^Iteration \d+[ \t]*[—–-][ \t]*`)

// HasDoubledPrefix reports whether the title extracted from a heading itself
// begins with another "Iteration N —" prefix.
//
// This rule exists because IsCanonicalHeader CANNOT catch it, and the reason is
// worth stating plainly: titleFromHeader strips exactly ONE "Iteration N —"
// prefix and FormatIterationHeader re-adds exactly ONE, so
//
//	## Iteration 40 — Iteration 40 — Global AI Eric prep addendum (...)
//
// is a FIXED POINT of the round trip. IsCanonicalHeader returns true for it, and
// for the four siblings like it in the live archive. A round-trip oracle is
// structurally blind to every corruption it is idempotent over — which is the
// general lesson, not a quirk of this one string. Any check built as "re-emit
// and compare" needs a companion rule for the fixed points, and this is it.
func HasDoubledPrefix(text string) bool {
	return doubledPrefixRe.MatchString(titleFromHeader(text))
}

// CanonicalHeaderShape is the writer's header with its number and title left as
// placeholders — the one wording every surface quotes when it tells a human what
// a broken heading was supposed to look like.
//
// It is PROSE, not a second definition: FormatIterationHeader is the executable
// contract and nothing branches on this string. It exists so the check producer
// and the audit dimension quote ONE sentence instead of inventing two that drift
// apart — the same single-definition rule the rest of this file is built on.
// TestCanonicalHeaderShapeMatchesTheWriter pins it to FormatIterationHeader's
// actual output, so the drift it guards against cannot happen silently.
const CanonicalHeaderShape = "## Iteration N — Title"

// HeadingDefectClass names one of the three INDEPENDENT ways an iterations.md
// heading fails the contract. Independent is the operative word: each class is
// invisible to the other two, which is why all three are checked rather than
// whichever one happened to be written first.
type HeadingDefectClass string

const (
	// DefectFrameOrphan — an H2 on the writer's "\n---\n" entry frame that
	// FormatIterationHeader would not have emitted. The worst of the three: it
	// is a real entry boundary carrying no number, so the whole narrative under
	// it is unaddressable AND is silently served as the tail of the PREVIOUS
	// entry (the 108/110/125/128/145/154 over-return).
	DefectFrameOrphan HeadingDefectClass = "frame-orphan"

	// DefectNonCanonicalNumbered — a heading iterHeadingRe DOES match, whose
	// text is not what FormatIterationHeader would emit: "## Iteration 70:
	// Concurrent Recording" (colon), "## Iteration 215 (revision 2) — ..."
	// (infix), "## Iteration 147" (no title), "### Iteration 2 — ..." (legacy
	// H3, the level 191 earned). These are addressable by number, so the frame
	// rule alone does not see the ones that sit mid-body — both rules are
	// required, neither subsumes the other.
	DefectNonCanonicalNumbered HeadingDefectClass = "non-canonical-numbered"

	// DefectDoubledPrefix — a title that itself begins with another
	// "Iteration N —". These PASS IsCanonicalHeader: the round trip is
	// idempotent over the corruption (see HasDoubledPrefix), so neither of the
	// other two classes can find them. Five live instances in rezbldrvault.
	DefectDoubledPrefix HeadingDefectClass = "doubled-prefix"
)

// HeadingDefect is one defective heading, reported ONCE with the class that
// describes it. See ScanHeadingDefects for why "once" needs saying.
type HeadingDefect struct {
	Line  int                // 1-indexed line within the source content
	Text  string             // the trimmed heading line as it appears
	Class HeadingDefectClass // which rule it broke
	Want  string             // the header the writer would have emitted
}

// ScanHeadingDefects returns every heading in an iterations.md that fails the
// contract, in file order, EACH ONE EXACTLY ONCE.
//
// This is the shared scan behind both surfaces — check.CheckIterationHeadings
// (the `iteration-headings` producer, which is what vp_check and the
// restart/wrap flows reach) and vaultaudit.auditIterationHeadings. Neither
// re-implements a condition; a third private copy of these rules is the defect
// this whole file exists to prevent.
//
// # Deduplication, and why it is not cosmetic
//
// A non-canonical NUMBERED heading that also sits on the frame satisfies rule 1
// AND rule 2 — recmeet's "## Iteration 93: ..." is exactly that. Reporting it
// twice would inflate every count the operator reads, and an auditor whose
// numbers are inflated is an auditor that gets waved off. The frame class WINS,
// because it is the strictly worse fact about the line: the heading is a real
// entry boundary the reader half could not see, and the repair that fixes the
// frame problem fixes the canonicity problem for free.
//
// The doubled-prefix class is disjoint from the other two by construction (a
// doubled heading is canonical, so it is neither an orphan nor non-canonical),
// so its precedence never actually fires — it is declared anyway so a future
// edit that makes the classes overlap still cannot double-report.
//
// # What it deliberately DOES NOT report
//
// Duplicate iteration numbers. That rule was written, flagged 17 "defects"
// across 6 projects on its first live run, was found on inspection to be
// flagging a DELIBERATE pattern (a session with several work units writes
// several narratives under one number — the operator has done it 15 times), and
// was deleted. See the comment on vaultaudit.auditIterationHeadings before you
// are tempted to re-add it.
func ScanHeadingDefects(content string) []HeadingDefect {
	claimed := make(map[int]bool)
	var out []HeadingDefect

	// Rule 1 first, so it owns any line the numbered rules would also claim.
	for _, o := range ScanFrameOrphans(content) {
		claimed[o.Line] = true
		out = append(out, HeadingDefect{
			Line:  o.Line,
			Text:  o.Text,
			Class: DefectFrameOrphan,
			Want:  wantHeaderFor(o.Text),
		})
	}

	for _, h := range ScanIterHeadings(content) {
		if claimed[h.Line] {
			continue
		}
		switch {
		case !IsCanonicalHeader(h.Text):
			claimed[h.Line] = true
			out = append(out, HeadingDefect{
				Line:  h.Line,
				Text:  h.Text,
				Class: DefectNonCanonicalNumbered,
				Want:  wantHeaderFor(h.Text),
			})
		case HasDoubledPrefix(h.Text):
			claimed[h.Line] = true
			out = append(out, HeadingDefect{
				Line:  h.Line,
				Text:  h.Text,
				Class: DefectDoubledPrefix,
				Want:  wantHeaderFor(h.Text),
			})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// wantHeaderFor returns the header FormatIterationHeader would have emitted for
// a defective heading, recovering the number and title from the heading itself.
//
// It returns CanonicalHeaderShape when no number can be recovered: an unnumbered
// framed orphan ("## 2026-06-17 Wrap") carries no N, and inventing one would be
// the check guessing at history. The repair there is a human decision about
// which iteration the narrative belongs to.
//
// The title is passed through StripDoubledPrefix, so the suggested repair for a
// doubled heading is the clean one rather than a faithful re-emission of the
// corruption.
func wantHeaderFor(text string) string {
	n, title, ok := RecoverHeader(text)
	if !ok {
		return CanonicalHeaderShape
	}
	if title == "" {
		// "## Iteration 147" has no title at all. A placeholder is honest;
		// FormatIterationHeader(147, "") would suggest a header with a trailing
		// space, which is not a shape anyone should copy. Note that the
		// PLACEHOLDER is a suggestion for a human to read, never something a
		// repair may write — see PlanHeadingMigration, which refuses this case
		// outright rather than committing "<title>" (or "(untitled)") to the
		// archive. A placeholder written to disk stops looking like a
		// placeholder the moment someone greps for it.
		title = "<title>"
	}
	return FormatIterationHeader(n, title)
}

// RecoverHeader parses a defective heading back into the (number, title) pair
// the writer should have been handed, reporting whether a number could be
// recovered AT ALL.
//
// This is the ONE place that answers "can this heading be repaired without a
// human?", and both the reporting surfaces (wantHeaderFor, behind the check and
// the audit dimension) and the repairing surface (PlanHeadingMigration) ask it.
// A second private copy of the question is exactly the defect
// heading_contract.go exists to prevent — and here it would be worse than
// cosmetic: the reporting half and the repairing half disagreeing about whether
// a number is recoverable is how a migration invents one.
//
// ok is false when the heading carries no "Iteration <digits>" at all
// ("## 2026-06-17 Wrap", "## Iteration — P1.1 committed"). Those have no N to
// recover, and deriving one from file position or from the gap it happens to
// fill is guessing at history; the repair is a human decision.
//
// ok is TRUE with an EMPTY title for "## Iteration 147" — numbered, titleless.
// The number is recoverable, the title is not, and the two facts must stay
// separable: a caller that only checks ok would otherwise happily emit a header
// with an invented title.
//
// The title is passed through trimHeaderSeparator (the malformed archive uses
// ':' where the writer uses '—') and StripDoubledPrefix (so a doubled heading
// recovers the clean title rather than a faithful re-emission of the
// corruption).
func RecoverHeader(text string) (n int, title string, ok bool) {
	trimmed := strings.TrimSpace(text)
	m := iterHeadingRe.FindStringSubmatch(trimmed)
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return 0, "", false
	}
	return n, StripDoubledPrefix(trimHeaderSeparator(titleFromHeader(trimmed))), true
}

// trimHeaderSeparator drops a leading separator that titleFromHeader left behind.
//
// titleFromHeader cuts only the two separators the WRITER emits (em dash and
// hyphen), which is correct for its job — IsCanonicalHeader is a round trip and
// must not be taught to forgive punctuation the writer never produces. But the
// live archive's malformed headings use others: "## Iteration 70: Concurrent
// Recording" leaves ": Concurrent Recording" as the title, and suggesting
// "## Iteration 70 — : Concurrent Recording" as the repair hands a human a
// second defect to fix. This tidies the SUGGESTION only; no rule consults it.
func trimHeaderSeparator(title string) string {
	return strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(title), ":;,.—–-"))
}

// StripDoubledPrefix removes repeated leading "Iteration N —" runs from a title,
// returning the bare title the writer should have been handed.
//
// It loops rather than stripping once: a title can be doubled more than twice,
// and a repair that fixes only the first layer leaves a heading that still
// fails HasDoubledPrefix while looking fixed. The regex is anchored, so a title
// that merely MENTIONS an iteration mid-sentence ("revisits the Iteration 5
// decision") is returned untouched.
func StripDoubledPrefix(title string) string {
	s := strings.TrimSpace(title)
	for {
		loc := doubledPrefixRe.FindStringIndex(s)
		if loc == nil {
			return s
		}
		s = strings.TrimSpace(s[loc[1]:])
	}
}
