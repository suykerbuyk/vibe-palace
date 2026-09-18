// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
)

const tsHeader = "# T\n\n**Status:** In Progress\n**Priority:** medium\n"

// tsVault builds a vault with the three task directories a project carries.
func tsVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"tasks", "tasks/done", "tasks/cancelled"} {
		if err := os.MkdirAll(filepath.Join(root, "Projects", "proj", d), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	return root
}

// tsWrite drops a task file at a vault-relative path and returns its full path.
func tsWrite(t *testing.T, root, rel, body string) string {
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

func tsRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

func tsGitInit(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"},
		{"add", "-A"}, {"commit", "-m", "seed"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable or failed (%v): %s", err, out)
		}
	}
}

// TestMigrateTaskStatusExpectationsMatchTheRealWriter is the anti-second-definition
// pin. This command must repair a file to whatever RetireTask/CancelTask actually
// stamp, not to a value copied into it once and left to rot. Driving both real
// writers and reading the result back keeps the coupling mechanical.
func TestMigrateTaskStatusExpectationsMatchTheRealWriter(t *testing.T) {
	root := tsVault(t)
	vault := storage.NewVault(root)

	tsWrite(t, root, "Projects/proj/tasks/r.md", tsHeader+"\n## Context\n\nBody.\n")
	tsWrite(t, root, "Projects/proj/tasks/c.md", tsHeader+"\n## Context\n\nBody.\n")

	if err := vault.RetireTask("proj", "r"); err != nil {
		t.Fatalf("retire: %v", err)
	}
	if err := vault.CancelTask("proj", "c", ""); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	got := map[string]string{}
	for _, c := range []struct{ dir, slug string }{{"done", "r"}, {"cancelled", "c"}} {
		body := tsRead(t, filepath.Join(root, "Projects", "proj", "tasks", c.dir, c.slug+".md"))
		_, v, ok := findStatusLineOutsideFences(body)
		if !ok {
			t.Fatalf("%s/%s has no Status line after the real writer ran", c.dir, c.slug)
		}
		got[c.dir] = v
	}

	for _, ad := range archiveDirs {
		if got[ad.dir] != ad.status {
			t.Errorf("archiveDirs says %s/ should be %q, but the real writer stamps %q — "+
				"this command would repair files to a value the writer does not use",
				ad.dir, ad.status, got[ad.dir])
		}
	}
}

func TestMigrateTaskStatusReportOnlyWritesNothing(t *testing.T) {
	root := tsVault(t)
	body := tsHeader + "\n## Context\n\nBody.\n"
	p := tsWrite(t, root, "Projects/proj/tasks/done/stale.md", body)

	var out bytes.Buffer
	sum, err := runTaskStatusMigration(root, "proj", false, &out)
	if err != nil {
		t.Fatalf("report run: %v", err)
	}
	if sum.Fix != 1 {
		t.Errorf("Fix = %d, want 1", sum.Fix)
	}
	if sum.Applied != 0 {
		t.Errorf("Applied = %d, want 0 — the bare command must not write", sum.Applied)
	}
	if got := tsRead(t, p); got != body {
		t.Errorf("file was modified by a REPORT run\n got: %q\nwant: %q", got, body)
	}
	if !strings.Contains(out.String(), "REPORT ONLY") {
		t.Errorf("report must say it wrote nothing: %s", out.String())
	}
}

// TestMigrateTaskStatusApplyRepairsAllThreeFlavours is the acceptance gate. A fix
// matching only the literal "In Progress" leaves the other two behind, which is
// the failure the derivation warned about.
func TestMigrateTaskStatusApplyRepairsAllThreeFlavours(t *testing.T) {
	root := tsVault(t)
	files := map[string]string{
		"inprogress": "In Progress",
		"pending":    "pending",
		"freetext":   "Active — execution started 2026-05-09",
	}
	paths := map[string]string{}
	for slug, status := range files {
		paths[slug] = tsWrite(t, root, "Projects/proj/tasks/done/"+slug+".md",
			"# T\n\n**Status:** "+status+"\n**Priority:** medium\n\n## Context\n\nBody.\n")
	}
	// A cancelled file gets the other terminal value, not "done".
	paths["cancelledone"] = tsWrite(t, root, "Projects/proj/tasks/cancelled/cancelledone.md",
		"# T\n\n**Status:** pending\n**Priority:** medium\n\n## Context\n\nBody.\n")
	tsGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskStatusMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if sum.Applied != 4 || sum.Failed != 0 {
		t.Fatalf("Applied = %d, Failed = %d, want 4 and 0; out:\n%s", sum.Applied, sum.Failed, out.String())
	}
	for slug := range files {
		_, v, ok := findStatusLineOutsideFences(tsRead(t, paths[slug]))
		if !ok || v != "done" {
			t.Errorf("done/%s status = %q (found=%v), want \"done\"", slug, v, ok)
		}
	}
	_, v, _ := findStatusLineOutsideFences(tsRead(t, paths["cancelledone"]))
	if v != "cancelled" {
		t.Errorf("cancelled/cancelledone status = %q, want \"cancelled\"", v)
	}
}

// TestMigrateTaskStatusLeavesMissingStatusLineAlone pins the rule that absence is
// the older header format. A tool that "repairs" these turns a handful of writes
// into dozens.
func TestMigrateTaskStatusLeavesMissingStatusLineAlone(t *testing.T) {
	root := tsVault(t)
	body := "# Legacy\n\nNo header block at all.\n\n## Context\n\nBody.\n"
	p := tsWrite(t, root, "Projects/proj/tasks/done/legacy.md", body)
	tsGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskStatusMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if sum.NoStatus != 1 || sum.Fix != 0 || sum.Applied != 0 {
		t.Errorf("NoStatus = %d, Fix = %d, Applied = %d; want 1, 0, 0", sum.NoStatus, sum.Fix, sum.Applied)
	}
	if got := tsRead(t, p); got != body {
		t.Errorf("a file with no Status line was rewritten\n got: %q\nwant: %q", got, body)
	}
	if strings.Contains(out.String(), "legacy") {
		t.Errorf("a no-Status file must not be reported per-file; got:\n%s", out.String())
	}
}

// TestMigrateTaskStatusIgnoresFencedStatusLines uses the real shape found in the
// live vault: a header saying "done" and a fenced sample saying "pending" (the
// legacy specimen this shape was originally measured against pre-rename).
// A fence-blind check reports this file as a disagreement and rewrites the sample.
func TestMigrateTaskStatusIgnoresFencedStatusLines(t *testing.T) {
	root := tsVault(t)
	body := "# T\n\n**Status:** done\n**Priority:** medium\n\n## Context\n\n" +
		"```\n**Status:** pending\n```\n\nBody.\n"
	p := tsWrite(t, root, "Projects/proj/tasks/done/fenced.md", body)
	tsGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskStatusMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if sum.OK != 1 || sum.Fix != 0 || sum.Applied != 0 {
		t.Errorf("OK = %d, Fix = %d, Applied = %d; want 1, 0, 0 — the fenced line is sample text",
			sum.OK, sum.Fix, sum.Applied)
	}
	if got := tsRead(t, p); got != body {
		t.Errorf("fenced sample was rewritten\n got: %q\nwant: %q", got, body)
	}
}

// TestMigrateTaskStatusRefusesASlugPresentInBothDirs guards the resolution order
// hazard: OverwriteTaskFile searches active first, so repairing a shadowed slug
// would edit the ACTIVE file.
func TestMigrateTaskStatusRefusesASlugPresentInBothDirs(t *testing.T) {
	root := tsVault(t)
	activeBody := "# T\n\n**Status:** pending\n**Priority:** medium\n\n## Context\n\nActive.\n"
	doneBody := "# T\n\n**Status:** In Progress\n**Priority:** medium\n\n## Context\n\nArchived.\n"
	activePath := tsWrite(t, root, "Projects/proj/tasks/dup.md", activeBody)
	donePath := tsWrite(t, root, "Projects/proj/tasks/done/dup.md", doneBody)
	tsGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskStatusMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if sum.Failed != 1 || sum.Applied != 0 {
		t.Errorf("Failed = %d, Applied = %d; want 1 and 0; out:\n%s", sum.Failed, sum.Applied, out.String())
	}
	if got := tsRead(t, activePath); got != activeBody {
		t.Errorf("the ACTIVE file was rewritten — this is the hazard the guard exists for\n got: %q", got)
	}
	if got := tsRead(t, donePath); got != doneBody {
		t.Errorf("archived file was rewritten despite the refusal\n got: %q", got)
	}
}

// TestMigrateTaskStatusExitsNonZeroWhenEveryWriteFailed pins the rule that a run
// which wrote NOTHING must not report success. It was written while the sibling
// `migrate task-preamble` printed "  !!" per failure and still returned nil,
// exiting 0 while saying "Applied 0" — the defect this command was built not to
// inherit. That sibling has since been fixed and carries its own equivalent pin,
// so this is now one of two tests on the same rule rather than the only one.
func TestMigrateTaskStatusExitsNonZeroWhenEveryWriteFailed(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	for _, d := range []string{"tasks", "tasks/done"} {
		if err := os.MkdirAll(filepath.Join(vaultDir, "Projects", "proj", d), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	// Two shadowed slugs: both are real disagreements, and both are refused,
	// so every candidate write fails and none succeeds.
	for _, slug := range []string{"dup1", "dup2"} {
		tsWrite(t, vaultDir, "Projects/proj/tasks/"+slug+".md",
			"# T\n\n**Status:** pending\n**Priority:** medium\n\n## Context\n\nActive.\n")
		tsWrite(t, vaultDir, "Projects/proj/tasks/done/"+slug+".md",
			"# T\n\n**Status:** In Progress\n**Priority:** medium\n\n## Context\n\nArchived.\n")
	}
	tsGitInit(t, vaultDir)

	var code int
	out := captureStdout(t, func() {
		code = cmdMigrateTaskStatus().Run([]string{"--project", "proj", "--apply"})
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

// TestMigrateTaskStatusApplyIgnoresAnotherProjectsDirt is THE discriminating test,
// and it is the shape that actually fired on 2026-09-05.
//
// The old precondition was `GitStatusClean` over the whole vault, and a vault holds
// every project. So a vibe-palace migration was refused because a dotfiles task
// file was open in another session — dirt in a directory this command never reads
// and never writes. Neither session was doing anything wrong and no discipline
// inside the migrating project could have prevented it.
//
// 🔴 THE DIRT MUST LIVE IN A DIFFERENT PROJECT. A fixture that dirtied this
// command's OWN target directory would go green under the old whole-tree gate too
// (by refusing) and under the new per-file check (by skipping), so it discriminates
// nothing. The other project is what makes this test able to fail.
func TestMigrateTaskStatusApplyIgnoresAnotherProjectsDirt(t *testing.T) {
	root := tsVault(t)
	target := tsWrite(t, root, "Projects/proj/tasks/done/stale.md", tsHeader+"\n## Context\n\nBody.\n")
	tsWrite(t, root, "Projects/other/tasks/in-flight.md", tsHeader+"\n## Context\n\nCommitted.\n")
	tsGitInit(t, root)

	// Another project, another session, actively being written.
	tsWrite(t, root, "Projects/other/tasks/in-flight.md", tsHeader+"\n## Context\n\nMid-edit.\n")

	var out bytes.Buffer
	sum, err := runTaskStatusMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("another project's dirt must not refuse the run: %v\n%s", err, out.String())
	}
	if sum.Applied != 1 || sum.Dirty != 0 || sum.Failed != 0 {
		t.Fatalf("Applied = %d, Dirty = %d, Failed = %d, want 1/0/0; out:\n%s",
			sum.Applied, sum.Dirty, sum.Failed, out.String())
	}
	if _, v, _ := findStatusLineOutsideFences(tsRead(t, target)); v != "done" {
		t.Errorf("target status = %q, want \"done\"", v)
	}
	// And the other session's work is exactly as it left it.
	if got := tsRead(t, filepath.Join(root, "Projects", "other", "tasks", "in-flight.md")); !strings.Contains(got, "Mid-edit.") {
		t.Errorf("the other project's in-flight file was disturbed:\n%s", got)
	}
}

// TestMigrateTaskStatusSkipsAFileWithUncommittedChanges is the half of the old gate
// that SURVIVED, narrowed to the path it is actually about.
//
// This repair is lossy per file: the value it overwrites exists nowhere in the file
// afterwards and git history is its only copy. So overwriting a file that carries
// uncommitted operator edits destroys work no `git checkout` can separate out. The
// file is skipped and named; every other file in the run is still repaired.
func TestMigrateTaskStatusSkipsAFileWithUncommittedChanges(t *testing.T) {
	root := tsVault(t)
	dirty := tsWrite(t, root, "Projects/proj/tasks/done/dirty.md", tsHeader+"\n## Context\n\nBody.\n")
	clean := tsWrite(t, root, "Projects/proj/tasks/done/clean.md", tsHeader+"\n## Context\n\nBody.\n")
	tsGitInit(t, root)

	edited := tsHeader + "\n## Context\n\nOperator edit in flight.\n"
	tsWrite(t, root, "Projects/proj/tasks/done/dirty.md", edited)

	var out bytes.Buffer
	sum, err := runTaskStatusMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("a dirty FILE is a skip, not a run failure: %v\n%s", err, out.String())
	}
	if sum.Dirty != 1 || sum.Applied != 1 {
		t.Fatalf("Dirty = %d, Applied = %d, want 1 and 1; out:\n%s", sum.Dirty, sum.Applied, out.String())
	}
	// 🔴 A dirty skip is NOT a Failed. The two have opposite remediations — a
	// refused shadow needs a human, a dirty file heals on the operator's next
	// commit — and counting them together is how a report stops saying what to do.
	if sum.Failed != 0 {
		t.Errorf("Failed = %d, want 0: a documented, self-healing skip is not a failure", sum.Failed)
	}
	// The skipped file is byte-identical to what the operator left.
	if got := tsRead(t, dirty); got != edited {
		t.Errorf("the skipped file was rewritten; the operator's edit is gone:\n%s", got)
	}
	if _, v, _ := findStatusLineOutsideFences(tsRead(t, clean)); v != "done" {
		t.Errorf("clean.md status = %q, want \"done\": one dirty file must not stop the run", v)
	}
	// The report has to NAME it, or the operator cannot act on the count.
	got := out.String()
	for _, want := range []string{"SKIP", "Projects/proj/tasks/done/dirty.md", "commit or stash"} {
		if !strings.Contains(got, want) {
			t.Errorf("the report omits %q, so the skip is a number with no remedy:\n%s", want, got)
		}
	}
}

// TestMigrateTaskStatusApplyRefusesANonGitVault pins the floor the narrowing kept.
// Without git there is no copy of the prior status ANYWHERE, so the recoverability
// argument has nothing to stand on and --apply refuses outright.
func TestMigrateTaskStatusApplyRefusesANonGitVault(t *testing.T) {
	root := tsVault(t)
	tsWrite(t, root, "Projects/proj/tasks/done/stale.md", tsHeader+"\n## Context\n\nBody.\n")

	var out bytes.Buffer
	if _, err := runTaskStatusMigration(root, "proj", true, &out); err == nil {
		t.Fatal("--apply on a non-git vault must refuse: git history is the only copy of the value being overwritten")
	}
}

// TestMigrateTaskStatusRollbackIsScopedToTheWriteSet is the rollback half of the
// acceptance, and it drives the PRINTED paths through a real `git checkout --`
// rather than asserting on the text.
//
// 🔴 THE CRITERION IS "DOES NOT REVERT ANYONE ELSE'S WORK", NOT "LEAVES NO
// HALF-WRITTEN FILE". The second is a property of atomicfile's tmp+fsync+rename: it
// is true today, was true under the old whole-tree gate, and would be true if the
// gate were deleted outright — so a test asserting it goes green whatever ships and
// discriminates nothing. What the narrowing can actually break is the SCOPE of the
// undo, which is what this measures: the run's own writes come back, and another
// session's in-flight work is untouched by the rollback the operator was handed.
func TestMigrateTaskStatusRollbackIsScopedToTheWriteSet(t *testing.T) {
	root := tsVault(t)
	target := tsWrite(t, root, "Projects/proj/tasks/done/stale.md", tsHeader+"\n## Context\n\nBody.\n")
	other := tsWrite(t, root, "Projects/other/tasks/in-flight.md", tsHeader+"\n## Context\n\nCommitted.\n")
	// A stamp BEHIND the binary, tracked from the start — the live-vault shape.
	// Behind, so the writer actually rewrites it and the rollback has something to
	// restore; tracked, so `git checkout --` can accept it as a pathspec.
	stamp := tsWrite(t, root, "Projects/proj/.surface", "surface = 1\n")
	// tsGitInit already stages and commits everything, so the tree starts clean.
	tsGitInit(t, root)
	stampBefore := tsRead(t, stamp)

	before := tsRead(t, target)
	inFlight := tsHeader + "\n## Context\n\nAnother session, mid-edit.\n"
	tsWrite(t, root, "Projects/other/tasks/in-flight.md", inFlight)

	var out bytes.Buffer
	sum, err := runTaskStatusMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v\n%s", err, out.String())
	}
	if sum.Applied != 1 {
		t.Fatalf("Applied = %d, want 1; out:\n%s", sum.Applied, out.String())
	}
	if len(sum.AppliedPaths) == 0 {
		t.Fatal("the run recorded no written paths, so the printed rollback would be empty")
	}
	// The stamp the locked writer touched belongs in the list: it is a byte this
	// run wrote, and an undo that omitted it leaves the stamp advanced.
	if !slices.Contains(sum.AppliedPaths, "Projects/proj/.surface") {
		t.Errorf("AppliedPaths = %v, missing the .surface stamp the writer touches", sum.AppliedPaths)
	}
	if tsRead(t, target) == before {
		t.Fatal("the target was not rewritten, so the rollback proves nothing")
	}

	// Run exactly the undo the banner hands the operator.
	args := append([]string{"-C", root, "checkout", "--"}, sum.AppliedPaths...)
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if co, cerr := cmd.CombinedOutput(); cerr != nil {
		t.Fatalf("the printed rollback does not run: %v: %s", cerr, co)
	}

	if got := tsRead(t, target); got != before {
		t.Errorf("the scoped rollback did not restore the file this run wrote:\n%s", got)
	}
	if got := tsRead(t, stamp); got != stampBefore {
		t.Errorf("the scoped rollback left the .surface stamp advanced:\n got: %q\nwant: %q", got, stampBefore)
	}
	// 🔴 The whole point of naming paths instead of `.`: the other session's
	// in-flight work survives the undo.
	if got := tsRead(t, other); got != inFlight {
		t.Errorf("the rollback reverted another session's in-flight work:\n%s", got)
	}

	// And the banner prints that list, scoped, without offering the whole-tree form.
	report := out.String()
	if !strings.Contains(report, "git -C "+root+" checkout --") {
		t.Errorf("the report does not print a runnable scoped rollback:\n%s", report)
	}
	if !strings.Contains(report, "Projects/proj/tasks/done/stale.md") {
		t.Errorf("the rollback list does not name the file it wrote:\n%s", report)
	}
	if strings.Contains(report, "git checkout .\n") {
		t.Errorf("the report offers a whole-tree checkout, which reverts other sessions' work:\n%s", report)
	}
}

// TestRepairPopulationMatchesTheDetector is the agreement test, and it is the
// point of routing both tools through internal/storage.
//
// This command exists to drive DimTaskStatusDirectory rule 1 to zero. If the two
// disagree about which archived files are wrong, the repair either leaves red
// behind or edits files the audit never flagged. Pinning them over TODAY's vault
// proves nothing — the live data happens to contain none of the divergent shapes,
// so a string-equality repair and a terminality detector score identically on it.
//
// The fixture is therefore built from exactly the inputs where a near-copy
// diverges. Each row names what a `found == want` repair would have done wrong.
func TestRepairPopulationMatchesTheDetector(t *testing.T) {
	root := tsVault(t)

	body := func(status string) string {
		return "# T\n\n**Status:** " + status + "\n**Priority:** medium\n\n## Context\n\nBody.\n"
	}

	// rel -> whether rule 1 should flag it (and therefore whether we repair it).
	cases := []struct {
		rel     string
		content string
		wantFix bool
		why     string
	}{
		{"Projects/proj/tasks/done/plain.md", body("In Progress"), true,
			"the ordinary stale case both definitions agree on"},
		{"Projects/proj/tasks/done/oddcase.md", body("Done"), false,
			"terminal but oddly cased — string equality would REWRITE a file the audit calls clean"},
		{"Projects/proj/tasks/done/othertermnal.md", body("cancelled"), false,
			"the OTHER terminal value in done/ — string equality would rewrite it to done, " +
				"mutating a file wholly outside the detector's remit"},
		{"Projects/proj/tasks/done/trailingws.md", body("done   "), false,
			"trailing whitespace — the detector trims, string equality does not"},
		{"Projects/proj/tasks/done/nostatus.md",
			"# Legacy\n\nNo header block.\n\n## Context\n\nBody.\n", false,
			"absent status is the older format, excluded by ruling on both sides"},
		{"Projects/proj/tasks/done/fenced.md",
			"# T\n\n**Status:** done\n**Priority:** medium\n\n## Context\n\n" +
				"```\n**Status:** planning\n```\n\nBody.\n", false,
			"a fenced sample is not metadata — both sides walk OutsideFences"},
		{"Projects/proj/tasks/cancelled/stale.md", body("pending"), true,
			"stale in cancelled/, repaired to StatusCancelled not StatusDone"},
		{"Projects/proj/tasks/cancelled/oddcase.md", body("CANCELLED"), false,
			"terminal, oddly cased, in the matching dir"},
	}
	for _, c := range cases {
		tsWrite(t, root, c.rel, c.content)
	}
	// An ACTIVE file carrying a terminal status is rule 2, never rule 1, and must
	// not appear in the repair population at all.
	tsWrite(t, root, "Projects/proj/tasks/interrupted.md", body("done"))

	// --- the repair's population -------------------------------------------
	var out bytes.Buffer
	sum, err := runTaskStatusMigration(root, "proj", false, &out)
	if err != nil {
		t.Fatalf("report run: %v", err)
	}
	repair := map[string]bool{}
	for _, p := range sum.Plans {
		repair["Projects/"+p.Project+"/tasks/"+p.Dir+"/"+p.Slug+".md"] = true
	}

	// --- the detector's rule-1 population -----------------------------------
	report, err := vaultaudit.Run(storage.NewVault(root))
	if err != nil {
		t.Fatalf("audit run: %v", err)
	}
	detector := map[string]bool{}
	for _, f := range report.Findings() {
		if f.Dimension != vaultaudit.DimTaskStatusDirectory {
			continue
		}
		// Artifact is "<rel>:<line>"; rule 1 is the archived half, rule 2 the active.
		rel := f.Artifact
		if i := strings.LastIndex(rel, ":"); i > 0 {
			rel = rel[:i]
		}
		if !strings.Contains(rel, "/tasks/done/") && !strings.Contains(rel, "/tasks/cancelled/") {
			continue // rule 2 — the interrupted-archive signature, not this repair's job
		}
		detector[rel] = true
	}

	// --- they must be identical ---------------------------------------------
	for _, c := range cases {
		inRepair, inDetector := repair[c.rel], detector[c.rel]
		if inRepair != c.wantFix {
			t.Errorf("repair population: %s in=%v want=%v — %s", c.rel, inRepair, c.wantFix, c.why)
		}
		if inDetector != c.wantFix {
			t.Errorf("detector population: %s in=%v want=%v — %s", c.rel, inDetector, c.wantFix, c.why)
		}
		if inRepair != inDetector {
			t.Errorf("DRIFT on %s: repair=%v detector=%v — %s", c.rel, inRepair, inDetector, c.why)
		}
	}
	if repair["Projects/proj/tasks/interrupted.md"] {
		t.Error("the repair claimed an ACTIVE file; rule 2 is repaired by completing the rename, not by rewriting")
	}
	if len(repair) != len(detector) {
		t.Errorf("population sizes differ: repair=%d detector=%d\nrepair=%v\ndetector=%v",
			len(repair), len(detector), repair, detector)
	}
}

// TestMigrateTaskStatusLeavesTheOtherTerminalValueAlone is the behaviour change
// the agreement test implies, asserted on its own so it cannot be lost in a
// refactor: a done/ file reading "cancelled" is already terminal, so it AGREES in
// the only sense the detector recognises and must not be rewritten to "done".
func TestMigrateTaskStatusLeavesTheOtherTerminalValueAlone(t *testing.T) {
	root := tsVault(t)
	body := "# T\n\n**Status:** cancelled\n**Priority:** medium\n\n## Context\n\nBody.\n"
	p := tsWrite(t, root, "Projects/proj/tasks/done/mixed.md", body)
	tsGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskStatusMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}
	if sum.OK != 1 || sum.Fix != 0 || sum.Applied != 0 {
		t.Errorf("OK = %d, Fix = %d, Applied = %d; want 1, 0, 0", sum.OK, sum.Fix, sum.Applied)
	}
	if got := tsRead(t, p); got != body {
		t.Errorf("a repair rewrote history the audit does not flag\n got: %q\nwant: %q", got, body)
	}
}

// TestMigrateTaskStatusRollbackOmitsAnUntrackedStamp is the pin on the one path
// that would turn the printed undo into a no-op.
//
// 🔴 `git checkout -- a b c` IS ALL-OR-NOTHING OVER ITS PATHSPECS. A project
// written into for the first time has no committed `.surface`, and naming an
// untracked path makes the WHOLE command a pathspec error — restoring none of the
// task files either, while an operator who pasted it believes the run was undone.
// So the rollback list carries only what git can act on, and the newly created
// stamp (which has no committed state to return to, and is regenerable) is left
// out deliberately.
//
// This is measured by RUNNING the printed command, not by reading the list: the
// list looking right is exactly the failure mode.
func TestMigrateTaskStatusRollbackOmitsAnUntrackedStamp(t *testing.T) {
	root := tsVault(t)
	target := tsWrite(t, root, "Projects/proj/tasks/done/stale.md", tsHeader+"\n## Context\n\nBody.\n")
	// No .surface committed: the migration's own write creates it.
	tsGitInit(t, root)
	before := tsRead(t, target)

	var out bytes.Buffer
	sum, err := runTaskStatusMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("apply run: %v\n%s", err, out.String())
	}
	if sum.Applied != 1 {
		t.Fatalf("Applied = %d, want 1; out:\n%s", sum.Applied, out.String())
	}
	if slices.Contains(sum.AppliedPaths, "Projects/proj/.surface") {
		t.Fatalf("AppliedPaths names an untracked stamp (%v); `git checkout --` would fail on it and "+
			"restore nothing", sum.AppliedPaths)
	}

	args := append([]string{"-C", root, "checkout", "--"}, sum.AppliedPaths...)
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if co, cerr := cmd.CombinedOutput(); cerr != nil {
		t.Fatalf("the printed rollback does not run: %v: %s", cerr, co)
	}
	if got := tsRead(t, target); got != before {
		t.Errorf("the rollback did not restore the task file:\n%s", got)
	}
}

// TestMigrateTaskStatusRefusesAShadowedArchivedPairAndDestroysNothing is this
// unit's defining assertion, and it asserts BYTES.
//
// 🔴 THE SUITE ALREADY BUILT THIS SHAPE AND COULD NOT SEE IT, FOR TWO
// INDEPENDENT REASONS, AND FIXING EITHER ALONE WOULD STILL LEAVE IT BLIND.
// TestRepairPopulationMatchesTheDetector is the only test that seeds a shadowed
// done/+cancelled/ pair (`oddcase`), and BOTH halves carry wantFix:false -- they
// are the deliberately-CLEAN oddly-cased fixtures, so neither is ever written and
// the mis-resolution never occurs. Meanwhile the file that IS repaired in that
// table has no twin in that vault. And that test compares PLAN SETS, not bytes,
// in report mode -- so even repositioned it could not observe a write landing on
// the wrong file. The fixtures build the dangerous shape and the dangerous
// condition in the same file, on the same two slug names, and never in the same
// vault. Nobody erred; the halves were split across fixtures each correct for its
// own purpose.
//
// 🔴 ASSERTING THE STATUS PROPERTY IS THE TRAP. Before the guard, this run
// replaced done/pair.md's ENTIRE BODY with cancelled/pair.md's and printed
// "Applied 2 rewrite(s)." at exit 0. Both variants were MEASURED against that
// break, and they do not agree:
//
//   - asserting IsTerminalStatus(done/'s status)  -> PASSES on the destroyed file
//   - asserting the literal "**Status:** done"    -> fails, and catches it
//
// The property assertion is the one a reviewer accepts as sufficient, and it is
// the one that goes green: the mis-resolved write does set a valid terminal
// status, just on the wrong file and after replacing its body.
//
// 🔴 DO NOT ADD THAT THE LITERAL "ONLY CATCHES IT BY LUCK". That sentence was
// here and is DELETED, not softened: it is not constructible for this command.
// archiveDirs is a fixed two-entry literal and a shadow requires two DIFFERENT
// directories, so the two selected files always carry different targets and the
// literal always catches it. The reversed-order route fails too -- after the
// first mis-resolution the file already reads "cancelled", which IsTerminalStatus
// accepts, so the scan skips it.
//
// This block exists to stop someone re-weakening the assertion below, which
// makes it the one place a stale claim does real damage.
//
// The assertion is on BYTES because the PROPERTY form goes green. The bodies
// below are distinguishable for exactly that reason.
func TestMigrateTaskStatusRefusesAShadowedArchivedPairAndDestroysNothing(t *testing.T) {
	root := tsVault(t)
	// 🔴 THE TWO FILES DIFFER IN EVERY FIELD A WHOLE-FILE REPLACEMENT WOULD CARRY:
	// body AND Priority, not just Status. The destroyed file gets the wrong
	// Priority and the wrong body as well as the wrong status, and an assertion
	// naming only the status is how the weak form creeps back in.
	doneBody := "# T\n\n**Status:** retired\n**Priority:** high\n\n## Context\n\nI am the DONE copy.\n"
	cancBody := "# T\n\n**Status:** retired\n**Priority:** low\n\n## Context\n\nI am the CANCELLED copy.\n"
	// 🔴 ANTI-VACUITY. The byte assertions below are only meaningful while the two
	// fixtures are distinguishable. Give them the same body and the mis-resolved
	// write still happens -- Applied = 2, done/ ends up **Status:** cancelled --
	// but both halves of the byte check go inert, and the test then goes red only
	// via Failed and the refusal message: passing for exactly the reason the
	// comment above rejects, with the assertion it calls "the check" doing
	// nothing.
	if doneBody == cancBody {
		t.Fatal("the two fixtures must differ in body: identical bodies make every byte " +
			"assertion in this test vacuous while the defect still reproduces")
	}
	donePath := tsWrite(t, root, "Projects/proj/tasks/done/pair.md", doneBody)
	cancPath := tsWrite(t, root, "Projects/proj/tasks/cancelled/pair.md", cancBody)
	tsGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskStatusMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("runTaskStatusMigration: %v", err)
	}

	// --- the refusal names the real cause -----------------------------------
	if sum.Failed != 1 {
		t.Errorf("Failed = %d, want 1 (the shadowed cancelled/ copy); out:\n%s", sum.Failed, out.String())
	}
	if !strings.Contains(out.String(), "the same slug also exists in tasks/done/") {
		t.Errorf("the refusal must name the shadowed pair and the resolver order; out:\n%s", out.String())
	}
	if strings.Contains(out.String(), "an ACTIVE task of the same slug exists") {
		t.Errorf("the refusal blamed an ACTIVE twin that does not exist -- false cause; out:\n%s", out.String())
	}

	// --- leg 1: the SHADOWED file is refused, byte-identical ------------------
	if got := tsRead(t, cancPath); got != cancBody {
		t.Fatalf("the refused cancelled/ copy was written:\n%s", got)
	}

	// --- leg 2: the RESOLVER-WINNING file is REPAIRED, and keeps its OWN -----
	// 🔴 done/ IS NAMED EXPLICITLY, AND SO ARE ITS FIELDS. It is the file the
	// destruction lands on, and leg 1 alone is GREEN ON A DESTROYED VAULT --
	// measured: with the guard removed, cancelled/pair.md is byte-identical to
	// its seed while done/pair.md has been replaced wholesale.
	done := tsRead(t, donePath)
	if !strings.Contains(done, "**Status:** done") {
		t.Errorf("done/ was not repaired to its own directory's terminal value:\n%s", done)
	}
	if !strings.Contains(done, "**Priority:** high") {
		t.Errorf("done/ lost its OWN Priority -- a whole-file replacement carries the "+
			"shadow's Priority too:\n%s", done)
	}
	if !strings.Contains(done, "I am the DONE copy.") {
		t.Errorf("done/ lost its OWN body -- this is the destruction this unit exists to "+
			"prevent:\n%s", done)
	}

	// --- leg 3: neither file received the OTHER's content --------------------
	if strings.Contains(done, "I am the CANCELLED copy.") || strings.Contains(done, "**Priority:** low") {
		t.Fatalf("done/ received the cancelled/ copy's content:\n%s", done)
	}
	if got := tsRead(t, cancPath); strings.Contains(got, "I am the DONE copy.") {
		t.Fatalf("cancelled/ received the done/ copy's content:\n%s", got)
	}
}

