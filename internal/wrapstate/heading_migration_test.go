// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// orphanFrame emits the writer's entry frame carrying an arbitrary heading —
// the shape a framed orphan actually has on disk. writerFrame cannot be used
// because it composes the CANONICAL header, which is the one thing these tests
// need to not have.
func orphanFrame(heading, body string) string {
	return "\n---\n" + heading + "\n\n" + strings.TrimSpace(body) + "\n"
}

// planFor is the common setup: one preamble, one clean numbered entry to anchor
// the file, then the heading under test on the writer's frame.
func planFor(t *testing.T, project, heading string) HeadingMigrationPlan {
	t.Helper()
	content := "# p — Iteration Narratives\n" +
		writerFrame(1, "anchor", "anchor body") +
		orphanFrame(heading, "the narrative under the heading")
	return PlanHeadingMigration(project, content)
}

// ---------------------------------------------------------------------------
// Class 1: a number already in the heading is RECOVERED, never derived.
// ---------------------------------------------------------------------------

func TestPlanRepairsNumberedHeadingsFromTheHeadingItself(t *testing.T) {
	// Every input is a live archive shape. The number in the output must be the
	// number that was already on the line — nothing here consults position.
	cases := []struct {
		name    string
		heading string
		want    string
		class   HeadingDefectClass
	}{
		{
			name:    "colon separator is CONSUMED, not kept",
			heading: "## Iteration 70: Concurrent Recording Task Plan + Architecture Review",
			want:    "## Iteration 70 — Concurrent Recording Task Plan + Architecture Review",
			class:   DefectFrameOrphan,
		},
		{
			name:    "infix moves into the title",
			heading: "## Iteration 215 (revision 2) — Filed the port plan",
			want:    "## Iteration 215 — (revision 2) — Filed the port plan",
			class:   DefectFrameOrphan,
		},
		{
			name:    "bare-word infix moves into the title",
			heading: "## Iteration 128 follow-up — committed, pushed, task retired",
			want:    "## Iteration 128 — follow-up — committed, pushed, task retired",
			class:   DefectFrameOrphan,
		},
		{
			name:    "doubled prefix is stripped",
			heading: "## Iteration 40 — Iteration 40 — Global AI Eric prep addendum",
			want:    "## Iteration 40 — Global AI Eric prep addendum",
			class:   DefectFrameOrphan,
		},
		{
			name:    "legacy H3 level is raised to H2",
			heading: "### Iteration 2 — early narrative",
			want:    "## Iteration 2 — early narrative",
			class:   DefectNonCanonicalNumbered,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := planFor(t, "no-such-project", tc.heading)
			if len(plan.Refusals) != 0 {
				t.Fatalf("refused a heading that carries its own number: %+v", plan.Refusals)
			}
			if len(plan.Rewrites) != 1 {
				t.Fatalf("Rewrites = %+v, want exactly one", plan.Rewrites)
			}
			got := plan.Rewrites[0]
			if got.To != tc.want {
				t.Errorf("To = %q, want %q", got.To, tc.want)
			}
			// The project is deliberately one with NO table rows: a recovered
			// number must never be credited to the operator's table.
			if got.Source != SourceRecovered {
				t.Errorf("Source = %q, want %q", got.Source, SourceRecovered)
			}
			if !IsCanonicalHeader(got.To) || HasDoubledPrefix(got.To) {
				t.Errorf("repair %q is not a canonical header", got.To)
			}
		})
	}
}

// TestPlanReusesTheCheckSuggestion pins the repair to the header the CHECK tells
// a human to expect. Two surfaces that disagree about what "fixed" means is the
// defect heading_contract.go exists to prevent, and here it would be the check
// reporting one repair while the migration silently wrote another.
func TestPlanReusesTheCheckSuggestion(t *testing.T) {
	content := "# p — Iteration Narratives\n" +
		writerFrame(1, "anchor", "body") +
		orphanFrame("## Iteration 70: Concurrent Recording", "body") +
		"\n## Iteration 71 — Iteration 71 — doubled\n\nbody\n"
	plan := PlanHeadingMigration("no-such-project", content)
	defects := ScanHeadingDefects(content)
	if len(plan.Rewrites) != len(defects) {
		t.Fatalf("Rewrites = %d, defects = %d", len(plan.Rewrites), len(defects))
	}
	byLine := make(map[int]HeadingDefect, len(defects))
	for _, d := range defects {
		byLine[d.Line] = d
	}
	for _, r := range plan.Rewrites {
		if r.To != byLine[r.Line].Want {
			t.Errorf("line %d: migration writes %q but the check suggests %q", r.Line, r.To, byLine[r.Line].Want)
		}
	}
}

