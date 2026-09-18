// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ---------------------------------------------------------------------------
// Fixtures.
//
// 🔴 THESE ARE THE PRIMARY SIGNAL, NOT A FALLBACK. CI configures no vault —
// re-derive with `grep -rn 'vault_path\|VP_VAULT\|vibe-palace-vault' .github/workflows/`
// — so a live-vault test runs for the author and nobody else. Every branch of the
// transform must be reachable from here or it is untested where it matters.
// ---------------------------------------------------------------------------

// preformatHeadingNext is the shape 29 of the 30 live files have: H1, blank,
// then a HEADING. Omitting the trailing blank is harmless here, which is exactly
// why this fixture must NOT be the one guarding the trailing blank.
const preformatHeadingNext = "# Plan: Phase 4 semantic search\n" +
	"\n" +
	"## Context\n" +
	"\n" +
	"Body text.\n" +
	"\n" +
	"## Verification\n" +
	"\n" +
	"- [ ] it works\n"

// preformatProseNext is the ONE live shape that makes the trailing blank a
// correctness requirement: the H1 is followed by a hard-wrapped paragraph opening
// with a bare `Slug:` pseudo-field. Without the blank, CommonMark folds both
// constructed fields into that sentence and the validator says nothing.
// Modelled on vibe-palace/done/vps-skill-artifacts-cross-ide.md.
const preformatProseNext = "# Plan: `vps-*` Skills — Directory-Form Persona Artifacts, Cross-IDE\n" +
	"\n" +
	"Slug: `vps-skill-artifacts-cross-ide` (existing vault task — this plan\n" +
	"supersedes its Phases section based on current code state).\n" +
	"\n" +
	"## Phases\n" +
	"\n" +
	"Body.\n"

// preformatFencedHashes carries '#'-prefixed lines INSIDE a fence — the TOML and
// shell-banner shape three live files have. A raw `^# ` count reads 3 titles
// here; a fence-aware one reads 1.
const preformatFencedHashes = "# Plan: adaptive room classification\n" +
	"\n" +
	"## Config\n" +
	"\n" +
	"```toml\n" +
	"# the palace section\n" +
	"# rooms are configurable\n" +
	"[palace.rooms.audio]\n" +
	"```\n" +
	"\n" +
	"## Done\n" +
	"\n" +
	"Yes.\n"

// preformatNoFinalNewline reproduces the four live files that end without one.
const preformatNoFinalNewline = "# Plan: weighted keyword scoring\n" +
	"\n" +
	"## Approach\n" +
	"\n" +
	"Score them."

// ---------------------------------------------------------------------------
// planTaskHeaderBlock — the pure transform. No vault, no files, no git.
// ---------------------------------------------------------------------------

// TestPlanTaskHeaderBlock_ConstructsBothFields is the per-class before/after
// fixture, asserted as whole-file byte equality against a literal.
//
// Break: make the transform return its input. This fails.
// Break: insert only **Status:**. This fails on the post-condition.
func TestPlanTaskHeaderBlock_ConstructsBothFields(t *testing.T) {
	if err := storage.ValidateWholeTaskFile(preformatHeadingNext); err == nil {
		t.Fatal("anti-vacuity precondition: the fixture must FAIL the validator before the transform")
	}

	after, outcome, reason := planTaskHeaderBlock(preformatHeadingNext, storage.StatusDone)
	if outcome != blockConstruct {
		t.Fatalf("outcome = %v, reason %q, want blockConstruct", outcome, reason)
	}
	want := "# Plan: Phase 4 semantic search\n" +
		"\n" +
		"**Status:** done\n" +
		"**Priority:** medium\n" +
		"\n" +
		"## Context\n" +
		"\n" +
		"Body text.\n" +
		"\n" +
		"## Verification\n" +
		"\n" +
		"- [ ] it works\n"
	if after != want {
		t.Errorf("transform output mismatch\n got: %q\nwant: %q", after, want)
	}
	if after == preformatHeadingNext {
		t.Error("the transform returned its input unchanged")
	}
	if err := storage.ValidateWholeTaskFile(after); err != nil {
		t.Errorf("constructed file does not validate: %v", err)
	}
}

// TestPlanTaskHeaderBlock_TrailingBlankIsALiteralByte is the trailing-blank
// guard, and it is a LITERAL BYTE CHECK rather than an assertion about rendering.
// There is no CommonMark parser in this repo and every existing gate shows zero
// difference with and without the blank, so a rendering-shaped assertion cannot
// be evaluated at all.
//
// 🔴 THE FIXTURE MUST BE THE PROSE-FOLLOWING SHAPE. Built from
// preformatHeadingNext this test stays GREEN while the defect is live, because
// an ATX heading interrupts a paragraph anyway.
//
// Break: drop the "" from the constructed run. This fails.
func TestPlanTaskHeaderBlock_TrailingBlankIsALiteralByte(t *testing.T) {
	after, outcome, reason := planTaskHeaderBlock(preformatProseNext, storage.StatusDone)
	if outcome != blockConstruct {
		t.Fatalf("outcome = %v, reason %q, want blockConstruct", outcome, reason)
	}
	lines := strings.Split(after, "\n")
	prio := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "**Priority:**") {
			prio = i
			break
		}
	}
	if prio < 0 {
		t.Fatalf("no **Priority:** line in output:\n%s", after)
	}
	if prio+1 >= len(lines) {
		t.Fatal("nothing follows the constructed run")
	}
	if lines[prio+1] != "" {
		t.Errorf("the line after the constructed run is %q, want the empty string — "+
			"without it CommonMark folds the run into the following paragraph and the validator is blind to it",
			lines[prio+1])
	}
	// The paragraph it would have been folded into must still be intact, and
	// still a paragraph of its own.
	if !strings.Contains(after, "\n\nSlug: `vps-skill-artifacts-cross-ide`") {
		t.Errorf("the following prose paragraph was not left standing alone:\n%s", after)
	}
}

