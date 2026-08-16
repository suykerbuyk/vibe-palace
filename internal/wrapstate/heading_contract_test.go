// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// orphanTexts is a small reader for the scan result: the assertions below care
// about WHICH headings were flagged, not about their line numbers.
func orphanTexts(orphans []FrameOrphan) []string {
	out := make([]string, len(orphans))
	for i, o := range orphans {
		out[i] = o.Text
	}
	return out
}

func TestScanFrameOrphans_MustFlag(t *testing.T) {
	// Every shape here is drawn from the live archive. Each one sits on the
	// writer's "\n---\n" frame and each one was invisible to the reader, so its
	// whole body was served as part of the PREVIOUS numbered entry.
	cases := []struct {
		name    string
		heading string
	}{
		{"date-shaped", "## 2026-06-17 Wrap"},
		{"prose-titled", "## Grok first-class native slash commands (no shim)"},
		{"number-leaded", "## 146 — Review + hardening pass"},
		{"numberless-iteration", "## Iteration — P1.1 committed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := "# project — Iteration Narratives\n" +
				writerFrame(110, "a", "narrative body") +
				"\n---\n" + tc.heading + "\n\norphan body\n"
			got := ScanFrameOrphans(content)
			if len(got) != 1 {
				t.Fatalf("ScanFrameOrphans = %v, want exactly [%q]", orphanTexts(got), tc.heading)
			}
			if got[0].Text != tc.heading {
				t.Errorf("Text = %q want %q", got[0].Text, tc.heading)
			}
			// The line number must point at the heading itself so a migration can
			// rewrite exactly that line.
			lines := strings.Split(content, "\n")
			if got[0].Line < 1 || got[0].Line > len(lines) {
				t.Fatalf("Line = %d out of range 1..%d", got[0].Line, len(lines))
			}
			if strings.TrimSpace(lines[got[0].Line-1]) != tc.heading {
				t.Errorf("Line %d = %q want the heading %q", got[0].Line, lines[got[0].Line-1], tc.heading)
			}
		})
	}
}

func TestScanFrameOrphans_MustNotFlag(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			// Its predecessor is the H1 file title, not the writer's frame. The
			// file's own table-of-contents style headings are not entries.
			name:    "not frame adjacent, follows H1",
			content: "# project — Iteration Narratives\n\n## Iteration Narratives\n\nprose\n",
		},
		{
			// A sub-heading INSIDE a narrative body. No "---" above it, so the
			// writer never emitted it as an entry boundary.
			name:    "in-body sub-heading",
			content: writerFrame(12, "a", "opening prose\n\n## Phase 1\n\nmore prose"),
		},
		{
			// recmeet's front matter. The "---" here CLOSES a YAML block; it is
			// not the writer's frame, and the heading below it is document
			// structure rather than an entry.
			name:    "yaml closer",
			content: "---\nshape: bookkeeping\nsummary: a session\n---\n## Session arc\n\nprose\n",
		},
		{
			// Exactly what FormatIterationHeader emits.
			name:    "canonical header",
			content: "# t\n" + writerFrame(5, "title", "body"),
		},
		{
			// A framed heading quoted inside a code fence is sample text. This is
			// the mdfence contract, not a second fence definition.
			name: "inside a code fence",
			content: writerFrame(9, "a", "the archive carries lines like:\n\n"+
				"```md\n---\n## 2026-06-17 Wrap\n```\n\nclosing prose"),
		},
		{
			// An inline ``` run must not open a fence — and must not become a
			// frame either. iterations.md:698 class.
			name:    "inline code run near a frame",
			content: writerFrame(3, "a", "see the ```bash tutorial``` for details"),
		},
		{
			// Four-space indent is an indented code block, not a heading.
			name:    "indented heading",
			content: "# t\n\n---\n    ## 2026-06-17 Wrap\n\nprose\n",
		},
		{
			// H3 is the level narratives use for their own sub-sections; the
			// writer never emits one as a header.
			name:    "h3 on a frame",
			content: "# t\n\n---\n### Results\n\nprose\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScanFrameOrphans(tc.content); len(got) != 0 {
				t.Fatalf("ScanFrameOrphans flagged %v, want none", orphanTexts(got))
			}
		})
	}
}

