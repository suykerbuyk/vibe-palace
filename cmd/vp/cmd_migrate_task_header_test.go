// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The fixtures below are the SHAPES measured on the live corpus, not invented
// ones. Every fixture in cmd_migrate_task_status_test.go is "# T", blank,
// "**Status:**" — the shape CreateTask writes — which is precisely why a live
// --apply failed 7 of 7 against shapes no fixture carried. Build the vault the
// test acts on; verify against the live shapes, not against canned strings.
const (
	// thBoth — a true un-bolded value above a stale bolded one.
	thBoth = "# Task 3.5: Portable Command Execution\n" +
		"Status: Done\n" +
		"\n" +
		"**Status:** pending\n" +
		"**Priority:** high\n\n" +
		"## Summary\n\nBody.\n"

	// thBareOnly — the bare line is the file's ONLY status declaration, and its
	// value is free prose. Deleting it would destroy the status.
	thBareOnly = "# Documentation accuracy pass\n" +
		"Status: Planned (reviewed, revised)\n\n" +
		"## Context\n\nBody.\n"

	// thMultiTitle — a modern header prepended above an intact legacy document
	// that carries its own un-bolded status. Classifying this as "both" would
	// carry the legacy document's status onto the modern header.
	thMultiTitle = "# Salvage HNSW recall harness\n\n" +
		"**Status:** retired\n" +
		"**Priority:** medium\n\n" +
		"# Plan: Salvage HNSW Recall Harness\n\n" +
		"Status: Planned, architecture-reviewed.\n\n" +
		"## Context\n\nBody.\n"

	// thClean — the shape the current writer produces.
	thClean = "# Ordinary task\n\n" +
		"**Status:** retired\n" +
		"**Priority:** medium\n\n" +
		"## Context\n\nBody.\n"
)

// thSeed lays down one specimen of every class and returns the vault root plus
// the absolute path of each file.
func thSeed(t *testing.T) (root string, paths map[string]string) {
	t.Helper()
	root = setupTestVaultEnv(t)
	paths = map[string]string{
		"both":        tsWrite(t, root, "Projects/proj/tasks/done/both.md", thBoth),
		"bare-only":   tsWrite(t, root, "Projects/proj/tasks/done/bareonly.md", thBareOnly),
		"multi-title": tsWrite(t, root, "Projects/proj/tasks/done/multi.md", thMultiTitle),
		"clean":       tsWrite(t, root, "Projects/proj/tasks/done/clean.md", thClean),
	}
	return root, paths
}

// TestMigrateTaskHeaderRepairsBothAndLeavesEveryOtherClassAlone is the scope
// test. The operator split this work deliberately: only the "both" class has a
// provably lossless repair, and a command that quietly widened to the others
// would be writing files nobody reviewed.
func TestMigrateTaskHeaderRepairsBothAndLeavesEveryOtherClassAlone(t *testing.T) {
	root, paths := thSeed(t)

	var out bytes.Buffer
	sum, err := runTaskHeaderMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("runTaskHeaderMigration: %v", err)
	}
	if sum.Failed != 0 {
		t.Fatalf("Failed = %d, want 0; out:\n%s", sum.Failed, out.String())
	}

	// Derive the expectation from the classifier's own report rather than
	// hardcoding a population: every file it classified as "both" is a file it
	// must have rewritten, and nothing else may be.
	if sum.Applied != sum.Both {
		t.Errorf("Applied = %d but Both = %d; every both-class file must be repaired and only those",
			sum.Applied, sum.Both)
	}
	if sum.Both == 0 {
		t.Fatal("the fixture seeded a both-class file but the classifier found none")
	}

	got := tsRead(t, paths["both"])
	if strings.Contains(got, "\nStatus: Done") {
		t.Errorf("the bare legacy line survived:\n%s", got)
	}
	if strings.Contains(got, "**Status:** pending") {
		t.Errorf("the STALE value survived — dropping the bare line alone would leave "+
			"the file asserting only the falsehood:\n%s", got)
	}
	if !strings.Contains(got, "**Status:** Done") {
		t.Errorf("the true value was not carried onto the bolded field:\n%s", got)
	}

	// The oracle. OverwriteTaskFile runs validateWholeTaskFile before it writes,
	// so a successful apply already IS the validator's verdict; re-classifying
	// proves the file also left the population it was in.
	if c := storage.ScanLegacyHeader(got).Class; c != storage.LegacyHeaderClean {
		t.Errorf("repaired file still classifies as %s, want %s", c, storage.LegacyHeaderClean)
	}

	for _, class := range []string{"bare-only", "multi-title", "clean"} {
		want := map[string]string{"bare-only": thBareOnly, "multi-title": thMultiTitle, "clean": thClean}[class]
		if got := tsRead(t, paths[class]); got != want {
			t.Errorf("%s file was rewritten — this command must never write it\n got: %q", class, got)
		}
	}
	if sum.BareOnly == 0 || sum.MultiTitle == 0 {
		t.Errorf("BareOnly = %d, MultiTitle = %d; both classes must be REPORTED, not silently dropped",
			sum.BareOnly, sum.MultiTitle)
	}
	if !strings.Contains(out.String(), "separate task") {
		t.Errorf("the report must say why a skipped class is skipped; got:\n%s", out.String())
	}
}

