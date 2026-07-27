// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/resumezone"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

const (
	pin  = resumezone.ResumePinMarker
	disp = resumezone.ResumeDisposableMarker
)

// fullyDeclaredResume is a resume every section of which has been ruled on: one
// pinned, one disposable. It is the SILENT case.
const fullyDeclaredResume = "# proj\n\n## Behavioral Notes\n" + pin + "\n\nnever do the bad thing\n\n" +
	"## Reference Documents\n" + disp + "\n\n| doc | tool |\n"

func TestCheckPinCoverage_EmptyVaultRootSkips(t *testing.T) {
	r := CheckPinCoverage(storage.NewVault(""))
	if r.Status != Skip {
		t.Errorf("status = %v, want Skip", r.Status)
	}
	if r.Summary != "no vault configured" {
		t.Errorf("summary = %q", r.Summary)
	}
}

func TestCheckPinCoverage_NoProjectsDir(t *testing.T) {
	r := CheckPinCoverage(storage.NewVault(t.TempDir()))
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass", r.Status)
	}
	if r.Summary != "no Projects/ directory" {
		t.Errorf("summary = %q", r.Summary)
	}
	if len(r.Details) != 0 {
		t.Errorf("healthy state must be silent, got details %v", r.Details)
	}
}

func TestCheckPinCoverage_ProjectsIsAFile(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, "Projects"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := CheckPinCoverage(storage.NewVault(vault))
	if r.Status != Info {
		t.Errorf("status = %v, want Info on an unreadable Projects/", r.Status)
	}
	if !strings.Contains(r.Summary, "scan Projects/") {
		t.Errorf("summary = %q", r.Summary)
	}
}

func TestCheckPinCoverage_MissingResumeIsNotAViolation(t *testing.T) {
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "Projects", "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := CheckPinCoverage(storage.NewVault(vault))
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass", r.Status)
	}
	if r.Summary != "0 resume.md fully declared" {
		t.Errorf("summary = %q", r.Summary)
	}
	if len(r.Details) != 0 {
		t.Errorf("healthy state must be silent, got details %v", r.Details)
	}
}

// Every section ruled on — one pinned, one disposable — is the silent case: Pass,
// no details, no names.
func TestCheckPinCoverage_AllSectionsDeclaredIsSilent(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "tidy", fullyDeclaredResume)

	r := CheckPinCoverage(storage.NewVault(vault))
	if r.Status != Pass {
		t.Fatalf("status = %v, want Pass", r.Status)
	}
	if r.Summary != "1 resume.md fully declared" {
		t.Errorf("summary = %q", r.Summary)
	}
	if len(r.Details) != 0 {
		t.Errorf("healthy state must be silent, got details %v", r.Details)
	}
}

// 🔴 THE REPORT MUST NAME THE SECTIONS. A count that says "2 undeclared" tells a
// reader nothing they can act on and rots the moment a heading is renamed — the
// exact failure mode the pin marker itself exists to avoid.
func TestCheckPinCoverage_NamesTheUndeclaredSections(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "leaky", "# proj\n\n## Behavioral Notes\n"+pin+"\n\nrules\n\n"+
		"## Current State\n\nphase 3, mid-refactor\n\n"+
		"## Reference Documents\n"+disp+"\n\n| doc | tool |\n\n"+
		"## Open Threads\n\n- the thing nobody wrote down\n")

	r := CheckPinCoverage(storage.NewVault(vault))
	if r.Status != Info {
		t.Fatalf("status = %v, want Info (advisory, never Fail)", r.Status)
	}
	if r.Summary != "1 of 1 resume.md hold undeclared live sections" {
		t.Errorf("summary = %q", r.Summary)
	}
	// Named, and in FILE order — the order a reader will walk the file in.
	if want := "  leaky: Current State; Open Threads"; r.Details[0] != want {
		t.Errorf("details[0] = %q, want %q", r.Details[0], want)
	}
	joined := strings.Join(r.Details, "\n")
	for _, unwanted := range []string{"Behavioral Notes", "Reference Documents"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("a declared section was reported as undeclared (%q):\n%s", unwanted, joined)
		}
	}
	// The remediation must state both rulings, so the reader is not left guessing
	// that "fix it" means "pin everything".
	for _, want := range []string{"LIVE STATE", pin, disp, "/vpc-wrap"} {
		if !strings.Contains(joined, want) {
			t.Errorf("details missing %q:\n%s", want, joined)
		}
	}
}

