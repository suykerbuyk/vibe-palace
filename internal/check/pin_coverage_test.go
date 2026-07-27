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

// underDeclaredResume pins one section and leaves two live. Every project below
// that needs a finding uses THIS EXACT BODY, so the only thing that can differ
// between an exposed project and a latent one is the core measurement — not the
// resume, not its size, not its name.
const underDeclaredResume = "# proj\n\n## Behavioral Notes\n" + pin + "\n\nrules\n\n" +
	"## Current State\n\nphase 3, mid-refactor\n\n" +
	"## Open Threads\n\n- the thing nobody wrote down\n"

// writeWorkflow writes <vault>/Projects/<slug>/workflow.md at exactly n bytes.
//
// It is how a test moves a project across the exposure boundary WITHOUT touching
// its resume: exposure is a property of the CORE (resume + workflow) measured
// against CoreMaxBytes, so padding the contract alone must be enough to flip the
// verdict. A test that had to fatten the resume could not tell the two apart.
func writeWorkflow(t *testing.T, vaultRoot, slug string, n int) {
	t.Helper()
	dir := filepath.Join(vaultRoot, "Projects", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workflow.md"), []byte(strings.Repeat("x", n)), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
}

// writeExposed scaffolds a project whose resume holds undeclared live sections
// AND whose core is over the floor — the shed ladder reduces this resume, so its
// undeclared sections are being dropped for real.
func writeExposed(t *testing.T, vaultRoot, slug string) {
	t.Helper()
	writeResume(t, vaultRoot, slug, underDeclaredResume)
	writeWorkflow(t, vaultRoot, slug, CoreMaxBytes)
}

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
//
// 🔴 AND IT MUST NAME THEM ON A PASS RUN. This project's core fits, so nothing is
// being shed and the status is Pass — but the census still prints, by project and
// by section. A latent set that only became visible once it turned exposed would
// be a defect maturing in the dark, which is the whole reason the pin-less
// exclusion count also rides in the summary of every run.
func TestCheckPinCoverage_LatentFindingsPassButAreStillNamed(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "leaky", "# proj\n\n## Behavioral Notes\n"+pin+"\n\nrules\n\n"+
		"## Current State\n\nphase 3, mid-refactor\n\n"+
		"## Reference Documents\n"+disp+"\n\n| doc | tool |\n\n"+
		"## Open Threads\n\n- the thing nobody wrote down\n")

	r := CheckPinCoverage(storage.NewVault(vault))
	if r.Status != Pass {
		t.Fatalf("status = %v, want Pass — the core fits, so nothing is being shed", r.Status)
	}
	if r.Summary != "1 of 1 latent — undeclared live sections, core fits, none shed" {
		t.Errorf("summary = %q", r.Summary)
	}
	if !strings.HasPrefix(r.Details[0], "LATENT") {
		t.Errorf("details[0] = %q, want the LATENT census header", r.Details[0])
	}
	// Named, and in FILE order — the order a reader will walk the file in.
	if want := "  leaky: Current State; Open Threads"; !strings.HasPrefix(r.Details[1], want) {
		t.Errorf("details[1] = %q, want it to start %q", r.Details[1], want)
	}
	joined := strings.Join(r.Details, "\n")
	if strings.Contains(joined, "EXPOSED") {
		t.Errorf("a latent-only run must not claim anything is exposed:\n%s", joined)
	}
	for _, unwanted := range []string{"Behavioral Notes", "Reference Documents"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("a declared section was reported as undeclared (%q):\n%s", unwanted, joined)
		}
	}
	// The remediation must state both rulings on a Pass run too, so the reader is
	// not left guessing that "fix it" means "pin everything".
	for _, want := range []string{"LIVE STATE", pin, disp, "/vpc-wrap"} {
		if !strings.Contains(joined, want) {
			t.Errorf("details missing %q:\n%s", want, joined)
		}
	}
}

