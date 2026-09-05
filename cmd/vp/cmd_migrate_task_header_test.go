// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
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

	// thBareOnlyWrapped — the bare-only shape whose value WRAPS, plus a bare
	// Priority line sitting under the wrap. Half the work of the bare-only
	// repair is deciding where the continuation goes, and a fixture with a
	// one-line value never reaches that half.
	thBareOnlyWrapped = "# Port friction analytics\n\n" +
		"Status: In Progress — refined 2026-06-21; two architecture reviews folded into the\n" +
		"numbered Phases 2026-06-23 (review #2 below + Grok cross-review); operator-approved.\n" +
		"Priority: Medium\n\n" +
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

	// thMultiTitleSections — H1 used as ORDINARY SECTION HEADINGS of one
	// document. The live specimen opens H2 sections well before its first
	// "rival" H1, and its later H1s read "Open Questions" and "Definition of
	// done". Demoting them would restructure a document nobody asked to
	// restructure.
	thMultiTitleSections = "# ADR-006 umbrella — CLOSED 2026-08-16\n\n" +
		"**Status:** retired\n**Priority:** high\n\n" +
		"## The thesis\n\nBody.\n\n" +
		"# PHASE 2 — DERIVE WHAT IS ACTUALLY DERIVABLE\n\nPhase body.\n\n" +
		"# Open Questions\n\nQuestions.\n"

	// thMultiTitleBadHeader — 🔴 THE TRAP FIXTURE, modelled on the one live file
	// the transform cannot fix. Its MODERN header is corrupted independently of
	// the two titles: free prose sits between its own **Status:** and
	// **Priority:** lines, so the header block is not contiguous.
	//
	// After the transform this file classifies CLEAN and the validator still
	// REFUSES it. A repair keyed on the classifier writes nothing for it and
	// says nothing about it — which is why the sign-off section exists.
	thMultiTitleBadHeader = "# Vault whole-file writes have no lock across RMW\n\n" +
		"**Status:** retired\n" +
		"Plan-reviewed 2026-06-06; design decisions below are locked.\n" +
		"**Priority:** medium\n\n" +
		"# Vault whole-file writes lack RMW serialization (lost-update hole)\n\n" +
		"## Problem\n\nBody.\n"

	// thClean — the shape the current writer produces.
	thClean = "# Ordinary task\n\n" +
		"**Status:** retired\n" +
		"**Priority:** medium\n\n" +
		"## Context\n\nBody.\n"

	// thInverted — the both shape with its premise reversed: the BOLDED value is
	// a correct terminal status and the bare line is prose.
	//
	// 🔴 The **Priority:** line is load-bearing. The live specimen that exposed
	// this class carries no priority field, so validateWholeTaskFile refuses its
	// repaired bytes at the missing-Priority arm — an unrelated guard pointing
	// the same way by luck. A fixture without it would pass for the wrong reason
	// and keep passing if the class were deleted.
	thInverted = "# Plan: Phase D — Parallel Operation\n" +
		"Status: Closed — operator accepted retrospective 2026-06-06\n" +
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
		"both":         tsWrite(t, root, "Projects/proj/tasks/done/both.md", thBoth),
		"bare-only":    tsWrite(t, root, "Projects/proj/tasks/done/bareonly.md", thBareOnly),
		"bare-wrapped": tsWrite(t, root, "Projects/proj/tasks/done/barewrapped.md", thBareOnlyWrapped),
		"multi-title":  tsWrite(t, root, "Projects/proj/tasks/done/multi.md", thMultiTitle),
		"mt-sections":  tsWrite(t, root, "Projects/proj/tasks/done/mtsections.md", thMultiTitleSections),
		"mt-badheader": tsWrite(t, root, "Projects/proj/tasks/done/mtbadheader.md", thMultiTitleBadHeader),
		"clean":        tsWrite(t, root, "Projects/proj/tasks/done/clean.md", thClean),
		"inverted":     tsWrite(t, root, "Projects/proj/tasks/done/inverted.md", thInverted),
	}
	return root, paths
}

