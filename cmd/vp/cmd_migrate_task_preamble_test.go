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