func TestScanFrameOrphans_YAMLCloserDoesNotHideARealOrphan(t *testing.T) {
	// The YAML exclusion must be narrow. A front-matter block at the top of the
	// file is skipped, but a genuine framed orphan later in the same file is
	// still flagged.
	content := "---\nshape: bookkeeping\nsummary: a session\n---\n## Session arc\n\nprose\n" +
		writerFrame(1, "one", "body") +
		"\n---\n## 2026-06-17 Wrap\n\norphan body\n"
	got := ScanFrameOrphans(content)
	if len(got) != 1 || got[0].Text != "## 2026-06-17 Wrap" {
		t.Fatalf("ScanFrameOrphans = %v, want exactly [## 2026-06-17 Wrap]", orphanTexts(got))
	}
}

func TestScanFrameOrphans_NonCanonicalNumberedHeadingIsAnOrphan(t *testing.T) {
	// "## Iteration 70: Title" IS matched by iterHeadingRe, so it still becomes
	// an Entry — but it is not what the writer would have emitted, so the scan
	// reports it for repair.
	content := "# t\n" + writerFrame(69, "prior", "body") + "\n---\n## Iteration 70: Title\n\nbody-70\n"
	got := ScanFrameOrphans(content)
	if !slices.Contains(orphanTexts(got), "## Iteration 70: Title") {
		t.Fatalf("ScanFrameOrphans = %v, want it to include the malformed numbered heading", orphanTexts(got))
	}
}

func TestIsCanonicalHeader(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"## Iteration 7 — t", true},
		{FormatIterationHeader(191, "a longer — dashed — title"), true},
		{"## Iteration 70: Title", false},
		{"## Iteration 147", false},
		{"## Iteration 128 follow-up — x", false},
		{"## 2026-06-17 Wrap", false},
		{"### Iteration 7 — t", false}, // legacy H3 is readable but not canonical
		{"## Iteration 7 - t", false},  // hyphen, not the em dash the writer emits
	}
	for _, tc := range cases {
		if got := IsCanonicalHeader(tc.text); got != tc.want {
			t.Errorf("IsCanonicalHeader(%q) = %v want %v", tc.text, got, tc.want)
		}
	}
}

func TestHasDoubledPrefix_AndTheOracleBlindness(t *testing.T) {
	const doubled = "## Iteration 40 — Iteration 40 — Global AI Eric prep addendum"

	if !HasDoubledPrefix(doubled) {
		t.Errorf("HasDoubledPrefix(%q) = false, want true", doubled)
	}
	// REVISITED DELIBERATELY. The previous version of this test asserted the
	// OPPOSITE — that IsCanonicalHeader(doubled) is TRUE — because
	// titleFromHeader stripped exactly one "Iteration N —" and
	// FormatIterationHeader re-added exactly one, making a doubled prefix a
	// FIXED POINT of the round trip. That blindness is what justified
	// HasDoubledPrefix existing as a separate rule, and the old pin ended by
	// saying: if this ever goes false the oracle grew teeth and the pin should
	// be revisited deliberately, not deleted in passing.
	//
	// The oracle grew teeth. FormatIterationHeader now strips the doubled
	// prefix, so the round trip no longer reproduces the corruption and the
	// heading is correctly non-canonical. The assertion is inverted rather than
	// removed, because which way it points is the whole record of whether the
	// writer hole is open or closed.
	if IsCanonicalHeader(doubled) {
		t.Errorf("IsCanonicalHeader(%q) = true; the writer now strips the doubled prefix, so the round trip must NOT reproduce it", doubled)
	}
	// And the rule still earns its place: it names the corruption, where
	// non-canonical-numbered would only say the line is not what the writer
	// emits. ScanHeadingDefects tests it FIRST for exactly that reason.

	for _, ok := range []string{
		"## Iteration 40 — Global AI Eric prep addendum",
		"## Iteration 7 — t",
		"## 2026-06-17 Wrap",
		"## Iteration 12 — revisits the Iteration 5 — decision",
	} {
		if HasDoubledPrefix(ok) {
			t.Errorf("HasDoubledPrefix(%q) = true, want false", ok)
		}
	}
}

