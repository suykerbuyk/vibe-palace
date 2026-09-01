// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

const tpHeader = "# A Task\n\n**Status:** pending\n**Priority:** high\n"

// tpVault builds a git-initialised vault (the --apply path requires a clean git
// tree, matching `vp migrate iteration-headings`) with one project.
func tpVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Projects", "proj", "tasks", "done"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Projects", "proj", "tasks", "cancelled"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return root
}

func tpGitInit(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"},
		{"add", "-A"}, {"commit", "-m", "seed"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable or failed (%v): %s", err, out)
		}
	}
}

func tpWrite(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return p
}

func tpRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

// TestMigrateTaskPreambleReportOnlyWritesNothing pins the plan-first posture.
// The default MUST not mutate.
func TestMigrateTaskPreambleReportOnlyWritesNothing(t *testing.T) {
	root := tpVault(t)
	body := tpHeader + "\nA standing directive.\n\n## The evidence\n\nBody.\n"
	p := tpWrite(t, root, "Projects/proj/tasks/live.md", body)

	var out bytes.Buffer
	sum, err := runTaskPreambleMigration(root, "", false, &out)
	if err != nil {
		t.Fatalf("report run: %v", err)
	}
	if sum.Moved != 1 {
		t.Errorf("Moved = %d, want 1", sum.Moved)
	}
	if sum.Applied != 0 {
		t.Errorf("Applied = %d, want 0 — the bare command must not write", sum.Applied)
	}
	if got := tpRead(t, p); got != body {
		t.Errorf("file was modified by a REPORT run\n got: %q\nwant: %q", got, body)
	}
	if !strings.Contains(out.String(), "REPORT ONLY") {
		t.Errorf("report must say it wrote nothing: %s", out.String())
	}
	// The root line routes through printVaultRoot now. It must still be the
	// report's first line, byte-identical to what this command emitted before
	// the four call sites were unified onto one helper.
	if got, want := out.String(), "Vault: "+root+"\n"; !strings.HasPrefix(got, want) {
		t.Errorf("report must open with %q; got:\n%s", want, got)
	}
}

