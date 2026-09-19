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

// dupPriorityFixture is a well-formed header block plus a SECOND **Priority:**
// line below it, outside the block — the live shape of eight archived files.
const dupPriorityFixture = "# T\n\n**Status:** retired\n**Priority:** medium\n\n## Context\n\nbody\n\n**Priority:** high\n"

// dupStatusFixture is the two-**Status:** shape: a terminal token in the block
// and a paragraph of provenance below it.
const dupStatusFixture = "# T\n\n**Status:** retired\n**Priority:** medium\n\n**Status:** Planned 2026-06-18. No code written.\n\n## Context\n\nbody\n"

// cleanFixture validates and must never be selected.
const cleanFixture = "# T\n\n**Status:** retired\n**Priority:** medium\n\n## Context\n\nbody\n"

// ---------------------------------------------------------------------------
// The seam table. A source-text grep CANNOT express "strict for these classes,
// permissive for that one" — the sibling's absence-grep idiom is not transferable
// to a file that legitimately contains both wrappers — so the pin is a DATA
// assertion over the table.
// ---------------------------------------------------------------------------

// TestTaskHeaderShapeSeamTableIsExhaustive fails when a class is added without a
// seam entry, rather than letting it fall through to a default.
//
// Break: delete an entry from taskHeaderShapeSeamFor. This test fails.
func TestTaskHeaderShapeSeamTableIsExhaustive(t *testing.T) {
	for _, c := range taskHeaderShapeWritingClasses {
		if _, ok := taskHeaderShapeSeamFor[c]; !ok {
			t.Errorf("class %s has no entry in taskHeaderShapeSeamFor: a class with no declared seam "+
				"must be a build-visible hole, not a silent default to the permissive writer", c)
		}
	}
	if len(taskHeaderShapeSeamFor) != len(taskHeaderShapeWritingClasses) {
		t.Errorf("taskHeaderShapeSeamFor has %d entries but %d writing classes are declared — the two "+
			"rosters have drifted", len(taskHeaderShapeSeamFor), len(taskHeaderShapeWritingClasses))
	}
}

// TestTaskHeaderShapeSeamsAreAsRuled is the catcher for "swap the seam on a
// RELABEL class". It is a data assertion, which is what actually goes red.
//
// Break: change shapeRelabelPriority to seamPermissive. This test fails.
func TestTaskHeaderShapeSeamsAreAsRuled(t *testing.T) {
	want := map[taskHeaderShapeClass]taskHeaderShapeSeam{
		shapeRelabelPriority: seamStrict,
		shapeRelabelStatus:   seamStrict,
	}
	for c, w := range want {
		got, ok := taskHeaderShapeSeamFor[c]
		if !ok {
			t.Errorf("class %s missing from the seam table", c)
			continue
		}
		if got != w {
			t.Errorf("class %s uses seam %d, want %d — strict wherever it suffices is a decision, not a default", c, got, w)
		}
	}
}

// TestTaskHeaderShapeStrictSeamIsEarned proves the strict marking rather than
// asserting it: for every class marked strict, the real before/after pair must go
// through the STRICT writer without refusal. Without this, seamStrict would be an
// assertion about itself.
func TestTaskHeaderShapeStrictSeamIsEarned(t *testing.T) {
	cases := []struct {
		class   taskHeaderShapeClass
		content string
	}{
		{shapeRelabelPriority, dupPriorityFixture},
		{shapeRelabelStatus, dupStatusFixture},
	}
	for _, tc := range cases {
		if taskHeaderShapeSeamFor[tc.class] != seamStrict {
			continue
		}
		root := t.TempDir()
		gitInitVault(t, root)
		seedArchivedTask(t, root, "p", "done", "x", tc.content)
		gitCommitAll(t, root)

		after, class, reason := planTaskHeaderShape(tc.content)
		if class != tc.class {
			t.Fatalf("%s: classified %s (reason %q)", tc.class, class, reason)
		}
		if err := storage.NewVault(root).OverwriteTaskFile("p", "x", after); err != nil {
			t.Errorf("%s is marked seamStrict but the STRICT writer refused its own output: %v", tc.class, err)
		}
	}
}