func TestStripDoubledPrefix(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  string
	}{
		{
			name:  "doubled once",
			title: "Iteration 40 — Global AI Eric prep addendum",
			want:  "Global AI Eric prep addendum",
		},
		{
			name:  "doubled twice",
			title: "Iteration 40 — Iteration 40 — Global AI Eric prep addendum",
			want:  "Global AI Eric prep addendum",
		},
		{
			name:  "hyphen and en dash variants",
			title: "Iteration 12 - Iteration 12 – real title",
			want:  "real title",
		},
		{
			name:  "mid-sentence mention untouched",
			title: "revisits the iteration 5 decision — and keeps it",
			want:  "revisits the iteration 5 decision — and keeps it",
		},
		{
			name:  "capitalised mid-sentence mention untouched",
			title: "why Iteration 5 mattered",
			want:  "why Iteration 5 mattered",
		},
		{
			name:  "clean title untouched",
			title: "Global AI Eric prep addendum",
			want:  "Global AI Eric prep addendum",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripDoubledPrefix(tc.title); got != tc.want {
				t.Errorf("StripDoubledPrefix(%q) = %q want %q", tc.title, got, tc.want)
			}
		})
	}
}

func TestStripDoubledPrefix_RepairsTheHeadingOracleCannotSee(t *testing.T) {
	// The end-to-end shape a migration will run: pull the title out of the
	// corrupt heading, strip the doubling, re-emit through the writer, and the
	// result must now be clean by BOTH rules.
	const doubled = "## Iteration 40 — Iteration 40 — Global AI Eric prep addendum"
	fixed := FormatIterationHeader(40, StripDoubledPrefix(titleFromHeader(doubled)))
	if fixed != "## Iteration 40 — Global AI Eric prep addendum" {
		t.Fatalf("repaired = %q", fixed)
	}
	if !IsCanonicalHeader(fixed) || HasDoubledPrefix(fixed) {
		t.Errorf("repaired heading still fails a rule: canonical=%v doubled=%v",
			IsCanonicalHeader(fixed), HasDoubledPrefix(fixed))
	}
}

// TestParseEntries_FramedOrphanDoesNotFuseOntoPrevious is the headline
// regression. Before the frame split, ParseEntries returned iteration 110 with
// the whole "## 2026-06-17 Wrap" section glued to the end of its body, and
// reported success.
func TestParseEntries_FramedOrphanDoesNotFuseOntoPrevious(t *testing.T) {
	const iterBody = "the real narrative for 110\nsecond line"
	content := "# project — Iteration Narratives\n" +
		writerFrame(110, "a", iterBody) +
		"\n---\n## 2026-06-17 Wrap\n\nthe orphan's own body\nmore orphan prose\n"

	got := ParseEntries(content)
	if len(got) != 1 {
		t.Fatalf("len=%d want 1 (the orphan has no N and must produce no entry): %+v", len(got), got)
	}
	e := got[0]
	if e.N != 110 {
		t.Fatalf("N=%d want 110", e.N)
	}
	if strings.Contains(e.Body, "2026-06-17 Wrap") {
		t.Errorf("entry 110 swallowed the orphan HEADING; Body=%q", e.Body)
	}
	if strings.Contains(e.Body, "the orphan's own body") {
		t.Errorf("entry 110 swallowed the orphan BODY; Body=%q", e.Body)
	}
	if e.Body != iterBody {
		t.Errorf("Body=%q want %q", e.Body, iterBody)
	}
	if e.Bytes != len(iterBody) {
		t.Errorf("Bytes=%d want %d", e.Bytes, len(iterBody))
	}
	// And the same through the accessor vp_get_iteration actually calls.
	last, ok := LastEntryByN(content, 110)
	if !ok || strings.Contains(last.Body, "2026-06-17 Wrap") {
		t.Errorf("LastEntryByN(110) over-returned: ok=%v Body=%q", ok, last.Body)
	}
}