// TestMigrateTaskPreambleApplyMovesTheText is the acceptance gate, and the
// MUTATION anchor: if --apply stops calling OverwriteTaskFile, this reds.
func TestMigrateTaskPreambleApplyMovesTheText(t *testing.T) {
	root := tpVault(t)
	const claim = "Head of the queue. Do these phases IN ORDER, and before any other work."
	body := tpHeader + "\n" + claim + "\n\n## The evidence\n\nBody.\n"
	p := tpWrite(t, root, "Projects/proj/tasks/live.md", body)
	tpGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskPreambleMigration(root, "", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if sum.Applied != 1 {
		t.Fatalf("Applied = %d, want 1", sum.Applied)
	}

	got := tpRead(t, p)
	if got == body {
		t.Fatal("--apply did not write the file")
	}
	// The preamble region is now empty and the text sits under Context.
	if _, outcome := storage.MovePreambleUnderContext(got); outcome != storage.PreambleEmpty {
		t.Errorf("migrated file still has a non-empty preamble (outcome %v):\n%s", outcome, got)
	}
	ci := strings.Index(got, "## "+storage.ConventionalFirstHeading)
	ti := strings.Index(got, claim)
	if ci < 0 || ti < ci {
		t.Errorf("moved text is not under the Context heading:\n%s", got)
	}
	// Header block byte-identical.
	if !strings.HasPrefix(got, tpHeader) {
		t.Errorf("header block changed:\n%s", got)
	}
	// Re-running is a no-op: the pass converges.
	sum2, err := runTaskPreambleMigration(root, "", false, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("second report: %v", err)
	}
	if sum2.Moved != 0 {
		t.Errorf("a second pass still wants to move %d file(s) — the migration does not converge", sum2.Moved)
	}
}

func TestMigrateTaskPreambleEmptyPreambleIsANoOp(t *testing.T) {
	root := tpVault(t)
	body := tpHeader + "\n## " + storage.ConventionalFirstHeading + "\n\nBody.\n"
	p := tpWrite(t, root, "Projects/proj/tasks/clean.md", body)
	tpGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskPreambleMigration(root, "", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if sum.Empty != 1 || sum.Moved != 0 || sum.Applied != 0 {
		t.Errorf("empty=%d moved=%d applied=%d, want 1/0/0", sum.Empty, sum.Moved, sum.Applied)
	}
	if got := tpRead(t, p); got != body {
		t.Errorf("an empty preamble must not be rewritten")
	}
}

// TestMigrateTaskPreambleSkipsNoH2 pins the Critical-3 branch end to end: the
// file is reported as its own class and its bytes are untouched even on --apply.
func TestMigrateTaskPreambleSkipsNoH2(t *testing.T) {
	root := tpVault(t)
	body := tpHeader + "\nFraming.\n\n### Only sub-headings\n\nMore.\n"
	p := tpWrite(t, root, "Projects/proj/tasks/no-h2.md", body)
	tpGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskPreambleMigration(root, "", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if sum.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", sum.Skipped)
	}
	if sum.Applied != 0 {
		t.Errorf("Applied = %d, want 0 — a no-H2 file must not be rewritten", sum.Applied)
	}
	if got := tpRead(t, p); got != body {
		t.Errorf("no-H2 file was modified\n got: %q", got)
	}
	if !strings.Contains(out.String(), "SKIP") {
		t.Errorf("the skip must be reported as its own class: %s", out.String())
	}
}

// TestMigrateTaskPreambleExcludesArchivedTasks pins the walk's scope. Every task
// writer refuses an archived task, so a finding there would be unrepairable.
func TestMigrateTaskPreambleExcludesArchivedTasks(t *testing.T) {
	root := tpVault(t)
	body := tpHeader + "\nArchived framing.\n\n## The evidence\n\nBody.\n"
	donePath := tpWrite(t, root, "Projects/proj/tasks/done/old.md", body)
	cancPath := tpWrite(t, root, "Projects/proj/tasks/cancelled/dropped.md", body)
	tpGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskPreambleMigration(root, "", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if sum.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0 — archived dirs must not be walked", sum.Scanned)
	}
	for _, p := range []string{donePath, cancPath} {
		if got := tpRead(t, p); got != body {
			t.Errorf("archived file %s was modified", p)
		}
	}
	if strings.Contains(out.String(), "old") || strings.Contains(out.String(), "dropped") {
		t.Errorf("archived slugs appeared in the report: %s", out.String())
	}
}

// TestMigrateTaskPreambleApplyRefusesDirtyVault matches the sibling migrator's
// safety posture: on a clean tree `git checkout .` is a guaranteed rollback.
func TestMigrateTaskPreambleApplyRefusesDirtyVault(t *testing.T) {
	root := tpVault(t)
	body := tpHeader + "\nFraming.\n\n## The evidence\n\nBody.\n"
	tpWrite(t, root, "Projects/proj/tasks/live.md", body)
	tpGitInit(t, root)
	// Dirty it.
	tpWrite(t, root, "Projects/proj/tasks/live.md", body+"\nextra\n")

	_, err := runTaskPreambleMigration(root, "", true, &bytes.Buffer{})
	if err == nil {
		t.Fatal("--apply must refuse a dirty vault git tree")
	}
	if !strings.Contains(err.Error(), "dirty") {
		t.Errorf("refusal must name the dirty tree, got %v", err)
	}
}

// tpLegacyHeader is a task file carrying a legacy BARE "Status:" line above the
// modern header block. It is the shape that refused 7 of 7 writes on the
// sibling migrator's first live --apply in iteration 376: the bare line breaks
// the contiguous "**Field:**" run after the title, so validateWholeTaskFile
// rejects the migrated bytes with "malformed header block".
//
// It is used here rather than an injected fault because a fixture that fails
// for a REAL reason keeps proving something when the fault-injection seam moves.
// The preamble is non-empty and an H2 exists, so the file is a genuine MOVE
// candidate — the refusal lands on the write, not on the plan.
//
// Why it is structurally unrepairable by THIS pass, which is the 376 lesson:
// the bare line stops headerBlock's field run at the title, so the "preamble"
// region swallows the real **Status:**/**Priority:** lines and the migration
// relocates the header itself. A file whose header block is already
// un-parseable cannot be fixed by a pass that keys off that same header block —
// which is why the repair is its own task, and why this command's only correct
// behaviour here is to refuse loudly and exit non-zero.
const tpLegacyHeader = "# A Task\nStatus: Done\n**Status:** pending\n**Priority:** high\n" +
	"\nFraming text that belongs under a heading.\n\n## The evidence\n\nBody.\n"

// TestMigrateTaskPreambleExitsNonZeroWhenEveryWriteFailed is the ported
// contract. The sibling `migrate task-status` pins the identical class in
// TestMigrateTaskStatusExitsNonZeroWhenEveryWriteFailed, whose comment named
// THIS command as the one that still had the defect.
//
// Before the fix, runTaskPreambleMigration printed "  !!" per refusal and
// returned `sum, nil` unconditionally, so this run exited 0 under a trailing
// "Applied 0 rewrite(s)." — telling any caller reading $? that a migration
// which wrote nothing had succeeded.
func TestMigrateTaskPreambleExitsNonZeroWhenEveryWriteFailed(t *testing.T) {
	root := tpVault(t)
	// Two candidates, both refused, so every attempted write fails and none
	// succeeds — the total-failure half of the contract.
	tpWrite(t, root, "Projects/proj/tasks/legacy1.md", tpLegacyHeader)
	tpWrite(t, root, "Projects/proj/tasks/legacy2.md", tpLegacyHeader)
	tpGitInit(t, root)

	var code int
	out := captureStdout(t, func() {
		code = cmdMigrateTaskPreamble().Run([]string{"--vault", root, "--project", "proj", "--apply"})
	})
	if code == cli.ExitOK {
		t.Errorf("exit code = %d (OK) after every write failed — a migration that wrote "+
			"nothing must not report success; out:\n%s", code, out)
	}
	if !strings.Contains(out, "2 file(s) FAILED.") {
		t.Errorf("summary must name the failures; got:\n%s", out)
	}
	if strings.Contains(out, "Applied 2") {
		t.Errorf("must not claim rewrites it did not make; got:\n%s", out)
	}
}

// TestMigrateTaskPreamblePartialFailureExitsNonZero pins the half that was the
// real design question: some writes refused, some succeeded.
//
// It exits non-zero, matching the sibling. The exit code answers "did the
// migration do what it was asked?", and a run that moved one of two preambles
// did not. It also leaves the vault PARTIALLY migrated, which is exactly the
// state a human has to look at.
//
// The successful write is asserted to have LANDED. Non-zero here means "read
// the report", not "nothing happened" — and the pass converges, so re-running
// after repairing the refused file is safe.
func TestMigrateTaskPreamblePartialFailureExitsNonZero(t *testing.T) {
	root := tpVault(t)
	const claim = "Framing that should end up under Context."
	goodBody := tpHeader + "\n" + claim + "\n\n## The evidence\n\nBody.\n"
	goodPath := tpWrite(t, root, "Projects/proj/tasks/good.md", goodBody)
	badPath := tpWrite(t, root, "Projects/proj/tasks/legacy.md", tpLegacyHeader)
	tpGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskPreambleMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if sum.Applied != 1 || sum.Failed != 1 {
		t.Errorf("Applied = %d, Failed = %d; want 1 and 1; out:\n%s", sum.Applied, sum.Failed, out.String())
	}
	// The good file really was migrated: partial progress is progress, and the
	// non-zero exit must not be read as "the run did nothing".
	if got := tpRead(t, goodPath); got == goodBody {
		t.Errorf("the writable file was not migrated:\n%s", got)
	}
	// The refused file is byte-identical. A refused write must not half-land.
	if got := tpRead(t, badPath); got != tpLegacyHeader {
		t.Errorf("refused file was modified\n got: %q", got)
	}

	// And the command surfaces it as non-zero, not merely in the summary.
	//
	// This runs on a FRESH vault, deliberately. Re-running against `root` would
	// exit non-zero because the successful write above left the git tree dirty
	// and --apply refuses that — a real refusal, but the wrong one, and the
	// assertion would then hold with the fix reverted.
	root2 := tpVault(t)
	tpWrite(t, root2, "Projects/proj/tasks/good.md", goodBody)
	tpWrite(t, root2, "Projects/proj/tasks/legacy.md", tpLegacyHeader)
	tpGitInit(t, root2)

	var code int
	runOut := captureStdout(t, func() {
		code = cmdMigrateTaskPreamble().Run([]string{"--vault", root2, "--project", "proj", "--apply"})
	})
	if code == cli.ExitOK {
		t.Errorf("exit code = %d (OK) with a refused write outstanding — partial failure "+
			"is a failure; out:\n%s", code, runOut)
	}
	// Pin that it failed for the RIGHT reason: one write landed, one was refused.
	if !strings.Contains(runOut, "Applied 1 rewrite(s).") || !strings.Contains(runOut, "1 file(s) FAILED.") {
		t.Errorf("expected a 1-applied/1-failed partial run, not some other refusal; got:\n%s", runOut)
	}
}

// TestMigrateTaskPreambleNothingToMigrateExitsZero pins the INVERTED defect,
// which is the same bug wearing the other face: a migration with no work is not
// a failed migration.
//
// Failed counts attempts that went wrong, never candidates that never existed,
// so a vault whose task files all have empty preambles — and a project with no
// task files at all — must both exit 0.
func TestMigrateTaskPreambleNothingToMigrateExitsZero(t *testing.T) {
	t.Run("no MOVE rows", func(t *testing.T) {
		root := tpVault(t)
		// Already migrated: preamble empty, nothing for the pass to do.
		tpWrite(t, root, "Projects/proj/tasks/clean.md",
			tpHeader+"\n## "+storage.ConventionalFirstHeading+"\n\nBody.\n")
		tpGitInit(t, root)

		var code int
		out := captureStdout(t, func() {
			code = cmdMigrateTaskPreamble().Run([]string{"--vault", root, "--project", "proj", "--apply"})
		})
		if code != cli.ExitOK {
			t.Errorf("exit code = %d, want %d — a migration with nothing to migrate is not a "+
				"failed migration; out:\n%s", code, cli.ExitOK, out)
		}
		if strings.Contains(out, "FAILED") {
			t.Errorf("a no-work run must not report failures; got:\n%s", out)
		}
	})

	t.Run("no task files at all", func(t *testing.T) {
		root := tpVault(t)
		// git cannot commit an empty tree — tpVault creates only directories,
		// and git does not track those — so tpGitInit would t.Skip and this
		// case would silently never run. Seed one non-task file so the walk
		// still finds an EMPTY tasks/ directory, which is the point.
		tpWrite(t, root, "README.md", "seed\n")
		tpGitInit(t, root)

		var code int
		out := captureStdout(t, func() {
			code = cmdMigrateTaskPreamble().Run([]string{"--vault", root, "--project", "proj", "--apply"})
		})
		if code != cli.ExitOK {
			t.Errorf("exit code = %d, want %d — an empty task directory is not a failure; out:\n%s",
				code, cli.ExitOK, out)
		}
	})
}

// TestMigrateTaskPreambleReadFailureCountsAsFailure pins the other branch that
// used to swallow. A file the migration could not open is a file it did not
// handle; reporting "  !!" and exiting 0 is the same defect one branch over.
func TestMigrateTaskPreambleReadFailureCountsAsFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: an unreadable file is still readable")
	}
	root := tpVault(t)
	p := tpWrite(t, root, "Projects/proj/tasks/unreadable.md",
		tpHeader+"\nFraming.\n\n## The evidence\n\nBody.\n")
	tpGitInit(t, root)
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })

	var out bytes.Buffer
	sum, err := runTaskPreambleMigration(root, "proj", false, &out)
	if err != nil {
		t.Fatalf("report run: %v", err)
	}
	if sum.Failed != 1 {
		t.Errorf("Failed = %d, want 1 — an unreadable file is a failure, not a silent skip; out:\n%s",
			sum.Failed, out.String())
	}
}