// ---------------------------------------------------------------------------
// Class 2: the operator's table — and the anti-derivation pin.
// ---------------------------------------------------------------------------

// TestAuthorizedTitleDerivation covers every authorized row using the REAL
// table, so the nine expected headers are computed rather than asserted from a
// second hardcoded list. It is the direct pin on the title rules: a redundant
// self-reference to the assigned number is removed, and nothing else is.
func TestAuthorizedTitleDerivation(t *testing.T) {
	want := map[string]string{
		"vibe-palace/7649":  "## Iteration 108 — Retire auto-capture-ide-hooks (verification + cleanup)",
		"vibe-palace/7813":  "## Iteration 111 — 2026-06-17 Wrap",
		"vibe-palace/8683":  "## Iteration 126 — Grok first-class native slash commands (`grok-first-class-command-shims`)",
		"vibe-palace/8951":  "## Iteration 129 — commit.msg vault archive — reverted the a0477e4 over-ignore",
		"vibe-palace/9873":  "## Iteration 146 — Review + revision of `vp-vault-status-command` plan",
		"vibe-palace/10397": "## Iteration 155 — 2026-06-23",
		"rezbldrvault/2289": "## Iteration 40 — Choon-Seng Tan manager calibration (2026-06-19)",
		"rusty-can/80":      "## Iteration 4 — P1.1 committed & retired (restart sweep)",
		"rusty-can/666":     "## Iteration 17 — `p1-5-codec-hardening` committed & retired (restart sweep)",
	}
	rows := AuthorizedAssignments()
	if len(rows) != len(want) {
		t.Fatalf("table has %d rows, expectations cover %d — the operator's table changed", len(rows), len(want))
	}
	for _, a := range rows {
		key := fmt.Sprintf("%s/%d", a.Project, a.Line)
		exp, ok := want[key]
		if !ok {
			t.Errorf("table row %s is not covered by an expectation", key)
			continue
		}
		title := authorizedTitle(a.Text, a.N)
		if title == "" {
			t.Errorf("%s: derived an EMPTY title from %q", key, a.Text)
			continue
		}
		if got := FormatIterationHeader(a.N, title); got != exp {
			t.Errorf("%s:\n got %q\nwant %q", key, got, exp)
		}
	}
}

// TestAuthorizedTitleKeepsNonRedundantText is the negative half of the title
// rules. The date-shaped heading is the one that matters: "2026-06-17 Wrap"
// assigned 111 begins with digits and a hyphen, so a "strip leading digits and a
// separator" rule would file it as "Iteration 111 — 06-17 Wrap" and silently
// mangle a date into a title.
func TestAuthorizedTitleKeepsNonRedundantText(t *testing.T) {
	cases := []struct {
		name    string
		heading string
		n       int
		want    string
	}{
		{"leading date is not a leading number", "## 2026-06-17 Wrap", 111, "2026-06-17 Wrap"},
		{"leading digits that are not the assigned N", "## 146 — Review", 155, "146 — Review"},
		{"a different iteration mentioned at the end survives", "## Reverted the change, iter 12", 155, "Reverted the change, iter 12"},
		{"an em dash inside the title is not a separator to eat", "## commit.msg archive — reverted a0477e4", 129, "commit.msg archive — reverted a0477e4"},
		{"the word Iteration without a separator is prose", "## Iterations are cheap", 4, "Iterations are cheap"},
		{"a longer number with the assigned N as a prefix", "## 1460 widgets", 146, "1460 widgets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := authorizedTitle(tc.heading, tc.n); got != tc.want {
				t.Errorf("authorizedTitle(%q, %d) = %q, want %q", tc.heading, tc.n, got, tc.want)
			}
		})
	}
}

