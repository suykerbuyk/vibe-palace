// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// This file plans the ONE-SHOT repair of the live iterations.md archives against
// the header contract in heading_contract.go. It is a pure function of file
// content: it decides WHAT to rewrite and WHAT to refuse, and writes nothing.
//
// # The rule the whole file is built to enforce
//
// A migration that repairs headings has exactly one way to corrupt history
// beyond recovery: INVENT AN ITERATION NUMBER. A framed orphan sitting between
// "## Iteration 107" and "## Iteration 109" looks like it "obviously" wants 108;
// one sitting after the last entry looks like it wants max+1; one that fills a
// numeric gap looks like it wants the gap. Every one of those is a heuristic
// dressed up as a fact, and once the number is written it is indistinguishable
// from a number the operator chose. There is no later check that can tell them
// apart, because the file is the only record.
//
// So the rule is absolute: A NUMBER MAY BE APPLIED ONLY BECAUSE IT IS EITHER
//
//   - already IN the heading (a numbered heading being normalised — the number
//     is recovered, never derived; see RecoverHeader), or
//   - on authorizedAssignments below, where a human put it.
//
// There is deliberately NO third branch. Position, gaps, neighbours and
// timestamps are not inputs to this file, and TestPlanRefusesOffListOrphan is
// the pin that keeps it that way.
//
// # Duplicate numbers are LEGAL here
//
// Three of the nine authorized rows are ADDENDUMS — a second narrative filed
// under a number that already has one, because a session with several work units
// writes several narratives. The operator has done this deliberately 15 times.
// A "fix the duplicates" pass over this archive would be an auditor inventing
// findings; the duplicate-number rule that once existed flagged 17 of these and
// was deleted for exactly that reason (see vaultaudit.auditIterationHeadings).

// AuthorizedAssignment is one iteration number a HUMAN assigned to one
// unnumbered framed orphan, keyed on all three of (project, line, exact heading
// text).
//
// All three parts of the key are load-bearing. Project and line locate the row;
// the exact TEXT is the drift guard. The archive is a live, syncing, hand-edited
// file, and a line number recorded on 2026-08-16 is a claim about a snapshot,
// not about the file the migration will actually open. Rewriting whatever
// happens to be at line 7649 because the table says 7649 is how a positional
// write silently lands on drifted content — so a row whose text no longer
// matches byte-for-byte is REFUSED and reported, never applied.
type AuthorizedAssignment struct {
	Project string // vault project slug
	Line    int    // 1-indexed line the heading sat on when the operator ruled
	Text    string // the heading line, trimmed, EXACTLY as it must still read
	N       int    // the iteration number the operator assigned
}

