// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mdutil

import (
	"strings"
	"testing"
)

// ── NormalizeSubheadingSlug ───────────────────────────────────────────────────

func TestNormalizeSubheadingSlug_NoSep(t *testing.T) {
	if got := NormalizeSubheadingSlug("Carried forward"); got != "Carried forward" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeSubheadingSlug_WithEmDash(t *testing.T) {
	if got := NormalizeSubheadingSlug("features-md-schema-migration — all phases complete"); got != "features-md-schema-migration" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeSubheadingSlug_EmDashAtStart(t *testing.T) {
	if got := NormalizeSubheadingSlug(" — trailing"); got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestNormalizeSubheadingSlug_DoubleDashNotMatch(t *testing.T) {
	if got := NormalizeSubheadingSlug("foo--bar"); got != "foo--bar" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeSubheadingSlug_EmDashNoSpaces(t *testing.T) {
	if got := NormalizeSubheadingSlug("foo—bar"); got != "foo—bar" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeSubheadingSlug_DoubleSpaceEmDash(t *testing.T) {
	if got := NormalizeSubheadingSlug("foo  — bar"); got != "foo " {
		t.Errorf("got %q, want %q", got, "foo ")
	}
}

func TestNormalizeSubheadingSlug_Empty(t *testing.T) {
	if got := NormalizeSubheadingSlug(""); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeSubheadingSlug_MultipleSeps(t *testing.T) {
	if got := NormalizeSubheadingSlug("a — b — c"); got != "a" {
		t.Errorf("got %q", got)
	}
}

// ── ReplaceSubsectionBody ────────────────────────────────────────────────────

const replaceSubsectionDoc = `# Doc

## Open Threads

### alpha

alpha body

### beta — some suffix

beta body

### gamma

gamma body

## Other Section

other content
`

func TestReplaceSubsectionBody_Basic(t *testing.T) {
	got, err := ReplaceSubsectionBody(replaceSubsectionDoc, "Open Threads", "beta", "new beta body")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "new beta body") {
		t.Error("missing new body")
	}
	if strings.Contains(got, "\nbeta body\n") {
		t.Error("old standalone beta body should be replaced")
	}
	if !strings.Contains(got, "alpha body") {
		t.Error("alpha section should be preserved")
	}
	if !strings.Contains(got, "gamma body") {
		t.Error("gamma section should be preserved")
	}
	if !strings.Contains(got, "other content") {
		t.Error("other section should be preserved")
	}
}

func TestReplaceSubsectionBody_ParentNotFound(t *testing.T) {
	_, err := ReplaceSubsectionBody(replaceSubsectionDoc, "Missing Parent", "alpha", "body")
	if err == nil || !strings.Contains(err.Error(), "Missing Parent") {
		t.Fatalf("want parent-not-found error, got %v", err)
	}
}

func TestReplaceSubsectionBody_SubNotFound(t *testing.T) {
	_, err := ReplaceSubsectionBody(replaceSubsectionDoc, "Open Threads", "nonexistent", "body")
	if err == nil {
		t.Fatal("want error for missing sub-heading")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention slug, got: %v", err)
	}
	if !strings.Contains(err.Error(), "alpha") {
		t.Errorf("error should list available slugs, got: %v", err)
	}
}

func TestReplaceSubsectionBody_AmbiguousMultiMatch(t *testing.T) {
	doc := "## Open Threads\n\n### dup\n\nbody1\n\n### dup\n\nbody2\n"
	got, err := ReplaceSubsectionBody(doc, "Open Threads", "dup", "new")
	if err == nil {
		t.Fatalf("multi-match should return hard error, got got=%q", got)
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should mention 'ambiguous'; got %v", err)
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Errorf("error should list candidate slugs; got %v", err)
	}
	if got != "" {
		t.Errorf("multi-match should return empty doc on error; got %q", got)
	}
}

func TestReplaceSubsectionBody_HTMLCommentInBodyRejected(t *testing.T) {
	doc := "## Open Threads\n\n### alpha\n\n<!-- marker -->\nalpha body\n"
	_, err := ReplaceSubsectionBody(doc, "Open Threads", "alpha", "new")
	if err == nil || !strings.Contains(err.Error(), "marker preservation") {
		t.Fatalf("want marker-preservation error, got %v", err)
	}
}

func TestReplaceSubsectionBody_HTMLCommentBetweenSectionsPreserved(t *testing.T) {
	doc := "## Open Threads\n\n<!-- between -->\n\n### alpha\n\nalpha body\n\n### beta\n\nbeta body\n"
	got, err := ReplaceSubsectionBody(doc, "Open Threads", "alpha", "new alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "<!-- between -->") {
		t.Error("HTML comment between sections should be preserved")
	}
}

func TestReplaceSubsectionBody_BodyWithCodeFence(t *testing.T) {
	newBody := "```go\n### not-a-heading\n```"
	got, err := ReplaceSubsectionBody(replaceSubsectionDoc, "Open Threads", "alpha", newBody)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "```go") {
		t.Error("code fence should be in output")
	}
	if !strings.Contains(got, "### not-a-heading") {
		t.Error("### inside code fence should survive verbatim")
	}
}

func TestReplaceSubsectionBody_SubAtVeryEndOfParent(t *testing.T) {
	doc := "## Open Threads\n\n### last\n\nlast body\n\n## Next\n\nnext content"
	got, err := ReplaceSubsectionBody(doc, "Open Threads", "last", "replaced")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "replaced") {
		t.Error("missing replacement")
	}
	if !strings.Contains(got, "next content") {
		t.Error("next section should be preserved")
	}
}

func TestReplaceSubsectionBody_EmptyBody(t *testing.T) {
	got, err := ReplaceSubsectionBody(replaceSubsectionDoc, "Open Threads", "alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "### alpha") {
		t.Error("heading should still be present")
	}
}

func TestReplaceSubsectionBody_ParentAtEndOfDoc(t *testing.T) {
	doc := "## First\n\ncontent\n\n## Open Threads\n\n### sub\n\nsub body"
	got, err := ReplaceSubsectionBody(doc, "Open Threads", "sub", "new sub")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "new sub") {
		t.Error("missing replacement")
	}
}

func TestReplaceSubsectionBody_NestedSiblings(t *testing.T) {
	doc := "## A\n\n### x\n\nx body\n\n## Open Threads\n\n### alpha\n\nalpha body\n\n## C\n\nc body"
	got, err := ReplaceSubsectionBody(doc, "Open Threads", "alpha", "new alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "new alpha") {
		t.Error("missing replacement")
	}
	if !strings.Contains(got, "x body") {
		t.Error("sibling section A should be untouched")
	}
	if !strings.Contains(got, "c body") {
		t.Error("sibling section C should be untouched")
	}
}

func TestReplaceSubsectionBody_SlugMatchesFullHeadingWhenNoSep(t *testing.T) {
	doc := "## Open Threads\n\n### Carried forward\n\nbullets\n"
	got, err := ReplaceSubsectionBody(doc, "Open Threads", "Carried forward", "new bullets")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "new bullets") {
		t.Error("missing replacement")
	}
}

// ── InsertSubsection ─────────────────────────────────────────────────────────

func TestInsertSubsection_ModeTop(t *testing.T) {
	doc := "## Open Threads\n\n### existing\n\nexisting body\n"
	got, err := InsertSubsection(doc, "Open Threads", InsertPosition{Mode: "top"}, "new-thread", "new body")
	if err != nil {
		t.Fatal(err)
	}
	newIdx := strings.Index(got, "### new-thread")
	existIdx := strings.Index(got, "### existing")
	if newIdx == -1 {
		t.Fatal("new-thread heading missing")
	}
	if newIdx >= existIdx {
		t.Error("top insert should precede existing sub-heading")
	}
}

func TestInsertSubsection_ModeBottom(t *testing.T) {
	doc := "## Open Threads\n\n### existing\n\nexisting body\n\n## Next\n\nnext"
	got, err := InsertSubsection(doc, "Open Threads", InsertPosition{Mode: "bottom"}, "new-thread", "new body")
	if err != nil {
		t.Fatal(err)
	}
	newIdx := strings.Index(got, "### new-thread")
	existIdx := strings.Index(got, "### existing")
	if newIdx == -1 {
		t.Fatal("new-thread heading missing")
	}
	if newIdx <= existIdx {
		t.Error("bottom insert should follow existing sub-heading")
	}
	if !strings.Contains(got, "next") {
		t.Error("next section should be preserved")
	}
}

func TestInsertSubsection_ModeAfter(t *testing.T) {
	doc := "## Open Threads\n\n### alpha\n\nalpha body\n\n### gamma\n\ngamma body\n"
	got, err := InsertSubsection(doc, "Open Threads", InsertPosition{Mode: "after", AnchorSlug: "alpha"}, "beta", "beta body")
	if err != nil {
		t.Fatal(err)
	}
	alphaIdx := strings.Index(got, "### alpha")
	betaIdx := strings.Index(got, "### beta")
	gammaIdx := strings.Index(got, "### gamma")
	if alphaIdx == -1 || betaIdx == -1 || gammaIdx == -1 {
		t.Fatal("missing expected headings")
	}
	if alphaIdx >= betaIdx || betaIdx >= gammaIdx {
		t.Errorf("order wrong: alpha=%d beta=%d gamma=%d", alphaIdx, betaIdx, gammaIdx)
	}
}

func TestInsertSubsection_ModeBefore(t *testing.T) {
	doc := "## Open Threads\n\n### alpha\n\nalpha body\n\n### gamma\n\ngamma body\n"
	got, err := InsertSubsection(doc, "Open Threads", InsertPosition{Mode: "before", AnchorSlug: "gamma"}, "beta", "beta body")
	if err != nil {
		t.Fatal(err)
	}
	alphaIdx := strings.Index(got, "### alpha")
	betaIdx := strings.Index(got, "### beta")
	gammaIdx := strings.Index(got, "### gamma")
	if alphaIdx == -1 || betaIdx == -1 || gammaIdx == -1 {
		t.Fatal("missing expected headings")
	}
	if alphaIdx >= betaIdx || betaIdx >= gammaIdx {
		t.Errorf("order wrong: alpha=%d beta=%d gamma=%d", alphaIdx, betaIdx, gammaIdx)
	}
}

func TestInsertSubsection_TopIntoEmptyParent(t *testing.T) {
	doc := "## Open Threads\n\n## Next\n\ncontent"
	got, err := InsertSubsection(doc, "Open Threads", InsertPosition{Mode: "top"}, "first", "first body")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "### first") {
		t.Error("new heading missing")
	}
	if !strings.Contains(got, "content") {
		t.Error("next section should be preserved")
	}
}

