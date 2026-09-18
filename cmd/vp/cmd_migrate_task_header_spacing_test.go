// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// modTimeStampFor builds the "**ModTime:** <today>" line every
// OverwriteTaskFileRewritingHeader call now appends (board-reporting-createtime-
// modtime-fields, ModTime is bucket 3/server-derived and is force-restamped on
// EVERY overwrite, including this migration's own repair write). The date is
// read from the real clock — these tests never inject one — so it is computed
// here rather than hardcoded.
func modTimeStampFor(t *testing.T) string {
	t.Helper()
	return "**ModTime:** " + storage.CalendarDay(time.Now())
}

// ---------------------------------------------------------------------------
// applyHeaderSpacingFix — pure function, no vault, no files.
//
// Targets applyHeaderSpacingFix's ACTUAL scope: a plumbing/staleness guard,
// not a substitute for storage.FindHeaderSpacingHazard's own line-number
// tests (internal/storage/tasks_test.go).
// ---------------------------------------------------------------------------

const hazardFixture = "# T\n\n**Status:** pending\n**Priority:** high\n**Depends:** dep\n**Note:** orphaned\n\n## Context\n\nBody.\n"

func TestApplyHeaderSpacingFixInsertsAtReportedLine(t *testing.T) {
	repaired, err := applyHeaderSpacingFix(hazardFixture, 6, "**Note:** orphaned")
	if err != nil {
		t.Fatalf("applyHeaderSpacingFix: %v", err)
	}
	want := "# T\n\n**Status:** pending\n**Priority:** high\n**Depends:** dep\n\n**Note:** orphaned\n\n## Context\n\nBody.\n"
	if repaired != want {
		t.Errorf("repaired content:\n%q\nwant:\n%q", repaired, want)
	}
}

// TestApplyHeaderSpacingFixRefusesWhenTextMismatches simulates a stale report
// or a caller-side plumbing bug — an externally introduced disagreement
// between the reported text and the file, not a self-consistent bug inside
// storage.FindHeaderSpacingHazard itself, which this check cannot detect.
func TestApplyHeaderSpacingFixRefusesWhenTextMismatches(t *testing.T) {
	_, err := applyHeaderSpacingFix(hazardFixture, 6, "**Note:** something else entirely")
	if err == nil {
		t.Fatal("expected a refusal when the reported text does not match the file")
	}
}

func TestApplyHeaderSpacingFixRefusesWhenLineIsBlank(t *testing.T) {
	// Line 2 (1-based) of hazardFixture is the blank line after the title.
	_, err := applyHeaderSpacingFix(hazardFixture, 2, "")
	if err == nil {
		t.Fatal("expected a refusal when the reported line is blank")
	}
}

func TestApplyHeaderSpacingFixRefusesWhenPrecedingLineAlreadyBlank(t *testing.T) {
	// Line 3 (1-based) is "**Status:** pending", immediately preceded by the
	// blank line after the title — there is no missing separator here.
	_, err := applyHeaderSpacingFix(hazardFixture, 3, "**Status:** pending")
	if err == nil {
		t.Fatal("expected a refusal when the line immediately before the reported position is already blank")
	}
}

func TestApplyHeaderSpacingFixRefusesWhenLineOutOfRange(t *testing.T) {
	lines := strings.Count(hazardFixture, "\n") + 1
	_, err := applyHeaderSpacingFix(hazardFixture, lines+10, "**Note:** orphaned")
	if err == nil {
		t.Fatal("expected a refusal when the reported line is out of range")
	}
}

func TestApplyHeaderSpacingFixStructuralDiffIsExactlyOneLine(t *testing.T) {
	repaired, err := applyHeaderSpacingFix(hazardFixture, 6, "**Note:** orphaned")
	if err != nil {
		t.Fatalf("applyHeaderSpacingFix: %v", err)
	}
	before := strings.Split(hazardFixture, "\n")
	after := strings.Split(repaired, "\n")
	if len(after) != len(before)+1 {
		t.Fatalf("line count = %d, want %d", len(after), len(before)+1)
	}
	if after[5] != "" {
		t.Errorf("inserted line (index 5) = %q, want blank", after[5])
	}
	for i := 0; i < 5; i++ {
		if after[i] != before[i] {
			t.Errorf("line %d changed ahead of the insertion point: %q != %q", i, after[i], before[i])
		}
	}
	for i := 5; i < len(before); i++ {
		if after[i+1] != before[i] {
			t.Errorf("line %d changed after the insertion point: %q != %q", i, after[i+1], before[i])
		}
	}
}