func TestParseEntries_FramedOrphanBetweenTwoEntries(t *testing.T) {
	// The orphan must not corrupt the FOLLOWING entry either: entry 111 starts
	// at its own header, not at the orphan's.
	const body110 = "narrative 110"
	const body111 = "narrative 111"
	content := "# project — Iteration Narratives\n" +
		writerFrame(110, "a", body110) +
		"\n---\n## Grok first-class native slash commands (no shim)\n\norphan prose\n" +
		writerFrame(111, "b", body111)

	got := ParseEntries(content)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2: %+v", len(got), got)
	}
	if got[0].N != 110 || got[0].Body != body110 {
		t.Errorf("entry 110 = %+v want body %q", got[0], body110)
	}
	if got[1].N != 111 || got[1].Body != body111 {
		t.Errorf("entry 111 = %+v want body %q", got[1], body111)
	}
	for _, e := range got {
		if strings.Contains(e.Body, "orphan prose") || strings.Contains(e.Body, "Grok first-class") {
			t.Errorf("entry %d absorbed orphan text: %q", e.N, e.Body)
		}
	}
}

func TestParseEntries_ConsecutiveOrphans(t *testing.T) {
	// Two orphans in a row: neither becomes an entry, and neither leaks into the
	// numbered entry that precedes them.
	content := "# t\n" +
		writerFrame(145, "a", "narrative 145") +
		"\n---\n## 146 — Review + hardening pass\n\nfirst orphan body\n" +
		"\n---\n## Iteration — P1.1 committed\n\nsecond orphan body\n"
	got := ParseEntries(content)
	if len(got) != 1 || got[0].N != 145 || got[0].Body != "narrative 145" {
		t.Fatalf("ParseEntries = %+v", got)
	}
	if orphans := ScanFrameOrphans(content); len(orphans) != 2 {
		t.Errorf("ScanFrameOrphans = %v want both", orphanTexts(orphans))
	}
}

func TestParseEntries_DoubledPrefixHeadingStillParses(t *testing.T) {
	// The doubled-prefix headings in the live archive are real, numbered entries.
	// The frame split must not drop them — HasDoubledPrefix is a repair signal,
	// not an exclusion.
	content := "# t\n" +
		"\n---\n## Iteration 40 — Iteration 40 — Global AI Eric prep addendum\n\nbody-40\n" +
		writerFrame(41, "next", "body-41")
	got := ParseEntries(content)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2: %+v", len(got), got)
	}
	if got[0].N != 40 || got[0].Body != "body-40" {
		t.Errorf("entry 40 = %+v", got[0])
	}
	if got[1].N != 41 || got[1].Body != "body-41" {
		t.Errorf("entry 41 = %+v", got[1])
	}
}

// TestCanonicalHeaderShapeMatchesTheWriter pins the prose shape to the WRITER's
// actual output. CanonicalHeaderShape is quoted at both surfaces to tell a human
// what a broken heading should have been; if FormatIterationHeader ever changes,
// that sentence must not go on confidently describing the old format.
func TestCanonicalHeaderShapeMatchesTheWriter(t *testing.T) {
	emitted := FormatIterationHeader(1, "Title")
	if want := strings.Replace(CanonicalHeaderShape, "N", "1", 1); emitted != want {
		t.Errorf("FormatIterationHeader(1, %q) = %q, but CanonicalHeaderShape describes %q",
			"Title", emitted, want)
	}
}