// TestMigrateTaskStatusStillRepairsAnUnshadowedArchivedFile is the over-refusal
// check, and a SEPARATE test rather than a branch of the one above: an
// over-broad guard would refuse the entire population and a branch inside the
// refusal test would be deleted along with it.
func TestMigrateTaskStatusStillRepairsAnUnshadowedArchivedFile(t *testing.T) {
	root := tsVault(t)
	p := tsWrite(t, root, "Projects/proj/tasks/done/lonely.md",
		"# T\n\n**Status:** retired\n**Priority:** medium\n\n## Context\n\nBody.\n")
	tsGitInit(t, root)

	var out bytes.Buffer
	sum, err := runTaskStatusMigration(root, "proj", true, &out)
	if err != nil {
		t.Fatalf("runTaskStatusMigration: %v", err)
	}
	if sum.Failed != 0 {
		t.Fatalf("Failed = %d on a file with no twin anywhere; out:\n%s", sum.Failed, out.String())
	}
	if !strings.Contains(tsRead(t, p), "**Status:** done") {
		t.Errorf("an unshadowed archived file was not repaired:\n%s", tsRead(t, p))
	}
}

// TestMigrateTaskStatusReportDoesNotPromiseAFixItWillRefuse pins the FIX row's
// position relative to the shadow guard.
//
// 🔴 A FIX ROW IS A PROMISE --apply WILL WRITE THAT FILE. This command used to
// print it before the guard ran, so a shadowed slug produced a FIX row and a
// refusal two lines apart and the operator had to reconcile them. Moving the
// print below the guard was a deliberate change with no assertion behind it --
// a reviewer moved it back and nothing went red -- which made it a preference
// rather than a pinned behaviour. This is that assertion.
//
// Same class as TestMigrateTaskHeaderReportAndApplyAgree in the Both-merge unit.
func TestMigrateTaskStatusReportDoesNotPromiseAFixItWillRefuse(t *testing.T) {
	seed := func(t *testing.T) string {
		root := tsVault(t)
		// Shadowed pair: done/ is repairable, cancelled/ is refused.
		tsWrite(t, root, "Projects/proj/tasks/done/pair.md",
			"# T\n\n**Status:** retired\n**Priority:** high\n\n## Context\n\nDONE.\n")
		tsWrite(t, root, "Projects/proj/tasks/cancelled/pair.md",
			"# T\n\n**Status:** retired\n**Priority:** low\n\n## Context\n\nCANCELLED.\n")
		tsGitInit(t, root)
		return root
	}

	var report bytes.Buffer
	if _, err := runTaskStatusMigration(seed(t), "proj", false, &report); err != nil {
		t.Fatalf("report run: %v", err)
	}
	var applied bytes.Buffer
	asum, err := runTaskStatusMigration(seed(t), "proj", true, &applied)
	if err != nil {
		t.Fatalf("apply run: %v", err)
	}

	// 🔴 FLOOR FIRST. "FIX count == Applied" is 0 == 0 for a command that selected
	// NOTHING, so without this the test passes vacuously -- proven by making the
	// guard refuse everything and watching it stay green. This unit exists because
	// a defect hid behind tests that could not fail; its own tests must be able to.
	// TestMigrateTaskHeaderReportAndApplyAgree carries the same precondition.
	if asum.Applied != 1 {
		t.Fatalf("Applied = %d, want 1 (the repairable done/ copy) -- without a nonzero floor "+
			"the comparison below is 0 == 0 and cannot fail; out:\n%s", asum.Applied, applied.String())
	}

	// Every FIX row the report printed must be a file --apply actually wrote.
	if got := strings.Count(report.String(), "  FIX   "); got != asum.Applied {
		t.Errorf("report printed %d FIX row(s) but --apply wrote %d file(s); a FIX row is a "+
			"promise the apply must honour\nreport:\n%s\napply:\n%s",
			got, asum.Applied, report.String(), applied.String())
	}
	// And specifically: no FIX row for the file the guard refuses.
	for _, line := range strings.Split(report.String(), "\n") {
		if strings.Contains(line, "FIX") && strings.Contains(line, "cancelled/") {
			t.Errorf("report promised a FIX for the shadowed cancelled/ copy, which --apply "+
				"refuses:\n%s", report.String())
		}
	}
}