// TestPlanTaskHeaderBlock_TrailingBlankFixtureChoiceIsLoadBearing records, as an
// executable fact, why the fixture above must be the prose shape: on the
// heading-following shape the defect is genuinely invisible.
//
// It asserts the WEAKNESS, so it goes red if someone "improves" the
// heading-shaped fixture into the guard and believes they are covered.
func TestPlanTaskHeaderBlock_TrailingBlankFixtureChoiceIsLoadBearing(t *testing.T) {
	for _, tc := range []struct {
		name      string
		src       string
		validWith bool
	}{
		{"heading follows", preformatHeadingNext, true},
		{"prose follows", preformatProseNext, true},
	} {
		after, outcome, _ := planTaskHeaderBlock(tc.src, storage.StatusDone)
		if outcome != blockConstruct {
			t.Fatalf("%s: outcome = %v", tc.name, outcome)
		}
		// Now simulate the break: remove the blank the transform emitted.
		broken := strings.Replace(after, "**Priority:** medium\n\n", "**Priority:** medium\n", 1)
		if err := storage.ValidateWholeTaskFile(broken); err != nil {
			t.Fatalf("%s: the broken form was expected to still VALIDATE — that is the point of "+
				"this test; got %v", tc.name, err)
		}
	}
	// Both shapes validate without the blank. The validator cannot tell them
	// apart, which is why the byte check above exists and why it needs the prose
	// fixture to be meaningful.
}

// TestPlanTaskHeaderBlock_FenceAwareness pins the H1 count to mdfence.
//
// Break: count H1s with a line-prefix scan over raw lines. This fails: the
// fenced '#' comments make the raw count 3 and the file is refused as a title
// defect it does not have.
func TestPlanTaskHeaderBlock_FenceAwareness(t *testing.T) {
	rawHashes := 0
	for _, l := range strings.Split(preformatFencedHashes, "\n") {
		if strings.HasPrefix(l, "# ") {
			rawHashes++
		}
	}
	if rawHashes < 2 {
		t.Fatalf("fixture precondition: it must carry fenced '# ' lines a raw scan would miscount, got %d", rawHashes)
	}
	after, outcome, reason := planTaskHeaderBlock(preformatFencedHashes, storage.StatusDone)
	if outcome != blockConstruct {
		t.Fatalf("outcome = %v, reason %q — a fence-aware scan sees exactly one H1 and this file must be SELECTED", outcome, reason)
	}
	if !strings.Contains(after, "# the palace section") {
		t.Error("the fenced content was altered")
	}
}

// TestPlanTaskHeaderBlock_FinalBytePreserved covers acceptance criterion 13.
//
// Break: rebuild the file with a writer that appends a trailing newline. This
// fails. Four live files lack a final newline and nothing else in the toolchain
// would report the normalisation.
func TestPlanTaskHeaderBlock_FinalBytePreserved(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"no final newline", preformatNoFinalNewline},
		{"final newline", preformatHeadingNext},
		{"trailing blank line", preformatHeadingNext + "\n"},
	} {
		after, outcome, reason := planTaskHeaderBlock(tc.src, storage.StatusDone)
		if outcome != blockConstruct {
			t.Fatalf("%s: outcome = %v, reason %q", tc.name, outcome, reason)
		}
		wantTail := tc.src[strings.LastIndex(strings.TrimRight(tc.src, "\n"), "\n")+1:]
		gotTail := after[strings.LastIndex(strings.TrimRight(after, "\n"), "\n")+1:]
		if gotTail != wantTail {
			t.Errorf("%s: trailing bytes changed\n got %q\nwant %q", tc.name, gotTail, wantTail)
		}
	}
}

// TestPlanTaskHeaderBlock_RefusesAnExistingFieldRun is the arm with NO live
// member, which is precisely why it needs a fixture.
//
// Such a file VALIDATES after a blind insert — the constructed run becomes the
// header block and the author's fields fall outside it — while the run has been
// silently split. Asserting validity alone would go green on it.
//
// Break: delete the IsHeaderFieldLine check. This fails.
func TestPlanTaskHeaderBlock_RefusesAnExistingFieldRun(t *testing.T) {
	src := "# Plan: something\n" +
		"\n" +
		"**Filed:** 2026-05-13\n" +
		"**Reviewed:** 2026-06-07\n" +
		"\n" +
		"## Body\n" +
		"\n" +
		"text\n"
	after, outcome, reason := planTaskHeaderBlock(src, storage.StatusDone)
	if outcome != blockRefused {
		t.Fatalf("outcome = %v, want blockRefused — a file with an existing field run must not be split", outcome)
	}
	if after != "" {
		t.Error("a refusal must not return transformed content")
	}
	if !strings.Contains(reason, "already opens a header field run") {
		t.Errorf("reason = %q, want it to name the existing run", reason)
	}

	// Falsify the refusal: neutralise ONLY the cited condition and confirm the
	// file's disposition actually changes. B1 shipped six refusals of which five
	// cited a cause that blocked nothing.
	neutralised := strings.Replace(src, "**Filed:** 2026-05-13\n**Reviewed:** 2026-06-07\n", "Filed on 2026-05-13.\n", 1)
	if _, outcome2, _ := planTaskHeaderBlock(neutralised, storage.StatusDone); outcome2 != blockConstruct {
		t.Errorf("neutralising the cited condition left outcome = %v; the refusal cites a cause that blocks nothing", outcome2)
	}
}