// authorizedAssignments is the complete list of iteration numbers assigned by
// hand for this migration. Nine rows, no more: every other repair in the plan
// recovers its number from the heading itself.
//
// Operator decision (2026-08-16), task `iterations-md-heading-contract`. Rows A,
// G and I are ADDENDUMS — a second narrative under a number that already has one
// — which is deliberate and legal (see the file comment). Zero-padding of the
// number was DECLINED in the same decision; FormatIterationHeader stays "%d".
//
// This table is a historical artifact, not a growth point. A future archive
// defect gets its own decision and its own migration; appending a tenth row here
// to fix a heading nobody ruled on would launder a guess through a table whose
// whole authority is that a human wrote every line of it.
var authorizedAssignments = []AuthorizedAssignment{
	// A — addendum to 108. The narrative that vp_get_iteration(108) was
	// silently serving as its own tail.
	{Project: "vibe-palace", Line: 7649, Text: "## Retire auto-capture-ide-hooks (verification + cleanup)", N: 108},
	// B — the 2026-06-17 wrap, filed with a date instead of a number.
	{Project: "vibe-palace", Line: 7813, Text: "## 2026-06-17 Wrap", N: 111},
	// C — filed with the task slug instead of a number.
	{Project: "vibe-palace", Line: 8683, Text: "## Grok first-class native slash commands (`grok-first-class-command-shims`)", N: 126},
	// D — filed with the change description instead of a number.
	{Project: "vibe-palace", Line: 8951, Text: "## commit.msg vault archive — reverted the a0477e4 over-ignore", N: 129},
	// E — the number is present but the "Iteration" word is not, so
	// iterHeadingRe never matched it and the reader could not see the entry.
	{Project: "vibe-palace", Line: 9873, Text: "## 146 — Review + revision of `vp-vault-status-command` plan", N: 146},
	// F — the number is present in a trailing "iter 155", again invisible to
	// iterHeadingRe, which requires the capitalised word.
	{Project: "vibe-palace", Line: 10397, Text: "## 2026-06-23 iter 155", N: 155},
	// G — addendum to 40 in the rezbldrvault archive.
	{Project: "rezbldrvault", Line: 2289, Text: "## Choon-Seng Tan manager calibration (2026-06-19)", N: 40},
	// H — "## Iteration — ..." with the number simply omitted.
	{Project: "rusty-can", Line: 80, Text: "## Iteration — P1.1 committed & retired (restart sweep)", N: 4},
	// I — addendum to 17, same omitted-number shape as H.
	{Project: "rusty-can", Line: 666, Text: "## Iteration — `p1-5-codec-hardening` committed & retired (restart sweep)", N: 17},
}

// AuthorizedAssignments returns a copy of the operator's assignment table, for
// surfaces that want to print or test it. It is a copy so no caller can grow the
// table at runtime — the table's authority is that a human wrote every row.
func AuthorizedAssignments() []AuthorizedAssignment {
	out := make([]AuthorizedAssignment, len(authorizedAssignments))
	copy(out, authorizedAssignments)
	return out
}

// RewriteSource records WHY a rewrite is allowed to name the number it names.
// It exists so the report can be read as an audit rather than taken on trust:
// every applied number is either recovered from the line or cited to a table row.
type RewriteSource string

const (
	// SourceRecovered — the number was already in the heading and was parsed
	// back out of it (RecoverHeader). No judgement, no human, no derivation.
	SourceRecovered RewriteSource = "recovered-from-heading"
	// SourceAuthorized — the number came from authorizedAssignments, where the
	// operator put it. The only other legal origin.
	SourceAuthorized RewriteSource = "operator-authorized"
)

// HeadingRewrite is one heading line the migration will replace, and only the
// heading line: From and To are whole trimmed lines, and applying a plan touches
// no other byte of the file.
type HeadingRewrite struct {
	Line   int                // 1-indexed line to replace
	From   string             // the trimmed line as it must still read on disk
	To     string             // the canonical header to write
	Class  HeadingDefectClass // the defect class that earned the rewrite
	Source RewriteSource      // why this number may be written
	N      int                // the number being written
}

// HeadingRefusal is a defective heading the migration will NOT repair, with the
// reason a human needs in order to decide what to do about it.
//
// Refusals are the point of the design, not a shortfall in it. Reporting "I can
// see this is broken and I will not guess" is the only honest output for a
// heading whose repair is a judgement call, and it is strictly better than a
// plausible invention that nothing downstream can distinguish from history.
type HeadingRefusal struct {
	Line   int                // 1-indexed line
	Text   string             // the trimmed heading as it appears
	Class  HeadingDefectClass // the defect class the scan assigned
	Reason string             // why this one is not repairable without a human
}

// AuthorizedRowState is what became of one row of the operator's table on the
// content actually scanned.
type AuthorizedRowState string