// ---------------------------------------------------------------------------
// The walk.
// ---------------------------------------------------------------------------

// TestMigrateTaskHeaderShape_RepairsAndIsIdempotent covers the writer-convergence
// half that byte-equality alone cannot: the no-op short-circuit guarantees
// identical bytes whenever transform(disk) == disk, independent of whether the
// SELECTOR converged, and a refused write leaves the file at its original bytes
// across every run. Fix == 0 && Applied == 0 on runs 2 and 3 is what has teeth.
//
// Break: make the detector also match "**Legacy priority:**" so the selector
// re-fires on an already-repaired file. This test fails on run 2.
func TestMigrateTaskHeaderShape_RepairsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	path := seedArchivedTask(t, root, "p", "done", "dup", dupPriorityFixture)
	gitCommitAll(t, root)

	var out bytes.Buffer
	sum, err := runTaskHeaderShapeMigration(root, "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Applied != 1 || sum.Failed != 0 {
		t.Fatalf("run 1: Applied = %d, Failed = %d, want 1 and 0\n%s", sum.Applied, sum.Failed, out.String())
	}
	first, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if verr := storage.ValidateWholeTaskFile(string(first)); verr != nil {
		t.Errorf("the file ON DISK does not validate after run 1: %v", verr)
	}
	if n := strings.Count(string(first), "**ModTime:**"); n != 1 {
		t.Errorf("run 1 left %d \"**ModTime:**\" lines, want exactly 1", n)
	}
	if _, _, ok := storage.FindHeaderSpacingHazard(string(first)); ok {
		t.Error("run 1 manufactured a header-spacing hazard for the adjacent tool")
	}

	gitCommitAll(t, root)
	for _, run := range []int{2, 3} {
		var o bytes.Buffer
		s, err := runTaskHeaderShapeMigration(root, "", true, &o)
		if err != nil {
			t.Fatal(err)
		}
		if s.Fix != 0 || s.Applied != 0 {
			t.Errorf("run %d: Fix = %d, Applied = %d, want 0 and 0 — the selector re-fired on an "+
				"already-repaired file\n%s", run, s.Fix, s.Applied, o.String())
		}
		again, _ := os.ReadFile(path)
		if string(again) != string(first) {
			t.Errorf("run %d changed the bytes", run)
		}
		if n := strings.Count(string(again), "**ModTime:**"); n != 1 {
			t.Errorf("run %d left %d \"**ModTime:**\" lines, want exactly 1", run, n)
		}
	}
}

// TestMigrateTaskHeaderShape_ShadowedSlugIsRefused — apply mode.
//
// The fixture makes the two files differ in title, status, priority AND body, so
// the writer's no-op short-circuit cannot mask a missing guard, and the active
// file is left VALID so it is never itself a repair target.
//
// Break: `if false && taskHeaderShadowed(...)`. This test fails on the ACTIVE
// file's bytes, not merely on a counter.
func TestMigrateTaskHeaderShape_ShadowedSlugIsRefused(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	const activeBody = "# A live task\n\n**Status:** planning\n**Priority:** low\n\n## Context\n\nACTIVE BODY.\n"
	activePath := seedArchivedTask(t, root, "p", "", "shadowed", activeBody)
	archivedPath := seedArchivedTask(t, root, "p", "done", "shadowed", dupPriorityFixture)
	gitCommitAll(t, root)

	var out bytes.Buffer
	sum, err := runTaskHeaderShapeMigration(root, "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Applied != 0 {
		t.Errorf("Applied = %d, want 0 — a shadowed slug must never be written", sum.Applied)
	}
	if sum.Failed != 1 {
		t.Errorf("Failed = %d, want 1 — a shadowed slug is a vault defect needing a human", sum.Failed)
	}
	if got, _ := os.ReadFile(activePath); string(got) != activeBody {
		t.Fatalf("🔴 THE ACTIVE FILE WAS REWRITTEN:\n%s", got)
	}
	if got, _ := os.ReadFile(archivedPath); string(got) != dupPriorityFixture {
		t.Errorf("the ARCHIVED file changed; the guard must refuse, not redirect:\n%s", got)
	}
	// The full rendered location, not just the sentence: the guard's 5th argument
	// is display-only, so a mis-binding still prints a plausible refusal naming a
	// task that does not exist.
	if !strings.Contains(out.String(), "p/done/shadowed") {
		t.Errorf("the refusal does not name the file it refused:\n%s", out.String())
	}
}