// 🔴 EXPOSED IS THE ROW THAT DEMANDS ACTION, AND IT COMES FIRST.
//
// Every project here carries the SAME resume, byte for byte. The only difference
// is workflow.md — so if the split were computed from the resume, from a size
// threshold of this check's own invention, or from a project name, this test
// could not pass. Exposure is the core measurement (resume + workflow vs
// CoreMaxBytes), the identical verdict CheckCoreFloor reports.
func TestCheckPinCoverage_ExposedIsInfoAndNamedBeforeLatent(t *testing.T) {
	vault := t.TempDir()
	writeExposed(t, vault, "zexposed")
	writeExposed(t, vault, "aexposed")
	writeResume(t, vault, "zlatent", underDeclaredResume)
	writeResume(t, vault, "alatent", underDeclaredResume)

	r := CheckPinCoverage(storage.NewVault(vault))
	if r.Status == Fail {
		t.Fatal("status = Fail — this advisory must never fail the build")
	}
	if r.Status != Info {
		t.Fatalf("status = %v, want Info — sections are being shed for real", r.Status)
	}
	// The headline alone must say which case the reader is in.
	if r.Summary != "2 of 4 EXPOSED — undeclared live sections being shed now; 2 latent" {
		t.Errorf("summary = %q", r.Summary)
	}
	if !strings.HasPrefix(r.Details[0], "EXPOSED") {
		t.Errorf("details[0] = %q, want the EXPOSED block first", r.Details[0])
	}

	joined := strings.Join(r.Details, "\n")
	latentHeaderAt := strings.Index(joined, "\nLATENT")
	if latentHeaderAt < 0 {
		t.Fatalf("the latent census is missing:\n%s", joined)
	}
	for _, name := range []string{"aexposed:", "zexposed:"} {
		at := strings.Index(joined, name)
		if at < 0 {
			t.Fatalf("%s not named:\n%s", name, joined)
		}
		if at > latentHeaderAt {
			t.Errorf("%s appears below the LATENT header — exposed projects must be named first:\n%s", name, joined)
		}
	}
	for _, name := range []string{"alatent:", "zlatent:"} {
		at := strings.Index(joined, name)
		if at < 0 {
			t.Fatalf("%s not named:\n%s", name, joined)
		}
		if at < latentHeaderAt {
			t.Errorf("%s appears above the LATENT header — a fitting core is not exposed:\n%s", name, joined)
		}
	}
	// Sorted within each bucket, so the report is stable run to run.
	if strings.Index(joined, "aexposed:") > strings.Index(joined, "zexposed:") {
		t.Errorf("exposed bucket not sorted by project:\n%s", joined)
	}
	if strings.Index(joined, "alatent:") > strings.Index(joined, "zlatent:") {
		t.Errorf("latent bucket not sorted by project:\n%s", joined)
	}
	// Both halves of an exposed project's core are shown, so the reader can see
	// WHICH half to shrink rather than guessing — as CheckCoreFloor does.
	for _, want := range []string{"resume", "workflow", "Current State; Open Threads"} {
		if !strings.Contains(joined, want) {
			t.Errorf("details missing %q:\n%s", want, joined)
		}
	}
}

// 🔴 THE BOUNDARY IS CoreMaxBytes ITSELF — NOT A NUMBER THIS CHECK INVENTED.
//
// A core of exactly the cap still fits, so its undeclared sections are latent and
// the run passes. One byte more and the ladder must reduce something, so the same
// sections become exposed and the run reports Info. If a future edit gives this
// check a threshold of its own, one of these two halves fails.
func TestCheckPinCoverage_ExposureBoundaryIsTheCoreCap(t *testing.T) {
	resumeBytes := len(underDeclaredResume)

	t.Run("exactly at the cap is latent", func(t *testing.T) {
		vault := t.TempDir()
		writeResume(t, vault, "edge", underDeclaredResume)
		writeWorkflow(t, vault, "edge", CoreMaxBytes-resumeBytes)

		r := CheckPinCoverage(storage.NewVault(vault))
		if r.Status != Pass {
			t.Fatalf("status = %v, want Pass — a core of exactly %d bytes still fits", r.Status, CoreMaxBytes)
		}
		if !strings.Contains(strings.Join(r.Details, "\n"), "edge: Current State; Open Threads") {
			t.Errorf("the latent census must still name it:\n%v", r.Details)
		}
	})

	t.Run("one byte over the cap is exposed", func(t *testing.T) {
		vault := t.TempDir()
		writeResume(t, vault, "edge", underDeclaredResume)
		writeWorkflow(t, vault, "edge", CoreMaxBytes-resumeBytes+1)

		r := CheckPinCoverage(storage.NewVault(vault))
		if r.Status != Info {
			t.Fatalf("status = %v, want Info one byte over the cap", r.Status)
		}
		if !strings.Contains(r.Summary, "1 of 1 EXPOSED") {
			t.Errorf("summary = %q", r.Summary)
		}
	})
}

// A project whose core is over the floor but whose resume is FULLY DECLARED is
// not a finding at all. Exposure is not a violation on its own — CheckCoreFloor
// reports that. This check only ever speaks about sections nobody has ruled on.
func TestCheckPinCoverage_OverCapButFullyDeclaredIsSilent(t *testing.T) {
	vault := t.TempDir()
	writeResume(t, vault, "fatbuttidy", fullyDeclaredResume)
	writeWorkflow(t, vault, "fatbuttidy", CoreMaxBytes)

	r := CheckPinCoverage(storage.NewVault(vault))
	if r.Status != Pass {
		t.Fatalf("status = %v, want Pass — an over-cap core with nothing undeclared is not this row's business", r.Status)
	}
	if r.Summary != "1 resume.md fully declared" {
		t.Errorf("summary = %q", r.Summary)
	}
	if len(r.Details) != 0 {
		t.Errorf("healthy state must be silent, got details %v", r.Details)
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
	if r.Status != Pass {
		t.Fatalf("status = %v, want Pass — the finding is real but this core fits", r.Status)
	}
	if want := "  teacher: How Marking Works"; !strings.HasPrefix(r.Details[1], want) {
		t.Errorf("details[1] = %q, want it to start %q — a fenced marker was read as a real declaration", r.Details[1], want)
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
	if r.Status != Pass {
		t.Fatalf("status = %v, want Pass — every core here fits, so every finding is latent", r.Status)
	}
	// 3 pin-declaring resumes scanned (alpha, tidy, zeta); unmarked excluded. The
	// pin-less exclusion count rides along on this Pass run, as does the census.
	if r.Summary != "2 of 3 latent — undeclared live sections, core fits, none shed (1 declare no pin zone)" {
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
