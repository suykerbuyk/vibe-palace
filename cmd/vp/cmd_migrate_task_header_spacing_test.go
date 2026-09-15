// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

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

	want := "# T\n\n**Status:** pending\n**Priority:** high\n**Depends:** dep\n\n**Note:** orphaned\n\n## Context\n\nBody.\n"
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

	want := "# T\n\n**Status:** pending\n**Priority:** high\n**Depends:** dep\n\n**Note:** orphaned\n\n## Context\n\nBody.\n"
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
	if sum2.Fix != 0 {
		t.Errorf("second run found %d hazard(s), want 0 (not idempotent)", sum2.Fix)
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

	want := "# T\n\n**Status:** pending\n**Priority:** high\n**Depends:** dep\n\n**Note:** orphaned\n\n## Context\n\nBody.\n"
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
	want := "# T\n\n**Status:** pending\n**Priority:** high\n**Depends:** dep\n\n**Note:** orphaned\n\n## Context\n\nBody.\n"
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
