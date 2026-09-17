// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"fmt"
	"os/exec"
	"slices"
	"sort"
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
	seedArchivedTask(t, vault, "p", "done", "stale", "# T\n\n**Status:** planning\n**Priority:** medium\n\nbody\n")

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
	seedTask(t, vault, "p", "midretire", "# T\n\n**Status:** done\n**Priority:** medium\n\nbody\n")

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
	seedArchivedTask(t, vault, "p", "done", "archived-live", "# T\n\n**Status:** planning\n")
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
	seedTask(t, vault, "p", "live", "# T\n\n**Status:** planning\n")
	seedTask(t, vault, "p", "iced", "# T\n\n**Status:** icebox\n")
	seedArchivedTask(t, vault, "p", "done", "gone", "# T\n\n**Status:** done\n")
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
	// Odd-cased non-terminal in the archive: must still be caught by rule 1a, per-file.
	seedArchivedTask(t, vault, "p", "done", "legacy", "# T\n\n**Status:** In Progress\n")
	// Odd-cased terminal in the archive: must NOT be caught — it agrees.
	seedArchivedTask(t, vault, "p", "done", "shouty", "# T\n\n**Status:** DONE\n")
	// Odd-cased terminal while active: must be caught by rule 2.
	seedTask(t, vault, "p", "midcancel", "# T\n\n**Status:** Cancelled\n")
	// Odd-cased LEGACY value in the archive: the value half is folded, so this must be
	// recognised as legacy and routed to the AGGREGATE, not to the per-file class. A
	// case-sensitive legacy test would silently send it to rule 1a instead, where it
	// would read as one more unique defect rather than one more row of scheduled debt.
	seedArchivedTask(t, vault, "p", "done", "mixedlegacy", "# T\n\n**Status:** Retired\n")

	findings, _, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}
	var perFile []string
	aggregates := map[string]int64{}
	for _, f := range findings {
		if f.Measure > 0 {
			aggregates[f.Artifact] = f.Measure
			continue
		}
		perFile = append(perFile, f.Artifact)
	}
	if len(perFile) != 2 {
		t.Fatalf("per-file findings = %v, want the odd-cased non-terminal archived file and the odd-cased active one", perFile)
	}
	joined := strings.Join(perFile, " ")
	if !strings.Contains(joined, "done/legacy.md") {
		t.Errorf("rule 1a must catch the odd-cased 'In Progress' spelling; got %v", perFile)
	}
	if !strings.Contains(joined, "tasks/midcancel.md") {
		t.Errorf("rule 2 must catch an odd-cased terminal value; got %v", perFile)
	}
	if strings.Contains(joined, "shouty") {
		t.Errorf("an odd-cased TERMINAL value in the archive agrees and must be silent; got %v", perFile)
	}
	if strings.Contains(joined, "mixedlegacy") {
		t.Errorf("an odd-cased LEGACY value must be aggregated, not reported per-file; got %v", perFile)
	}
	if got := aggregates["Projects/p/tasks/done"]; got != 1 {
		t.Errorf("aggregate Measure for the odd-cased legacy value = %d, want 1 (case is folded on the VALUE)", got)
	}
}

