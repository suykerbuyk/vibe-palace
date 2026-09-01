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
	if err := vault.CancelTask("proj", "c"); err != nil {
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
	// A cancelled file gets the other terminal value, not "retired".
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
		if !ok || v != "retired" {
			t.Errorf("done/%s status = %q (found=%v), want \"retired\"", slug, v, ok)
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
// live vault: a header saying "retired" and a fenced sample saying "pending".
// A fence-blind check reports this file as a disagreement and rewrites the sample.
func TestMigrateTaskStatusIgnoresFencedStatusLines(t *testing.T) {
	root := tsVault(t)
	body := "# T\n\n**Status:** retired\n**Priority:** medium\n\n## Context\n\n" +
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

// TestMigrateTaskStatusExitsNonZeroWhenEveryWriteFailed is the class the sibling
// `migrate task-preamble` does not cover: it prints "  !!" per failure and still
// returns nil, so a run that wrote NOTHING exits 0 and says "Applied 0". This
// command must not inherit that.
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

// TestMigrateTaskStatusApplyRefusesDirtyVault matches the sibling's guard: under
// --apply the tree must be clean so `git checkout .` is a guaranteed rollback.
func TestMigrateTaskStatusApplyRefusesDirtyVault(t *testing.T) {
	root := tsVault(t)
	tsWrite(t, root, "Projects/proj/tasks/done/stale.md", tsHeader+"\n## Context\n\nBody.\n")
	tsGitInit(t, root)
	tsWrite(t, root, "Projects/proj/tasks/done/stale.md", tsHeader+"\n## Context\n\nDirty.\n")

	var out bytes.Buffer
	if _, err := runTaskStatusMigration(root, "proj", true, &out); err == nil {
		t.Fatal("apply on a dirty vault tree must refuse")
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
		{"Projects/proj/tasks/done/oddcase.md", body("Retired"), false,
			"terminal but oddly cased — string equality would REWRITE a file the audit calls clean"},
		{"Projects/proj/tasks/done/othertermnal.md", body("cancelled"), false,
			"the OTHER terminal value in done/ — string equality would rewrite it to retired, " +
				"mutating a file wholly outside the detector's remit"},
		{"Projects/proj/tasks/done/trailingws.md", body("retired   "), false,
			"trailing whitespace — the detector trims, string equality does not"},
		{"Projects/proj/tasks/done/nostatus.md",
			"# Legacy\n\nNo header block.\n\n## Context\n\nBody.\n", false,
			"absent status is the older format, excluded by ruling on both sides"},
		{"Projects/proj/tasks/done/fenced.md",
			"# T\n\n**Status:** retired\n**Priority:** medium\n\n## Context\n\n" +
				"```\n**Status:** pending\n```\n\nBody.\n", false,
			"a fenced sample is not metadata — both sides walk OutsideFences"},
		{"Projects/proj/tasks/cancelled/stale.md", body("pending"), true,
			"stale in cancelled/, repaired to StatusCancelled not StatusRetired"},
		{"Projects/proj/tasks/cancelled/oddcase.md", body("CANCELLED"), false,
			"terminal, oddly cased, in the matching dir"},
	}
	for _, c := range cases {
		tsWrite(t, root, c.rel, c.content)
	}
	// An ACTIVE file carrying a terminal status is rule 2, never rule 1, and must
	// not appear in the repair population at all.
	tsWrite(t, root, "Projects/proj/tasks/interrupted.md", body("retired"))

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
// the only sense the detector recognises and must not be rewritten to "retired".
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