// TestMigrateTaskHeaderRepairsTheWritableClassesAndLeavesTheRestAlone is the
// scope test. Two classes have a repair — "both" carries a value across, and
// "bare-only" constructs the whole header block — and a command that quietly
// widened to the two that need a per-file judgment call would be writing files
// nobody reviewed.
func TestMigrateTaskHeaderRepairsTheWritableClassesAndLeavesTheRestAlone(t *testing.T) {
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
	// hardcoding a population: every file it classified as a WRITABLE class is a
	// file it must have rewritten, and nothing else may be.
	// Derived from the classifier's own report and the sign-off list: every file
	// in a writable class is repaired EXCEPT the ones handed to a human.
	wantApplied := sum.Both + sum.BareOnly + sum.MultiTitle - len(sum.SignOff)
	if sum.Applied != wantApplied {
		t.Errorf("Applied = %d, want %d (Both=%d BareOnly=%d MultiTitle=%d SignOff=%d)",
			sum.Applied, wantApplied, sum.Both, sum.BareOnly, sum.MultiTitle, len(sum.SignOff))
	}
	if sum.Both == 0 || sum.BareOnly == 0 || sum.AppliedMultiTitle == 0 {
		t.Fatalf("the fixture seeded all three writable classes but got Both=%d BareOnly=%d AppliedMultiTitle=%d",
			sum.Both, sum.BareOnly, sum.AppliedMultiTitle)
	}
	if len(sum.SignOff) != 2 {
		t.Fatalf("SignOff = %d, want 2 (H1-as-sections and the corrupted modern header); out:\n%s",
			len(sum.SignOff), out.String())
	}
	if sum.AppliedBareOnly != sum.BareOnly {
		t.Errorf("AppliedBareOnly = %d, want %d — the paired-command notice is keyed on this count",
			sum.AppliedBareOnly, sum.BareOnly)
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

	// --- the bare-only construction ------------------------------------------
	bare := tsRead(t, paths["bare-only"])
	if !strings.Contains(bare, "**Status:** Planned (reviewed, revised)") {
		t.Errorf("the legacy value was not carried onto a constructed Status field:\n%s", bare)
	}
	if !strings.Contains(bare, "**Priority:** medium") {
		t.Errorf("no Priority field was constructed, so the file is still one the validator "+
			"refuses — the whole reason this is construction and not promotion:\n%s", bare)
	}
	if c := storage.ScanLegacyHeader(bare).Class; c != storage.LegacyHeaderClean {
		t.Errorf("constructed file still classifies as %s, want %s", c, storage.LegacyHeaderClean)
	}
	wrapped := tsRead(t, paths["bare-wrapped"])
	if !strings.Contains(wrapped, "**Priority:** Medium") {
		t.Errorf("the bare Priority under the wrap was not read:\n%s", wrapped)
	}
	if !strings.Contains(wrapped, "numbered Phases 2026-06-23") {
		t.Errorf("the wrapped continuation was dropped:\n%s", wrapped)
	}

	// --- the multi-title demotion --------------------------------------------
	multi := tsRead(t, paths["multi-title"])
	if !strings.Contains(multi, "## Plan: Salvage HNSW Recall Harness") {
		t.Errorf("the second title was not demoted:\n%s", multi)
	}
	if strings.Contains(multi, "\n# ") {
		t.Errorf("the file still carries a second H1:\n%s", multi)
	}
	if c := storage.ScanLegacyHeader(multi).Class; c != storage.LegacyHeaderClean {
		t.Errorf("demoted file still classifies as %s, want %s", c, storage.LegacyHeaderClean)
	}

	for _, class := range []string{"mt-sections", "mt-badheader", "clean", "inverted"} {
		want := map[string]string{"mt-sections": thMultiTitleSections,
			"mt-badheader": thMultiTitleBadHeader, "clean": thClean, "inverted": thInverted}[class]
		if got := tsRead(t, paths[class]); got != want {
			t.Errorf("%s file was rewritten — this command must never write it\n got: %q", class, got)
		}
	}
	if sum.MultiTitle == 0 || sum.Inverted == 0 {
		t.Errorf("MultiTitle = %d, Inverted = %d; every unrepairable class must be "+
			"REPORTED, not silently dropped", sum.MultiTitle, sum.Inverted)
	}
	if !strings.Contains(out.String(), "separate task") {
		t.Errorf("the report must say why a skipped class is skipped; got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "SIGN-OFF REQUIRED") {
		t.Errorf("the report has no sign-off section, so the files it refused are unnamed; got:\n%s",
			out.String())
	}
	// The pairing notice is the operator's only warning that this run created
	// audit findings on purpose.
	if !strings.Contains(out.String(), "vp migrate task-status --apply") {
		t.Errorf("the report does not name the mandatory paired command; got:\n%s", out.String())
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

// TestMigrateTaskHeaderRefusesTheInvertedShapeAndMakesNoNewFinding is the
// acceptance test for the inverted class, and it asserts the consequence rather
// than the mechanism.
//
// The failure this class prevents is not "a file was rewritten" — it is "a
// repair manufactured work for a sibling tool". So the assertion is made against
// vaultaudit.DimTaskStatusDirectory itself: after --apply, the dimension must
// report nothing new. Checking the bytes by eye would pass on a rewrite that
// happened to look plausible; checking the dimension cannot.
func TestMigrateTaskHeaderRefusesTheInvertedShapeAndMakesNoNewFinding(t *testing.T) {
	root, paths := thSeed(t)

	before, err := vaultaudit.Run(storage.NewVault(root))
	if err != nil {
		t.Fatalf("audit before: %v", err)
	}

	var out bytes.Buffer
	sum, err := runTaskHeaderMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("runTaskHeaderMigration: %v", err)
	}

	// An unrepairable class is REPORTED, never ATTEMPTED. Counting it as a
	// failure would make every run of this command exit non-zero forever, which
	// is what the live vault did while its one inverted file classified "both".
	if sum.Failed != 0 {
		t.Errorf("Failed = %d, want 0 — a class declined by design is not a failed attempt; out:\n%s",
			sum.Failed, out.String())
	}
	if sum.Inverted != 1 {
		t.Fatalf("Inverted = %d, want 1; out:\n%s", sum.Inverted, out.String())
	}

	if got := tsRead(t, paths["inverted"]); got != thInverted {
		t.Fatalf("the inverted file was rewritten; this command must never write it:\n%s", got)
	}
	// The report must name BOTH values: the whole reason the file is skipped is
	// that a human has to decide which of the two is true.
	for _, want := range []string{"inverted", "retired", "Closed — operator accepted"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report omits %q — the operator cannot judge the file from it; out:\n%s", want, out.String())
		}
	}

	// --- the consequence -----------------------------------------------------
	after, err := vaultaudit.Run(storage.NewVault(root))
	if err != nil {
		t.Fatalf("audit after: %v", err)
	}
	count := func(r vaultaudit.Report) int {
		n := 0
		for _, f := range r.Findings() {
			if f.Dimension == vaultaudit.DimTaskStatusDirectory {
				n++
			}
		}
		return n
	}
	// The rise must be EXACTLY the headers this run constructed on purpose. The
	// bare-only repair knowingly makes its files visible to this dimension for
	// the first time — that is the pairing the report announces — so a blanket
	// "must not rise" would now be measuring the wrong thing. What must still
	// hold is that the INVERTED file contributed none of it.
	if got, want := count(after), count(before)+sum.AppliedBareOnly; got != want {
		t.Errorf("task-status-directory findings went %d -> %d with %d constructed header(s); "+
			"want exactly %d — anything more means a repair manufactured a finding nobody planned",
			count(before), got, sum.AppliedBareOnly, want)
	}
	for _, f := range after.Findings() {
		if f.Dimension == vaultaudit.DimTaskStatusDirectory && strings.Contains(f.Artifact, "inverted") {
			t.Errorf("the inverted file produced a task-status-directory finding (%s); it was "+
				"never written, so it cannot have gained one", f.Artifact)
		}
	}
}

// TestMigrateTaskHeaderPairedWithTaskStatusReturnsTheAuditToBaseline is the
// acceptance test for the CONSEQUENCE of the bare-only repair, and it is the
// reason the two commands are documented as one operation.
//
// 🔴 The rise is not a bug and the test asserts it happens. DimTaskStatusDirectory
// skips a file whose status is absent — absence is the older format, not a claim —
// so a bare-only file in done/ is invisible to it. Constructing the header makes
// the file's claim readable for the first time, and since none of the legacy
// values is terminal, every constructed header is immediately a rule-1 finding.
// Running the pass alone therefore leaves the audit worse, which is exactly what
// a reviewer would see and mistake for a defect.
//
// The second half closes it. This drives both commands in order and asserts the
// dimension returns to its baseline — and that the relocated prose is still there
// afterwards, which is the pin for why a flattened value was rejected: task-status
// replaces the WHOLE Status value, so prose flattened onto the field would be
// destroyed by the very command that has to run next.
func TestMigrateTaskHeaderPairedWithTaskStatusReturnsTheAuditToBaseline(t *testing.T) {
	root := setupTestVaultEnv(t)
	tsWrite(t, root, "Projects/proj/tasks/done/bareonly.md", thBareOnly)
	wrappedPath := tsWrite(t, root, "Projects/proj/tasks/done/barewrapped.md", thBareOnlyWrapped)
	// The real vault gitignores the advisory lock dir; a test vault that does
	// not is not modelling the thing this test reasons about.
	tsWrite(t, root, ".gitignore", ".vp-locks/\n")
	tsGitInit(t, root)

	vault := storage.NewVault(root)
	count := func(label string) int {
		r, err := vaultaudit.Run(vault)
		if err != nil {
			t.Fatalf("audit %s: %v", label, err)
		}
		n := 0
		for _, f := range r.Findings() {
			if f.Dimension == vaultaudit.DimTaskStatusDirectory {
				n++
			}
		}
		return n
	}

	baseline := count("before")

	var out bytes.Buffer
	sum, err := runTaskHeaderMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("runTaskHeaderMigration: %v", err)
	}
	if sum.Failed != 0 {
		t.Fatalf("Failed = %d, want 0; out:\n%s", sum.Failed, out.String())
	}
	if sum.AppliedBareOnly != 2 {
		t.Fatalf("AppliedBareOnly = %d, want 2; out:\n%s", sum.AppliedBareOnly, out.String())
	}

	// Half one: the findings appear, on purpose.
	if got, want := count("after header"), baseline+sum.AppliedBareOnly; got != want {
		t.Fatalf("findings = %d after constructing %d header(s), want %d — if this ever comes back "+
			"EQUAL to the baseline, the constructed status is not reaching the dimension and the "+
			"pairing below is proving nothing", got, sum.AppliedBareOnly, want)
	}

	// Half two, reached the way the BANNER says to reach it.
	//
	// 🔴 This deliberately does NOT `git add -A`. The paired command gates on a
	// whole-vault clean tree, and the 34 writes half one just made are what
	// dirty it — so a test that cleaned the tree by any means of its own would
	// pass while the printed instruction failed, which is exactly the defect a
	// review found here. The commit below is driven from the paths the banner
	// PRINTS, through the same storage.CommitAndPushPaths that
	// `vp vault commit --paths` calls.
	printed := taskHeaderPrintedCommitPaths(t, out.String())
	if len(printed) <= sum.AppliedBareOnly {
		t.Fatalf("the banner printed %d path(s) for %d written file(s) — it must also name the "+
			".surface stamp each write restamps, or the tree stays dirty:\n%s",
			len(printed), sum.AppliedBareOnly, out.String())
	}
	if _, err := storage.CommitAndPushPaths(root, "construct legacy task headers", printed, false); err != nil {
		t.Fatalf("committing exactly the paths the banner printed failed: %v", err)
	}
	if clean, err := storage.GitStatusClean(root); err != nil || !clean {
		t.Fatalf("the vault is still dirty after committing the printed paths (clean=%v err=%v); "+
			"the paired command will refuse", clean, err)
	}

	var statusOut bytes.Buffer
	ssum, err := runTaskStatusMigration(root, "proj", true, &statusOut)
	if err != nil {
		t.Fatalf("runTaskStatusMigration: %v", err)
	}
	if ssum.Failed != 0 {
		t.Fatalf("task-status Failed = %d, want 0; out:\n%s", ssum.Failed, statusOut.String())
	}
	if got := count("after status"); got != baseline {
		t.Errorf("findings = %d after the paired run, want the baseline %d — the pair does not "+
			"close what the first half opens", got, baseline)
	}

	// --- the reason flattening was rejected ----------------------------------
	after := tsRead(t, wrappedPath)
	if !strings.Contains(after, "**Status:** retired") {
		t.Fatalf("task-status did not stamp the constructed field:\n%s", after)
	}
	for _, want := range []string{
		// The value's FIRST line. Relocating only the overflow hands this one to
		// the field task-status has just replaced, so it would be gone here.
		"Status: In Progress — refined 2026-06-21; two architecture reviews folded into the",
		// ...and its continuation, which flattening would have cost.
		"numbered Phases 2026-06-23 (review #2 below + Grok cross-review); operator-approved.",
	} {
		if !strings.Contains(after, want) {
			t.Errorf("the legacy header did not survive migrate task-status — %q is gone. "+
				"replaceStatusLine replaces the WHOLE Status value, so anything left in that "+
				"field is destroyed by the mandatory second half:\n%s", want, after)
		}
	}
	// The same loss, on a file whose status never wrapped at all.
	plain := tsRead(t, filepath.Join(root, "Projects/proj/tasks/done/bareonly.md"))
	if !strings.Contains(plain, "Status: Planned (reviewed, revised)") {
		t.Errorf("a one-line legacy status was destroyed by the pair; it survives nowhere:\n%s", plain)
	}
	if !strings.Contains(after, "**Priority:** Medium") {
		t.Errorf("the constructed Priority field did not survive the paired run:\n%s", after)
	}
}

// TestMigrateTaskHeaderBareOnlyReportNamesThePrioritySource keeps the operator
// able to review what they are about to authorize. "medium" READ from a file and
// "medium" SUPPLIED for a file that states none are different facts, and only one
// of them is a decision. A report that printed the value alone would hide which.
func TestMigrateTaskHeaderBareOnlyReportNamesThePrioritySource(t *testing.T) {
	root := setupTestVaultEnv(t)
	tsWrite(t, root, "Projects/proj/tasks/done/supplied.md", thBareOnly)
	tsWrite(t, root, "Projects/proj/tasks/done/stated.md", thBareOnlyWrapped)

	var out bytes.Buffer
	if _, err := runTaskHeaderMigration(root, "proj", false, &out); err != nil {
		t.Fatalf("runTaskHeaderMigration: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		string(storage.PriorityFromDefault),
		string(storage.PriorityFromRun),
		"relocating the 1-line legacy run",
		"relocating the 2-line legacy run",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the plan does not report %q; out:\n%s", want, got)
		}
	}
	// Report-only must still say nothing was written.
	if strings.Contains(got, "Applied") {
		t.Errorf("a report-only run claimed to have applied something; out:\n%s", got)
	}
}

// taskHeaderPrintedCommitPaths extracts the --paths argument out of the pairing
// banner THE WAY A SHELL WOULD, and that is the point of it.
//
// The test must consume the same string the operator pastes, not a struct field
// that could be right while the print is wrong. It models two shell rules:
// backslash-newline is a line continuation and disappears, and the indentation
// that follows it does NOT — it survives inside the argument. That second rule
// is what makes the surrounding double quotes load-bearing: without them the
// value word-splits and `--paths` gets only the first entry. Measured before the
// quotes were added: 1 of 68 paths committed, exit 0, paired command still
// refused.
func taskHeaderPrintedCommitPaths(t *testing.T, out string) []string {
	t.Helper()
	_, after, ok := strings.Cut(out, "--paths ")
	if !ok {
		t.Fatalf("the banner names no --paths to commit:\n%s", out)
	}
	after, _, _ = strings.Cut(after, "\n  2.")

	// A quoted value is what survives the paste. Refuse to parse an unquoted one
	// rather than silently reading what the shell would not.
	quoted, ok := strings.CutPrefix(strings.TrimSpace(after), `"`)
	if !ok {
		t.Fatalf("the --paths value is not quoted; a shell would word-split it at the "+
			"continuation indentation and commit only the first entry:\n%s", out)
	}
	value, ok := strings.CutSuffix(strings.TrimSpace(quoted), `"`)
	if !ok {
		t.Fatalf("the --paths value has no closing quote:\n%s", out)
	}

	// What the shell hands the process: continuations removed, indentation kept.
	value = strings.ReplaceAll(value, "\\\n", "")
	var paths []string
	for p := range strings.SplitSeq(value, ",") {
		// The flag parser trims each entry; do the same, and only that.
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// TestMigrateTaskHeaderBannerNamesTheCommitStepAndNotTidy pins the two halves of
// the instruction that a review found unrunnable.
//
// The banner must name a commit step, because migrate task-status refuses the
// dirty tree this command's own writes create. And it must NOT name
// `vp vault tidy` for that step: tidy's sweep rules cover sessions, transcripts,
// KG files, drawers, audits and .surface stamps and carry no Projects/*/tasks/**
// pattern, so it would report these files as dirt and commit none of them.
func TestMigrateTaskHeaderBannerNamesTheCommitStepAndNotTidy(t *testing.T) {
	root := setupTestVaultEnv(t)
	tsWrite(t, root, "Projects/proj/tasks/done/bareonly.md", thBareOnly)
	tsGitInit(t, root)

	var out bytes.Buffer
	if _, err := runTaskHeaderMigration(root, "proj", true, &out); err != nil {
		t.Fatalf("runTaskHeaderMigration: %v", err)
	}
	got := out.String()

	for _, want := range []string{
		"vp vault commit",
		"--paths",
		"Projects/proj/tasks/done/bareonly.md",
		"vp migrate task-status --apply",
		"vp audit vault",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the banner omits %q, so the printed sequence is not the runnable one:\n%s", want, got)
		}
	}
	// The whole-tree caveat: step 1 commits only this run's writes, and step 2
	// gates on the entire vault.
	if !strings.Contains(got, "WHOLE vault tree") {
		t.Errorf("the banner does not warn that step 2 needs the whole tree clean, so an operator "+
			"with unrelated dirt will see exit 2 and blame this command:\n%s", got)
	}
	if strings.Contains(got, "vault tidy") {
		t.Errorf("the banner names `vault tidy` as the commit step; tidy classifies task files as "+
			"REPORTED dirt, never swept, so it would commit nothing:\n%s", got)
	}
	// Order matters: committing after the migration is the whole point.
	commitAt := strings.Index(got, "vp vault commit")
	statusAt := strings.Index(got, "vp migrate task-status --apply")
	auditAt := strings.Index(got, "vp audit vault")
	if !(commitAt < statusAt && statusAt < auditAt) {
		t.Errorf("the three steps are not printed in runnable order (commit=%d status=%d audit=%d):\n%s",
			commitAt, statusAt, auditAt, got)
	}
}

// TestMigrateTaskHeaderReportsThePrioritySourceTally keeps the one FABRICATED
// value in this operation countable in a line rather than greppable across a
// several-hundred-line report. The operator authorized "medium" as a policy for
// the files that state no priority; how many files that turned out to be is the
// number they need to see.
func TestMigrateTaskHeaderReportsThePrioritySourceTally(t *testing.T) {
	root := setupTestVaultEnv(t)
	tsWrite(t, root, "Projects/proj/tasks/done/supplied.md", thBareOnly)
	tsWrite(t, root, "Projects/proj/tasks/done/stated.md", thBareOnlyWrapped)

	var out bytes.Buffer
	sum, err := runTaskHeaderMigration(root, "proj", false, &out)
	if err != nil {
		t.Fatalf("runTaskHeaderMigration: %v", err)
	}
	if got, want := sum.PrioritySources[storage.PriorityFromDefault], 1; got != want {
		t.Errorf("PriorityFromDefault = %d, want %d", got, want)
	}
	if got, want := sum.PrioritySources[storage.PriorityFromRun], 1; got != want {
		t.Errorf("PriorityFromRun = %d, want %d", got, want)
	}
	// The tally must name the fabricated value, not just count it.
	for _, want := range []string{
		"Priority sources:",
		"1 SUPPLIED DEFAULT",
		strconv.Quote(storage.LegacyPriorityDefault),
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the tally omits %q:\n%s", want, out.String())
		}
	}
	// A tally is only useful if it sums to the class.
	total := 0
	for _, n := range sum.PrioritySources {
		total += n
	}
	if total != sum.BareOnly {
		t.Errorf("priority sources sum to %d but BareOnly = %d — a file was constructed without "+
			"its source being counted", total, sum.BareOnly)
	}
}

// TestMigrateTaskHeaderSignsOffTheFileTheClassifierWouldHide is the operator-facing
// half of the trap, and the reason the write decision is keyed on the validator.
//
// 🔴 After the multi-title transform, ScanLegacyHeader reports `clean` for every
// file it touched — INCLUDING the one whose modern header is corrupted
// independently of its titles. A repair keyed on the classifier would write what
// it could, report "0 multi-title remaining", and leave behind a file no tool can
// write and no report mentions. This asserts the opposite: unwritten, named, and
// named with enough detail to act on.
func TestMigrateTaskHeaderSignsOffTheFileTheClassifierWouldHide(t *testing.T) {
	root := setupTestVaultEnv(t)
	trap := tsWrite(t, root, "Projects/proj/tasks/done/mtbadheader.md", thMultiTitleBadHeader)
	sections := tsWrite(t, root, "Projects/proj/tasks/done/mtsections.md", thMultiTitleSections)
	tsWrite(t, root, "Projects/proj/tasks/done/multi.md", thMultiTitle)

	var out bytes.Buffer
	sum, err := runTaskHeaderMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("runTaskHeaderMigration: %v", err)
	}
	got := out.String()

	// Neither sign-off file was written.
	if v := tsRead(t, trap); v != thMultiTitleBadHeader {
		t.Errorf("the corrupted-header file was rewritten:\n%s", v)
	}
	if v := tsRead(t, sections); v != thMultiTitleSections {
		t.Errorf("the H1-as-sections file was rewritten:\n%s", v)
	}
	// A refusal is not a failure: these are declined by design, and counting them
	// as failures would make every run exit non-zero forever.
	if sum.Failed != 0 {
		t.Errorf("Failed = %d, want 0 — a file handed to a human is not a failed attempt", sum.Failed)
	}
	if len(sum.SignOff) != 2 {
		t.Fatalf("SignOff = %d, want 2; out:\n%s", len(sum.SignOff), got)
	}

	// The trap, demonstrated: the classifier would have called the transformed
	// file clean, so nothing keyed on it would ever mention this file.
	demoted := storage.ScanLegacyHeader(thMultiTitleBadHeader)
	if demoted.Class != storage.LegacyHeaderMultiTitle {
		t.Fatalf("fixture classifies as %s", demoted.Class)
	}

	// The report must carry enough to decide WITHOUT opening the file.
	for _, want := range []string{
		"SIGN-OFF REQUIRED",
		"proj/done/mtbadheader",
		"contiguous header block", // the validator's own reason
		"proj/done/mtsections",
		"already opened this document's body", // the structural reason
		"H1  line",                            // every title, with its line number
		"fld line",                            // every field line, with its line number
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the sign-off section omits %q, so the operator cannot judge from the report "+
				"alone:\n%s", want, got)
		}
	}
	// The one repairable file was still written — a sign-off row must not stop
	// the rest of the run.
	if sum.AppliedMultiTitle != 1 {
		t.Errorf("AppliedMultiTitle = %d, want 1; a refused file must not block its siblings",
			sum.AppliedMultiTitle)
	}
}