// TestTaskStatusDirectory_FencedStatusIsNotAClaim — this project's task files quote
// metadata-shaped lines inside code fences constantly. Flagging one would be
// inventing a finding against sample text.
func TestTaskStatusDirectory_FencedStatusIsNotAClaim(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	body := "# T\n\n**Status:** done\n\n## Example\n\n```md\n**Status:** planning\n```\n\ntail\n"
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
	seedArchivedTask(t, vault, "p", "done", "sample-only", "# T\n\n```md\n**Status:** planning\n```\n\nbody\n")

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
	seedArchivedTask(t, vault, "p", "done", "stale", "# T\n\n**Status:** planning\n")

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
	seedTask(t, vault, "p", "midretire", "# T\n\n**Status:** done\n")

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

// TestTaskStatusDirectory_LegacyRetiredArchivedCollapsesToOneRowPerProject pins the
// rule-1 split: the legacy population aggregates, the non-legacy one does not.
//
// The assertion is deliberately NOT "zero findings for the legacy files". Silence is
// also what a broken scan produces, so an assertion of absence cannot distinguish
// "correctly aggregated" from "predicate returned nothing". One row per directory
// carrying a specific magnitude can.
//
// Breaks, each a distinct failure mode:
//   - revert to per-file emission          -> N findings instead of two aggregates
//   - collapse to one vault-wide row       -> the per-project artifacts disappear
//   - drop or hard-code Measure            -> the count assertion fails, rows survive
//   - route the non-legacy value into the aggregate -> the per-file finding disappears
func TestTaskStatusDirectory_LegacyRetiredArchivedCollapsesToOneRowPerProject(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	legacy := "# T\n\n**Status:** " + storage.StatusDoneLegacy + "\n**Priority:** medium\n\nbody\n"
	for _, slug := range []string{"a", "b", "c"} {
		seedArchivedTask(t, vault, "p1", "done", slug, legacy)
	}
	for _, slug := range []string{"d", "e"} {
		seedArchivedTask(t, vault, "p2", "done", slug, legacy)
	}
	// Non-terminal AND non-legacy: this one must stay individually addressable.
	seedArchivedTask(t, vault, "p1", "done", "odd", "# T\n\n**Status:** planning\n**Priority:** medium\n\nbody\n")

	findings, unknowns, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Errorf("unexpected unknowns: %v", unknowns)
	}

	aggregates := map[string]int64{}
	var perFile []Finding
	for _, f := range findings {
		if f.Measure > 0 {
			aggregates[f.Artifact] = f.Measure
			continue
		}
		perFile = append(perFile, f)
	}

	// Two projects, two distinct aggregate rows, each carrying its own count.
	want := map[string]int64{
		"Projects/p1/tasks/done": 3,
		"Projects/p2/tasks/done": 2,
	}
	if len(aggregates) != len(want) {
		t.Fatalf("aggregate rows = %v, want exactly one per project: %v", aggregates, want)
	}
	for artifact, n := range want {
		got, ok := aggregates[artifact]
		if !ok {
			t.Errorf("no aggregate row for %s — a vault-wide row loses the per-project artifact "+
				"and with it the self-clearing property", artifact)
			continue
		}
		if got != n {
			t.Errorf("%s Measure = %d, want %d (the count of legacy files in that directory)", artifact, got, n)
		}
	}

	// The non-legacy file keeps its own per-file finding, with Measure zero.
	if len(perFile) != 1 {
		t.Fatalf("per-file findings = %v, want exactly the non-legacy archived file", statusArtifacts(perFile))
	}
	if want := "Projects/p1/tasks/done/odd.md:3"; perFile[0].Artifact != want {
		t.Errorf("per-file artifact = %q, want %q", perFile[0].Artifact, want)
	}
	if perFile[0].Measure != 0 {
		t.Errorf("a categorical per-file finding must carry Measure 0, got %d", perFile[0].Measure)
	}
	if !strings.Contains(perFile[0].Detail, "non-terminal status") {
		t.Errorf("the per-file detail must stay the rule-1a text; got %q", perFile[0].Detail)
	}
	// The aggregate must name the repair act, not the files.
	for artifact := range want {
		for _, f := range findings {
			if f.Artifact == artifact && !strings.Contains(f.Detail, "vp migrate task-board-fields") {
				t.Errorf("aggregate %s must name the repair command; got %q", artifact, f.Detail)
			}
		}
	}
}

// TestTaskStatusDirectory_LegacyAggregateIsPerArchiveDirectory: done/ and cancelled/
// are separate populations and get separate rows, because the artifact is the
// directory the count is ABOUT.
//
// Break: key the aggregate on the project instead of the directory. The two counts
// merge into one row and the reader can no longer tell which archive directory holds
// the debt.
func TestTaskStatusDirectory_LegacyAggregateIsPerArchiveDirectory(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	legacy := "# T\n\n**Status:** " + storage.StatusDoneLegacy + "\n"
	seedArchivedTask(t, vault, "p", "done", "a", legacy)
	seedArchivedTask(t, vault, "p", "done", "b", legacy)
	seedArchivedTask(t, vault, "p", "cancelled", "c", legacy)

	findings, _, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, f := range findings {
		got[f.Artifact] = f.Measure
	}
	want := map[string]int64{
		"Projects/p/tasks/done":      2,
		"Projects/p/tasks/cancelled": 1,
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want one per archive directory: %v", got, want)
	}
	for a, n := range want {
		if got[a] != n {
			t.Errorf("%s Measure = %d, want %d", a, got[a], n)
		}
	}
}