// TestPlanAppliesAnAuthorizedRow drives a real table row end to end: the row's
// heading is planted at the row's recorded line with the row's recorded text,
// and the plan must credit the number to the operator.
func TestPlanAppliesAnAuthorizedRow(t *testing.T) {
	row := findRow(t, "rusty-can", 80)
	content := archiveWith(t, map[int]string{80: row.Text})
	plan := PlanHeadingMigration("rusty-can", content)

	var got *HeadingRewrite
	for i := range plan.Rewrites {
		if plan.Rewrites[i].Line == 80 {
			got = &plan.Rewrites[i]
		}
	}
	if got == nil {
		t.Fatalf("no rewrite at line 80; plan = %+v", plan)
	}
	if got.Source != SourceAuthorized {
		t.Errorf("Source = %q, want %q", got.Source, SourceAuthorized)
	}
	if got.N != row.N {
		t.Errorf("N = %d, want the operator's %d", got.N, row.N)
	}
	if want := "## Iteration 4 — P1.1 committed & retired (restart sweep)"; got.To != want {
		t.Errorf("To = %q, want %q", got.To, want)
	}
	for _, a := range plan.Authorized {
		if a.Row.Line == 80 && a.State != RowMatched {
			t.Errorf("row state = %q, want %q", a.State, RowMatched)
		}
	}
}

// TestPlanRefusesOffListOrphan is THE anti-derivation pin, and the single most
// important assertion in this file.
//
// The planted orphan sits between two numbered entries with an obvious-looking
// gap between them, at the end of a file whose max is obvious, in a project that
// HAS authorized rows (so "this project is covered" cannot be mistaken for "this
// line is covered"). Every heuristic a migration could reach for — fill the gap,
// take max+1, take the previous entry's number, take the next one's — points at
// a specific number, and the plan must take NONE of them.
//
// If this test ever goes green while the migration numbers the orphan, the
// migration has started inventing history, and nothing downstream can tell.
func TestPlanRefusesOffListOrphan(t *testing.T) {
	const offList = "## An unruled narrative nobody assigned a number"
	content := "# p — Iteration Narratives\n" +
		writerFrame(107, "before the gap", "body") +
		orphanFrame(offList, "the unruled narrative body") +
		writerFrame(109, "after the gap", "body")

	// "rusty-can" is a project the operator's table DOES cover — at lines 80 and
	// 666, neither of which is this one.
	plan := PlanHeadingMigration("rusty-can", content)

	for _, r := range plan.Rewrites {
		if r.From == offList {
			t.Fatalf("DERIVED a number for an off-list orphan: %q -> %q (source %q)", r.From, r.To, r.Source)
		}
	}
	var found *HeadingRefusal
	for i := range plan.Refusals {
		if plan.Refusals[i].Text == offList {
			found = &plan.Refusals[i]
		}
	}
	if found == nil {
		t.Fatalf("off-list orphan was neither rewritten nor refused — it vanished from the report: %+v", plan)
	}
	if !strings.Contains(found.Reason, "guess") {
		t.Errorf("refusal reason does not explain the rule: %q", found.Reason)
	}
	// And it must survive Apply untouched.
	after, err := plan.Apply(content)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(after, "\n"+offList+"\n") {
		t.Errorf("Apply rewrote the refused orphan")
	}
}