func TestInsertSubsection_BottomIntoEmptyParent(t *testing.T) {
	doc := "## Open Threads\n\n## Next\n\ncontent"
	got, err := InsertSubsection(doc, "Open Threads", InsertPosition{Mode: "bottom"}, "first", "first body")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "### first") {
		t.Error("new heading missing")
	}
}

func TestInsertSubsection_SlugAlreadyExists(t *testing.T) {
	doc := "## Open Threads\n\n### existing\n\nexisting body\n"
	_, err := InsertSubsection(doc, "Open Threads", InsertPosition{Mode: "top"}, "existing", "dup")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want already-exists error, got %v", err)
	}
}

func TestInsertSubsection_ParentNotFound(t *testing.T) {
	doc := "## Open Threads\n\n### x\n\nbody\n"
	_, err := InsertSubsection(doc, "Missing", InsertPosition{Mode: "top"}, "x", "body")
	if err == nil || !strings.Contains(err.Error(), "Missing") {
		t.Fatalf("want parent-not-found error, got %v", err)
	}
}

func TestInsertSubsection_AnchorNotFound(t *testing.T) {
	doc := "## Open Threads\n\n### alpha\n\nalpha body\n"
	_, err := InsertSubsection(doc, "Open Threads", InsertPosition{Mode: "after", AnchorSlug: "ghost"}, "new", "body")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want anchor-not-found error, got %v", err)
	}
}