// ---------------------------------------------------------------------------
// vp migrate task-header-spacing — command layer.
//
// EPHEMERAL synthetic vaults under t.TempDir() via tsVault/tsWrite/tsRead/
// tsGitInit, exactly like cmd_migrate_task_status_test.go. No test here
// touches the operator's real vault.
// ---------------------------------------------------------------------------

func TestMigrateTaskHeaderSpacingReportOnlyWritesNothing(t *testing.T) {
	root := tsVault(t)
	p := tsWrite(t, root, "Projects/proj/tasks/hazard.md", hazardFixture)

	var out bytes.Buffer
	sum, err := runTaskHeaderSpacingMigration(root, "proj", false, &out)
	if err != nil {
		t.Fatalf("report run: %v", err)
	}
	if sum.Fix != 1 || sum.Applied != 0 {
		t.Fatalf("Fix = %d, Applied = %d, want 1 and 0", sum.Fix, sum.Applied)
	}
	if got := tsRead(t, p); got != hazardFixture {
		t.Errorf("a report-only run modified the file\n got: %q\nwant: %q", got, hazardFixture)
	}
	if !strings.Contains(out.String(), "**Note:** orphaned") {
		t.Errorf("report must name the exact candidate text, got:\n%s", out.String())
	}
}

func TestMigrateTaskHeaderSpacingApplyInsertsExactlyOneBlankLine(t *testing.T) {
	root := tsVault(t)
	p := tsWrite(t, root, "Projects/proj/tasks/hazard.md", hazardFixture)
	tsGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskHeaderSpacingMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if sum.Applied != 1 || sum.Failed != 0 {
		t.Fatalf("Applied = %d, Failed = %d, want 1 and 0; out:\n%s", sum.Applied, sum.Failed, out.String())
	}

	// OverwriteTaskFileRewritingHeader force-restamps ModTime unconditionally
	// (board-reporting-createtime-modtime-fields, bucket 3), landing right
	// after Depends — the last core field this migration's own repair write
	// runs through the same shared overwriteTaskFile body as every other
	// overwrite.
	want := "# T\n\n**Status:** pending\n**Priority:** high\n**Depends:** dep\n" +
		modTimeStampFor(t) + "\n\n**Note:** orphaned\n\n## Context\n\nBody.\n"
	if got := tsRead(t, p); got != want {
		t.Errorf("repaired file:\n%q\nwant:\n%q", got, want)
	}
}

func TestMigrateTaskHeaderSpacingReachesArchivedFiles(t *testing.T) {
	root := tsVault(t)
	donePath := tsWrite(t, root, "Projects/proj/tasks/done/hazard-done.md", hazardFixture)
	cancelledPath := tsWrite(t, root, "Projects/proj/tasks/cancelled/hazard-cancelled.md", hazardFixture)
	tsGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskHeaderSpacingMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if sum.Applied != 2 || sum.Failed != 0 {
		t.Fatalf("Applied = %d, Failed = %d, want 2 and 0; out:\n%s", sum.Applied, sum.Failed, out.String())
	}

	want := "# T\n\n**Status:** pending\n**Priority:** high\n**Depends:** dep\n" +
		modTimeStampFor(t) + "\n\n**Note:** orphaned\n\n## Context\n\nBody.\n"
	if got := tsRead(t, donePath); got != want {
		t.Errorf("done/ file not repaired:\n%q", got)
	}
	if got := tsRead(t, cancelledPath); got != want {
		t.Errorf("cancelled/ file not repaired:\n%q", got)
	}
}

func TestMigrateTaskHeaderSpacingIdempotentOnSecondRun(t *testing.T) {
	root := tsVault(t)
	tsWrite(t, root, "Projects/proj/tasks/hazard.md", hazardFixture)
	tsGitInit(t, root)

	var out1 bytes.Buffer
	if _, err := runTaskHeaderSpacingMigration(root, "proj", true, &out1); err != nil {
		t.Fatalf("first apply run: %v", err)
	}

	var out2 bytes.Buffer
	sum2, err := runTaskHeaderSpacingMigration(root, "proj", false, &out2)
	if err != nil {
		t.Fatalf("second (report) run: %v", err)
	}
	// Post board-reporting-header-spacing-schema-aware-fix: the ModTime line
	// the first run's own restamp produces is a known extension field and is
	// never itself flagged, so this converges to zero hazards, restoring the
	// original pre-regression guarantee.
	if sum2.Fix != 0 {
		t.Errorf("second run found %d hazard(s), want 0", sum2.Fix)
	}
}