// 🔴 A RESUME THAT PINS NOTHING IS A DIFFERENT CONDITION — excluded from the scan,
// never flagged, and never silent about being excluded.
//
// Flagging it would dump the whole table of contents as "undeclared", which names
// everything and therefore nothing, and its remedy ("declare a pin zone") is not
// this check's remedy ("rule on these named sections"). It is already reported
// elsewhere: the ladder refuses to shed such a resume and bootstrap says why.
func TestCheckPinCoverage_NoPinMarkerIsExcludedNotFlagged(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "unmarked", "# proj\n\n## Current State\n\nphase 1\n\n## Open Threads\n\n- a thing\n")

	r := CheckPinCoverage(storage.NewVault(vault))
	if r.Status != Pass {
		t.Fatalf("status = %v, want Pass — a pin-less resume is a different condition, not a violation", r.Status)
	}
	if len(r.Details) != 0 {
		t.Errorf("a pin-less resume must not be named section-by-section, got details %v", r.Details)
	}
	// Excluded, but never invisible: the count rides in the summary even on Pass.
	if r.Summary != "0 resume.md fully declared (1 declare no pin zone)" {
		t.Errorf("summary = %q — the exclusion must stay visible", r.Summary)
	}
}

// A pin marker stranded in the preamble pins nothing that was not already inline,
// so the document still declares no zone and is still excluded. This mirrors the
// ladder's own rule; a divergence here would have the advisory reporting on a
// document the ladder treats differently.
func TestCheckPinCoverage_PreambleMarkerIsNotADeclaration(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "stranded", "# proj\n"+pin+"\n\n## Current State\n\nphase 1\n")

	r := CheckPinCoverage(storage.NewVault(vault))
	if r.Status != Pass || len(r.Details) != 0 {
		t.Fatalf("status = %v details = %v, want Pass/none", r.Status, r.Details)
	}
	if !strings.Contains(r.Summary, "(1 declare no pin zone)") {
		t.Errorf("summary = %q — a preamble marker must not count as a pin zone", r.Summary)
	}
}

// A marker quoted inside a code fence is DOCUMENTATION, not a declaration — the
// resume template documents both markers, and any resume explaining the mechanism
// to a human will too. The section doing the explaining has been ruled on by
// nobody, so it is live and must be named.
func TestCheckPinCoverage_FencedMarkerDoesNotDeclare(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "teacher", "# proj\n\n## Rules\n"+pin+"\n\nreal pin\n\n"+
		"## How Marking Works\n\nLike this:\n\n```markdown\n## Sample\n"+disp+"\n```\n\nThat is all.\n")

	r := CheckPinCoverage(storage.NewVault(vault))
	if r.Status != Info {
		t.Fatalf("status = %v, want Info", r.Status)
	}
	if want := "  teacher: How Marking Works"; r.Details[0] != want {
		t.Errorf("details[0] = %q, want %q — a fenced marker was read as a real declaration", r.Details[0], want)
	}
}

// A section carrying BOTH markers has been ruled on twice, and the tie-break keeps
// content: it counts as pinned and is NOT reported. Sending an author back to a
// section that already has a ruling is a false report.
func TestCheckPinCoverage_BothMarkersIsNotUndeclared(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "contradictory", "# proj\n\n## Notes\n"+pin+"\n"+disp+"\n\nkeep me\n")

	r := CheckPinCoverage(storage.NewVault(vault))
	if r.Status != Pass {
		t.Fatalf("status = %v, want Pass", r.Status)
	}
	if r.Summary != "1 resume.md fully declared" {
		t.Errorf("summary = %q — a contradictory declaration is still a declaration", r.Summary)
	}
}