// TestMigrateTaskHeaderShape_ShadowedSlugIsRefusedInREPORTMode is the half the
// apply-mode test cannot see: a report that printed FIX and "re-run with --apply"
// for a file apply categorically refuses is the exact lie plan-first prevents.
//
// Break: move the taskHeaderShadowed call inside `if apply {`. This test fails
// and the apply-mode one stays green.
func TestMigrateTaskHeaderShape_ShadowedSlugIsRefusedInREPORTMode(t *testing.T) {
	root := t.TempDir()
	seedArchivedTask(t, root, "p", "", "shadowed",
		"# A live task\n\n**Status:** planning\n**Priority:** low\n\n## Context\n\nACTIVE BODY.\n")
	seedArchivedTask(t, root, "p", "done", "shadowed", dupPriorityFixture)

	var out bytes.Buffer
	sum, err := runTaskHeaderShapeMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	// Scanned is asserted POSITIVELY: an all-negative test passes over an empty
	// corpus, which is what a mistyped --project produces.
	if sum.Scanned != 1 {
		t.Fatalf("Scanned = %d, want 1 — the assertions below are vacuous over an empty corpus", sum.Scanned)
	}
	if sum.Fix != 0 {
		t.Errorf("Fix = %d, want 0 — report counted a file apply refuses as fixable", sum.Fix)
	}
	if strings.Contains(out.String(), "FIX   p/shadowed") {
		t.Errorf("report printed a FIX row for a shadowed slug:\n%s", out.String())
	}
	if sum.Failed != 1 {
		t.Errorf("Failed = %d, want 1 — a shadowed slug is a vault defect in either mode", sum.Failed)
	}
	if strings.Contains(out.String(), "Re-run with --apply") {
		t.Errorf("report told the operator to apply a file apply refuses:\n%s", out.String())
	}
}

// TestMigrateTaskHeaderShape_OtherDefectRosterIsPrinted pins that a malformed file
// which is NOT this command's defect still reaches the operator, with the
// VALIDATOR's own message rather than a cause this command guessed.
//
// Break: delete the `if reason != ""` print. This test fails.
func TestMigrateTaskHeaderShape_OtherDefectRosterIsPrinted(t *testing.T) {
	root := t.TempDir()
	// Missing Status: malformed, but Unit C's defect, not ours.
	seedArchivedTask(t, root, "p", "done", "othersick", "# T\n\n**Priority:** medium\n\n## Context\n\nbody\n")

	var out bytes.Buffer
	sum, err := runTaskHeaderShapeMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.OtherDefect != 1 {
		t.Errorf("OtherDefect = %d, want 1", sum.OtherDefect)
	}
	s := out.String()
	if !strings.Contains(s, "not this command's defect") {
		t.Errorf("the other-defect roster was not printed:\n%s", s)
	}
	if !strings.Contains(s, "missing Status") {
		t.Errorf("the printed reason is not the validator's true cause:\n%s", s)
	}
}