// TestPlanTaskHeaderBlock_ReportsOtherDefectsRatherThanRefusing covers the
// not-this-command's-defect roster. Each arm is falsified the same way.
func TestPlanTaskHeaderBlock_ReportsOtherDefectsRatherThanRefusing(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{
			"already has a Status field",
			"# T\n\n**Status:** done\n\n## B\n\nx\n",
			"already declares",
		},
		{
			"two titles",
			"# T\n\n# T again\n\n## B\n\nx\n",
			"H1 heading(s) outside fences",
		},
		{
			"no H2 at all",
			"# T\n\n### Sub\n\nx\n",
			"no \"## \" H2 heading",
		},
		{
			"unterminated fence",
			"# T\n\n## B\n\n```go\nnever closed\n",
			"unterminated code fence",
		},
	}
	for _, tc := range cases {
		after, outcome, reason := planTaskHeaderBlock(tc.src, storage.StatusDone)
		if outcome != blockNoWork {
			t.Errorf("%s: outcome = %v, want blockNoWork (reported, not refused)", tc.name, outcome)
		}
		if after != "" {
			t.Errorf("%s: returned content for a non-candidate", tc.name)
		}
		if !strings.Contains(reason, tc.want) {
			t.Errorf("%s: reason = %q, want it to contain %q", tc.name, reason, tc.want)
		}
	}
}

// TestPlanTaskHeaderBlock_AlreadyValidNeedsNothing keeps the command off files it
// has no business touching, silently.
func TestPlanTaskHeaderBlock_AlreadyValidNeedsNothing(t *testing.T) {
	src := "# T\n\n**Status:** done\n**Priority:** high\n\n## B\n\nx\n"
	if err := storage.ValidateWholeTaskFile(src); err != nil {
		t.Fatalf("fixture precondition: must already validate, got %v", err)
	}
	_, outcome, reason := planTaskHeaderBlock(src, storage.StatusDone)
	if outcome != blockNoWork || reason != "" {
		t.Errorf("outcome = %v reason = %q, want blockNoWork with no reason", outcome, reason)
	}
}

// TestPlanTaskHeaderBlock_StatusComesFromTheDirectory covers the cancelled/
// branch, which has NO live member — all 30 targets sit in done/. Without this
// fixture the status is effectively hardcoded and nobody would know until a
// cancelled/ file entered the class.
//
// Break: hardcode storage.StatusDone at the insertion site. The cancelled case
// fails.
func TestPlanTaskHeaderBlock_StatusComesFromTheDirectory(t *testing.T) {
	for _, tc := range []struct{ sub, want string }{
		{"done", "done"},
		{"cancelled", "cancelled"},
	} {
		dirStatus, ok := taskHeaderBlockStatusFor(tc.sub)
		if !ok {
			t.Fatalf("%s/: no status mapping", tc.sub)
		}
		after, outcome, _ := planTaskHeaderBlock(preformatHeadingNext, dirStatus)
		if outcome != blockConstruct {
			t.Fatalf("%s/: outcome = %v", tc.sub, outcome)
		}
		meta := storage.ParseTaskMetaFromContent("t", after, true)
		if meta.Status != tc.want {
			t.Errorf("%s/: constructed Status = %q, want %q", tc.sub, meta.Status, tc.want)
		}
		if !storage.IsTerminalStatus(meta.Status) {
			t.Errorf("%s/: constructed Status %q is not terminal — it would drop the file into "+
				"the status-rewrite population of the migrations this repair exists to unblock", tc.sub, meta.Status)
		}
	}
	if _, ok := taskHeaderBlockStatusFor(""); ok {
		t.Error("the ACTIVE directory must have no status mapping")
	}
}

// TestPlanTaskHeaderBlock_FabricatedPriorityIsPinned is the expected-GREEN break
// made into a real assertion up front: change the fabricated value and this goes
// red, so the operator-approved default cannot drift silently.
func TestPlanTaskHeaderBlock_FabricatedPriorityIsPinned(t *testing.T) {
	if storage.LegacyPriorityDefault != "medium" {
		t.Fatalf("LegacyPriorityDefault = %q, want %q — the operator approved this exact value",
			storage.LegacyPriorityDefault, "medium")
	}
	after, _, _ := planTaskHeaderBlock(preformatHeadingNext, storage.StatusDone)
	if !strings.Contains(after, "**Priority:** medium\n") {
		t.Errorf("constructed priority is not the operator-approved default:\n%s", after)
	}
	meta := storage.ParseTaskMetaFromContent("t", after, true)
	if meta.Priority != "medium" {
		t.Errorf("parsed Priority = %q, want medium", meta.Priority)
	}
}

// TestPlanTaskHeaderBlock_NoFileIsSpecialCased covers acceptance criterion 10.
// Ruling 4 was reversed to remove the per-file conditional; a reversal nothing
// tests is a preference.
//
// Break: add a per-file provenance section for one slug. This fails.
func TestPlanTaskHeaderBlock_NoFileIsSpecialCased(t *testing.T) {
	// The conflicted file's distinguishing shape: a `## Status:` HEADING that
	// contradicts the directory-derived value.
	conflicted := "# Plan: Phase 6 Task 6.1 — Palace Metadata System (Revised)\n" +
		"\n" +
		"## Status: Ready for implementation\n" +
		"\n" +
		"## Context\n" +
		"\n" +
		"x\n"
	agreeing := strings.Replace(conflicted, "Ready for implementation", "Done", 1)

	var runs []string
	for _, src := range []string{conflicted, agreeing, preformatHeadingNext} {
		after, outcome, reason := planTaskHeaderBlock(src, storage.StatusDone)
		if outcome != blockConstruct {
			t.Fatalf("outcome = %v, reason %q", outcome, reason)
		}
		// 🔴 EXTRACT BY DIFFING THE LINE SEQUENCES, NOT BY SLICING TO THE FIRST
		// BLANK. An earlier version of this test sliced the output to its first
		// "\n\n" and compared that — which captures the two constructed fields
		// and NOTHING a break appends after them, so a per-file provenance
		// section inserted below the run left this test GREEN. Found by applying
		// the break, which is the only reason it is written this way now.
		runs = append(runs, strings.Join(insertedLines(src, after), "\n"))
	}
	for i := 1; i < len(runs); i++ {
		if runs[i] != runs[0] {
			t.Errorf("the inserted run differs between files — no file may be special-cased\n"+
				"file 0: %q\nfile %d: %q", runs[0], i, runs[i])
		}
	}
	// And the body heading must survive untouched: Ruling 4's surviving half.
	after, _, _ := planTaskHeaderBlock(conflicted, storage.StatusDone)
	if !strings.Contains(after, "## Status: Ready for implementation") {
		t.Error("the body status heading was removed; Ruling 4 keeps all 12")
	}
}