func TestInsertSubsection_MissingAnchorSlug(t *testing.T) {
	doc := "## Open Threads\n\n### alpha\n\nalpha body\n"
	_, err := InsertSubsection(doc, "Open Threads", InsertPosition{Mode: "after"}, "new", "body")
	if err == nil || !strings.Contains(err.Error(), "anchor_slug") {
		t.Fatalf("want anchor_slug-required error, got %v", err)
	}
}

func TestInsertSubsection_UnknownMode(t *testing.T) {
	doc := "## Open Threads\n\n### alpha\n\nalpha body\n"
	_, err := InsertSubsection(doc, "Open Threads", InsertPosition{Mode: "sideways"}, "new", "body")
	if err == nil {
		t.Fatal("want error for unknown mode")
	}
}

// ── RemoveSubsection ─────────────────────────────────────────────────────────

func TestRemoveSubsection_Basic(t *testing.T) {
	doc := "## Open Threads\n\n### alpha\n\nalpha body\n\n### beta\n\nbeta body\n"
	got, err := RemoveSubsection(doc, "Open Threads", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "### alpha") {
		t.Error("alpha heading should be gone")
	}
	if strings.Contains(got, "alpha body") {
		t.Error("alpha body should be gone")
	}
	if !strings.Contains(got, "beta body") {
		t.Error("beta section should be preserved")
	}
}

func TestRemoveSubsection_NotFound(t *testing.T) {
	doc := "## Open Threads\n\n### alpha\n\nalpha body\n"
	_, err := RemoveSubsection(doc, "Open Threads", "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want not-found error, got %v", err)
	}
	if !strings.Contains(err.Error(), "alpha") {
		t.Errorf("error should list available slugs, got: %v", err)
	}
}

func TestRemoveSubsection_MultiMatch(t *testing.T) {
	doc := "## Open Threads\n\n### dup\n\nbody1\n\n### dup\n\nbody2\n"
	got, err := RemoveSubsection(doc, "Open Threads", "dup")
	if err == nil {
		t.Fatalf("multi-match should return hard error, got got=%q", got)
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should mention 'ambiguous'; got %v", err)
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Errorf("error should list candidate slugs; got %v", err)
	}
	if got != "" {
		t.Errorf("multi-match should return empty doc on error; got %q", got)
	}
}

func TestRemoveSubsection_ParentNotFound(t *testing.T) {
	doc := "## Open Threads\n\n### alpha\n\nalpha body\n"
	_, err := RemoveSubsection(doc, "Missing", "alpha")
	if err == nil || !strings.Contains(err.Error(), "Missing") {
		t.Fatalf("want parent-not-found error, got %v", err)
	}
}