// TestMigrateTaskHeaderMultiTitleAloneAsksOnlyForACommit pins the split between
// the two writable classes' consequences.
//
// A constructed bare-only header makes files newly visible to
// task-status-directory and OBLIGES the paired migration. A demoted title changes
// no reader's answer and obliges nothing beyond committing. Printing the
// task-status step after a multi-title-only run would send an operator to a
// command with nothing to do.
func TestMigrateTaskHeaderMultiTitleAloneAsksOnlyForACommit(t *testing.T) {
	root := setupTestVaultEnv(t)
	tsWrite(t, root, "Projects/proj/tasks/done/multi.md", thMultiTitle)

	var out bytes.Buffer
	sum, err := runTaskHeaderMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("runTaskHeaderMigration: %v", err)
	}
	if sum.AppliedMultiTitle != 1 || sum.AppliedBareOnly != 0 {
		t.Fatalf("AppliedMultiTitle=%d AppliedBareOnly=%d, want 1/0", sum.AppliedMultiTitle, sum.AppliedBareOnly)
	}
	got := out.String()
	if !strings.Contains(got, "vp vault commit") {
		t.Errorf("a run that rewrote files did not ask for a commit:\n%s", got)
	}
	if strings.Contains(got, "vp migrate task-status --apply") {
		t.Errorf("a multi-title-only run asked for the paired migration, which has nothing to do: "+
			"a demoted title manufactures no task-status-directory finding:\n%s", got)
	}
}