// TestPlanTaskHeaderBlock_PostConditionRefusesAnEmptyHeading is the fixture the
// post-condition actually needs, and its absence is why an earlier version of
// this file asserted the gate was unreachable. IT IS REACHABLE.
//
// 🔴 THE DETECTOR AND THE VALIDATOR COUNT HEADINGS WITH DIFFERENT PREDICATES.
// This file's selection uses headingLevel, which accepts a '#' run followed by
// nothing — an EMPTY heading is level 1 to it. storage's isH1Line and isH2Line
// require the literal "# " / "## " prefix after trimming, so an empty heading is
// not a heading to them at all. A file whose only H1-shaped line is a bare "#"
// therefore passes the detector's "exactly one H1" precondition and produces a
// construction the validator refuses at its missing-title arm. The same holds
// one level down for a bare "##" and the missing-section arm.
//
// So the post-condition is LOAD-BEARING, not defence in depth, and it is the
// only thing standing between that disagreement and a written file. Break it
// (replace the ValidateWholeTaskFile call on the rebuilt bytes with nil) and
// this test fails.
func TestPlanTaskHeaderBlock_PostConditionRefusesAnEmptyHeading(t *testing.T) {
	for _, tc := range []struct{ name, src, wantIn string }{
		{"empty H1, no text after the hash", "#\n\n## B\n\nx\n", "missing title"},
		{"empty H1 with a trailing space", "# \n\n## B\n\nx\n", "missing title"},
		{"empty H2, no text after the hashes", "# T\n\n##\n\nx\n", "missing section"},
		{"empty H2 with a trailing space", "# T\n\n## \n\nx\n", "missing section"},
	} {
		after, outcome, reason := planTaskHeaderBlock(tc.src, storage.StatusDone)
		if outcome != blockRefused {
			t.Errorf("%s: outcome = %v, want blockRefused — the construction is invalid and only the "+
				"post-condition catches it", tc.name, outcome)
			continue
		}
		if after != "" {
			t.Errorf("%s: a refusal must not return transformed content", tc.name)
		}
		if !strings.Contains(reason, "does not validate") || !strings.Contains(reason, tc.wantIn) {
			t.Errorf("%s: reason = %q, want it to name the validator and %q", tc.name, reason, tc.wantIn)
		}
	}
}

// TestPlanTaskHeaderBlock_DetectorAndValidatorAgreeOnRealisticShapes is what the
// unreachability test was reaching for, stated as a property that is actually
// true: for every shape the live population contains or plausibly could, a
// SELECTED file produces a construction the validator accepts and the fence-blind
// reader parses the same way the fence-aware validator does.
//
// It is NOT a claim that the post-condition never fires — see the empty-heading
// test above, which is the counterexample that broke the earlier claim. What this
// pins is the boundary: these shapes are on the safe side of it, and if one of
// them ever crosses over, the post-condition has started doing work nobody
// predicted and the detector needs a look.
func TestPlanTaskHeaderBlock_DetectorAndValidatorAgreeOnRealisticShapes(t *testing.T) {
	shapes := map[string]string{
		"fenced Status sample in the body":  "# T\n\n## Example\n\n```\n**Status:** done\n**Priority:** low\n```\n\n## B\n\nx\n",
		"H1 not on line 1":                  "Intro prose.\n\n# T\n\n## B\n\nx\n",
		"H1 on the last line, H2 above":     "## B\n\nx\n\n# T\n",
		"fenced H1 before the real one":     "```\n# fake\n```\n\n# T\n\n## B\n\nx\n",
		"H2 abutting the H1 with no blank":  "# T\n## B\n\nx\n",
		"CRLF line endings":                 "# T\r\n\r\n## B\r\n\r\nx\r\n",
		"indented H1":                       "   # T\n\n## B\n\nx\n",
		"heading-following (the common 29)": preformatHeadingNext,
		"prose-following (the one)":         preformatProseNext,
		"fenced '#' comments":               preformatFencedHashes,
	}
	for name, src := range shapes {
		after, outcome, reason := planTaskHeaderBlock(src, storage.StatusDone)
		if outcome != blockConstruct {
			t.Fatalf("%s: outcome = %v, reason %q — this shape must be SELECTED, or the property "+
				"below is being tested against the wrong inputs", name, outcome, reason)
		}
		if err := storage.ValidateWholeTaskFile(after); err != nil {
			t.Errorf("%s: a SELECTED input produced an INVALID construction (%v). The detector and "+
				"the validator have diverged on a shape that was on the safe side of the boundary.", name, err)
		}
		// 🔴 The fence-blind reader must agree with the fence-aware validator.
		// parseTaskMeta scans the WHOLE file first-wins and does not know about
		// fences; it binds the constructed values only because the constructed
		// run precedes any fenced sample. That is a property of where the run is
		// inserted, not a guarantee, so it is asserted rather than assumed.
		meta := storage.ParseTaskMetaFromContent("t", after, true)
		if meta.Status != storage.StatusDone {
			t.Errorf("%s: parseTaskMeta bound Status %q, want %q — the fence-blind reader and the "+
				"fence-aware validator disagree about this file", name, meta.Status, storage.StatusDone)
		}
		if meta.Priority != storage.LegacyPriorityDefault {
			t.Errorf("%s: parseTaskMeta bound Priority %q, want %q", name, meta.Priority, storage.LegacyPriorityDefault)
		}
	}
}