// TestTaskStatusDirectory_LegacyRetiredActiveStillFiresRule2: the concession to the
// legacy vocabulary is READ-side and must be SYMMETRIC. An ACTIVE file saying
// "retired" is still the interrupted-archive signature — a live crash to repair one
// file at a time — and must not be swallowed by rule 1's aggregation.
//
// Break: make the legacy value non-terminal for rule 2, or let the rule-1 aggregate
// absorb it. Either way the crash signature goes unreported.
func TestTaskStatusDirectory_LegacyRetiredActiveStillFiresRule2(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedTask(t, vault, "p", "live", "# T\n\n**Status:** "+storage.StatusDoneLegacy+"\n**Priority:** medium\n\nbody\n")

	findings, _, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly the active file carrying the legacy value", statusArtifacts(findings))
	}
	if want := "Projects/p/tasks/live.md:3"; findings[0].Artifact != want {
		t.Errorf("artifact = %q, want %q — rule 2 stays PER-FILE", findings[0].Artifact, want)
	}
	if findings[0].Measure != 0 {
		t.Errorf("rule 2 is categorical and must carry Measure 0, got %d — it was aggregated", findings[0].Measure)
	}
	if !strings.Contains(findings[0].Detail, "COMPLETING THE RENAME") {
		t.Errorf("rule-2 detail must carry its repair; got %q", findings[0].Detail)
	}
}

// runEvidencePartIn executes ONE derivation of an evidence string with the vault as
// cwd, and returns its stdout lines. It tolerates a non-zero exit, because every
// derivation here is a grep that legitimately exits 1 when its class is empty.
func runEvidencePartIn(t *testing.T, root, part string) []string {
	t.Helper()
	cmd := exec.Command("bash", "-c", part)
	cmd.Dir = root
	out, _ := cmd.Output() //nolint:errcheck // exit 1 = empty class
	var got []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			got = append(got, line)
		}
	}
	sort.Strings(got)
	return got
}