// TestPlanRefusesDriftedAuthorizedRow: the operator ruled on line 80 of
// rusty-can carrying one exact string. If the archive has moved on, the number
// must NOT be stamped onto whatever occupies that line now — a positional write
// onto drifted content corrupts a different narrative and leaves no trace.
func TestPlanRefusesDriftedAuthorizedRow(t *testing.T) {
	const drifted = "## Some entirely different narrative that moved here"
	content := archiveWith(t, map[int]string{80: drifted})
	plan := PlanHeadingMigration("rusty-can", content)

	for _, r := range plan.Rewrites {
		if r.Line == 80 {
			t.Fatalf("rewrote a DRIFTED line: %q -> %q", r.From, r.To)
		}
	}
	var found *HeadingRefusal
	for i := range plan.Refusals {
		if plan.Refusals[i].Line == 80 {
			found = &plan.Refusals[i]
		}
	}
	if found == nil {
		t.Fatalf("drifted line 80 was not refused: %+v", plan)
	}
	if !strings.Contains(found.Reason, "DRIFTED") {
		t.Errorf("refusal reason does not name drift: %q", found.Reason)
	}
	for _, a := range plan.Authorized {
		if a.Row.Line == 80 && a.State != RowDrifted {
			t.Errorf("row state = %q, want %q", a.State, RowDrifted)
		}
	}
}

// TestPlanRefusesTitlelessNumberedHeading — "## Iteration 147" is addressable
// but carries no title. A number is recoverable; a title is not, and no
// placeholder may be committed to the archive.
func TestPlanRefusesTitlelessNumberedHeading(t *testing.T) {
	for _, heading := range []string{"## Iteration 147", "## Iteration 159", "## Iteration 159   ", "### Iteration 3"} {
		t.Run(heading, func(t *testing.T) {
			plan := planFor(t, "vibe-palace", heading)
			if len(plan.Rewrites) != 0 {
				t.Fatalf("rewrote a titleless heading: %+v", plan.Rewrites)
			}
			if len(plan.Refusals) != 1 {
				t.Fatalf("Refusals = %+v, want exactly one", plan.Refusals)
			}
			if !strings.Contains(plan.Refusals[0].Reason, "TITLELESS") {
				t.Errorf("reason does not name the problem: %q", plan.Refusals[0].Reason)
			}
		})
	}
}

// TestPlanIsIdempotent: running the migration twice must be a no-op the second
// time, and the authorized rows must report already-applied rather than drift.
// A one-shot that alarms on its own output teaches the operator to ignore it.
func TestPlanIsIdempotent(t *testing.T) {
	row := findRow(t, "rusty-can", 80)
	content := archiveWith(t, map[int]string{80: row.Text})
	first := PlanHeadingMigration("rusty-can", content)
	after, err := first.Apply(content)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	second := PlanHeadingMigration("rusty-can", after)
	if !second.Empty() {
		t.Errorf("second run would rewrite %+v", second.Rewrites)
	}
	for _, a := range second.Authorized {
		if a.Row.Line == 80 && a.State != RowAlreadyApplied {
			t.Errorf("row state on re-run = %q (found %q), want %q", a.State, a.Found, RowAlreadyApplied)
		}
	}
	third, err := second.Apply(after)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if third != after {
		t.Errorf("second Apply changed bytes")
	}
}

// ---------------------------------------------------------------------------
// Apply: heading lines and nothing else.
// ---------------------------------------------------------------------------

func TestApplyChangesOnlyHeadingLines(t *testing.T) {
	row := findRow(t, "rusty-can", 80)
	content := archiveWith(t, map[int]string{
		80:  row.Text,
		200: "## Iteration 9: colon separated",
		300: "## An off-list orphan",
	})
	plan := PlanHeadingMigration("rusty-can", content)
	after, err := plan.Apply(content)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	before := strings.Split(content, "\n")
	now := strings.Split(after, "\n")
	if len(before) != len(now) {
		t.Fatalf("line count changed: %d -> %d", len(before), len(now))
	}
	changed := map[int]bool{}
	for i := range before {
		if before[i] != now[i] {
			changed[i+1] = true
			if !strings.HasPrefix(strings.TrimSpace(before[i]), "#") {
				t.Errorf("line %d changed but is not a heading: %q -> %q", i+1, before[i], now[i])
			}
			if !strings.HasPrefix(now[i], "## Iteration ") {
				t.Errorf("line %d was not rewritten to a canonical header: %q", i+1, now[i])
			}
		}
	}
	if len(changed) != len(plan.Rewrites) {
		t.Errorf("changed %d lines but planned %d rewrites", len(changed), len(plan.Rewrites))
	}
	if changed[300] {
		t.Errorf("the off-list orphan at line 300 was rewritten")
	}
}