func TestMigrateTaskHeaderSpacingDefaultsToEveryProject(t *testing.T) {
	root := tsVault(t)
	// A second project, seeded by hand — tsVault only builds "proj".
	for _, d := range []string{"tasks", "tasks/done", "tasks/cancelled"} {
		if err := os.MkdirAll(filepath.Join(root, "Projects", "second", d), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	pOne := tsWrite(t, root, "Projects/proj/tasks/hazard.md", hazardFixture)
	pTwo := tsWrite(t, root, "Projects/second/tasks/hazard.md", hazardFixture)
	tsGitInit(t, root)

	var out bytes.Buffer
	// No --project narrowing: only == "".
	sum, err := runTaskHeaderSpacingMigration(root, "", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if sum.Applied != 2 || sum.Failed != 0 {
		t.Fatalf("Applied = %d, Failed = %d, want 2 and 0 (both projects); out:\n%s", sum.Applied, sum.Failed, out.String())
	}

	want := "# T\n\n**Status:** pending\n**Priority:** high\n**Depends:** dep\n" +
		modTimeStampFor(t) + "\n\n**Note:** orphaned\n\n## Context\n\nBody.\n"
	if got := tsRead(t, pOne); got != want {
		t.Errorf("proj's file not repaired:\n%q", got)
	}
	if got := tsRead(t, pTwo); got != want {
		t.Errorf("second's file not repaired — default scope did not reach every project:\n%q", got)
	}
}

func TestMigrateTaskHeaderSpacingLeavesCleanFilesUntouched(t *testing.T) {
	root := tsVault(t)
	clean := "# T\n\n**Status:** pending\n**Priority:** high\n\n## Context\n\nBody.\n"
	p := tsWrite(t, root, "Projects/proj/tasks/clean.md", clean)
	tsGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskHeaderSpacingMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if sum.Fix != 0 || sum.Applied != 0 {
		t.Fatalf("Fix = %d, Applied = %d, want 0 and 0", sum.Fix, sum.Applied)
	}
	if got := tsRead(t, p); got != clean {
		t.Errorf("a clean file was modified\n got: %q\nwant: %q", got, clean)
	}
}

func TestMigrateTaskHeaderSpacingModTimeNeverStacksAcrossRepeatedApply(t *testing.T) {
	root := tsVault(t)
	// Deliberately has BOTH a genuine legacy hazard (orphaned "**Note:**",
	// unknown field name — still must be fixed) AND starts with no ModTime,
	// so the first run's own repair write triggers OverwriteTaskFileRewritingHeader's
	// unconditional restamp — the exact interaction that used to cascade.
	p := tsWrite(t, root, "Projects/proj/tasks/hazard.md", hazardFixture)
	tsGitInit(t, root)

	var out1 bytes.Buffer
	sum1, err := runTaskHeaderSpacingMigration(root, "proj", true, &out1)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if sum1.Applied != 1 || sum1.Failed != 0 {
		t.Fatalf("run 1: Applied=%d Failed=%d, want 1 and 0", sum1.Applied, sum1.Failed)
	}
	afterRun1 := tsRead(t, p)
	if n := strings.Count(afterRun1, "**ModTime:**"); n != 1 {
		t.Fatalf("after run 1: %d ModTime line(s), want exactly 1:\n%s", n, afterRun1)
	}

	for i, run := range []int{2, 3} {
		_ = i
		var out bytes.Buffer
		sum, err := runTaskHeaderSpacingMigration(root, "proj", true, &out)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if sum.Fix != 0 || sum.Applied != 0 {
			t.Errorf("run %d: Fix=%d Applied=%d, want 0 and 0 (the fix must never re-trip on its own ModTime restamp)", run, sum.Fix, sum.Applied)
		}
		got := tsRead(t, p)
		if got != afterRun1 {
			t.Errorf("run %d: content changed on a no-op run:\n%q\nwant (unchanged from after run 1):\n%q", run, got, afterRun1)
		}
		if n := strings.Count(got, "**ModTime:**"); n != 1 {
			t.Errorf("run %d: %d ModTime line(s), want exactly 1 (the exact stacking regression this test pins)", run, n)
		}
	}
}

// TestMigrateTaskHeaderSpacingAlreadyMigratedFileIsUntouched covers the
// simpler companion shape: a file with NO hazard at all, ModTime already
// legitimately populated adjacent to Depends (exactly what every task file
// created after board-reporting-createtime-modtime-fields looks like).
// Before this fix, this shape alone was enough to trip the false positive on
// the very first run, with no orphaned-Note hazard needed at all.
func TestMigrateTaskHeaderSpacingAlreadyMigratedFileIsUntouched(t *testing.T) {
	root := tsVault(t)
	clean := "# T\n\n**Status:** planning\n**Priority:** high\n**Depends:** dep\n**ModTime:** 2026-01-01\n\n## Context\n\nBody.\n"
	p := tsWrite(t, root, "Projects/proj/tasks/clean.md", clean)
	tsGitInit(t, root)

	for run := 1; run <= 3; run++ {
		var out bytes.Buffer
		sum, err := runTaskHeaderSpacingMigration(root, "proj", true, &out)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if sum.Fix != 0 || sum.Applied != 0 {
			t.Errorf("run %d: Fix=%d Applied=%d, want 0 and 0", run, sum.Fix, sum.Applied)
		}
		if got := tsRead(t, p); got != clean {
			t.Errorf("run %d: file changed from its original, already-correct content:\n%q", run, got)
		}
	}
}

// ---------------------------------------------------------------------------
// cmdMigrateTaskHeaderSpacing — the CLI dispatch wrapper (flags, exit codes).
// ---------------------------------------------------------------------------

func TestMigrateTaskHeaderSpacingCommandRejectsUnknownFlag(t *testing.T) {
	var code int
	out := captureStdout(t, func() {
		code = cmdMigrateTaskHeaderSpacing().Run([]string{"--nope"})
	})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser (%d); out:\n%s", code, cli.ExitUser, out)
	}
}

func TestMigrateTaskHeaderSpacingCommandReportRun(t *testing.T) {
	root := tsVault(t)
	tsWrite(t, root, "Projects/proj/tasks/hazard.md", hazardFixture)

	var code int
	out := captureStdout(t, func() {
		code = cmdMigrateTaskHeaderSpacing().Run([]string{"--vault", root, "--project", "proj"})
	})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; out:\n%s", code, out)
	}
	if !strings.Contains(out, "**Note:** orphaned") {
		t.Errorf("report must name the hazard; out:\n%s", out)
	}
}

func TestMigrateTaskHeaderSpacingCommandApplySucceeds(t *testing.T) {
	root := tsVault(t)
	p := tsWrite(t, root, "Projects/proj/tasks/hazard.md", hazardFixture)
	tsGitInit(t, root)

	var code int
	out := captureStdout(t, func() {
		code = cmdMigrateTaskHeaderSpacing().Run([]string{"--vault", root, "--project", "proj", "--apply"})
	})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want ExitOK; out:\n%s", code, out)
	}
	want := "# T\n\n**Status:** pending\n**Priority:** high\n**Depends:** dep\n" +
		modTimeStampFor(t) + "\n\n**Note:** orphaned\n\n## Context\n\nBody.\n"
	if got := tsRead(t, p); got != want {
		t.Errorf("repaired file:\n%q\nwant:\n%q", got, want)
	}
}