// TestMigrateTaskHeaderMultiTitleMakesNoNewAuditFinding asserts the CONSEQUENCE
// rather than the mechanism, the way the inverted-class test does.
//
// The transform leaves the modern **Status:** first in the file, so scanTaskStatus
// returns the same value it did before and the archived files stay terminal. This
// is the sharp contrast with the bare-only repair, which manufactures one finding
// per file on purpose.
func TestMigrateTaskHeaderMultiTitleMakesNoNewAuditFinding(t *testing.T) {
	root := setupTestVaultEnv(t)
	tsWrite(t, root, "Projects/proj/tasks/done/multi.md", thMultiTitle)
	tsWrite(t, root, "Projects/proj/tasks/done/mtbadheader.md", thMultiTitleBadHeader)

	vault := storage.NewVault(root)
	count := func(label string) int {
		r, err := vaultaudit.Run(vault)
		if err != nil {
			t.Fatalf("audit %s: %v", label, err)
		}
		n := 0
		for _, f := range r.Findings() {
			if f.Dimension == vaultaudit.DimTaskStatusDirectory {
				n++
			}
		}
		return n
	}
	before := count("before")

	var out bytes.Buffer
	if _, err := runTaskHeaderMigration(root, "proj", true, &out); err != nil {
		t.Fatalf("runTaskHeaderMigration: %v", err)
	}
	if got := count("after"); got != before {
		t.Errorf("task-status-directory findings went %d -> %d; the multi-title transform must leave "+
			"the modern Status first and so change no reader's answer", before, got)
	}
}

