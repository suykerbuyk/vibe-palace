// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

func TestPinnedResumeZone_KeepsPinnedDropsRest(t *testing.T) {
	const content = `---
type: project-resume
---

# proj — Working Context

## What This Project Is
<!-- vp:pin -->

A thing.

## Current State

Narrative that rots.

## Behavioral Notes
<!-- vp:pin -->

NEVER place a .vibe-palace.toml at $HOME.

## Project History

| # | Summary |
`
	zone, declared := pinnedResumeZone(content)
	if !declared {
		t.Fatal("declared=false on a document with two pin markers")
	}
	for _, want := range []string{"type: project-resume", "# proj — Working Context", "A thing.", "NEVER place a .vibe-palace.toml"} {
		if !strings.Contains(zone, want) {
			t.Errorf("pinned zone dropped %q", want)
		}
	}
	for _, unwanted := range []string{"Narrative that rots.", "| # | Summary |"} {
		if strings.Contains(zone, unwanted) {
			t.Errorf("pinned zone kept un-pinned content %q", unwanted)
		}
	}
}

// 🔴 A MARKER INSIDE A CODE FENCE IS DOCUMENTATION, NOT A DECLARATION.
//
// This is the third time this project has had to say so: 191's iteration-heading
// parser and 204's task-header parser were both fence-unaware, and both silently
// read sample text as structure. A resume that DOCUMENTS the pin marker inside a
// fenced example — which the template does, and which any resume explaining the
// mechanism to a human will do — must not thereby pin the section that documents
// it, nor count as having declared a zone at all.
func TestPinnedResumeZone_MarkerInAFenceIsNotADeclaration(t *testing.T) {
	const content = "# proj\n\n## How Pinning Works\n\nMark a section like this:\n\n```markdown\n## Some Section\n" +
		ResumePinMarker + "\n```\n\nThat is all.\n"

	zone, declared := pinnedResumeZone(content)
	if declared {
		t.Errorf("a marker quoted inside a code fence was read as a real declaration; zone=%q", zone)
	}
}

// A heading inside a fence is not a section boundary either — otherwise the
// fenced sample above would split the document and pin the wrong lines.
func TestPinnedResumeZone_HeadingInAFenceIsNotASection(t *testing.T) {
	const content = "# proj\n\n## Real Section\n" + ResumePinMarker + "\n\nkeep me\n\n```markdown\n## Fake Section\n```\n\nkeep me too\n\n## Shed Me\n\ndrop me\n"

	zone, declared := pinnedResumeZone(content)
	if !declared {
		t.Fatal("declared=false")
	}
	if !strings.Contains(zone, "keep me too") {
		t.Error("the fenced '## Fake Section' was treated as a real heading and truncated the pinned section")
	}
	if strings.Contains(zone, "drop me") {
		t.Error("un-pinned section survived")
	}
}

// A marker in the preamble pins nothing that was not already inline. Honoring it
// as a declaration would shed the entire body down to the frontmatter on the
// strength of one stray line — so it does not count, and the resume stays whole.
func TestPinnedResumeZone_MarkerInPreambleAloneIsNotADeclaration(t *testing.T) {
	const content = "# proj\n" + ResumePinMarker + "\n\n## State\n\nnarrative\n"

	if _, declared := pinnedResumeZone(content); declared {
		t.Error("a marker above the first H2 was read as a zone declaration — the whole body would be shed")
	}
}

func TestPinnedResumeZone_NoMarkerNoDeclaration(t *testing.T) {
	const content = "# proj\n\n## State\n\nnarrative\n\n## History\n\nmore\n"

	zone, declared := pinnedResumeZone(content)
	if declared || zone != "" {
		t.Errorf("declared=%v zone=%q, want false/empty — an unmarked resume must never be shed", declared, zone)
	}
}

func TestPinnedResumeZone_NoHeadingsAtAll(t *testing.T) {
	if _, declared := pinnedResumeZone("just some prose, no headings\n"); declared {
		t.Error("declared=true on a document with no H2 sections")
	}
}