// TestMigrateTaskHeaderReportOnlyWritesNothing pins the plan-first default. A
// command whose default is "write" gets its report skimmed afterwards instead of
// read before.
func TestMigrateTaskHeaderReportOnlyWritesNothing(t *testing.T) {
	root, paths := thSeed(t)
	before := map[string]string{}
	for k, p := range paths {
		before[k] = tsRead(t, p)
	}

	var out bytes.Buffer
	sum, err := runTaskHeaderMigration(root, "proj", false, &out)
	if err != nil {
		t.Fatalf("runTaskHeaderMigration: %v", err)
	}
	if sum.Applied != 0 {
		t.Errorf("Applied = %d under report-only; want 0", sum.Applied)
	}
	if sum.Both == 0 {
		t.Error("report-only must still CLASSIFY; Both = 0")
	}
	for k, p := range paths {
		if got := tsRead(t, p); got != before[k] {
			t.Errorf("%s was modified by a report-only run\n got: %q", k, got)
		}
	}
	if !strings.Contains(out.String(), "REPORT ONLY") {
		t.Errorf("the mode banner must say so; got:\n%s", out.String())
	}
}

// TestMigrateTaskHeaderRefusesArchivedShadow covers the hazard that makes an
// archive-reaching writer different from every other one: Vault.resolveTaskFile
// searches ACTIVE first, so overwriting an archived slug that also exists under
// tasks/ would silently rewrite the active file instead.
func TestMigrateTaskHeaderRefusesArchivedShadow(t *testing.T) {
	root := setupTestVaultEnv(t)
	activeBody := "# Active twin\n\n**Status:** pending\n**Priority:** medium\n\n## Context\n\nActive.\n"
	activePath := tsWrite(t, root, "Projects/proj/tasks/dup.md", activeBody)
	archivedPath := tsWrite(t, root, "Projects/proj/tasks/done/dup.md", thBoth)

	var out bytes.Buffer
	sum, err := runTaskHeaderMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("runTaskHeaderMigration: %v", err)
	}
	if sum.Failed != 1 || sum.Applied != 0 {
		t.Errorf("Failed = %d, Applied = %d; want 1 and 0; out:\n%s", sum.Failed, sum.Applied, out.String())
	}
	if got := tsRead(t, activePath); got != activeBody {
		t.Errorf("the ACTIVE file was rewritten — this is the hazard the guard exists for\n got: %q", got)
	}
	if got := tsRead(t, archivedPath); got != thBoth {
		t.Errorf("archived file was rewritten despite the refusal\n got: %q", got)
	}
}