// TestMigrateTaskHeaderShape_SelectionIsExactly is the selection assertion a
// Scanned-count differential cannot make: a walk that sees every file and selects
// the wrong subset passes a cardinality compare.
//
// Break: remove "cancelled" from taskHeaderShapeDirs. This test fails.
func TestMigrateTaskHeaderShape_SelectionIsExactly(t *testing.T) {
	root := t.TempDir()
	seedArchivedTask(t, root, "alpha", "done", "dup-priority", dupPriorityFixture)
	seedArchivedTask(t, root, "alpha", "cancelled", "dup-status", dupStatusFixture)
	seedArchivedTask(t, root, "beta", "done", "already-fine", cleanFixture)
	seedArchivedTask(t, root, "beta", "cancelled", "other-defect", "# T\n\n**Priority:** medium\n\n## Context\n\nbody\n")
	// An ACTIVE file with the same defect must appear in NEITHER walk.
	seedArchivedTask(t, root, "alpha", "", "active-dup", dupPriorityFixture)

	var out bytes.Buffer
	sum, err := runTaskHeaderShapeMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range sum.Decisions {
		if d.Class == shapeNoWork {
			continue
		}
		got["Projects/"+d.Project+"/tasks/"+d.Sub+"/"+d.Slug+".md"] = true
	}
	want := map[string]bool{
		"Projects/alpha/tasks/done/dup-priority.md":    true,
		"Projects/alpha/tasks/cancelled/dup-status.md": true,
	}
	for p := range want {
		if !got[p] {
			t.Errorf("selected set is MISSING %s", p)
		}
	}
	for p := range got {
		if !want[p] {
			t.Errorf("selected set has EXTRA %s", p)
		}
	}
	// Every selected file must be one the whole-file validator actually rejects.
	for _, d := range sum.Decisions {
		if d.Class == shapeNoWork {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(root, "Projects", d.Project, "tasks", d.Sub, d.Slug+".md"))
		if rerr != nil {
			t.Fatal(rerr)
		}
		if storage.ValidateWholeTaskFile(string(b)) == nil {
			t.Errorf("%s was selected but it already validates", d.Slug)
		}
	}
}

// b2BareLegacyFixture is the INSERT+RELOCATE shape: a bare legacy status line
// directly under the title, and no Priority field anywhere.
const b2BareLegacyFixture = "# Plan: Phase D\n" +
	"Status: Closed — operator accepted retrospective 2026-06-06; AC2 carried forward.\n" +
	"\n" +
	"**Status:** retired\n" +
	"**Source:** doc/RESUMPTION-PLAN.md\n" +
	"\n" +
	"## Objective\n" +
	"\n" +
	"body\n"

// TestTaskHeaderShapeSeamTableIsNotAConstant is the assertion that the table is
// doing real work.
//
// A per-class seam map that only ever yields ONE value is an elaborate constant:
// every test over it passes, the exhaustiveness check passes, and nothing would
// notice if the switch collapsed to a single writer. This fails the moment that
// becomes true, so the table has to keep earning its shape.
func TestTaskHeaderShapeSeamTableIsNotAConstant(t *testing.T) {
	seen := map[taskHeaderShapeSeam]int{}
	for _, c := range taskHeaderShapeWritingClasses {
		seen[taskHeaderShapeSeamFor[c]]++
	}
	if seen[seamStrict] == 0 {
		t.Error("no class uses the STRICT seam: strict-wherever-it-suffices is the rule, not the exception")
	}
	if seen[seamPermissive] == 0 {
		t.Error("no class uses the PERMISSIVE seam, so this table is a constant wearing a map's clothes — " +
			"collapse it to a single writer or restore the class that needs the escape hatch")
	}
}

// TestTaskHeaderShapePermissiveSeamIsRequired proves the permissive marking is
// EARNED rather than chosen, the mirror of the strict-seam contract test.
//
// Without this, "seamPermissive" would be an assertion about itself: nothing else
// in the suite distinguishes a class that genuinely needs the escape hatch from
// one that was simply marked for it.
func TestTaskHeaderShapePermissiveSeamIsRequired(t *testing.T) {
	cases := []struct {
		class   taskHeaderShapeClass
		content string
	}{
		{shapeInsertRelocate, b2BareLegacyFixture},
	}
	for _, tc := range cases {
		if taskHeaderShapeSeamFor[tc.class] != seamPermissive {
			t.Fatalf("%s is no longer marked permissive; this test must be re-scoped", tc.class)
		}
		after, class, reason := planTaskHeaderShape(tc.content)
		if class != tc.class {
			t.Fatalf("%s: classified %s (reason %q)", tc.class, class, reason)
		}

		root := t.TempDir()
		gitInitVault(t, root)
		seedArchivedTask(t, root, "p", "done", "x", tc.content)
		gitCommitAll(t, root)
		v := storage.NewVault(root)

		// The STRICT writer must REFUSE this output. If it accepts, the class does
		// not need the escape hatch and must be moved to seamStrict, which is the
		// stronger policy.
		if err := v.OverwriteTaskFile("p", "x", after); err == nil {
			t.Errorf("%s is marked seamPermissive but the STRICT writer ACCEPTED its output; "+
				"move it to seamStrict rather than keeping the weaker policy", tc.class)
		}
		// And the permissive writer must accept it, or the marking is simply wrong.
		if err := v.OverwriteTaskFileRewritingHeader("p", "x", after); err != nil {
			t.Errorf("%s is marked seamPermissive but the PERMISSIVE writer refused its output: %v", tc.class, err)
		}
	}
}