// H3 is not H2: a pinned H2 carries its sub-headings with it, and an H3 cannot
// itself be pinned or start a section.
func TestPinnedResumeZone_H3StaysWithItsParent(t *testing.T) {
	const content = "# proj\n\n## Notes\n" + ResumePinMarker + "\n\n### Sub\n\nsub body\n\n## Diary\n\n### Also Sub\n\ndrop\n"

	zone, declared := pinnedResumeZone(content)
	if !declared {
		t.Fatal("declared=false")
	}
	if !strings.Contains(zone, "sub body") {
		t.Error("an H3 inside a pinned H2 was dropped")
	}
	if strings.Contains(zone, "drop") {
		t.Error("an H3 inside an un-pinned H2 survived")
	}
}

// --- UndeclaredLiveSections -------------------------------------------------

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 🔴 ABSENCE IS NOT A VALUE.
//
// A resume with no markers at all has not declared that everything is sheddable
// — it has declared nothing, and every section in it is live state until someone
// rules otherwise. All of them must be reported, by name, in file order.
func TestUndeclaredLiveSections_NoMarkersReportsEverySection(t *testing.T) {
	const content = "---\ntype: project-resume\n---\n\n# proj\n\n## State\n\nnarrative\n\n## History\n\nmore\n"

	got := UndeclaredLiveSections(content)
	want := []string{"State", "History"}
	if !eqStrings(got, want) {
		t.Errorf("UndeclaredLiveSections = %q, want %q", got, want)
	}
}

func TestUndeclaredLiveSections_OnlyPinnedReportsNothing(t *testing.T) {
	const content = "# proj\n\n## Notes\n" + ResumePinMarker + "\n\nkeep\n\n## Rules\n" + ResumePinMarker + "\n\nkeep too\n"

	if got := UndeclaredLiveSections(content); len(got) != 0 {
		t.Errorf("UndeclaredLiveSections = %q, want none — every section is declared", got)
	}
}

func TestUndeclaredLiveSections_OnlyDisposableReportsNothing(t *testing.T) {
	const content = "# proj\n\n## Diary\n" + ResumeDisposableMarker + "\n\nrots\n\n## History\n" + ResumeDisposableMarker + "\n\nalso rots\n"

	if got := UndeclaredLiveSections(content); len(got) != 0 {
		t.Errorf("UndeclaredLiveSections = %q, want none — a disposable declaration is still a declaration", got)
	}
}

func TestUndeclaredLiveSections_MixReportsOnlyTheUnmarked(t *testing.T) {
	const content = "# proj\n\n## Behavioral Notes\n" + ResumePinMarker + "\n\npinned\n\n" +
		"## Current State\n\nnobody ruled on this\n\n" +
		"## Project History\n" + ResumeDisposableMarker + "\n\nsheddable\n\n" +
		"## Open Questions\n\nnor this\n"

	got := UndeclaredLiveSections(content)
	want := []string{"Current State", "Open Questions"}
	if !eqStrings(got, want) {
		t.Errorf("UndeclaredLiveSections = %q, want %q", got, want)
	}
}

// A marker quoted inside a code fence is documentation, not a declaration — the
// same rule pinnedResumeZone obeys, and it must not diverge here. A resume that
// explains the disposable marker inside a fenced example (the template will) has
// still declared nothing about the section doing the explaining, so that section
// is live and must be reported.
func TestUndeclaredLiveSections_MarkerInAFenceDoesNotDeclare(t *testing.T) {
	const content = "# proj\n\n## How Marking Works\n\nMark a section like this:\n\n```markdown\n## Some Section\n" +
		ResumeDisposableMarker + "\n" + ResumePinMarker + "\n```\n\nThat is all.\n"

	got := UndeclaredLiveSections(content)
	want := []string{"How Marking Works"}
	if !eqStrings(got, want) {
		t.Errorf("UndeclaredLiveSections = %q, want %q — a fenced marker was read as a real declaration", got, want)
	}
}

