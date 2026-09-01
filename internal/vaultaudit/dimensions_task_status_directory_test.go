// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// DimTaskStatusDirectory has TWO rules that must break independently, plus a third
// class that must never break at all. Each test below drives exactly one of them,
// against a vault built on disk rather than a canned string — workflow.md, verify
// against the live vault, not fixtures.

// seedArchivedTask writes a task into done/ or cancelled/, which seedTask cannot do.
func seedArchivedTask(t *testing.T, vault *storage.Vault, project, sub, slug, body string) {
	t.Helper()
	mkdirs(t, vault.Root, "Projects", project)
	writeFile(t, vault.Root, "Projects/"+project+"/tasks/"+sub+"/"+slug+".md", body)
}

func statusArtifacts(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Artifact)
	}
	return out
}

// TestTaskStatusDirectory_Rule1_ArchivedNonTerminal is the original finding: a file
// in the archive whose body still claims a live state.
func TestTaskStatusDirectory_Rule1_ArchivedNonTerminal(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedArchivedTask(t, vault, "p", "done", "stale", "# T\n\n**Status:** pending\n**Priority:** medium\n\nbody\n")

	findings, unknowns, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly the archived non-terminal file", statusArtifacts(findings))
	}
	if want := "Projects/p/tasks/done/stale.md:3"; findings[0].Artifact != want {
		t.Errorf("artifact = %q, want %q", findings[0].Artifact, want)
	}
	if !strings.Contains(findings[0].Detail, "non-terminal status") {
		t.Errorf("detail must name the rule it broke; got %q", findings[0].Detail)
	}
	// A rule-1 finding must not be mistaken for the crash signature.
	if strings.Contains(findings[0].Detail, "COMPLETING THE RENAME") {
		t.Errorf("rule-1 detail must not carry rule-2's repair; got %q", findings[0].Detail)
	}
}

// TestTaskStatusDirectory_Rule1_CoversCancelledToo — cancelled/ is archived on the
// same footing as done/. Asserted explicitly rather than trusted to the loop.
func TestTaskStatusDirectory_Rule1_CoversCancelledToo(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedArchivedTask(t, vault, "p", "cancelled", "stale", "# T\n\n**Status:** in_progress\n\nbody\n")

	findings, _, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || !strings.HasPrefix(findings[0].Artifact, "Projects/p/tasks/cancelled/") {
		t.Fatalf("findings = %v, want the cancelled/ file", statusArtifacts(findings))
	}
}

// TestTaskStatusDirectory_Rule2_ActiveTerminal is the interrupted-archive signature
// created by rewrite-then-rename (2026-09-01): the stamp landed, the rename did not.
func TestTaskStatusDirectory_Rule2_ActiveTerminal(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "midretire", "# T\n\n**Status:** retired\n**Priority:** medium\n\nbody\n")

	findings, unknowns, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly the active terminal file", statusArtifacts(findings))
	}
	if want := "Projects/p/tasks/midretire.md:3"; findings[0].Artifact != want {
		t.Errorf("artifact = %q, want %q", findings[0].Artifact, want)
	}
	// The whole value of rule 2 over "these disagree" is that it says what to do.
	if !strings.Contains(findings[0].Detail, "COMPLETING THE RENAME") {
		t.Errorf("rule-2 detail must state the repair; got %q", findings[0].Detail)
	}
	// And it must warn against the tempting wrong repair.
	if !strings.Contains(findings[0].Detail, "Do not instead") {
		t.Errorf("rule-2 detail must rule out rewriting the status back; got %q", findings[0].Detail)
	}
}

// TestTaskStatusDirectory_RulesAreIndependent proves neither rule is doing the
// other's work: one vault, one specimen of each, two findings with distinct details.
func TestTaskStatusDirectory_RulesAreIndependent(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedArchivedTask(t, vault, "p", "done", "archived-live", "# T\n\n**Status:** pending\n")
	seedTask(t, vault, "p", "active-dead", "# T\n\n**Status:** cancelled\n")

	findings, _, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %v, want one per rule", statusArtifacts(findings))
	}
	var sawArchived, sawActive bool
	for _, f := range findings {
		switch {
		case strings.Contains(f.Artifact, "/done/"):
			sawArchived = true
			if !strings.Contains(f.Detail, "non-terminal status") {
				t.Errorf("archived finding carries the wrong detail: %q", f.Detail)
			}
		default:
			sawActive = true
			if !strings.Contains(f.Detail, "COMPLETING THE RENAME") {
				t.Errorf("active finding carries the wrong detail: %q", f.Detail)
			}
		}
	}
	if !sawArchived || !sawActive {
		t.Errorf("both rules must fire: archived=%v active=%v", sawArchived, sawActive)
	}
}

// TestTaskStatusDirectory_AgreeingFilesAreSilent is the POSITIVE CONTROL. A
// dimension that flagged everything would pass every test above while making the
// audit useless.
func TestTaskStatusDirectory_AgreeingFilesAreSilent(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "live", "# T\n\n**Status:** pending\n")
	seedTask(t, vault, "p", "iced", "# T\n\n**Status:** icebox\n")
	seedArchivedTask(t, vault, "p", "done", "gone", "# T\n\n**Status:** retired\n")
	seedArchivedTask(t, vault, "p", "cancelled", "dropped", "# T\n\n**Status:** cancelled\n")

	findings, _, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("agreeing files must produce no findings, got %v", statusArtifacts(findings))
	}
}