// TestPlanTaskHeaderBlock_Idempotent: a repaired file is no longer a candidate.
func TestPlanTaskHeaderBlock_Idempotent(t *testing.T) {
	after, outcome, _ := planTaskHeaderBlock(preformatHeadingNext, storage.StatusDone)
	if outcome != blockConstruct {
		t.Fatalf("first pass outcome = %v", outcome)
	}
	_, outcome2, reason2 := planTaskHeaderBlock(after, storage.StatusDone)
	if outcome2 != blockNoWork || reason2 != "" {
		t.Errorf("second pass outcome = %v reason = %q, want blockNoWork with no reason", outcome2, reason2)
	}
}

// insertedLines returns every line the transform ADDED, in order, by walking the
// before and after line sequences together. It makes no assumption about where
// the insertion lands or how much of it there is, which is the property the
// no-special-casing assertion needs: a break that appends extra lines anywhere
// must show up here.
func insertedLines(before, after string) []string {
	b := strings.Split(before, "\n")
	a := strings.Split(after, "\n")
	var added []string
	i := 0
	for j := 0; j < len(a); j++ {
		if i < len(b) && a[j] == b[i] {
			i++
			continue
		}
		added = append(added, a[j])
	}
	return added
}

// ---------------------------------------------------------------------------
// The command — a throwaway vault per test.
// ---------------------------------------------------------------------------

func TestMigrateTaskHeaderBlock_ReportWritesNothing(t *testing.T) {
	root := t.TempDir()
	path := seedArchivedTask(t, root, "p", "done", "legacy", preformatHeadingNext)

	var out bytes.Buffer
	sum, err := runTaskHeaderBlockMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Fix != 1 {
		t.Errorf("Fix = %d, want 1", sum.Fix)
	}
	if sum.Applied != 0 {
		t.Errorf("Applied = %d, want 0 — report mode must not write", sum.Applied)
	}
	got, _ := os.ReadFile(path)
	if string(got) != preformatHeadingNext {
		t.Error("report mode modified the file on disk")
	}
	if !strings.Contains(out.String(), "REPORT ONLY") {
		t.Errorf("report mode did not say so:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "FABRICATED") {
		t.Errorf("the report must name the priority as fabricated:\n%s", out.String())
	}
}

// TestMigrateTaskHeaderBlock_ActiveFilesAreNeverTouched is the scope fence.
//
// Break: add "" to taskHeaderBlockDirs. This fails.
func TestMigrateTaskHeaderBlock_ActiveFilesAreNeverTouched(t *testing.T) {
	root := t.TempDir()
	active := seedArchivedTask(t, root, "p", "", "activelegacy", preformatHeadingNext)

	var out bytes.Buffer
	sum, err := runTaskHeaderBlockMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0 — the active directory must not be walked", sum.Scanned)
	}
	got, _ := os.ReadFile(active)
	if string(got) != preformatHeadingNext {
		t.Error("an ACTIVE file was modified")
	}
}

// TestMigrateTaskHeaderBlock_ApplyWritesAndIsIdempotent covers the three-run
// shape: run 1 applies, runs 2 and 3 select nothing and write nothing.
//
// 🔴 IT ASSERTS Fix == 0 && Applied == 0 ON RUNS 2 AND 3, NOT BYTE EQUALITY.
// The seam short-circuits on identical content BEFORE validating and BEFORE the
// restamp, so byte equality is guaranteed whenever transform(disk) == disk — it
// would go green on a file that was never repaired at all.
func TestMigrateTaskHeaderBlock_ApplyWritesAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	path := seedArchivedTask(t, root, "p", "done", "legacy", preformatHeadingNext)
	gitCommitAll(t, root)

	var out bytes.Buffer
	sum, err := runTaskHeaderBlockMigration(root, "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Applied != 1 || sum.Failed != 0 {
		t.Fatalf("run 1: Applied = %d Failed = %d, want 1 and 0\n%s", sum.Applied, sum.Failed, out.String())
	}

	onDisk, _ := os.ReadFile(path)
	if err := storage.ValidateWholeTaskFile(string(onDisk)); err != nil {
		// Read from DISK, not from the computed output: the restamp is applied
		// after validation and its result is never re-validated.
		t.Errorf("the file ON DISK does not validate after apply: %v", err)
	}
	if n := strings.Count(string(onDisk), "**ModTime:**"); n != 1 {
		t.Errorf("**ModTime:** appears %d time(s) after run 1, want exactly 1", n)
	}
	if _, _, hazard := storage.FindHeaderSpacingHazard(string(onDisk)); hazard {
		t.Error("the written file fires a header-spacing hazard")
	}

	for run := 2; run <= 3; run++ {
		var o bytes.Buffer
		s, err := runTaskHeaderBlockMigration(root, "", true, &o)
		if err != nil {
			t.Fatal(err)
		}
		if s.Fix != 0 || s.Applied != 0 {
			t.Errorf("run %d: Fix = %d Applied = %d, want 0 and 0 — the selector re-fired on a repaired file",
				run, s.Fix, s.Applied)
		}
		// 🔴 Fix == 0 && Applied == 0 IS NECESSARY AND NOT SUFFICIENT, and the
		// break protocol is what showed it: with every convergence precondition
		// removed, a repaired file falls through to the existing-field-run
		// REFUSAL and BOTH of those counters stay at zero anyway. The run would
		// look converged while printing a "??" row against a file it had just
		// repaired correctly — a refusal citing a cause that should not apply.
		// Convergence means the file is classified as needing nothing, silently.
		//
		// Measured, so the attribution is right: under that break BOTH assertions
		// below fire. `Refused == 0` alone is sufficient to catch it; the
		// per-decision check is what names WHICH file and WHY, which is the
		// difference between a red test and a diagnosable one. Neither is
		// redundant and neither is "the" discriminator.
		if s.Refused != 0 {
			t.Errorf("run %d: Refused = %d, want 0 — a correctly repaired file must be classified as "+
				"needing nothing, not refused", run, s.Refused)
		}
		for _, d := range s.Decisions {
			if d.Slug != "legacy" {
				continue
			}
			if d.Outcome != blockNoWork || d.Reason != "" {
				t.Errorf("run %d: repaired file classified %v with reason %q, want blockNoWork and no reason",
					run, d.Outcome, d.Reason)
			}
		}
		again, _ := os.ReadFile(path)
		if n := strings.Count(string(again), "**ModTime:**"); n != 1 {
			t.Errorf("run %d: **ModTime:** appears %d time(s), want exactly 1", run, n)
		}
	}
}