// A section carrying BOTH markers is a contradiction, and the resolution is
// deliberate rather than incidental: the pin wins, the content is kept, and the
// section is NOT reported as undeclared. Someone did rule on it — twice — so
// sending the author back to it as an omission would be a false report. This
// test exists so the tie-break cannot be changed by accident.
func TestUndeclaredLiveSections_BothMarkersIsPinnedNotUndeclared(t *testing.T) {
	const content = "# proj\n\n## Contradictory\n" + ResumePinMarker + "\n" + ResumeDisposableMarker + "\n\nkeep me\n\n## Plain\n\nlive\n"

	got := UndeclaredLiveSections(content)
	want := []string{"Plain"}
	if !eqStrings(got, want) {
		t.Errorf("UndeclaredLiveSections = %q, want %q", got, want)
	}

	zone, declared := pinnedResumeZone(content)
	if !declared {
		t.Fatal("declared=false — a section carrying both markers must still count as a pin declaration")
	}
	if !strings.Contains(zone, "keep me") {
		t.Error("the disposable marker beat the pin marker; a contradictory declaration must resolve toward keeping content")
	}
}

func TestUndeclaredLiveSections_NoHeadingsAtAll(t *testing.T) {
	if got := UndeclaredLiveSections("just some prose, no headings\n"); len(got) != 0 {
		t.Errorf("UndeclaredLiveSections = %q, want none — there are no sections to report", got)
	}
}

// The preamble is not a section. It is unconditionally inline, it has no heading
// of its own to name, and a marker stranded above the first H2 declares nothing
// — so neither the preamble nor a phantom entry for it may ever be reported.
func TestUndeclaredLiveSections_PreambleIsNeverReported(t *testing.T) {
	const content = "---\ntype: project-resume\n---\n\n# proj — Working Context\n\nsome unmarked preamble prose\n\n## Only Section\n" + ResumePinMarker + "\n\nbody\n"

	if got := UndeclaredLiveSections(content); len(got) != 0 {
		t.Errorf("UndeclaredLiveSections = %q, want none — the preamble is not a section", got)
	}

	const stranded = "# proj\n" + ResumeDisposableMarker + "\n\n## State\n\nnarrative\n"
	got := UndeclaredLiveSections(stranded)
	want := []string{"State"}
	if !eqStrings(got, want) {
		t.Errorf("UndeclaredLiveSections = %q, want %q — a marker in the preamble must not declare the section below it", got, want)
	}
}

// An H3 can neither be declared nor start a section: its ruling is its parent
// H2's. Otherwise every sub-heading in a resume would be reported as undeclared
// live state and the report would be pure noise.
func TestUndeclaredLiveSections_H3IsNotASection(t *testing.T) {
	const content = "# proj\n\n## Notes\n" + ResumePinMarker + "\n\n### Sub\n\nbody\n\n## Diary\n\n### Also Sub\n\nbody\n"

	got := UndeclaredLiveSections(content)
	want := []string{"Diary"}
	if !eqStrings(got, want) {
		t.Errorf("UndeclaredLiveSections = %q, want %q", got, want)
	}
}

// The live template must actually declare a zone — otherwise every project
// scaffolded from it ships a resume the ladder cannot shed, and the fix reaches
// nobody. This is the "capability built, nothing invokes it" check, applied to
// the marker itself.
func TestResumeTemplateDeclaresAPinZone(t *testing.T) {
	raw, err := templates.FS().ReadFile("templates/resume.md")
	if err != nil {
		t.Fatalf("read embedded resume template: %v", err)
	}
	zone, declared := pinnedResumeZone(string(raw))
	if !declared {
		t.Fatal("the embedded resume template declares no pin zone — every new project would ship an unsheddable resume")
	}
	if !strings.Contains(zone, "Behavioral Notes") {
		t.Errorf("the template pins a zone that does not include the behavioral notes — the one section that is load-bearing for correctness:\n%s", zone)
	}
}