// TestApplyRefusesAPlanFromDifferentContent: the plan carries the exact text it
// expects on each line, and Apply re-checks it. This is the guard against a plan
// being reused against a file that has since changed — a positional write onto
// content nobody planned against.
func TestApplyRefusesAPlanFromDifferentContent(t *testing.T) {
	content := "# p\n" + writerFrame(1, "a", "b") + orphanFrame("## Iteration 9: colon", "body")
	plan := PlanHeadingMigration("no-such-project", content)
	if len(plan.Rewrites) != 1 {
		t.Fatalf("setup: want one rewrite, got %+v", plan.Rewrites)
	}
	moved := "extra line inserted at the top\n" + content
	if _, err := plan.Apply(moved); err == nil {
		t.Fatalf("Apply accepted a plan computed against different content")
	}
}

// ---------------------------------------------------------------------------
// Two absolute invariants, asserted over everything the tests can produce.
// ---------------------------------------------------------------------------

// nastyHeadings is a spread of malformed, adversarial and empty-ish headings.
// Both invariant tests below run the WHOLE set through the planner.
var nastyHeadings = []string{
	"## Iteration 147",
	"## Iteration 0",
	"## Iteration 108",
	"## Iteration 108 —",
	"## Iteration 108 — ",
	"## Iteration 108 -",
	"## Iteration 108:",
	"## Iteration 108 : ",
	"## Iteration",
	"## Iteration —",
	"## Iteration — ",
	"## 108",
	"## 108 —",
	"## 108.5 — a fractional heading",
	"## Iteration 108.5 — a fractional heading",
	"## iteration 108 — lowercase",
	"## Iteration 108 — Iteration 108 — Iteration 108 — triple",
	"## 2026-06-17 Wrap",
	"## ---",
	"## `code`",
	"## (untitled)",
	"## Retire auto-capture-ide-hooks (verification + cleanup)",
}

// TestNoPlaceholderTitleIsEverWritten. The check SUGGESTS "## Iteration 147 —
// <title>" so a human reading a report knows what is missing; the migration must
// never WRITE a placeholder of any kind. A placeholder committed to the archive
// stops reading as a placeholder — it just looks like a narrative someone named
// badly, and the fact that the title was never known is lost.
func TestNoPlaceholderTitleIsEverWritten(t *testing.T) {
	banned := []string{"(untitled)", "<title>", "untitled", "TODO", "Title"}
	for _, project := range []string{"vibe-palace", "rusty-can", "rezbldrvault", "no-such-project"} {
		for _, h := range nastyHeadings {
			plan := planFor(t, project, h)
			for _, r := range plan.Rewrites {
				for _, b := range banned {
					if strings.Contains(r.To, b) {
						t.Errorf("project %s, heading %q: wrote a placeholder title %q in %q", project, h, b, r.To)
					}
				}
				if strings.TrimSpace(strings.TrimPrefix(r.To, IterationHeadingPrefix)) == "" {
					t.Errorf("project %s, heading %q: wrote an empty header %q", project, h, r.To)
				}
			}
		}
	}
}

// numberedHeaderRe pins the shape of everything the migration writes: "## ",
// "Iteration ", DIGITS ONLY, " — ", then a non-empty title.
var numberedHeaderRe = regexp.MustCompile(`^## Iteration (\d+) — (.+)$`)

