// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// DimTaskFileValidity reports ARCHIVED task files the whole-file validator refuses.
// Each test below drives exactly one property, against a vault built on disk.
//
// validTask is the shape storage.CreateTask produces: one H1, a contiguous field
// run carrying Status and Priority, and at least one H2.
const validTaskFile = "# T\n\n**Status:** done\n**Priority:** medium\n\n## Context\n\nbody\n"

// TestTaskFileValidity_ArchivedTerminalMalformedIsReported is the regression this
// whole dimension exists for: an archived file whose Status AGREES with its
// directory — so DimTaskStatusDirectory is silent — and whose shape is invalid. No
// dimension reported this file before this one existed.
//
// Break: key the predicate on the Status value instead of the validator. The file
// agrees with its directory, so it produces no finding and the test fails.
func TestTaskFileValidity_ArchivedTerminalMalformedIsReported(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	// Terminal status, agreeing with done/ — and NO H2.
	seedArchivedTask(t, vault, "p", "done", "invisible", "# T\n\n**Status:** done\n**Priority:** medium\n\nbody with no section\n")

	findings, unknowns, err := auditTaskFileValidity(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly the malformed archived file", statusArtifacts(findings))
	}
	if want := "Projects/p/tasks/done/invisible.md"; findings[0].Artifact != want {
		t.Errorf("artifact = %q, want %q", findings[0].Artifact, want)
	}
	if !strings.Contains(findings[0].Detail, "missing section") {
		t.Errorf("detail must carry the validator's own message; got %q", findings[0].Detail)
	}
	// And the sibling dimension must still be silent on it — that is what makes this
	// file the gap rather than a duplicate.
	statusFindings, _, serr := auditTaskStatusDirectory(vault)
	if serr != nil {
		t.Fatal(serr)
	}
	if len(statusFindings) != 0 {
		t.Errorf("task-status-directory must be silent on an agreeing file; got %v", statusArtifacts(statusFindings))
	}
}

// TestTaskFileValidity_RegisteredInTheAudit calls the top-level Run, not the bare
// predicate. It is the only mechanical guard in this package against a predicate
// that is written but wired nowhere; nothing else would catch it.
//
// Break: delete the dimension's row from `var dimensions` in audit.go. The test
// fails with "… is not registered in the audit".
func TestTaskFileValidity_RegisteredInTheAudit(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedArchivedTask(t, vault, "p", "done", "bad", "# T\n\n**Status:** done\n**Priority:** medium\n\nno section\n")

	report, err := Run(vault)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range report.Dimensions {
		if d.Name != DimTaskFileValidity {
			continue
		}
		if len(d.New) != 1 {
			t.Fatalf("registered dimension found %d findings, want 1", len(d.New))
		}
		if d.Evidence == "" {
			t.Error("dimension must carry an evidence command")
		}
		return
	}
	t.Fatalf("%s is not registered in the audit", DimTaskFileValidity)
}

// TestTaskFileValidity_FenceAwareness — this project's task files quote
// metadata-shaped and heading-shaped lines inside code fences constantly. A file
// whose ONLY "## " sits inside a fence has no addressable section and must still be
// reported.
//
// Break: swap the fence-aware scan for a plain line scan (in the validator, or by
// re-implementing the rule here). The fenced "## " counts, the file reads as valid,
// and the test fails.
func TestTaskFileValidity_FenceAwareness(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	body := "# T\n\n**Status:** done\n**Priority:** medium\n\nprose\n\n```md\n## Not A Real Section\n```\n\ntail\n"
	seedArchivedTask(t, vault, "p", "done", "fenced", body)

	findings, _, err := auditTaskFileValidity(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want the file whose only H2 is fenced", statusArtifacts(findings))
	}
	if !strings.Contains(findings[0].Detail, "missing section") {
		t.Errorf("a fenced H2 must not count as a section; got %q", findings[0].Detail)
	}
}

// TestTaskFileValidity_ActiveFilesAreNotReported is the DISJOINTNESS guard. An
// active file with the same defect must produce zero findings from THIS dimension,
// because DimTaskPreamble's PreambleSkippedNoH2 class already reports it — and the
// test asserts that sibling really does report it, so the collision being avoided is
// demonstrated rather than asserted.
//
// Break: add the active directory to this dimension's walk. Both dimensions then
// report the same byte and the test fails.
func TestTaskFileValidity_ActiveFilesAreNotReported(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "activebad", "# T\n\n**Status:** planning\n**Priority:** medium\n\nno section here\n")

	findings, _, err := auditTaskFileValidity(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("active files are out of scope for this dimension; got %v", statusArtifacts(findings))
	}
	// The overlap this exclusion exists to avoid is real, not theoretical.
	preamble, _, perr := auditTaskPreamble(vault)
	if perr != nil {
		t.Fatal(perr)
	}
	var sawIt bool
	for _, f := range preamble {
		if strings.Contains(f.Artifact, "activebad") {
			sawIt = true
		}
	}
	if !sawIt {
		t.Errorf("task-preamble must be the dimension reporting an active no-H2 file, "+
			"or this exclusion is leaving the defect unreported; got %v", statusArtifacts(preamble))
	}
}