// TestMigrateTaskHeaderSpacingCommandExitsNonZeroWithoutGit pins the same
// "apply requires a recoverable vault" precondition every sibling migrate
// command carries: --apply against a non-git vault must fail loudly, not
// silently no-op.
func TestMigrateTaskHeaderSpacingCommandExitsNonZeroWithoutGit(t *testing.T) {
	root := tsVault(t)
	tsWrite(t, root, "Projects/proj/tasks/hazard.md", hazardFixture)
	// Deliberately no tsGitInit: the vault is not a git repo.

	var code int
	out := captureStdout(t, func() {
		code = cmdMigrateTaskHeaderSpacing().Run([]string{"--vault", root, "--project", "proj", "--apply"})
	})
	if code == cli.ExitOK {
		t.Errorf("exit code = %d (OK) for --apply against a non-git vault, want a failure; out:\n%s", code, out)
	}
}

// ---------------------------------------------------------------------------
// The shadow guard.
//
// This command reads a task by PATH and writes it back by SLUG, and
// Vault.resolveTaskFile searches active, then done/, then cancelled/, returning
// the FIRST hit. Without a guard, repairing a file in a LATER directory writes
// the repaired bytes over the file in the EARLIER one — destroying it, leaving
// the hazard unrepaired, and exiting 0.
//
// 🔴 EVERY FIXTURE BELOW GIVES THE PROJECT AND THE TASK SLUG DISTINCT NAMES, AND
// EVERY TEST ASSERTS THE WHOLE REFUSAL LINE RATHER THAN A COUNTER. Both are
// deliberate and both replace weaker checks:
//
//   - sum.Failed == 1 is NOT discriminating. A read error, a write error and a
//     validator refusal each satisfy it with no guard involved, so a test that
//     keys its refusal evidence on that counter cannot tell "the guard fired"
//     from "something else broke". The rendered line is the primary evidence
//     here and the counter is support.
//   - taskHeaderShadowed takes (project, sub, slug) while THIS command's loop
//     variable named `slug` is the PROJECT and the task's slug is `taskSlug`. A
//     transposed call compiles. With project == slug the rendered location is
//     symmetric and a string assertion cannot see the transposition; with
//     distinct names it renders "beta-task/done/alpha" and the assertion fails.
//
// 🔴 THE PERMISSIVE SEAM IS WHY THESE FIXTURES MAY DIFFER FROM EACH OTHER, AND
// THAT IS THE OPPOSITE OF THE SIBLING'S SITUATION. cmd_migrate_task_sections.go
// uses the STRICT seam, so its shadow test must give the colliding files
// byte-identical headers or refuseHeaderChange refuses the write on its own and
// the test passes with the guard deleted. This command uses
// OverwriteTaskFileRewritingHeader, so no header compare runs and mismatched
// headers cannot stand in for the guard. If this command is ever switched to the
// strict seam, every test below goes vacuous and the fixtures must be made
// header-identical.
// ---------------------------------------------------------------------------