// TestEveryWrittenNumberHasAProvenance. There are exactly two legal origins for
// a number this migration writes — recovered from the line, or on the operator's
// table — and this asserts BOTH that the shape is integral (no "108.5", no
// invented decoration) and that the value traces back to one of the two.
func TestEveryWrittenNumberHasAProvenance(t *testing.T) {
	rowsByProject := map[string]map[int]AuthorizedAssignment{}
	for _, a := range AuthorizedAssignments() {
		if rowsByProject[a.Project] == nil {
			rowsByProject[a.Project] = map[int]AuthorizedAssignment{}
		}
		rowsByProject[a.Project][a.Line] = a
	}

	for _, project := range []string{"vibe-palace", "rusty-can", "rezbldrvault", "no-such-project"} {
		for _, h := range nastyHeadings {
			plan := planFor(t, project, h)
			for _, r := range plan.Rewrites {
				m := numberedHeaderRe.FindStringSubmatch(r.To)
				if m == nil {
					t.Errorf("project %s, heading %q: %q is not an integral canonical header", project, h, r.To)
					continue
				}
				n, err := strconv.Atoi(m[1])
				if err != nil || n != r.N {
					t.Errorf("project %s, heading %q: header names %s but plan says N=%d", project, h, m[1], r.N)
				}
				switch r.Source {
				case SourceRecovered:
					recovered, _, ok := RecoverHeader(r.From)
					if !ok || recovered != r.N {
						t.Errorf("project %s, heading %q: claims recovered N=%d but %q yields (%d,%v)",
							project, h, r.N, r.From, recovered, ok)
					}
				case SourceAuthorized:
					row, ok := rowsByProject[project][r.Line]
					if !ok || row.N != r.N || row.Text != r.From {
						t.Errorf("project %s, heading %q: claims operator authority for N=%d at line %d, but no such row",
							project, h, r.N, r.Line)
					}
				default:
					t.Errorf("project %s, heading %q: rewrite with no provenance (source %q)", project, h, r.Source)
				}
			}
		}
	}
}

// TestFormatIterationHeaderIsNotZeroPadded pins the DECLINED change. Zero-padding
// the number was proposed alongside this migration and rejected by the operator;
// it stays "%d". A padded header would also break every reader that string-
// matches "Iteration 7", so the decline is load-bearing, not cosmetic.
func TestFormatIterationHeaderIsNotZeroPadded(t *testing.T) {
	if got := FormatIterationHeader(7, "t"); got != "## Iteration 7 — t" {
		t.Errorf("FormatIterationHeader(7, \"t\") = %q, want %q", got, "## Iteration 7 — t")
	}
}

// ---------------------------------------------------------------------------
// Ordering and body integrity.
// ---------------------------------------------------------------------------

// TestMigrationPreservesFileOrderAndAdjacency. Sorting the archive was DECLINED:
// file order IS the historical record, and two narratives filed under one number
// (an addendum) are adjacent because they were written that way. A heading-only
// rewrite cannot move anything, and this asserts that a duplicate-numbered pair
// stays adjacent and in order both when it is already canonical (the two 177s)
// and when the second half is created by the migration (the two 108s).
func TestMigrationPreservesFileOrderAndAdjacency(t *testing.T) {
	row := findRow(t, "vibe-palace", 7649)
	content := archiveWith(t, map[int]string{
		7603: "## Iteration 108 — AGENTS.md as host-local managed shim",
		7649: row.Text,
		9000: "## Iteration 177 — blind-writer-vault-lock-gap shipped",
		9100: "## Iteration 177 — addendum: commit 1 landed",
	})
	plan := PlanHeadingMigration("vibe-palace", content)
	after, err := plan.Apply(content)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var order []IterHeading
	for _, h := range ScanIterHeadings(after) {
		if h.N == 108 || h.N == 177 {
			order = append(order, h)
		}
	}
	if len(order) != 4 {
		t.Fatalf("expected two 108s and two 177s after migration, got %+v", order)
	}
	wantLines := []int{7603, 7649, 9000, 9100}
	for i, h := range order {
		if h.Line != wantLines[i] {
			t.Fatalf("heading %d at line %d, want %d — the migration MOVED content", i, h.Line, wantLines[i])
		}
	}
	if order[0].N != 108 || order[1].N != 108 {
		t.Errorf("the two 108s are not adjacent: %+v", order[:2])
	}
	if order[2].N != 177 || order[3].N != 177 {
		t.Errorf("the two 177s are not adjacent: %+v", order[2:])
	}
	if !strings.Contains(after, "## Iteration 108 — Retire auto-capture-ide-hooks (verification + cleanup)") {
		t.Errorf("the addendum did not get its authorized number")
	}
}