const (
	// RowMatched — the row matched a live defect byte-for-byte and produced a
	// rewrite in the plan. Named for the MATCH, not for a write: the same plan
	// is what a report-only run prints, and a row reported as "applied" on a run
	// that wrote nothing is a state label lying about what happened.
	RowMatched AuthorizedRowState = "matched"
	// RowAlreadyApplied — the line already carries the exact header this row
	// would have produced. The migration is idempotent, and a second run must
	// report "nothing to do" rather than a drift alarm.
	RowAlreadyApplied AuthorizedRowState = "already-applied"
	// RowDrifted — the line exists but does not carry the recorded text, so the
	// row is REFUSED. This is the state the whole (project, line, text) key
	// exists to produce.
	RowDrifted AuthorizedRowState = "drifted"
	// RowAbsent — the file has no such line at all (shorter than the recorded
	// line number, or the project is missing).
	RowAbsent AuthorizedRowState = "absent"
)

// AuthorizedRowResult reports one table row's fate, including the text actually
// found, so a drift report tells a human what is really there rather than only
// that something is wrong.
type AuthorizedRowResult struct {
	Row   AuthorizedAssignment
	State AuthorizedRowState
	Found string // the trimmed line actually at Row.Line ("" when absent)
}

// HeadingMigrationPlan is the complete decision for ONE project's iterations.md:
// what will be rewritten, what is refused, and what became of every authorized
// row that names this project. It is produced without touching the filesystem.
type HeadingMigrationPlan struct {
	Project    string
	Rewrites   []HeadingRewrite
	Refusals   []HeadingRefusal
	Authorized []AuthorizedRowResult
}

// Empty reports whether the plan would change nothing.
func (p HeadingMigrationPlan) Empty() bool { return len(p.Rewrites) == 0 }