// TestMigrateTaskHeaderSignOffStatesTheReasonItActuallyHas guards the report
// against asserting the design's central claim about a row where it does not
// hold.
//
// 🔴 The claim is "the write decision is the VALIDATOR's verdict, per file". It is
// true of a file whose transformed bytes the validator refused. It is FALSE of a
// file refused on SHAPE: that refusal happens before the transform runs, so the
// validator never saw any bytes and there was nothing for it to refuse. A blanket
// "each of these is reported because the validator refuses it" tells an operator
// something untrue about half the section.
func TestMigrateTaskHeaderSignOffStatesTheReasonItActuallyHas(t *testing.T) {
	root := setupTestVaultEnv(t)
	tsWrite(t, root, "Projects/proj/tasks/done/mtsections.md", thMultiTitleSections)
	tsWrite(t, root, "Projects/proj/tasks/done/mtbadheader.md", thMultiTitleBadHeader)

	var out bytes.Buffer
	sum, err := runTaskHeaderMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("runTaskHeaderMigration: %v", err)
	}
	if len(sum.SignOff) != 2 {
		t.Fatalf("SignOff = %d, want 2; out:\n%s", len(sum.SignOff), out.String())
	}
	got := out.String()

	// The blanket claim must be gone.
	if strings.Contains(got, "Each is reported because the VALIDATOR") {
		t.Errorf("the section still asserts the validator refused every row, which is false for a "+
			"shape refusal:\n%s", got)
	}
	// Both reasons must be named, and distinguished.
	for _, want := range []string{
		"shape",
		"validator",
		"before any transform ran",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the section does not explain %q as a distinct reason:\n%s", want, got)
		}
	}
	// And each row must say which one applies to IT.
	kinds := map[string]storage.LegacyRefusalKind{}
	for _, so := range sum.SignOff {
		kinds[so.Where] = so.Kind
	}
	if kinds["proj/done/mtsections"] != storage.LegacyRefusedShape {
		t.Errorf("the H1-as-sections row is kind %v, want shape", kinds["proj/done/mtsections"])
	}
	if kinds["proj/done/mtbadheader"] != storage.LegacyRefusedValidator {
		t.Errorf("the corrupted-header row is kind %v, want validator", kinds["proj/done/mtbadheader"])
	}
	for _, so := range sum.SignOff {
		marker := "refused on " + so.Kind.String()
		if !strings.Contains(got, marker) {
			t.Errorf("row %q does not print its own reason (%q):\n%s", so.Where, marker, got)
		}
	}
}