// TestMigrateTaskHeaderShape_InsertRelocateDisarmsTheLegacyHeaderRepair pins the
// cross-command property, which no assertion inside this command's own output can
// see.
//
// Constructing the Priority alone would CLEAR the legacy-header repair's validator
// oracle while leaving the bare line in place — converting a jammed trap into a
// live one, where that repair merges the prose onto the status field and a later
// whole-line rewrite destroys it. Relocating the line is what makes the file
// classify Clean instead.
func TestMigrateTaskHeaderShape_InsertRelocateDisarmsTheLegacyHeaderRepair(t *testing.T) {
	after, class, reason := planTaskHeaderShape(b2BareLegacyFixture)
	if class != shapeInsertRelocate {
		t.Fatalf("classified %s (reason %q)", class, reason)
	}
	if scan := storage.ScanLegacyHeader(after); scan.Class != storage.LegacyHeaderClean {
		t.Errorf("after the repair ScanLegacyHeader reports %s, want Clean — the legacy-header repair "+
			"would still plan a merge on this file", scan.Class)
	}
	if strings.Contains(after, "**Status:** Closed") {
		t.Error("the bare line was merged onto the bold Status field")
	}
}

// TestPlanTaskHeaderShape_WedgedHeaderRunIsRefused pins the deterministic refusal.
//
// 🔴 THE REFUSED OUTPUT WOULD HAVE VALIDATED. A relocation here produces a file
// the whole-file validator accepts while the continuation of one field's value
// sits directly beneath a DIFFERENT field, where it reads as that field's value.
// No validator, audit dimension or existing test reports that, so this refusal is
// the only thing that can.
//
// Break: relocate instead of refusing, at any wedge count. This test fails.
func TestPlanTaskHeaderShape_WedgedHeaderRunIsRefused(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{{
		// n = 1. No structural signal either way: this wedge is in fact the
		// continuation of the Status value above it, and nothing in the bytes says so.
		name:    "one wedge",
		content: "# T\n\n**Status:** retired\nfor `/vpc-execute-plan` pending human sign-off.\n**Priority:** high\n\n## Context\n\nbody\n",
	}, {
		// n = 1, reached only after the extra titles are demoted. The refusal must
		// cite the WEDGE, not report that the demotion "did not work".
		name:    "one wedge behind two titles",
		content: "# T\n\n**Status:** retired\nPlan-reviewed 2026-06-06; design decisions below are locked.\n**Priority:** medium\n\n# T restated\n\n## Problem\n\nbody\n",
	}, {
		// n > 1. Here the COUNT is structural proof that the field values wrap.
		name:    "several wedges",
		content: "# T\n\n**Status:** retired\ncontinuation one.\n**Priority:** Low — a value\ncontinuation two.\n**Filed:** 2026-05-13\ncontinuation three.\n\n## Problem\n\nbody\n",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if storage.ValidateWholeTaskFile(tc.content) == nil {
				t.Fatal("precondition: the fixture must FAIL the validator")
			}
			after, class, reason := planTaskHeaderShape(tc.content)
			if class != shapeNoWork {
				t.Fatalf("a wedged header run was TRANSFORMED (class %s); it must be refused:\n%s", class, after)
			}
			if after != "" {
				t.Error("a refused file must yield no content")
			}
			if !strings.Contains(reason, "prose wedged into the header field run") {
				t.Errorf("refusal cites the wrong cause: %q", reason)
			}
			if !strings.Contains(reason, "hand edit") {
				t.Errorf("the refusal does not tell the operator what to do instead: %q", reason)
			}
		})
	}
}