// PlanHeadingMigration decides, for one project's iterations.md content, which
// headings can be repaired and which must be handed back to a human.
//
// It walks ScanHeadingDefects — the same single scan the check producer and the
// audit dimension walk — and routes each defect by ONE question: is the number
// already in the heading?
//
//	YES, with a title    → rewrite to the canonical header. The number is
//	                       RECOVERED, never derived. This covers both the
//	                       non-canonical numbered headings ("## Iteration 70:
//	                       Title", "## Iteration 215 (revision 2) — ...") and the
//	                       doubled prefixes, and it also covers the frame orphans
//	                       that happen to carry a number — the frame class wins
//	                       the label, but the repair is still mechanical.
//	YES, with NO title   → REFUSE. "## Iteration 147" is addressable but
//	                       titleless, and no title can be invented for it. Never
//	                       "(untitled)", never "<title>": a placeholder written
//	                       to disk stops reading as a placeholder.
//	NO                   → the row must be on authorizedAssignments, matched on
//	                       project AND line AND exact text. Otherwise REFUSE.
//
// The Want the scan already computed is reused verbatim for the recovered case
// rather than recomputed here, so the header the check TELLS a human to expect
// and the header the migration WRITES cannot drift apart.
func PlanHeadingMigration(project, content string) HeadingMigrationPlan {
	plan := HeadingMigrationPlan{Project: project}
	lines := strings.Split(content, "\n")

	// Index the operator's rows for this project by line. Line alone is the
	// lookup key; the TEXT is then compared byte-for-byte, so a drifted line
	// produces a loud refusal instead of quietly falling through to the
	// off-list branch (which would report the right refusal for the wrong
	// reason and hide the fact that the archive moved).
	rows := make(map[int]AuthorizedAssignment)
	for _, a := range authorizedAssignments {
		if a.Project == project {
			rows[a.Line] = a
		}
	}
	consumed := make(map[int]bool)

	for _, d := range ScanHeadingDefects(content) {
		n, title, ok := RecoverHeader(d.Text)
		switch {
		case ok && title != "":
			plan.Rewrites = append(plan.Rewrites, HeadingRewrite{
				Line: d.Line, From: d.Text, To: d.Want,
				Class: d.Class, Source: SourceRecovered, N: n,
			})
		case ok:
			plan.Refusals = append(plan.Refusals, HeadingRefusal{
				Line: d.Line, Text: d.Text, Class: d.Class,
				Reason: fmt.Sprintf("numbered (%d) but TITLELESS; a title cannot be invented — a human must supply one", n),
			})
		default:
			row, listed := rows[d.Line]
			switch {
			case !listed:
				plan.Refusals = append(plan.Refusals, HeadingRefusal{
					Line: d.Line, Text: d.Text, Class: d.Class,
					Reason: "no iteration number in the heading and no operator assignment for this line; " +
						"a number derived from file position, from a numeric gap, or from neighbouring " +
						"entries would be a guess indistinguishable from history",
				})
			case row.Text != d.Text:
				plan.Refusals = append(plan.Refusals, HeadingRefusal{
					Line: d.Line, Text: d.Text, Class: d.Class,
					Reason: fmt.Sprintf("operator assignment %d recorded for this line reads %q; the archive has DRIFTED, "+
						"and writing the assignment onto whatever is here now would corrupt a different narrative", row.N, row.Text),
				})
			case authorizedTitle(d.Text, row.N) == "":
				plan.Refusals = append(plan.Refusals, HeadingRefusal{
					Line: d.Line, Text: d.Text, Class: d.Class,
					Reason: fmt.Sprintf("operator assigned %d, but the heading has no title left once its own "+
						"redundant reference to %d is removed; a title cannot be invented", row.N, row.N),
				})
			default:
				consumed[d.Line] = true
				plan.Rewrites = append(plan.Rewrites, HeadingRewrite{
					Line: d.Line, From: d.Text, To: FormatIterationHeader(row.N, authorizedTitle(d.Text, row.N)),
					Class: d.Class, Source: SourceAuthorized, N: row.N,
				})
			}
		}
	}

	// Report every table row's fate, not only the ones that fired. A row that
	// silently matched nothing is the failure mode a count of rewrites cannot
	// show: the migration would report "6 repaired" and look successful while
	// three narratives stayed unaddressable.
	for _, a := range authorizedAssignments {
		if a.Project != project {
			continue
		}
		res := AuthorizedRowResult{Row: a}
		switch {
		case consumed[a.Line]:
			res.State = RowMatched
			res.Found = a.Text
		case a.Line < 1 || a.Line > len(lines):
			res.State = RowAbsent
		default:
			res.Found = strings.TrimSpace(lines[a.Line-1])
			// Already-applied is judged on the NUMBER, not on the exact header:
			// the whole point of the row was to give this narrative an
			// addressable N, and a later hand-edit of its title does not undo
			// that. Requiring a byte-exact match would make an ordinary title
			// fix show up as archive drift on every subsequent run, and an alarm
			// that cries wolf is an alarm nobody reads on the day it is right.
			if n, _, ok := RecoverHeader(res.Found); ok && n == a.N && IsCanonicalHeader(res.Found) {
				res.State = RowAlreadyApplied
			} else {
				res.State = RowDrifted
			}
		}
		plan.Authorized = append(plan.Authorized, res)
	}

	return plan
}

// Apply returns content with every planned heading line replaced, and nothing
// else changed.
//
// It re-verifies each From against the line on disk before replacing it. The
// plan was computed from this same content in the normal flow, so the check
// costs nothing and never fires — which is precisely why it is here: the one run
// where content and plan have come apart (a re-plan skipped, a file re-read
// between phases, a caller reusing a plan) is the run that would rewrite the
// wrong line, and it must fail loudly instead of succeeding quietly.
//
// Split/Join on "\n" round-trips byte-for-byte, including the trailing newline
// and any CR bytes, so a line the plan does not name is returned unchanged.
func (p HeadingMigrationPlan) Apply(content string) (string, error) {
	if len(p.Rewrites) == 0 {
		return content, nil
	}
	lines := strings.Split(content, "\n")
	for _, r := range p.Rewrites {
		if r.Line < 1 || r.Line > len(lines) {
			return "", fmt.Errorf("wrapstate: %s line %d is past end of file (%d lines)", p.Project, r.Line, len(lines))
		}
		if got := strings.TrimSpace(lines[r.Line-1]); got != r.From {
			return "", fmt.Errorf("wrapstate: %s line %d reads %q, plan expected %q; refusing to rewrite drifted content",
				p.Project, r.Line, got, r.From)
		}
		lines[r.Line-1] = r.To
	}
	return strings.Join(lines, "\n"), nil
}