// TestMigrateTaskHeaderExitsNonZeroWhenEveryWriteFailed pins the rule its
// siblings both carry: a run that wrote NOTHING must not report success.
func TestMigrateTaskHeaderExitsNonZeroWhenEveryWriteFailed(t *testing.T) {
	root := setupTestVaultEnv(t)
	// Two shadowed slugs: both are real both-class files, and both are refused,
	// so every candidate write fails and none succeeds.
	for _, slug := range []string{"dup1", "dup2"} {
		tsWrite(t, root, "Projects/proj/tasks/"+slug+".md",
			"# T\n\n**Status:** pending\n**Priority:** medium\n\n## Context\n\nActive.\n")
		tsWrite(t, root, "Projects/proj/tasks/done/"+slug+".md", thBoth)
	}

	var code int
	out := captureStdout(t, func() {
		code = cmdMigrateTaskHeader().Run([]string{"--project", "proj", "--apply"})
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

// TestMigrateTaskHeaderIsIdempotent proves a second apply finds nothing left to
// do. A repaired file classifies as clean, so the population converges rather
// than the command rewriting the same file on every run.
func TestMigrateTaskHeaderIsIdempotent(t *testing.T) {
	root, paths := thSeed(t)

	var first bytes.Buffer
	if _, err := runTaskHeaderMigration(root, "proj", true, &first); err != nil {
		t.Fatalf("first run: %v", err)
	}
	afterFirst := tsRead(t, paths["both"])

	var second bytes.Buffer
	sum, err := runTaskHeaderMigration(root, "proj", true, &second)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if sum.Both != 0 || sum.Applied != 0 {
		t.Errorf("second run found Both = %d and Applied = %d; want 0 and 0; out:\n%s",
			sum.Both, sum.Applied, second.String())
	}
	if got := tsRead(t, paths["both"]); got != afterFirst {
		t.Errorf("a second run modified an already-repaired file\n was: %q\n now: %q", afterFirst, got)
	}
}

// TestMigrateTaskHeaderWalksEveryTaskDirectory proves the scan is not
// archive-only. The multi-title class includes ACTIVE tasks, and a legacy header
// is not made harmless by living in a directory the command declines to visit.
func TestMigrateTaskHeaderWalksEveryTaskDirectory(t *testing.T) {
	root := setupTestVaultEnv(t)
	tsWrite(t, root, "Projects/proj/tasks/active.md", thMultiTitle)
	tsWrite(t, root, "Projects/proj/tasks/done/archived.md", thBoth)
	tsWrite(t, root, "Projects/proj/tasks/cancelled/dropped.md", thBareOnly)

	var out bytes.Buffer
	sum, err := runTaskHeaderMigration(root, "proj", false, &out)
	if err != nil {
		t.Fatalf("runTaskHeaderMigration: %v", err)
	}
	for _, dir := range []string{"active", "done/archived", "cancelled/dropped"} {
		if !strings.Contains(out.String(), filepath.ToSlash(dir)) {
			t.Errorf("report does not mention %q — a directory went unvisited; out:\n%s", dir, out.String())
		}
	}
	if sum.Scanned != 3 {
		t.Errorf("Scanned = %d, want the 3 seeded files", sum.Scanned)
	}
}

// TestMigrateTaskHeaderIgnoresFencedSpecimens is the self-inflicted-wound test at
// the command level. The task files that DOCUMENT this defect quote its
// specimens inside code fences; a fence-blind pass rewrites the very files
// describing the bug, including the one this work was planned in.
func TestMigrateTaskHeaderIgnoresFencedSpecimens(t *testing.T) {
	root := setupTestVaultEnv(t)
	doc := "# A shared fence-aware classifier for legacy task headers\n\n" +
		"**Status:** pending\n" +
		"**Priority:** medium\n\n" +
		"## The two shapes, measured\n\n" +
		"```\n" + thBoth + "```\n\n" +
		"Prose after the fence.\n"
	p := tsWrite(t, root, "Projects/proj/tasks/the-task-itself.md", doc)

	var out bytes.Buffer
	sum, err := runTaskHeaderMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("runTaskHeaderMigration: %v", err)
	}
	if sum.Both != 0 || sum.Applied != 0 {
		t.Errorf("a fenced specimen was read as structure: Both = %d, Applied = %d; out:\n%s",
			sum.Both, sum.Applied, out.String())
	}
	if got := tsRead(t, p); got != doc {
		t.Errorf("the task file documenting this defect was rewritten\n got: %q", got)
	}
}

// TestMigrateTaskHeaderIsRegistered pins the wiring. The command is reachable
// only through a two-word registry lookup, and its parent's Subcommands list is
// what `vp migrate` and `vp help migrate` print — a command registered but
// unlisted works and cannot be found, which is how `migrate task-status`
// shipped invisible.
func TestMigrateTaskHeaderIsRegistered(t *testing.T) {
	reg, _, _ := testRegistry()
	if _, ok := reg.Lookup("migrate task-header"); !ok {
		t.Fatal(`command "migrate task-header" not registered`)
	}
	parent, ok := reg.Lookup("migrate")
	if !ok {
		t.Fatal(`parent command "migrate" not registered`)
	}
	for _, want := range []string{"migrate task-header", "migrate task-status"} {
		if !contains(parent.Subcommands, want) {
			t.Errorf("%q is registered but absent from the migrate Subcommands list, "+
				"so `vp migrate` will not print it", want)
		}
		if _, ok := reg.Lookup(want); !ok {
			t.Errorf("%q is listed as a subcommand but is not registered", want)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// ensure the fixtures really are the classes they claim, so a later edit to one
// cannot silently turn this file's tests into assertions about nothing.
func TestMigrateTaskHeaderFixturesAreTheClassesTheyClaim(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    storage.LegacyHeaderClass
	}{
		{"both", thBoth, storage.LegacyHeaderBoth},
		{"bare-only", thBareOnly, storage.LegacyHeaderBareOnly},
		{"multi-title", thMultiTitle, storage.LegacyHeaderMultiTitle},
		{"clean", thClean, storage.LegacyHeaderClean},
	} {
		if got := storage.ScanLegacyHeader(tc.content).Class; got != tc.want {
			t.Errorf("%s fixture classifies as %s, want %s", tc.name, got, tc.want)
		}
	}
}