// defectFixture is one archive carrying EVERY shape the three conditions must
// decide about — the must-flag classes and, just as importantly, the must-not
// shapes drawn from the live vault. Both surfaces' tests build on the same
// vocabulary, because a fixture that only contains defects proves nothing about
// an auditor's restraint.
func defectFixture() string {
	return strings.Join([]string{
		// A section title, not an entry: its predecessor is the H1, not a frame.
		"# recmeet — Iteration Narratives",
		"",
		"## Iteration Narratives",
		"",
		"prose",
		// A canonical entry whose BODY carries its own H2 sub-heading. 14 of
		// these live in vibe-palace and none of them is a defect.
		writerFrame(5, "canonical entry", "opening prose\n\n## Phase 1\n\nmore prose"),
		// Frame orphan: on the writer's frame, no number at all.
		"\n---\n## 2026-06-17 Wrap\n\norphan body",
		// DEDUP: on the frame AND matched by the reader. One finding, not two.
		"\n---\n## Iteration 93: Live capture\n\nbody-93",
		// Non-canonical numbered heading that is NOT frame-adjacent — invisible
		// to the frame rule, which is why that rule alone is not enough.
		writerFrame(146, "prior", "prose\n\n## Iteration 147\n\nmore prose"),
		// Doubled prefix: PASSES the canonicity oracle, so only its own rule
		// can see it.
		"\n---\n## Iteration 40 — Iteration 40 — Global AI Eric prep addendum\n\nbody-40",
		// A fenced sample heading is documentation, not a defect.
		writerFrame(200, "fenced sample", "the wrong level looks like:\n\n```md\n### Iteration 999 — sample\n```"),
		// recmeet's front matter: this "---" CLOSES a YAML block.
		"\n---\nshape: bookkeeping\nsummary: a session\n---\n## Session arc\n\nprose",
		// Two entries under ONE number. DELIBERATE (multi-work-unit sessions),
		// and a rule that flagged it invented 17 findings and was deleted.
		writerFrame(177, "the feature shipped", "body"),
		writerFrame(177, "addendum: the lint sweep staged as commit 2", "body"),
	}, "\n")
}

// lineOf returns the 1-indexed line carrying heading, failing when it is absent
// or ambiguous. It locates the line INDEPENDENTLY of the scan under test, so an
// off-by-one in the scan shows up as a failure rather than as agreement.
func lineOf(t *testing.T, content, heading string) int {
	t.Helper()
	found := 0
	for i, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) == heading {
			if found != 0 {
				t.Fatalf("heading %q appears on lines %d and %d; the fixture must be unambiguous",
					heading, found, i+1)
			}
			found = i + 1
		}
	}
	if found == 0 {
		t.Fatalf("heading %q is not in the fixture", heading)
	}
	return found
}

func TestScanHeadingDefects_FlagsAllThreeClasses(t *testing.T) {
	content := defectFixture()
	got := ScanHeadingDefects(content)

	type want struct {
		heading string
		class   HeadingDefectClass
		want    string
	}
	cases := []want{
		{"## 2026-06-17 Wrap", DefectFrameOrphan, CanonicalHeaderShape},
		{"## Iteration 93: Live capture", DefectFrameOrphan, "## Iteration 93 — Live capture"},
		{"## Iteration 147", DefectNonCanonicalNumbered, "## Iteration 147 — <title>"},
		{
			"## Iteration 40 — Iteration 40 — Global AI Eric prep addendum",
			DefectDoubledPrefix,
			"## Iteration 40 — Global AI Eric prep addendum",
		},
	}
	if len(got) != len(cases) {
		t.Fatalf("ScanHeadingDefects returned %d defects, want %d:\n%s",
			len(got), len(cases), renderDefects(got))
	}
	// File order is part of the contract: a report ordered by map iteration is
	// irreproducible between runs.
	for i, c := range cases {
		d := got[i]
		if d.Text != c.heading {
			t.Fatalf("defect %d = %q, want %q (file order):\n%s", i, d.Text, c.heading, renderDefects(got))
		}
		if d.Class != c.class {
			t.Errorf("%q class = %q, want %q", d.Text, d.Class, c.class)
		}
		if line := lineOf(t, content, c.heading); d.Line != line {
			t.Errorf("%q line = %d, want %d", d.Text, d.Line, line)
		}
		if d.Want != c.want {
			t.Errorf("%q expected-shape = %q, want %q", d.Text, d.Want, c.want)
		}
	}
}