// leadingIterationWordRe matches a title that opens with the bare word
// "Iteration" and a separator — "## Iteration — P1.1 committed", the shape the
// archive uses when the number was simply omitted. The separator is REQUIRED, so
// a title that merely starts with the word ("Iterations are cheap") is untouched,
// and a digit after the word cannot match (a numbered heading never reaches
// here anyway — RecoverHeader claimed it).
var leadingIterationWordRe = regexp.MustCompile(`^Iteration[ \t]*[—–:;,-][ \t]*`)

// authorizedTitle derives the title for an operator-assigned heading: the
// heading text with its "## " stripped, minus any REDUNDANT SELF-REFERENCE to
// the number now being written.
//
// Only redundancy is removed, and only redundancy that names THIS number:
//
//   - a leading bare "Iteration" + separator  ("Iteration — P1.1 …" → "P1.1 …")
//   - a leading literal N + separator          ("146 — Review …"    → "Review …")
//   - a trailing "iter N" / "iteration N"      ("2026-06-23 iter 155" → "2026-06-23")
//
// The leading-number rule matches the ASSIGNED NUMBER LITERALLY, never "leading
// digits". That distinction is the whole safety of this function: "## 2026-06-17
// Wrap" is assigned 111, and a leading-digits rule would strip "2026" and its
// "-" separator and file the narrative as "Iteration 111 — 06-17 Wrap". A title
// is not a place to be clever — nothing here invents a word, and a heading that
// reduces to nothing is refused by the caller rather than given a placeholder.
func authorizedTitle(headingText string, n int) string {
	s := strings.TrimSpace(headingText)
	for strings.HasPrefix(s, "#") {
		s = s[1:]
	}
	s = strings.TrimSpace(s)

	if loc := leadingIterationWordRe.FindStringIndex(s); loc != nil {
		s = s[loc[1]:]
	}
	num := strconv.Itoa(n)
	if rest, cut := strings.CutPrefix(s, num); cut {
		// Only a SEPARATOR may follow the number; anything else means the
		// leading digits were part of the prose ("1460 widgets") and the match
		// was an accident of prefix.
		if trimmed, sep := cutLeadingSeparator(rest); sep {
			s = trimmed
		}
	}
	s = strings.TrimSpace(s)
	if m := trailingIterRefRe.FindStringSubmatch(s); m != nil && m[1] == num {
		s = s[:len(s)-len(m[0])]
	}
	return strings.TrimSpace(s)
}

// cutLeadingSeparator removes one leading heading separator (with surrounding
// spaces) and reports whether one was actually there. The dash class carries em
// dash, en dash and hyphen because all three appear in the archive; colon and
// semicolon appear too.
func cutLeadingSeparator(s string) (string, bool) {
	t := strings.TrimLeft(s, " \t")
	for _, sep := range []string{"—", "–", "-", ":", ";", ","} {
		if rest, cut := strings.CutPrefix(t, sep); cut {
			return strings.TrimSpace(rest), true
		}
	}
	return s, false
}

// trailingIterRefRe matches a trailing ", iter N" / " iteration N" — the
// archive's other way of carrying the number informally
// ("## 2026-06-23 iter 155"). It is anchored to the END, and its captured number
// is compared against the ASSIGNED one by the caller: a title that mentions a
// DIFFERENT iteration in its closing words ("… supersedes iteration 12") is not
// redundant with the number being written and must survive intact.
var trailingIterRefRe = regexp.MustCompile(`(?i)[ \t]*[,;-]?[ \t]*iter(?:ation)?[ \t]+(\d+)[ \t]*$`)