// shadowVictim is the file the resolver would WRONGLY write: hazard-free (so it
// is counted Clean and is never itself a repair target) and different from
// hazardFixture in title, status, priority and body, so the writer's
// identical-content no-op cannot stand in for the guard either.
const shadowVictim = "# The real file\n\n**Status:** in_progress\n**Priority:** low\n\n## Context\n\nMust not be overwritten.\n"

func TestMigrateTaskHeaderSpacing_ShadowedSlugIsRefused(t *testing.T) {
	root := tsVault(t)
	active := tsWrite(t, root, "Projects/alpha/tasks/beta-task.md", shadowVictim)
	archived := tsWrite(t, root, "Projects/alpha/tasks/done/beta-task.md", hazardFixture)
	tsGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskHeaderSpacingMigration(root, "alpha", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}

	wantLine := "  !!    alpha/done/beta-task: an ACTIVE task of the same slug exists; " +
		"refusing (the writer resolves active first)\n"
	if !strings.Contains(out.String(), wantLine) {
		t.Errorf("the refusal line is missing or reworded.\nwant: %q\nout:\n%s", wantLine, out.String())
	}
	if got := tsRead(t, active); got != shadowVictim {
		t.Fatalf("🔴 THE ACTIVE FILE WAS REWRITTEN WITH THE ARCHIVED BODY:\n%s", got)
	}
	if got := tsRead(t, archived); got != hazardFixture {
		t.Errorf("the archived file was modified despite the refusal; the run refused, it did not redirect:\n%s", got)
	}
	if sum.Applied != 0 {
		t.Errorf("Applied = %d, want 0 — a shadowed slug must never be written", sum.Applied)
	}
	if sum.Failed != 1 {
		t.Errorf("Failed = %d, want 1 — a shadowed slug is a vault defect needing a human", sum.Failed)
	}
}

