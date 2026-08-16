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
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// These tests drive the COMMAND, not the planner. The planner is pinned in
// internal/wrapstate; what is unproven until something runs the seam is the part
// that only exists here — flag parsing, vault resolution, the clean-tree gate,
// the compare-and-set write, and the stamp. A helper test does not prove the
// path the helper is installed on, and the write path is where a migration
// stops being a report and starts being history.

// offListOrphanHeading is a framed narrative that is NOT on the operator's
// table. It sits between two numbered entries with an inviting gap. Nothing may
// number it.
const offListOrphanHeading = "## A narrative nobody assigned a number"

// plantArchive writes an iterations.md for a project, creating parents.
func plantArchive(t *testing.T, root, project, content string) string {
	t.Helper()
	path := filepath.Join(root, "Projects", project, "iterations.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// archiveAt builds an iterations.md placing each heading on EXACTLY the given
// 1-indexed line, on the writer's "---" frame. The operator's table is keyed on
// line numbers in the thousands, so a fixture that only approximates them would
// run the whole test with the real table quietly bypassed.
func archiveAt(t *testing.T, headings map[int]string) string {
	t.Helper()
	maxLine := 0
	for n := range headings {
		if n > maxLine {
			maxLine = n
		}
	}
	lines := make([]string, maxLine+6)
	for i := range lines {
		lines[i] = "filler narrative text"
	}
	lines[0] = "# p — Iteration Narratives"
	for n, h := range headings {
		if n < 4 {
			t.Fatalf("archiveAt: line %d is too close to the top to carry a frame", n)
		}
		lines[n-3] = ""
		lines[n-2] = "---"
		lines[n-1] = h
		lines[n] = ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// authorizedRow returns the operator's row for (project, line) or fails.
func authorizedRow(t *testing.T, project string, line int) wrapstate.AuthorizedAssignment {
	t.Helper()
	for _, a := range wrapstate.AuthorizedAssignments() {
		if a.Project == project && a.Line == line {
			return a
		}
	}
	t.Fatalf("no authorized row for %s line %d", project, line)
	return wrapstate.AuthorizedAssignment{}
}

// seedMigrationVault builds a git-clean vault carrying every case the migration
// must distinguish, and returns the root.
func seedMigrationVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// rusty-can: one authorized row planted at its exact recorded line, plus an
	// off-list orphan and a colon-separated numbered heading.
	rc := authorizedRow(t, "rusty-can", 80)
	plantArchive(t, root, "rusty-can", archiveAt(t, map[int]string{
		80:  rc.Text,
		200: "## Iteration 9: colon separated",
		300: offListOrphanHeading,
	}))

	// vibe-palace: an authorized addendum next to the entry it belongs under,
	// plus the titleless heading that must never get a placeholder title.
	vp := authorizedRow(t, "vibe-palace", 7649)
	plantArchive(t, root, "vibe-palace", archiveAt(t, map[int]string{
		7603: "## Iteration 108 — AGENTS.md as host-local managed shim",
		7649: vp.Text,
		9896: "## Iteration 147",
	}))

	// A clean archive: the migration must leave it untouched and unmentioned.
	plantArchive(t, root, "clean-proj",
		"# clean-proj — Iteration Narratives\n\n---\n## Iteration 1 — all good\n\nbody\n")

	gitInitCommit(t, root)
	return root
}

// captureStderr (cmd_init_test.go) is reused here: the commands report to
// stderr, so the report a human reads IS what these tests assert on.

// readArchive returns a project's iterations.md content.
func readArchive(t *testing.T, root, project string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "Projects", project, "iterations.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestMigrateIterationHeadingsReportOnlyWritesNothing: the bare command is a
// report. A one-shot whose default is "write" gets its report read afterwards,
// which is the wrong order for a command whose most important output is a list
// of things a human must decide.
func TestMigrateIterationHeadingsReportOnlyWritesNothing(t *testing.T) {
	root := seedMigrationVault(t)
	before := readArchive(t, root, "rusty-can")

	var code int
	out := captureStderr(t, func() {
		code = cmdMigrateIterationHeadings().Run([]string{"--vault", root})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	if got := readArchive(t, root, "rusty-can"); got != before {
		t.Errorf("the report-only run wrote to the archive")
	}
	if !strings.Contains(out, "REPORT ONLY") {
		t.Errorf("report does not say it wrote nothing:\n%s", out)
	}
	if stamp, _ := surface.ReadStamp(filepath.Join(root, "Projects", "rusty-can")); stamp.Surface != 0 {
		t.Errorf("the report-only run stamped the vault (surface %d)", stamp.Surface)
	}
}

// TestMigrateIterationHeadingsAppliesThroughTheRealSeam drives the command's own
// Run with --apply and then audits the bytes on disk: heading lines changed,
// every other byte identical, the off-list orphan untouched, the titleless
// heading untouched, and the surface stamp written by the vault write path.
func TestMigrateIterationHeadingsAppliesThroughTheRealSeam(t *testing.T) {
	root := seedMigrationVault(t)
	before := map[string]string{
		"rusty-can":   readArchive(t, root, "rusty-can"),
		"vibe-palace": readArchive(t, root, "vibe-palace"),
		"clean-proj":  readArchive(t, root, "clean-proj"),
	}

	var code int
	out := captureStderr(t, func() {
		code = cmdMigrateIterationHeadings().Run([]string{"--vault", root, "--apply"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit = %d\n%s", code, out)
	}

	// The clean archive must be byte-identical and must not appear in the report.
	if got := readArchive(t, root, "clean-proj"); got != before["clean-proj"] {
		t.Errorf("a clean archive was rewritten")
	}
	if strings.Contains(out, "clean-proj") {
		t.Errorf("a clean archive was reported as defective:\n%s", out)
	}

	// Heading-only diff, checked line by line on both changed archives.
	for _, project := range []string{"rusty-can", "vibe-palace"} {
		was := strings.Split(before[project], "\n")
		now := strings.Split(readArchive(t, root, project), "\n")
		if len(was) != len(now) {
			t.Fatalf("%s: line count changed %d -> %d", project, len(was), len(now))
		}
		for i := range was {
			if was[i] == now[i] {
				continue
			}
			if !strings.HasPrefix(now[i], "## Iteration ") {
				t.Errorf("%s line %d changed to a non-header: %q -> %q", project, i+1, was[i], now[i])
			}
			if !strings.HasPrefix(strings.TrimSpace(was[i]), "##") {
				t.Errorf("%s line %d was not a heading before: %q -> %q", project, i+1, was[i], now[i])
			}
		}
	}

	rc := readArchive(t, root, "rusty-can")
	if !strings.Contains(rc, "## Iteration 4 — P1.1 committed & retired (restart sweep)") {
		t.Errorf("the authorized row was not applied")
	}
	if !strings.Contains(rc, "## Iteration 9 — colon separated") {
		t.Errorf("the colon-separated heading was not normalised")
	}
	if !strings.Contains(rc, offListOrphanHeading) {
		t.Errorf("the off-list orphan was rewritten")
	}

	vp := readArchive(t, root, "vibe-palace")
	if !strings.Contains(vp, "## Iteration 108 — Retire auto-capture-ide-hooks (verification + cleanup)") {
		t.Errorf("the authorized addendum was not applied")
	}
	if !strings.Contains(vp, "\n## Iteration 147\n") {
		t.Errorf("the titleless heading was rewritten")
	}

	// The write went through the vault layer, so the .surface stamp exists next
	// to the archive. This is the cheap structural proof that the migration did
	// NOT hand-roll an os.WriteFile: the stamp is a side effect of
	// atomicfile.Write, which only the vault write path reaches.
	stamp, err := surface.ReadStamp(filepath.Join(root, "Projects", "rusty-can"))
	if err != nil {
		t.Fatalf("read .surface: %v", err)
	}
	if stamp.Surface == 0 {
		t.Errorf("no .surface stamp after an applying run — the write bypassed the vault write path")
	}
}

// TestMigrateIterationHeadingsRefusesOffListOrphan is the anti-derivation pin AT
// THE SEAM. The planner has its own copy of this assertion; this one proves the
// refusal survives all the way to the bytes on disk and to the operator's
// report. A guard that holds in a unit test and not on the installed path is not
// a guard.
func TestMigrateIterationHeadingsRefusesOffListOrphan(t *testing.T) {
	root := seedMigrationVault(t)

	var code int
	out := captureStderr(t, func() {
		code = cmdMigrateIterationHeadings().Run([]string{"--vault", root, "--apply"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	if !strings.Contains(rcLine(t, root, 300), offListOrphanHeading) {
		t.Fatalf("the off-list orphan at line 300 was rewritten to %q", rcLine(t, root, 300))
	}
	if !strings.Contains(out, "REFUSED") || !strings.Contains(out, offListOrphanHeading) {
		t.Errorf("the off-list orphan was not reported as refused:\n%s", out)
	}
	// Not merely "not rewritten" — no number may appear anywhere near it.
	for _, invented := range []string{"Iteration 8 — A narrative", "Iteration 10 — A narrative", "Iteration 301 — A narrative"} {
		if strings.Contains(readArchive(t, root, "rusty-can"), invented) {
			t.Errorf("a number was invented for the off-list orphan: %q", invented)
		}
	}
}

// rcLine returns 1-indexed line n of rusty-can's archive.
func rcLine(t *testing.T, root string, n int) string {
	t.Helper()
	lines := strings.Split(readArchive(t, root, "rusty-can"), "\n")
	if n < 1 || n > len(lines) {
		t.Fatalf("line %d out of range 1..%d", n, len(lines))
	}
	return lines[n-1]
}

// TestMigrateIterationHeadingsRefusesDriftedRow: the operator's row records the
// exact text that must still be at line 80. Put something else there and the
// number must not be stamped onto it.
func TestMigrateIterationHeadingsRefusesDriftedRow(t *testing.T) {
	root := t.TempDir()
	const drifted = "## An entirely different narrative that moved here"
	plantArchive(t, root, "rusty-can", archiveAt(t, map[int]string{80: drifted}))
	gitInitCommit(t, root)

	var code int
	out := captureStderr(t, func() {
		code = cmdMigrateIterationHeadings().Run([]string{"--vault", root, "--apply"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	if got := rcLine(t, root, 80); got != drifted {
		t.Errorf("line 80 was rewritten despite drift: %q", got)
	}
	if !strings.Contains(out, "DRIFTED") {
		t.Errorf("the drift was not reported:\n%s", out)
	}
}

// TestMigrateIterationHeadingsNeverWritesAPlaceholderTitle. The check SUGGESTS
// "## Iteration 147 — <title>" so a human knows what is missing; nothing may
// write a placeholder into the archive, where it stops reading as a placeholder
// and just looks like a badly named narrative.
func TestMigrateIterationHeadingsNeverWritesAPlaceholderTitle(t *testing.T) {
	root := seedMigrationVault(t)
	captureStderr(t, func() {
		cmdMigrateIterationHeadings().Run([]string{"--vault", root, "--apply"})
	})
	for _, project := range []string{"rusty-can", "vibe-palace", "clean-proj"} {
		body := readArchive(t, root, project)
		for _, banned := range []string{"(untitled)", "<title>", "Iteration 108.5", "108.5"} {
			if strings.Contains(body, banned) {
				t.Errorf("%s: archive contains %q after migration", project, banned)
			}
		}
	}
}

// TestMigrateIterationHeadingsRefusesDirtyTree: this rewrites the project's own
// narrative history, the one vault file with no second copy, so the cheapest
// undo — `git checkout .` — has to exist before the first byte is written.
func TestMigrateIterationHeadingsRefusesDirtyTree(t *testing.T) {
	root := seedMigrationVault(t)
	if err := os.WriteFile(filepath.Join(root, "dirty.md"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := readArchive(t, root, "rusty-can")

	var code int
	out := captureStderr(t, func() {
		code = cmdMigrateIterationHeadings().Run([]string{"--vault", root, "--apply"})
	})
	if code == cli.ExitOK {
		t.Errorf("applied against a dirty vault tree:\n%s", out)
	}
	if !strings.Contains(out, "dirty") {
		t.Errorf("the refusal does not explain itself:\n%s", out)
	}
	if got := readArchive(t, root, "rusty-can"); got != before {
		t.Errorf("the refused run still wrote")
	}
}

// TestMigrateIterationHeadingsSummaryCounts asserts the roll-up the report is
// built from, per class and per number-provenance, so a future change that
// silently reclassifies a repair shows up as a count.
func TestMigrateIterationHeadingsSummaryCounts(t *testing.T) {
	root := seedMigrationVault(t)
	var buf bytes.Buffer
	sum, err := runIterationHeadingMigration(root, false, &buf)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// rusty-can: authorized@80 + colon@200 ; vibe-palace: authorized@7649.
	if sum.Rewrites != 3 {
		t.Errorf("Rewrites = %d, want 3\n%s", sum.Rewrites, buf.String())
	}
	// rusty-can's off-list orphan @300 and vibe-palace's titleless @9896.
	if sum.Refusals != 2 {
		t.Errorf("Refusals = %d, want 2\n%s", sum.Refusals, buf.String())
	}
	if sum.BySource[wrapstate.SourceAuthorized] != 2 {
		t.Errorf("operator-authorized = %d, want 2", sum.BySource[wrapstate.SourceAuthorized])
	}
	if sum.BySource[wrapstate.SourceRecovered] != 1 {
		t.Errorf("recovered = %d, want 1", sum.BySource[wrapstate.SourceRecovered])
	}
	if sum.Written != 0 {
		t.Errorf("a report-only run reported %d files written", sum.Written)
	}
	// This fixture has no rezbldrvault archive, so the operator's row for it
	// could never be reached by the per-project loop. It must still be named:
	// a run that prints "rewrote 3" while a ruled-on narrative stays
	// unaddressable, and says nothing about it, is the silent half of failure.
	if !strings.Contains(buf.String(), "rezbldrvault @L2289 -> Iteration 40") {
		t.Errorf("an operator row for an absent project was not reported:\n%s", buf.String())
	}
}

// TestMigrateIterationHeadingsIsIdempotent: after a successful apply, a second
// run must plan nothing and report the authorized rows as already applied. The
// two refusals stay — they are permanent until a human rules on them.
func TestMigrateIterationHeadingsIsIdempotent(t *testing.T) {
	root := seedMigrationVault(t)
	captureStderr(t, func() {
		cmdMigrateIterationHeadings().Run([]string{"--vault", root, "--apply"})
	})
	after := readArchive(t, root, "rusty-can")

	var buf bytes.Buffer
	sum, err := runIterationHeadingMigration(root, false, &buf)
	if err != nil {
		t.Fatalf("second plan: %v", err)
	}
	if sum.Rewrites != 0 {
		t.Errorf("second run would rewrite %d headings\n%s", sum.Rewrites, buf.String())
	}
	if sum.Refusals != 2 {
		t.Errorf("second run refusals = %d, want the same 2", sum.Refusals)
	}
	// The rows this fixture actually planted must report already-applied, NOT
	// drift. A migration that flags its own output as archive drift on every
	// subsequent run is an alarm nobody reads on the day it is right. (The rows
	// this fixture does not plant legitimately report absent/drifted — the
	// fixture is a partial vault, not a broken one.)
	for _, want := range []string{
		"operator row 4 @L80: already-applied",
		"operator row 108 @L7649: already-applied",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in the re-run report:\n%s", want, buf.String())
		}
	}
	if got := readArchive(t, root, "rusty-can"); got != after {
		t.Errorf("the report-only second run wrote")
	}
}

// TestMigrateIterationsPreambleAtTheSeam drives the separate preamble command:
// the false comment goes, the true one elsewhere stays, and the diff is confined
// to the comment block.
func TestMigrateIterationsPreambleAtTheSeam(t *testing.T) {
	root := t.TempDir()
	const falseBlock = `<!-- Each iteration gets a section below, newest first.
     An "iteration" is a logical unit of work (feature, fix, refactor)
     that may span multiple sessions. -->`
	plantArchive(t, root, "vibe-palace",
		"# vibe-palace — Iteration Narratives\n\n"+falseBlock+"\n\n---\n## Iteration 1 — a\n\nbody\n")
	// rezbldr's sentence is TRUE and scoped to preserved pre-rename entries.
	const rezbldrTrue = "Preserved verbatim (newest-first ordering) for historical continuity."
	plantArchive(t, root, "rezbldr",
		"# rezbldr — Iteration History\n\n## Pre-Rename Era\n\n"+rezbldrTrue+"\n\n---\n## Iteration 3 — Tutorial\n\nbody\n")
	gitInitCommit(t, root)

	var code int
	out := captureStderr(t, func() {
		code = cmdMigrateIterationsPreamble().Run([]string{"--vault", root, "--apply"})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	vp := readArchive(t, root, "vibe-palace")
	if strings.Contains(vp, "newest first") {
		t.Errorf("the false claim survived:\n%s", vp)
	}
	if !strings.Contains(vp, wrapstate.AccurateIterationsPreamble) {
		t.Errorf("the accurate sentence was not written:\n%s", vp)
	}
	if !strings.Contains(vp, "## Iteration 1 — a") || !strings.Contains(vp, "\nbody\n") {
		t.Errorf("the preamble repair disturbed the archive body:\n%s", vp)
	}
	if got := readArchive(t, root, "rezbldr"); !strings.Contains(got, rezbldrTrue) {
		t.Errorf("rezbldr's TRUE newest-first sentence was removed:\n%s", got)
	}
}

// TestMigrateIterationsPreambleReportOnly: same plan-first posture, same reason.
func TestMigrateIterationsPreambleReportOnly(t *testing.T) {
	root := t.TempDir()
	const falseBlock = `<!-- Each iteration gets a section below, newest first.
     An "iteration" is a logical unit of work (feature, fix, refactor)
     that may span multiple sessions. -->`
	content := "# p\n\n" + falseBlock + "\n\n---\n## Iteration 1 — a\n\nbody\n"
	plantArchive(t, root, "p", content)
	gitInitCommit(t, root)

	var code int
	out := captureStderr(t, func() {
		code = cmdMigrateIterationsPreamble().Run([]string{"--vault", root})
	})
	if code != cli.ExitOK {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	if got := readArchive(t, root, "p"); got != content {
		t.Errorf("the report-only run wrote")
	}
	if !strings.Contains(out, "REPORT ONLY") {
		t.Errorf("report does not say it wrote nothing:\n%s", out)
	}
}