// TestMigrateTaskHeaderBlock_ShadowedSlugIsRefusedInBothModes.
//
// 🔴 IT ASSERTS THE ACTIVE FILE'S BYTES, NOT THE EXIT CODE. B1's equivalent test
// once passed for the wrong reason: its STRICT seam refused independently of the
// guard. This command uses the PERMISSIVE seam, which runs no header comparison
// at all, so nothing masks the guard's absence and a deleted guard really does
// clobber the active file.
//
// Break: delete the taskHeaderShadowed call. This fails.
// Break: move it inside `if apply`. The report-mode half fails.
func TestMigrateTaskHeaderBlock_ShadowedSlugIsRefusedInBothModes(t *testing.T) {
	const activeBody = "# ACTIVE task, must not be touched\n\n**Status:** in_progress\n**Priority:** high\n\n## Plan\n\nlive work\n"

	for _, apply := range []bool{false, true} {
		root := t.TempDir()
		gitInitVault(t, root)
		activePath := seedArchivedTask(t, root, "p", "", "dup", activeBody)
		seedArchivedTask(t, root, "p", "done", "dup", preformatHeadingNext)
		gitCommitAll(t, root)

		var out bytes.Buffer
		sum, err := runTaskHeaderBlockMigration(root, "", apply, &out)
		if err != nil {
			t.Fatal(err)
		}
		if sum.Fix != 0 {
			t.Errorf("apply=%v: Fix = %d, want 0 — a shadowed slug must never be printed as FIX", apply, sum.Fix)
		}
		// 🔴 THE REFUSAL MUST BE COUNTED WHERE THE SUMMARY READS IT. The run
		// prints a refusal row and "%d refused" in the same output; incrementing
		// only Failed made the summary say "0 refused" beside its own visible
		// refusal. A counter reading zero next to its own evidence is the
		// silent-instrument class with the instrument present and lying.
		//
		// Break: drop the sum.Refused++ at the shadow arm. This goes red.
		if sum.Refused != 1 {
			t.Errorf("apply=%v: Refused = %d, want 1 — the summary's refused count must include the "+
				"shadow refusal it just printed; out:\n%s", apply, sum.Refused, out.String())
		}
		// And it must STILL fail the run: a shadowed slug is an operator problem
		// that exit 0 would bury.
		if sum.Failed != 1 {
			t.Errorf("apply=%v: Failed = %d, want 1 — a shadowed slug must make the run exit non-zero",
				apply, sum.Failed)
		}
		if !strings.Contains(out.String(), "refused") {
			t.Errorf("apply=%v: the summary never uses the word the counter counts:\n%s", apply, out.String())
		}
		if !strings.Contains(out.String(), "an ACTIVE task of the same slug exists") {
			t.Errorf("apply=%v: the report did not name the shadow:\n%s", apply, out.String())
		}
		got, _ := os.ReadFile(activePath)
		if string(got) != activeBody {
			t.Errorf("apply=%v: THE ACTIVE FILE WAS CLOBBERED\n got: %q\nwant: %q", apply, got, activeBody)
		}
	}
}

// TestMigrateTaskHeaderBlock_DirtyFileIsSkippedNotFailed — git holds the only
// copy of an uncommitted change and this is a whole-file overwrite.
func TestMigrateTaskHeaderBlock_DirtyFileIsSkippedNotFailed(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	seedArchivedTask(t, root, "p", "done", "clean", preformatHeadingNext)
	gitCommitAll(t, root)
	dirtyPath := seedArchivedTask(t, root, "p", "done", "dirty", preformatProseNext)

	var out bytes.Buffer
	sum, err := runTaskHeaderBlockMigration(root, "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Dirty != 1 {
		t.Errorf("Dirty = %d, want 1", sum.Dirty)
	}
	if sum.Failed != 0 {
		t.Errorf("Failed = %d, want 0 — a dirty skip is not a failure", sum.Failed)
	}
	if sum.Applied != 1 {
		t.Errorf("Applied = %d, want 1 — the clean file must still be repaired", sum.Applied)
	}
	got, _ := os.ReadFile(dirtyPath)
	if string(got) != preformatProseNext {
		t.Error("the dirty file was written")
	}
}

// TestMigrateTaskHeaderBlock_ApplyRequiresAGitRepo. HasUncommittedChanges returns
// CLEAN, not an error, outside a git repo, so without this gate every file would
// look committed.
//
// Break: drop the requireVaultGitRepo call. This fails.
func TestMigrateTaskHeaderBlock_ApplyRequiresAGitRepo(t *testing.T) {
	root := t.TempDir()
	seedArchivedTask(t, root, "p", "done", "legacy", preformatHeadingNext)

	var out bytes.Buffer
	if _, err := runTaskHeaderBlockMigration(root, "", true, &out); err == nil {
		t.Fatal("--apply outside a git repo must refuse")
	}
	// Report mode is fine without git.
	var o2 bytes.Buffer
	if _, err := runTaskHeaderBlockMigration(root, "", false, &o2); err != nil {
		t.Fatalf("report mode must not need git: %v", err)
	}
}