// TestEvidence_ReproducesTheGoRule_TaskStatusDirectory enrolls DimTaskStatusDirectory
// in the differential discipline TestEvidence_ReproducesTheGoRule states as doctrine:
// "RECORD THE GREP, NEVER THE COUNT is only honest if the grep reproduces the rule."
//
// It is a sibling rather than a row in that test's table for two reasons. The shared
// fixture there carries no task files at all, so enrolling into it would trip its own
// "agreement is vacuous" precondition; and this dimension's rule 1b reports a COUNT
// where the others report a list, so the comparison is per-class by SHAPE rather than
// one set equality. That shape split is the point: a differential test cannot demand
// identical artifact sets for a class the Go rule reports as one row with a magnitude
// while the shell reports one line per file.
//
// 🔴 THE FIXTURE CARRIES NO FENCED STATUS LINE, DELIBERATELY. The Go scan is
// fence-aware and a line-oriented grep cannot be, so on a fenced specimen the two
// legitimately disagree and this test would fail for the declared-gap reason rather
// than a real one. That shape stays in _FencedStatusIsNotAClaim and
// _OnlyFencedStatusIsNoClaimAtAll, and it is deliberately outside this differential's
// reach. Do NOT "fix" the disagreement by making the evidence grep fence-aware — it
// cannot be, and pretending otherwise deletes an honest gap.
//
// Break it in BOTH directions:
//   - restore a complement-inverted rule-1 grep (match the terminal values instead of
//     excluding them) and the shell and the scan disagree on every row — which is the
//     bug that shipped, and this test names it.
//   - leave rule 1b's evidence a LIST while the rule emits a COUNT, and the shape
//     mismatch fails: the parsed (count, directory) pairs stop existing.
func TestEvidence_ReproducesTheGoRule_TaskStatusDirectory(t *testing.T) {
	for _, tool := range []string{"bash", "grep", "comm", "sort", "uniq", "cut"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	vault := storage.NewVault(t.TempDir())
	legacy := "# T\n\n**Status:** " + storage.StatusDoneLegacy + "\n"
	// rule 1b: the legacy population, two projects, different magnitudes.
	seedArchivedTask(t, vault, "p1", "done", "l1", legacy)
	seedArchivedTask(t, vault, "p1", "done", "l2", legacy)
	seedArchivedTask(t, vault, "p2", "done", "l3", legacy)
	// rule 1a: archived, neither terminal nor legacy.
	seedArchivedTask(t, vault, "p1", "done", "odd", "# T\n\n**Status:** planning\n")
	// Agreeing files, which must appear in NO class.
	seedArchivedTask(t, vault, "p1", "done", "fine", "# T\n\n**Status:** "+storage.StatusDone+"\n")
	seedArchivedTask(t, vault, "p1", "cancelled", "dropped", "# T\n\n**Status:** "+storage.StatusCancelled+"\n")
	seedArchivedTask(t, vault, "p2", "cancelled", "dropped2", "# T\n\n**Status:** "+storage.StatusCancelled+"\n")
	// rule 2: active file carrying an archived claim.
	seedTask(t, vault, "p1", "live", "# T\n\n**Status:** "+storage.StatusCancelled+"\n")
	// The absent-status class: measured by the evidence, never a finding.
	seedArchivedTask(t, vault, "p1", "done", "bare", "# T\n\nno header block at all\n")

	findings, _, err := auditTaskStatusDirectory(vault)
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(EvidenceTaskStatusDirectory, " ; ")
	if len(parts) != 4 {
		t.Fatalf("evidence must carry 4 per-class derivations, got %d — the shape split is the contract", len(parts))
	}

	// Sort the Go side into the same three shapes the evidence declares.
	var goRule1a, goRule2 []string
	goRule1b := map[string]int64{}
	for _, f := range findings {
		switch {
		case f.Measure > 0:
			goRule1b[f.Artifact] = f.Measure
		case strings.Contains(f.Detail, "COMPLETING THE RENAME"):
			goRule2 = append(goRule2, strings.SplitN(f.Artifact, ":", 2)[0])
		default:
			goRule1a = append(goRule1a, strings.SplitN(f.Artifact, ":", 2)[0])
		}
	}
	sort.Strings(goRule1a)
	sort.Strings(goRule2)
	if len(goRule1a) == 0 || len(goRule1b) == 0 || len(goRule2) == 0 {
		t.Fatalf("precondition: the fixture must exercise all three reported classes, "+
			"or agreement is vacuous (1a=%v 1b=%v 2=%v)", goRule1a, goRule1b, goRule2)
	}

	// --- class 1a: LIST vs per-file artifacts -----------------------------------
	if got := runEvidencePartIn(t, vault.Root, parts[0]); !slices.Equal(got, goRule1a) {
		t.Errorf("rule 1a: evidence prints %v, the dimension reports %v", got, goRule1a)
	}

	// --- class 1b: COUNT PER DIRECTORY vs (Artifact, Measure) -------------------
	shellRule1b := map[string]int64{}
	for _, line := range runEvidencePartIn(t, vault.Root, parts[1]) {
		var n int64
		var dir string
		if _, serr := fmt.Sscanf(line, "%d %s", &n, &dir); serr != nil {
			t.Fatalf("rule 1b evidence must print `<count> <directory>` pairs (a COUNT, matching Measure); "+
				"got %q — if this is a list, the evidence shape no longer matches the rule's output", line)
		}
		shellRule1b[dir] = n
	}
	if len(shellRule1b) != len(goRule1b) {
		t.Errorf("rule 1b: evidence counts %v, the dimension reports %v", shellRule1b, goRule1b)
	}
	for dir, want := range goRule1b {
		if got := shellRule1b[dir]; got != want {
			t.Errorf("rule 1b: evidence counts %d for %s, the dimension's Measure is %d", got, dir, want)
		}
	}

	// --- class 2: LIST vs per-file artifacts ------------------------------------
	var shellRule2 []string
	for _, line := range runEvidencePartIn(t, vault.Root, parts[2]) {
		shellRule2 = append(shellRule2, strings.SplitN(line, ":", 2)[0])
	}
	sort.Strings(shellRule2)
	if !slices.Equal(shellRule2, goRule2) {
		t.Errorf("rule 2: evidence prints %v, the dimension reports %v", shellRule2, goRule2)
	}

	// --- absent-status: measured, and explicitly NOT a finding ------------------
	absent := runEvidencePartIn(t, vault.Root, parts[3])
	if len(absent) == 0 {
		t.Fatal("precondition: the fixture must carry an absent-status file, or the fourth derivation is vacuous")
	}
	reported := statusArtifacts(findings)
	for _, a := range absent {
		for _, r := range reported {
			if strings.HasPrefix(r, a) {
				t.Errorf("the absent-status class must never be a finding, but %s is reported as %s", a, r)
			}
		}
	}
}
