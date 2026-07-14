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