// TestPlanTaskHeaderShape_CleanDemotionStillRepairs is the negative that stops the
// refusal above from swallowing the class it shares an arm with.
//
// Break: refuse every two-title file. This test fails.
func TestPlanTaskHeaderShape_CleanDemotionStillRepairs(t *testing.T) {
	const content = "# First title\n\n**Status:** retired\n**Priority:** medium\n\n# Second wording\n\n## Problem\n\nbody\n"
	after, class, reason := planTaskHeaderShape(content)
	if class != shapeDupTitle {
		t.Fatalf("class = %s, want shapeDupTitle (reason %q)", class, reason)
	}
	if storage.ValidateWholeTaskFile(after) != nil {
		t.Error("the demoted file does not validate")
	}
}

// TestMigrateTaskHeaderShape_PartialRepairIsNeverWritten is the ALL-OR-NOTHING
// proof, and it asserts on the BYTES ON DISK rather than on a planner outcome.
//
// 🔴 THE FAILURE IT GUARDS IS WORSE THAN EITHER ALTERNATIVE. `vault-write-concurrency`
// needs two things: its extra title demoted, AND a prose wedge resolved. The wedge
// half is REFUSED by rule, because nothing in the bytes distinguishes a wedge from
// a wrapped value's continuation. If the command applied the demotion anyway, the
// file would be half repaired by the command and half expected by hand — and
// NEITHER list would own the whole file. The command's roster would show it
// repaired; the hand-edit list would describe a file that no longer looks like the
// one it describes.
//
// A clean refusal and a clean repair are both recoverable. A silent half is not.
//
// Break: in the two-title arm, return the demoted content instead of refusing when
// a wedge remains. This test fails on the file's bytes.
func TestMigrateTaskHeaderShape_PartialRepairIsNeverWritten(t *testing.T) {
	// Both defects at once: two unfenced H1s, and prose wedged into the field run.
	const bothDefects = "# T\n" +
		"\n" +
		"**Status:** retired\n" +
		"Plan-reviewed 2026-06-06; design decisions below are locked.\n" +
		"**Priority:** medium\n" +
		"\n" +
		"# T restated\n" +
		"\n" +
		"## Problem\n" +
		"\n" +
		"body\n"
	if storage.ValidateWholeTaskFile(bothDefects) == nil {
		t.Fatal("precondition: the fixture must FAIL the validator")
	}

	root := t.TempDir()
	gitInitVault(t, root)
	path := seedArchivedTask(t, root, "p", "done", "both", bothDefects)
	gitCommitAll(t, root)

	var out bytes.Buffer
	sum, err := runTaskHeaderShapeMigration(root, "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Applied != 0 {
		t.Errorf("Applied = %d, want 0 — a file needing a refused half must not be partially repaired", sum.Applied)
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != bothDefects {
		t.Fatalf("🔴 THE FILE WAS PARTIALLY REPAIRED. It is now owned by neither the command's roster "+
			"nor the hand-edit list:\n%s", got)
	}
	// And it must still be VISIBLE, on the other-defect roster, with the true cause.
	if sum.OtherDefect != 1 {
		t.Errorf("OtherDefect = %d, want 1 — a refused file that appears on no roster is invisible", sum.OtherDefect)
	}
	if !strings.Contains(out.String(), "prose wedged into the header field run") {
		t.Errorf("the roster does not name the blocking cause:\n%s", out.String())
	}
}

// TestTaskHeaderShapeSeamRefusesAPartialRepair pins ALL-OR-NOTHING where it
// actually lives: in the locked writer, not in this command.
//
// 🔴 ESTABLISHED BY SUCCESSIVE REMOVAL, NOT BY READING. With the wedge refusal
// removed, the file's bytes were still unchanged. With the wedge refusal AND the
// planner's post-condition both removed, the write reached the seam and the seam
// refused it: "write: malformed header block: the \"**Priority:**\" line is not
// part of the contiguous header block after the title".
//
// So the three layers do different jobs, and only the last is a guarantee:
//
//	wedge refusal            names the TRUE CAUSE on the other-defect roster
//	planner post-condition   keeps a known-bad transform away from the writer
//	the locked writer        REFUSES the write — this is what makes it all-or-nothing
//
// That ordering matters for where future work may safely economise: the first two
// are diagnostic quality and can be re-shaped; removing the third would be
// removing the guarantee. A guard that lives in the layer every caller goes
// through is the only kind that holds for callers nobody has written yet.
func TestTaskHeaderShapeSeamRefusesAPartialRepair(t *testing.T) {
	// A half-repaired file: the extra title demoted, the prose wedge left in place.
	const halfRepaired = "# T\n" +
		"\n" +
		"**Status:** retired\n" +
		"Plan-reviewed 2026-06-06; design decisions below are locked.\n" +
		"**Priority:** medium\n" +
		"\n" +
		"## T restated\n" +
		"\n" +
		"## Problem\n" +
		"\n" +
		"body\n"
	if storage.ValidateWholeTaskFile(halfRepaired) == nil {
		t.Fatal("precondition: a half-repaired file must still FAIL the validator, or there is nothing to refuse")
	}

	root := t.TempDir()
	gitInitVault(t, root)
	const seeded = "# T\n\n**Status:** retired\n**Priority:** medium\n\n## Problem\n\nbody\n"
	path := seedArchivedTask(t, root, "p", "done", "x", seeded)
	gitCommitAll(t, root)

	err := storage.NewVault(root).OverwriteTaskFile("p", "x", halfRepaired)
	if err == nil {
		t.Fatal("the locked writer ACCEPTED a half-repaired file; all-or-nothing rests on this refusal")
	}
	if !strings.Contains(err.Error(), "malformed header block") {
		t.Errorf("the seam refused for the wrong reason: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != seeded {
		t.Error("the file changed despite the refusal")
	}
}

// ---------------------------------------------------------------------------
// The ARCHIVED pair. The two tests above seed an ACTIVE twin, which is the
// shape taskHeaderShadowWinner covered BEFORE it was widened, so both stay green
// on a guard narrowed back to active-only. The pair below is the shape the
// widening exists for, and one machine cannot build it: machine A retires slug
// X, machine B cancels slug X, and git merges two files at different paths with
// no conflict.
// ---------------------------------------------------------------------------

// TestMigrateTaskHeaderShape_ArchivedPairIsRefused pins the done/+cancelled/
// pair with NO active twin.
//
// done/ holds a file that needs nothing, so the ONLY thing that can refuse the
// cancelled/ copy is the guard walking past the active directory into done/.
//
// Break: narrow taskHeaderShadowDirs to []string{""}. This test fails; the two
// active-twin tests above stay green, which is exactly why it has to exist.
func TestMigrateTaskHeaderShape_ArchivedPairIsRefused(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	donePath := seedArchivedTask(t, root, "p", "done", "paired", cleanFixture)
	cancelledPath := seedArchivedTask(t, root, "p", "cancelled", "paired", dupPriorityFixture)
	gitCommitAll(t, root)

	var out bytes.Buffer
	sum, err := runTaskHeaderShapeMigration(root, "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	// Positive first: an all-negative test passes over an empty corpus.
	if sum.Scanned != 2 {
		t.Fatalf("Scanned = %d, want 2 — the assertions below are vacuous over an empty corpus", sum.Scanned)
	}
	if sum.Applied != 0 {
		t.Errorf("Applied = %d, want 0 — an archived pair must never be written", sum.Applied)
	}
	if sum.Fix != 0 {
		t.Errorf("Fix = %d, want 0 — the cancelled/ copy was counted fixable", sum.Fix)
	}
	if sum.Failed != 1 {
		t.Errorf("Failed = %d, want 1 — an archived pair is a vault defect needing a human", sum.Failed)
	}
	// The bytes, not only the counters: a guard that refused the wrong half would
	// satisfy every count above while rewriting done/.
	if got, _ := os.ReadFile(donePath); string(got) != cleanFixture {
		t.Fatalf("🔴 THE done/ FILE WAS REWRITTEN — the writer resolved done/ while the command read cancelled/:\n%s", got)
	}
	if got, _ := os.ReadFile(cancelledPath); string(got) != dupPriorityFixture {
		t.Errorf("the cancelled/ file changed; the guard must refuse, not redirect:\n%s", got)
	}
	// The winner is named, not merely "a conflict": an operator who is not told
	// WHICH directory wins cannot tell this apart from an active twin, and the
	// two need different repairs.
	if !strings.Contains(out.String(), "the same slug also exists in tasks/done/") {
		t.Errorf("the refusal does not name done/ as the winning directory:\n%s", out.String())
	}
}

// TestMigrateTaskHeaderShape_ArchivedPairReasonNamesTheRealWinner pins the
// STORED reason, not stdout.
//
// The main-resident sibling of this bug shipped precisely because only the
// printed line was asserted: the line was correct and the Reason recorded on the
// decision said "an ACTIVE task of the same slug exists", which is false for a
// done/+cancelled/ pair. Whoever consumes the roster reads the Reason.
//
// Break: restore the literal at the call site
// (`d.Reason = "a conflicting task of the same slug exists; the writer resolves by slug"`).
// This test fails; the one above stays green.
func TestMigrateTaskHeaderShape_ArchivedPairReasonNamesTheRealWinner(t *testing.T) {
	root := t.TempDir()
	seedArchivedTask(t, root, "p", "done", "paired", cleanFixture)
	seedArchivedTask(t, root, "p", "cancelled", "paired", dupPriorityFixture)

	var out bytes.Buffer
	sum, err := runTaskHeaderShapeMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	var refused []taskHeaderShapeDecision
	for _, d := range sum.Decisions {
		if d.Failed {
			refused = append(refused, d)
		}
	}
	if len(refused) != 1 {
		t.Fatalf("refused decisions = %d, want 1", len(refused))
	}
	got := refused[0].Reason
	if !strings.Contains(got, "tasks/done/") {
		t.Errorf("stored Reason does not name the winning directory:\n  got %q", got)
	}
	if strings.Contains(got, "ACTIVE") {
		t.Errorf("stored Reason claims an ACTIVE twin, and there is none:\n  got %q", got)
	}
	// The stored reason and the printed line come from one walk, so they must
	// agree on the WINNER — the one fact a literal silently drops. They are not
	// byte-identical (the printed form interpolates "refusing (...)"), so pin the
	// winner clause they share rather than asserting containment of the whole.
	const winnerClause = "the same slug also exists in tasks/done/;"
	if !strings.Contains(got, winnerClause) || !strings.Contains(out.String(), winnerClause) {
		t.Errorf("stored Reason and printed line disagree about the winning directory:\n  stored %q\n  printed %s",
			got, out.String())
	}
}

// TestMigrateTaskHeaderShape_CleanArchivedPairIsNotRefused pins the guard's
// POSITION against the needs-nothing escape.
//
// The guard is correct only where it sits. Moved ahead of the `class ==
// shapeNoWork` escape it refuses files that need nothing, citing a cause that
// blocks nothing — the defect class this project has now shipped twice. Nothing
// else in the tree fails on that move: `go vet ./...` and `go test ./...` both
// stay green, which is what this test is for.
//
// Break: move the taskHeaderShadowed call above the `if class == shapeNoWork`
// block. This test fails; every other test in the package stays green.
func TestMigrateTaskHeaderShape_CleanArchivedPairIsNotRefused(t *testing.T) {
	root := t.TempDir()
	seedArchivedTask(t, root, "p", "done", "twinned", cleanFixture)
	seedArchivedTask(t, root, "p", "cancelled", "twinned", cleanFixture)

	var out bytes.Buffer
	sum, err := runTaskHeaderShapeMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Scanned != 2 {
		t.Fatalf("Scanned = %d, want 2 — the assertions below are vacuous over an empty corpus", sum.Scanned)
	}
	if sum.NoWork != 2 {
		t.Errorf("NoWork = %d, want 2 — both halves of the pair need nothing", sum.NoWork)
	}
	if sum.Failed != 0 {
		t.Errorf("Failed = %d, want 0 — a file that needs nothing was refused", sum.Failed)
	}
	if strings.Contains(out.String(), "refusing") {
		t.Errorf("a file that needs nothing was refused, with a cause that blocks nothing:\n%s", out.String())
	}
	for _, d := range sum.Decisions {
		if d.Failed || d.Reason != "" {
			t.Errorf("decision for %s/%s carries a reason for a file that needs nothing: %q",
				d.Sub, d.Slug, d.Reason)
		}
	}
}