func TestMigrateTaskHeaderSpacing_ShadowedSlugIsRefusedInREPORTMode(t *testing.T) {
	root := tsVault(t)
	active := tsWrite(t, root, "Projects/alpha/tasks/beta-task.md", shadowVictim)
	tsWrite(t, root, "Projects/alpha/tasks/done/beta-task.md", hazardFixture)

	var out bytes.Buffer
	sum, err := runTaskHeaderSpacingMigration(root, "alpha", false, &out)
	if err != nil {
		t.Fatalf("report run: %v", err)
	}
	s := out.String()

	wantLine := "  !!    alpha/done/beta-task: an ACTIVE task of the same slug exists; " +
		"refusing (the writer resolves active first)\n"
	if !strings.Contains(s, wantLine) {
		t.Errorf("report did not surface the refusal.\nwant: %q\nout:\n%s", wantLine, s)
	}
	if strings.Contains(s, "FIX   alpha/beta-task") {
		t.Errorf("report printed FIX for a shadowed slug apply will refuse:\n%s", s)
	}
	if strings.Contains(s, "Re-run with --apply") {
		t.Errorf("report told the operator to apply a run with nothing appliable:\n%s", s)
	}
	if sum.Fix != 0 {
		t.Errorf("Fix = %d, want 0 — report counted a file apply refuses as fixable", sum.Fix)
	}
	if sum.Failed != 1 {
		t.Errorf("Failed = %d, want 1 — a shadowed slug is a vault defect in either mode", sum.Failed)
	}
	if got := tsRead(t, active); got != shadowVictim {
		t.Errorf("report mode wrote to the active file:\n%s", got)
	}
}

// TestMigrateTaskHeaderSpacing_ArchivedPairIsRefused covers the shape the
// original active-only guard missed entirely: a slug in BOTH done/ and
// cancelled/ with NO active twin. resolveTaskFile returns the done/ copy, so
// repairing the cancelled/ one writes over done/.
//
// 🔴 "THE ACTIVE FILE IS UNCHANGED" IS VACUOUS HERE — THERE IS NO ACTIVE FILE.
// The bytes that must not change are the done/ copy's, because done/ is what the
// resolver picks. Keying this test on the word "active" would assert nothing.
//
// The state is not constructible by one machine but is reachable by MERGE:
// machine A retires the slug, machine B cancels it, git merges two files at
// different paths with no conflict.
//
// Break: restore the old early-out `if sub == "" || !fileExists(<active>)` in
// taskHeaderShadowed. This test fails; the two above stay green.
func TestMigrateTaskHeaderSpacing_ArchivedPairIsRefused(t *testing.T) {
	for _, apply := range []bool{false, true} {
		name := "report"
		if apply {
			name = "apply"
		}
		t.Run(name, func(t *testing.T) {
			root := tsVault(t)
			done := tsWrite(t, root, "Projects/alpha/tasks/done/beta-task.md", shadowVictim)
			cancelled := tsWrite(t, root, "Projects/alpha/tasks/cancelled/beta-task.md", hazardFixture)
			if apply {
				tsGitInit(t, root)
			}

			var out bytes.Buffer
			sum, err := runTaskHeaderSpacingMigration(root, "alpha", apply, &out)
			if err != nil {
				t.Fatalf("run: %v", err)
			}

			wantLine := "  !!    alpha/cancelled/beta-task: the same slug also exists in tasks/done/; " +
				"refusing (the writer resolves done before cancelled)\n"
			if !strings.Contains(out.String(), wantLine) {
				t.Errorf("the archived-pair refusal is missing or reworded.\nwant: %q\nout:\n%s",
					wantLine, out.String())
			}
			// done/ is what resolveTaskFile picks, so done/ is the file an
			// unguarded run destroys. This is the assertion that matters here.
			if got := tsRead(t, done); got != shadowVictim {
				t.Fatalf("🔴 THE done/ FILE WAS REWRITTEN WITH THE cancelled/ BODY:\n%s", got)
			}
			if got := tsRead(t, cancelled); got != hazardFixture {
				t.Errorf("the cancelled/ file was modified despite the refusal:\n%s", got)
			}
			if sum.Applied != 0 || sum.Fix != 0 {
				t.Errorf("Applied = %d, Fix = %d, want 0 and 0", sum.Applied, sum.Fix)
			}
			if sum.Failed != 1 {
				t.Errorf("Failed = %d, want 1", sum.Failed)
			}
		})
	}
}