// TestMigrationPreservesEntryBodies is the body-integrity assertion.
//
// Every entry that existed before must have a byte-identical body after, and the
// narrative that was under a framed orphan must come through as its own entry
// with its text unchanged. Note that no body SHRINKS here even though six live
// entries do: Phase 1 already taught ParseEntries to stop at a frame orphan, so
// by the time the migration runs the over-return is fixed in the reader and the
// only thing left to change is the heading line.
func TestMigrationPreservesEntryBodies(t *testing.T) {
	const orphanBody = "the addendum narrative, byte for byte"
	row := findRow(t, "vibe-palace", 7649)
	content := archiveWith(t, map[int]string{
		7603: "## Iteration 108 — original",
		7649: row.Text,
	})
	// Cut the file off just after the orphan's heading and give it one known
	// body line, so the addendum's body is a string this test can compare
	// against exactly rather than "whatever filler ran to EOF".
	lines := strings.Split(content, "\n")[:7650]
	lines = append(lines, orphanBody, "")
	content = strings.Join(lines, "\n")

	beforeByLine := map[int]Entry{}
	for _, e := range ParseEntries(content) {
		beforeByLine[e.Line] = e
	}
	plan := PlanHeadingMigration("vibe-palace", content)
	after, err := plan.Apply(content)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	afterByLine := map[int]Entry{}
	for _, e := range ParseEntries(after) {
		afterByLine[e.Line] = e
	}
	for line, was := range beforeByLine {
		now, ok := afterByLine[line]
		if !ok {
			t.Errorf("entry at line %d disappeared", line)
			continue
		}
		if now.Body != was.Body {
			t.Errorf("entry at line %d: body changed\n before %q\n  after %q", line, was.Body, now.Body)
		}
		if now.N != was.N {
			t.Errorf("entry at line %d: N changed %d -> %d", line, was.N, now.N)
		}
	}
	addendum, ok := afterByLine[7649]
	if !ok {
		t.Fatalf("the migrated orphan did not become an entry; entries: %v", afterByLine)
	}
	if addendum.N != 108 {
		t.Errorf("addendum N = %d, want 108", addendum.N)
	}
	if addendum.Body != orphanBody {
		t.Errorf("addendum body = %q, want %q", addendum.Body, orphanBody)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// findRow returns the operator's row for (project, line), failing the test if it
// is gone — so a table edit surfaces as a named failure rather than as a nil
// dereference in an unrelated assertion.
func findRow(t *testing.T, project string, line int) AuthorizedAssignment {
	t.Helper()
	for _, a := range AuthorizedAssignments() {
		if a.Project == project && a.Line == line {
			return a
		}
	}
	t.Fatalf("no authorized row for %s line %d", project, line)
	return AuthorizedAssignment{}
}

// archiveWith builds a synthetic iterations.md that places each given heading on
// EXACTLY the requested 1-indexed line, on the writer's "---" frame, padding the
// gaps with inert filler.
//
// Exact line placement is the point: the operator's table is keyed on line
// numbers in the thousands, and a fixture that only approximates them would
// exercise the lookup with the real table bypassed. The filler is a bare word —
// not "key:" shaped — so it can never be mistaken for YAML front matter and turn
// a frame into a front-matter closer.
func archiveWith(t *testing.T, headings map[int]string) string {
	t.Helper()
	maxLine := 0
	for n := range headings {
		if n > maxLine {
			maxLine = n
		}
	}
	lines := make([]string, maxLine+8)
	for i := range lines {
		lines[i] = "filler narrative text"
	}
	lines[0] = "# p — Iteration Narratives"
	for n, h := range headings {
		if n < 4 {
			t.Fatalf("archiveWith: line %d is too close to the top to carry a frame", n)
		}
		lines[n-3] = ""    // blank before the frame
		lines[n-2] = "---" // the writer's frame
		lines[n-1] = h     // the heading itself
		lines[n] = ""      // blank after the heading, as the writer emits
	}
	return strings.Join(lines, "\n") + "\n"
}
