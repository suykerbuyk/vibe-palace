// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package absorb

import (
	"strings"
	"testing"
)

func TestSplitHeadingBasic(t *testing.T) {
	src := "# Title\n\nIntro text.\n\n## First\n\nalpha\n\n## Second\n\nbeta\n"
	got := Split([]byte(src), SplitHeading)
	if len(got) != 3 {
		t.Fatalf("expected 3 sections, got %d: %+v", len(got), got)
	}
	if got[0].Heading != "Title" || got[0].Level != 1 {
		t.Errorf("section[0] = %+v", got[0])
	}
	if got[1].Heading != "First" || !strings.Contains(got[1].Body, "alpha") {
		t.Errorf("section[1] = %+v", got[1])
	}
	if got[2].Heading != "Second" || !strings.Contains(got[2].Body, "beta") {
		t.Errorf("section[2] = %+v", got[2])
	}
}

func TestSplitHeadingPreamble(t *testing.T) {
	src := "intro without heading\nanother line\n\n## First\n\nalpha\n"
	got := Split([]byte(src), SplitHeading)
	if len(got) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(got))
	}
	if got[0].Heading != "" || got[0].Level != 0 {
		t.Errorf("preamble section = %+v", got[0])
	}
	if !strings.Contains(got[0].Body, "intro without heading") {
		t.Errorf("preamble lost body: %+v", got[0])
	}
}

func TestSplitHeadingSkipsManagedBlock(t *testing.T) {
	src := "# Title\n\nIntro.\n\n<!-- vibe-palace:begin v=1 sha=abcdef0 -->\n## Vibe-Palace Integration\nblock body\n<!-- vibe-palace:end -->\n\n## After\n\nafter text.\n"
	got := Split([]byte(src), SplitHeading)
	for _, s := range got {
		if strings.Contains(s.Body, "block body") {
			t.Fatalf("managed block leaked into section: %+v", s)
		}
		if s.Heading == "Vibe-Palace Integration" {
			t.Fatalf("managed heading leaked as section")
		}
	}
	// Should have: Title, After.
	if len(got) != 2 {
		t.Fatalf("expected 2 sections, got %d: %+v", len(got), got)
	}
	if got[1].Heading != "After" {
		t.Errorf("section[1] = %+v", got[1])
	}
}

func TestSplitHeadingIgnoresHeadingsInFence(t *testing.T) {
	src := "# Title\n\nintro\n\n```\n# not-a-heading\n## also-not\n```\n\n## Real\n\nreal body\n"
	got := Split([]byte(src), SplitHeading)
	if len(got) != 2 {
		t.Fatalf("expected 2 sections, got %d: %+v", len(got), got)
	}
	if got[1].Heading != "Real" {
		t.Errorf("section[1] = %+v (body %q)", got[1], got[1].Body)
	}
}

func TestSplitHeadingPreservesNestedHeadings(t *testing.T) {
	src := "## Parent\n\nparent body\n\n### Child\n\nchild body\n\n## Sibling\n\nsibling body\n"
	got := Split([]byte(src), SplitHeading)
	// Our splitter is flat — ### Child opens a new section. That's OK
	// for routing purposes; document the contract here.
	if len(got) != 3 {
		t.Fatalf("expected 3 sections, got %d: %+v", len(got), got)
	}
	if got[1].Level != 3 || got[1].Heading != "Child" {
		t.Errorf("child section = %+v", got[1])
	}
}

func TestSplitWholeFile(t *testing.T) {
	src := "free-form content\nwith multiple lines\nand no headings\n"
	got := Split([]byte(src), SplitWholeFile)
	if len(got) != 1 {
		t.Fatalf("expected 1 section, got %d", len(got))
	}
	if got[0].Level != 0 || got[0].Heading != "" {
		t.Errorf("whole-file section = %+v", got[0])
	}
	if !strings.Contains(got[0].Body, "free-form content") {
		t.Errorf("body lost: %+v", got[0])
	}
}

func TestSplitWholeFileEmptyAfterBlock(t *testing.T) {
	// File with only a managed block yields zero sections.
	src := "<!-- vibe-palace:begin v=1 sha=abcdef0 -->\nbody\n<!-- vibe-palace:end -->\n"
	if got := Split([]byte(src), SplitWholeFile); len(got) != 0 {
		t.Errorf("expected 0 sections from managed-only file, got %d: %+v", len(got), got)
	}
	if got := Split([]byte(src), SplitHeading); len(got) != 0 {
		t.Errorf("expected 0 sections (heading) from managed-only file, got %d: %+v", len(got), got)
	}
}