func TestCheckPinCoverage_MixedSortedAndNeverFails(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "zeta", "# z\n\n## Rules\n"+pin+"\n\nr\n\n## Diary\n\nlive\n")
	writeResume(t, vault, "alpha", "# a\n\n## Rules\n"+pin+"\n\nr\n\n## Threads\n\nlive\n")
	writeResume(t, vault, "tidy", fullyDeclaredResume)
	writeResume(t, vault, "unmarked", "# u\n\n## State\n\nlive\n")
	writeResume(t, vault, ".hidden", "# h\n\n## Rules\n"+pin+"\n\nr\n\n## Diary\n\nlive\n")
	writeResume(t, vault, "_shared", "# s\n\n## Rules\n"+pin+"\n\nr\n\n## Diary\n\nlive\n")

	r := CheckPinCoverage(storage.NewVault(vault))
	if r.Status == Fail {
		t.Fatal("status = Fail — this advisory must never fail the build")
	}
	if r.Status != Info {
		t.Fatalf("status = %v, want Info", r.Status)
	}
	// 3 pin-declaring resumes scanned (alpha, tidy, zeta); unmarked excluded.
	if r.Summary != "2 of 3 resume.md hold undeclared live sections (1 declare no pin zone)" {
		t.Errorf("summary = %q", r.Summary)
	}
	joined := strings.Join(r.Details, "\n")
	for _, skip := range []string{"tidy", "unmarked", ".hidden", "_shared"} {
		if strings.Contains(joined, skip+":") {
			t.Errorf("details must not mention %q:\n%s", skip, joined)
		}
	}
	alphaAt := strings.Index(joined, "alpha:")
	zetaAt := strings.Index(joined, "zeta:")
	if alphaAt < 0 || zetaAt < 0 || alphaAt > zetaAt {
		t.Errorf("violations not sorted by project (alpha@%d, zeta@%d):\n%s", alphaAt, zetaAt, joined)
	}
}

// TestCheckPinCoverage_IsReadOnly proves the check never mutates the vault: same
// bytes, same modification time, on the nose.
func TestCheckPinCoverage_IsReadOnly(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "leaky", "# proj\n\n## Rules\n"+pin+"\n\nr\n\n## Current State\n\nlive\n")
	path := filepath.Join(vault, "Projects", "leaky", "resume.md")

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	statBefore, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	CheckPinCoverage(storage.NewVault(vault))

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("resume.md content changed — the check must be strictly read-only")
	}
	statAfter, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Error("resume.md mtime changed — the check must be strictly read-only")
	}
}

// 🔴 THE SHIPPED TEMPLATE, MEASURED BY THE CHECK THAT WATCHES IT.
//
// This is the two halves of the fix meeting: the template declares three states,
// and this check is the only thing in the tree that reads them. The assertion is
// deliberately EXACT rather than "at least these", because both directions are
// defects:
//
//   - `Current State` and `Open Threads` MUST be reported. They are live state.
//     Marking them disposable — which the template did before this change — asserts
//     that a session's own working context is safe to drop, and nothing noticed.
//   - Nothing ELSE may be reported. `Reference Documents` is a pure pointer table
//     an agent re-derives from a tool call, so it is genuinely disposable; the two
//     correctness sections are pinned. A new unmarked section appearing here should
//     fail this test and force the author to rule on it.
//
// So every project scaffolded from this template reports two undeclared live
// sections on day one. That is the intended, honest reading — not a false
// positive: those sections ARE live and they ARE in the zone the ladder sheds.
func TestCheckPinCoverage_ScaffoldTemplateLeavesLiveStateLive(t *testing.T) {
	raw, err := templates.FS().ReadFile("templates/resume.md")
	if err != nil {
		t.Fatalf("read embedded resume template: %v", err)
	}
	body := string(raw)

	if _, declared := resumezone.PinnedZone(body); !declared {
		t.Fatal("the embedded resume template declares no pin zone")
	}

	got := resumezone.UndeclaredLiveSections(body)
	want := []string{"Current State", "Open Threads"}
	if len(got) != len(want) {
		t.Fatalf("template undeclared sections = %q, want exactly %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("template undeclared sections = %q, want exactly %q", got, want)
		}
	}

	// The one section marked disposable must be the pointer table, and it must
	// therefore be absent from the live list above.
	if !strings.Contains(body, "## Reference Documents\n"+disp+"\n") {
		t.Error("`## Reference Documents` is no longer marked disposable — it is the only " +
			"section here an agent can re-derive from a tool call at no cost")
	}
	for _, live := range []string{"## Current State\n" + disp, "## Open Threads\n" + disp} {
		if strings.Contains(body, live) {
			t.Errorf("live state was marked disposable: %q", live)
		}
	}
}