// TestMigrateTaskHeaderBlock_RollbackBannerQuotesEachPath — a list joined into
// one quoted value keeps the continuation indentation inside the argument, and
// `git checkout --` then reverts nothing while looking like it worked.
func TestMigrateTaskHeaderBlock_RollbackBannerListsEveryPathSeparately(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	seedArchivedTask(t, root, "p", "done", "alpha", preformatHeadingNext)
	seedArchivedTask(t, root, "p", "done", "beta", preformatProseNext)
	// Pre-create and COMMIT the stamp so GitPathIsTracked says true and it
	// reaches the rollback list. Without this it is untracked, correctly
	// dropped, and the population falls back to one path per task file — which
	// is exactly why this fixture and not a one-file one.
	if err := os.WriteFile(filepath.Join(root, "Projects", "p", ".surface"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, root)

	var out bytes.Buffer
	sum, err := runTaskHeaderBlockMigration(root, "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Applied != 2 {
		t.Fatalf("Applied = %d, want 2:\n%s", sum.Applied, out.String())
	}

	// 🔴 AN EXPLICIT want LIST, NOT A WALK OF sum.AppliedPaths. The earlier
	// version of this test iterated AppliedPaths and asserted each member was
	// quoted, which made it self-referential: whatever the code put in the slice
	// was what got checked, so a stamp that never arrived and an untracked stamp
	// that did BOTH passed. Two breaks survived that shape — the same defect
	// task-sections had already closed, whose banner and doc comment were copied
	// here without the fixture that makes them testable.
	want := []string{
		"Projects/p/tasks/done/alpha.md",
		"Projects/p/tasks/done/beta.md",
		"Projects/p/.surface",
	}
	for _, w := range want {
		if !slicesContains(sum.AppliedPaths, w) {
			t.Errorf("AppliedPaths %v is missing %q — the documented undo would omit a path the "+
				"writer dirtied, so the operator runs it and the vault is still dirty", sum.AppliedPaths, w)
		}
	}
	if len(sum.AppliedPaths) != 3 {
		t.Errorf("AppliedPaths = %v, want exactly 3 (two task files and the tracked stamp)", sum.AppliedPaths)
	}

	s := out.String()
	// EACH path separately quoted. A list joined into one quoted value keeps the
	// continuation indentation inside the argument, and git receives one bogus
	// pathspec instead of three real ones.
	for _, w := range want {
		if !strings.Contains(s, `"`+w+`"`) {
			t.Errorf("banner does not quote %q on its own:\n%s", w, s)
		}
	}
	if strings.Contains(s, `"Projects/p/tasks/done/alpha.md Projects/`) {
		t.Errorf("banner joined several paths into ONE quoted argument:\n%s", s)
	}
	if !strings.Contains(s, "git -C "+root+" checkout --") {
		t.Errorf("the banner must scope the undo to the vault root:\n%s", s)
	}
	if !strings.Contains(s, "Do NOT use `git checkout .`") {
		t.Errorf("the scoped-rollback warning is missing:\n%s", s)
	}
}

// TestMigrateTaskHeaderBlock_UntrackedStampIsNotInTheRollbackList is the other
// half of the same guard, and it is the one the GitPathIsTracked gate exists for.
//
// A stamp git has never seen must be OMITTED: `git checkout -- <untracked>` is a
// pathspec error, and git applies it to the WHOLE command — so one such path
// makes the undo restore NONE of the task files either, while looking to the
// operator like it worked. The doc comment on taskHeaderBlockRecordWrite says
// exactly this and nothing pinned it until now.
//
// Break: delete the GitPathIsTracked check in taskHeaderBlockRecordWrite. This
// test fails.
func TestMigrateTaskHeaderBlock_UntrackedStampIsNotInTheRollbackList(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	seedArchivedTask(t, root, "p", "done", "alpha", preformatHeadingNext)
	gitCommitAll(t, root) // no .surface committed: the writer creates it untracked

	var out bytes.Buffer
	sum, err := runTaskHeaderBlockMigration(root, "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Applied != 1 {
		t.Fatalf("Applied = %d, want 1:\n%s", sum.Applied, out.String())
	}
	for _, p := range sum.AppliedPaths {
		if strings.HasSuffix(p, ".surface") {
			t.Errorf("an UNTRACKED stamp reached the rollback list (%q); `git checkout --` would "+
				"fail for every path, restoring nothing", p)
		}
	}
}

// TestMigrateTaskHeaderBlock_ApplyReportsTheFabrication is where an operator
// LEARNS the priority value is invented. The value is not recoverable from the
// bytes afterwards, so the run output and the commit message are the only two
// places it is ever stated — and nothing pinned the run output.
//
// Break: delete the fabrication notice from the apply summary. This test fails.
func TestMigrateTaskHeaderBlock_ApplyReportsTheFabrication(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	seedArchivedTask(t, root, "p", "done", "legacy", preformatHeadingNext)
	gitCommitAll(t, root)

	var out bytes.Buffer
	if _, err := runTaskHeaderBlockMigration(root, "", true, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "FABRICATION") {
		t.Errorf("the apply summary never says the priority is a fabrication:\n%s", s)
	}
	if !strings.Contains(s, storage.LegacyPriorityDefault) {
		t.Errorf("the apply summary does not name the fabricated value %q:\n%s",
			storage.LegacyPriorityDefault, s)
	}
	if !strings.Contains(s, "commit message") {
		t.Errorf("the apply summary does not tell the operator where the provenance must go:\n%s", s)
	}
}

// TestMigrateTaskHeaderBlock_OtherDefectRowsArePrinted is the silent-instrument
// guard: a file this command declines to repair because the defect is not ours
// is COUNTED in OtherDefect and must also be PRINTED. A counter with no row is
// the shape this project closed six times at iteration 427 — code that detects a
// problem, discards the signal, and surfaces a plausible-looking number.
//
// Break: delete the Fprintf in the blockNoWork arm, keep sum.OtherDefect++.
// This test fails.
func TestMigrateTaskHeaderBlock_OtherDefectRowsArePrinted(t *testing.T) {
	root := t.TempDir()
	// Malformed, but the defect belongs to another unit: it already has a Status.
	seedArchivedTask(t, root, "p", "done", "hasstatus", "# T\n\n**Status:** done\n\n## B\n\nx\n")

	var out bytes.Buffer
	sum, err := runTaskHeaderBlockMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.OtherDefect != 1 {
		t.Fatalf("OtherDefect = %d, want 1:\n%s", sum.OtherDefect, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "hasstatus") {
		t.Errorf("the other-defect file is counted but never named in the report:\n%s", s)
	}
	if !strings.Contains(s, "not this command's defect") {
		t.Errorf("the other-defect row does not say whose defect it is:\n%s", s)
	}
}

// TestMigrateTaskHeaderBlock_PopulationDifferential compares the command's
// selected set against the audit's missing-Status class over a seeded vault
// holding one representative of each disposition.
//
// 🔴 It asserts SET EQUALITY IN BOTH DIRECTIONS and names the differing paths.
// A cardinality compare passes a walk that drops one file and gains another.
//
// Break: remove one file from the command's walk. This fails.
func TestMigrateTaskHeaderBlock_PopulationDifferential(t *testing.T) {
	root := t.TempDir()
	want := map[string]bool{}
	for _, tc := range []struct {
		slug, body string
		candidate  bool
	}{
		{"heading-next", preformatHeadingNext, true},
		{"prose-next", preformatProseNext, true},
		{"fenced-hashes", preformatFencedHashes, true},
		{"no-final-newline", preformatNoFinalNewline, true},
		{"already-valid", "# T\n\n**Status:** done\n**Priority:** high\n\n## B\n\nx\n", false},
		{"has-status", "# T\n\n**Status:** done\n\n## B\n\nx\n", false},
		{"two-titles", "# T\n\n# T2\n\n## B\n\nx\n", false},
		{"no-h2", "# T\n\n### Sub\n\nx\n", false},
	} {
		seedArchivedTask(t, root, "p", "done", tc.slug, tc.body)
		if tc.candidate {
			want["p/"+tc.slug] = true
		}
	}

	var out bytes.Buffer
	sum, err := runTaskHeaderBlockMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range sum.Decisions {
		if d.Outcome == blockConstruct {
			got[d.Project+"/"+d.Slug] = true
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("%s is a candidate the command did not select", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("%s was selected but is not a candidate", k)
		}
	}
	// Anti-vacuity: the selection must be non-empty, or this proves nothing.
	if len(got) == 0 {
		t.Fatal("the command selected nothing; this test would pass vacuously")
	}
	// Every selected file must be one the real validator rejects.
	for _, d := range sum.Decisions {
		if d.Outcome != blockConstruct {
			continue
		}
		raw, _ := os.ReadFile(filepath.Join(root, "Projects", d.Project, "tasks", d.Sub, d.Slug+".md"))
		if err := storage.ValidateWholeTaskFile(string(raw)); err == nil {
			t.Errorf("%s was selected but already validates", d.Slug)
		}
	}
}

// TestMigrateTaskHeaderBlock_ArchivedPairIsRefused closes a COVERAGE gap, not a
// live defect: the behaviour below is already correct at this revision.
//
// 🔴 THE ARCHIVED-PAIR BRANCH OF THE SHARED GUARD USED TO BE EXERCISED BY ONLY
// ONE COMMAND'S TESTS. Before this test existed, narrowing taskHeaderShadowWinner
// back to active-only reddened only the task-sections and task-header-spacing
// tests; this command reached the branch and stayed green. That is history, not
// a current fact -- with this test in place the same break reds this command too,
// which is the entire point of adding it. Do not read the old state as the
// present one.
//
// The gap was structurally invisible per-branch: this command branched before the
// guard was widened, so only the merged tree carries both halves and only a test
// written after the merge can see the hole.
func TestMigrateTaskHeaderBlock_ArchivedPairIsRefused(t *testing.T) {
	for _, apply := range []bool{false, true} {
		name := "report"
		if apply {
			name = "apply"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			// Same slug in done/ AND cancelled/, no active twin. The resolver
			// picks done/, so repairing the cancelled/ copy writes the wrong file.
			donePath := seedArchivedTask(t, root, "p", "done", "pair", preformatHeadingNext)
			cancPath := seedArchivedTask(t, root, "p", "cancelled", "pair", preformatHeadingNext)
			doneBefore, cancBefore := blockBytes(t, donePath), blockBytes(t, cancPath)
			tsGitInit(t, root)

			var out bytes.Buffer
			sum, err := runTaskHeaderBlockMigration(root, "", apply, &out)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "the same slug also exists in tasks/done/") {
				t.Errorf("the archived-pair refusal is missing or reworded; out:\n%s", out.String())
			}
			if strings.Contains(out.String(), "an ACTIVE task of the same slug exists") {
				t.Errorf("blamed an ACTIVE twin that does not exist -- false cause; out:\n%s", out.String())
			}
			// The done/ copy is the resolver's winner AND its own directory, so it
			// is repairable; only the cancelled/ copy is refused.
			if sum.Failed != 1 {
				t.Errorf("Failed = %d, want 1 (the shadowed cancelled/ copy); out:\n%s", sum.Failed, out.String())
			}
			if got := blockBytes(t, cancPath); got != cancBefore {
				t.Fatalf("the refused cancelled/ copy was written:\n%s", got)
			}
			if !apply {
				if got := blockBytes(t, donePath); got != doneBefore {
					t.Fatalf("a report-only run wrote done/:\n%s", got)
				}
			}
		})
	}
}

// blockBytes is a byte-exact read for the shadowed-pair assertions. Bytes, not
// parsed fields: a mis-resolved write produces a file whose header parses
// perfectly and whose BODY came from somewhere else.
func blockBytes(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}