// TestTaskFileValidity_ValidArchivedFilesAreSilent is the POSITIVE CONTROL. Without
// it, a predicate that flagged every file would pass every other test here.
//
// Break: make the predicate report every file it reads. The test fails.
func TestTaskFileValidity_ValidArchivedFilesAreSilent(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedArchivedTask(t, vault, "p", "done", "fine", validTaskFile)
	seedArchivedTask(t, vault, "p", "cancelled", "alsofine",
		"# T\n\n**Status:** cancelled\n**Priority:** high\n\n## Context\n\nbody\n")

	findings, unknowns, err := auditTaskFileValidity(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("valid archived files must be silent, got %v", statusArtifacts(findings))
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}
}

// TestTaskFileValidity_UnreadableFileIsUnknownNotFinding pins the THREE-outcome
// contract. "I could not look here" is not "this is clean", and it is also not a
// hard error that would drop the whole dimension.
//
// Break: append the unreadable file to findings instead of unknowns. The test fails.
func TestTaskFileValidity_UnreadableFileIsUnknownNotFinding(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedArchivedTask(t, vault, "p", "done", "fine", validTaskFile)
	seedArchivedTask(t, vault, "p", "done", "locked", validTaskFile)
	locked := filepath.Join(vault.Root, "Projects", "p", "tasks", "done", "locked.md")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Skipf("cannot chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })
	if _, err := os.ReadFile(locked); err == nil {
		t.Skip("running as root: an unreadable file is still readable")
	}

	findings, unknowns, err := auditTaskFileValidity(vault)
	if err != nil {
		t.Fatalf("an unreadable FILE must not fail the whole dimension: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("an unreadable file is not a finding, got %v", statusArtifacts(findings))
	}
	if len(unknowns) != 1 || !strings.Contains(unknowns[0], "locked.md") {
		t.Errorf("unknowns = %v, want the unreadable file", unknowns)
	}
}

// TestTaskFileValidity_ArtifactIsPathNotRule protects the baseline from churning on
// PARTIAL repair. The validator returns on first failure, so a file's failing rule
// moves as it is repaired; the artifact must not move with it, or a half-repaired
// file reads as a NEW finding rather than the same known-bad file.
//
// Break: append the rule name (or a line number) to Artifact. Both halves fail.
func TestTaskFileValidity_ArtifactIsPathNotRule(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	// Two files failing DIFFERENT rules keep path-shaped artifacts.
	seedArchivedTask(t, vault, "p", "done", "noh2", "# T\n\n**Status:** done\n**Priority:** medium\n\nbody\n")
	seedArchivedTask(t, vault, "p", "done", "twotitles", "# T\n\n**Status:** done\n**Priority:** medium\n\n## C\n\n# Second\n")

	findings, _, err := auditTaskFileValidity(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %v, want both malformed files", statusArtifacts(findings))
	}
	byArtifact := map[string]string{}
	for _, f := range findings {
		if !strings.HasSuffix(f.Artifact, ".md") {
			t.Errorf("artifact %q must be the path alone — no rule, no line", f.Artifact)
		}
		if f.Measure != 0 {
			t.Errorf("this dimension is categorical; artifact %q carries Measure %d", f.Artifact, f.Measure)
		}
		byArtifact[f.Artifact] = f.Detail
	}
	// The two really are failing different rules, so the artifacts are not stable by
	// accident of both files being identical.
	d1, d2 := byArtifact["Projects/p/tasks/done/noh2.md"], byArtifact["Projects/p/tasks/done/twotitles.md"]
	if d1 == "" || d2 == "" {
		t.Fatalf("both files must be reported by path; got %v", statusArtifacts(findings))
	}
	if d1 == d2 {
		t.Fatalf("precondition: the two fixtures must fail DIFFERENT rules, or stability is vacuous")
	}

	// PARTIAL REPAIR: fix the first rule, leaving a second defect behind. The artifact
	// must be byte-identical; only the Detail may move.
	seedArchivedTask(t, vault, "p", "done", "twotitles", "# T\n\n**Status:** done\n**Priority:** medium\n**Priority:** high\n\n## C\n\nbody\n")
	after, _, aerr := auditTaskFileValidity(vault)
	if aerr != nil {
		t.Fatal(aerr)
	}
	var found bool
	for _, f := range after {
		if f.Artifact != "Projects/p/tasks/done/twotitles.md" {
			continue
		}
		found = true
		if f.Detail == d2 {
			t.Errorf("precondition: the partial repair must change the failing rule, or this proves nothing")
		}
	}
	if !found {
		t.Error("a partially repaired file must keep the SAME artifact — the baseline keys on it")
	}
}