// TestScanHeadingDefects_ReportsEachHeadingOnce is the dedup pin. "## Iteration
// 93: Live capture" satisfies BOTH the frame rule and the canonicity rule.
// Reporting it twice inflates every count the operator reads, and an auditor
// whose numbers are inflated is an auditor that gets waved off.
func TestScanHeadingDefects_ReportsEachHeadingOnce(t *testing.T) {
	content := defectFixture()

	// It really is a member of both rules — otherwise this test passes vacuously.
	both := "## Iteration 93: Live capture"
	inFrameScan := false
	for _, o := range ScanFrameOrphans(content) {
		if o.Text == both {
			inFrameScan = true
		}
	}
	if !inFrameScan {
		t.Fatalf("fixture no longer exercises dedup: %q is not a frame orphan", both)
	}
	if IsCanonicalHeader(both) {
		t.Fatalf("fixture no longer exercises dedup: %q is canonical", both)
	}

	seen := map[int]int{}
	for _, d := range ScanHeadingDefects(content) {
		seen[d.Line]++
	}
	for line, n := range seen {
		if n != 1 {
			t.Errorf("line %d reported %d times; every defective heading is ONE finding", line, n)
		}
	}
}

func TestScanHeadingDefects_MustNotFlag(t *testing.T) {
	flagged := map[string]bool{}
	for _, d := range ScanHeadingDefects(defectFixture()) {
		flagged[d.Text] = true
	}
	// Every one of these is drawn from the live vault, and every one of them
	// would be an INVENTED finding. This codebase deleted a previous rule for
	// inventing 17 — an auditor that invents findings is worse than one that
	// misses them, because it teaches you to wave off the real ones.
	for _, quiet := range []string{
		"## Iteration Narratives",                // a section title (recmeet L8)
		"## Phase 1",                             // in-body sub-heading, 14 live in vibe-palace
		"## Session arc",                         // heading under a YAML closer, 5 live in recmeet
		"### Iteration 999 — sample",             // inside a code fence
		"## Iteration 5 — canonical entry",       // exactly what the writer emits
		"## Iteration 177 — the feature shipped", // duplicate N is DELIBERATE...
		"## Iteration 177 — addendum: the lint sweep staged as commit 2", // ...and legal
	} {
		if flagged[quiet] {
			t.Errorf("%q was flagged; it is legal archive content, not a defect", quiet)
		}
	}
}

func TestScanHeadingDefects_CleanArchiveIsSilent(t *testing.T) {
	content := "# p — Iteration Narratives\n" +
		writerFrame(1, "one", "body one") +
		writerFrame(2, "two", "body two\n\n## In-body section\n\nmore") +
		writerFrame(2, "two, addendum", "body three")
	if got := ScanHeadingDefects(content); len(got) != 0 {
		t.Fatalf("a clean archive produced findings:\n%s", renderDefects(got))
	}
}

// renderDefects formats a scan result for a failure message. A test that prints
// only a count makes the next reader re-run it by hand to find out what changed.
func renderDefects(defects []HeadingDefect) string {
	var b strings.Builder
	for _, d := range defects {
		b.WriteString("  line ")
		b.WriteString(strconv.Itoa(d.Line))
		b.WriteString(" [")
		b.WriteString(string(d.Class))
		b.WriteString("] ")
		b.WriteString(d.Text)
		b.WriteString(" → ")
		b.WriteString(d.Want)
		b.WriteString("\n")
	}
	return b.String()
}