// TestTaskStatusDirectory_ValueMatchingIsCaseInsensitive covers the real corpus,
// which spells its values inconsistently: "In Progress" alongside "in_progress".
//
// The KEY stays case-sensitive — see storage.IsTerminalStatus for why folding the
// value is not the iteration-347 defect and folding the key would be.
func TestTaskStatusDirectory_ValueMatchingIsCaseInsensitive(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	// Legacy spelling in the archive: must still be caught by rule 1.
	seedArchivedTask(t, vault, "p", "done", "legacy", "# T\n\n**Status:** In Progress\n")
	// Odd-cased terminal in the archive: must NOT be caught — it agrees.
	seedArchivedTask(t, vault, "p", "done", "shouty", "# T\n\n**Status:** RETIRED\n")
	// Odd-cased terminal while active: must be caught by rule 2.
	seedTask(t, vault, "p", "midcancel", "# T\n\n**Status:** Cancelled\n")

	findings, _, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}
	got := statusArtifacts(findings)
	if len(got) != 2 {
		t.Fatalf("findings = %v, want the legacy archived file and the odd-cased active one", got)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "done/legacy.md") {
		t.Errorf("rule 1 must catch the legacy 'In Progress' spelling; got %v", got)
	}
	if !strings.Contains(joined, "tasks/midcancel.md") {
		t.Errorf("rule 2 must catch an odd-cased terminal value; got %v", got)
	}
	if strings.Contains(joined, "shouty") {
		t.Errorf("an odd-cased TERMINAL value in the archive agrees and must be silent; got %v", got)
	}
}

// TestTaskStatusDirectory_FencedStatusIsNotAClaim — this project's task files quote
// metadata-shaped lines inside code fences constantly. Flagging one would be
// inventing a finding against sample text.
func TestTaskStatusDirectory_FencedStatusIsNotAClaim(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	body := "# T\n\n**Status:** retired\n\n## Example\n\n```md\n**Status:** pending\n```\n\ntail\n"
	seedArchivedTask(t, vault, "p", "done", "quotes", body)

	findings, _, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("a fenced **Status:** sample must not produce a finding, got %v", statusArtifacts(findings))
	}
}

// TestTaskStatusDirectory_OnlyFencedStatusIsNoClaimAtAll is the sharper half of
// fence-awareness: when the file's ONLY Status-shaped line is fenced, the file makes
// no claim, so there is nothing to disagree with. A fence-blind reading would call
// this archived-file-says-pending and emit a finding nobody can act on.
func TestTaskStatusDirectory_OnlyFencedStatusIsNoClaimAtAll(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedArchivedTask(t, vault, "p", "done", "sample-only", "# T\n\n```md\n**Status:** pending\n```\n\nbody\n")

	findings, _, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Errorf("a file whose only Status line is fenced makes no claim, got %v", statusArtifacts(findings))
	}
}

// TestTaskStatusDirectory_AbsentStatusIsNotAFinding pins the ruling. Most archived
// files predate the header block entirely; absence is the older FORMAT, not a live
// CLAIM. A rule that flagged them would emit dozens of un-actionable red rows beside
// a handful of real ones — the "permanent un-actionable red" the 2026-08-31 review
// named as the reason this plan was not executable as filed.
//
// The class is surfaced through EvidenceTaskStatusDirectory instead, so it stays
// measurable without failing the audit. If this test ever fails, that ruling has
// been reversed by accident.
func TestTaskStatusDirectory_AbsentStatusIsNotAFinding(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedArchivedTask(t, vault, "p", "done", "ancient", "# An Old Task\n\nNo header block at all.\n")
	seedArchivedTask(t, vault, "p", "cancelled", "older", "# Older\n\nAlso no header block.\n")
	seedTask(t, vault, "p", "headerless", "# Active But Headerless\n\nNo status line.\n")

	findings, unknowns, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("a missing status line is not an unknown either: %v", unknowns)
	}
	if len(findings) != 0 {
		t.Errorf("absent status must never be a finding, got %v", statusArtifacts(findings))
	}
	// And the evidence command must still let a reader size the class, or it is
	// invisible rather than merely non-fatal.
	if !strings.Contains(EvidenceTaskStatusDirectory, "absent-status class") {
		t.Error("the evidence command must carry a derivation for the absent-status class")
	}
}

// TestTaskStatusDirectory_ArchiveSubdirsAreNotDoubleCounted — done/ and cancelled/
// are reached as their own directory rows, so the active walk must not descend into
// them. A regression here would report every archived file twice.
func TestTaskStatusDirectory_ArchiveSubdirsAreNotDoubleCounted(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedArchivedTask(t, vault, "p", "done", "stale", "# T\n\n**Status:** pending\n")

	findings, _, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly one — the archive walk must not double-count", statusArtifacts(findings))
	}
}

// TestTaskStatusDirectory_RegisteredInTheAudit proves the dimension actually runs.
// A dimension that exists but is never registered is a check that runs nowhere —
// the failure this audit package is named after.
func TestTaskStatusDirectory_RegisteredInTheAudit(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "midretire", "# T\n\n**Status:** retired\n")

	report, err := Run(vault)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range report.Dimensions {
		if d.Name != DimTaskStatusDirectory {
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
	t.Fatalf("%s is not registered in the audit", DimTaskStatusDirectory)
}
