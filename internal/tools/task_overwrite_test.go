// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Tests for vp_manage_task action=overwrite — the typed whole-file writer, and
// the only sanctioned MCP path to text `amend` cannot address: the preamble
// above the first H2, an H2 heading's own wording, a whole-file migration.
//
// Under the ruled server-owns-vault architecture this is the SUCCESSOR to
// `vp tasks edit`, not a parallel to it: with no local vault for an editor to
// open, an absent overwrite means the preamble has no writer at all.

// overwriteFixture creates one active task and returns the vault, the tool, and
// the task's current whole-file content.
func overwriteFixture(t *testing.T, project, slug string) (*storage.Vault, string) {
	t.Helper()
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask(project, storage.TaskSpec{
		Slug:     slug,
		Title:    "Original Title",
		Content:  "Filed 2026-08-23 out of a sweep.\n\n## The gap\n\nOriginal framing.\n",
		Priority: "high",
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	_, content, err := vault.GetTask(project, slug)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	return vault, content
}

func callManageTask(t *testing.T, vault *storage.Vault, p manageTaskParams) (any, error) {
	t.Helper()
	params, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return ManageTaskTool(vault).Handler(context.Background(), params)
}

// TestOverwriteRoundTripRevisesBodyAndHeadingWording covers the whole-file
// round trip: the file comes back byte-for-byte as sent, and an H2 heading's own
// WORDING — which amend cannot change, because amend is keyed on that text — is
// revised.
//
// 🔴 It does NOT cover the preamble, despite the fixture text reading like one.
// CreateTask emits "## Context" FIRST, so everything passed as content lands
// UNDER that heading and a created task's preamble is EMPTY. This test was
// originally named ...RevisesPreambleAndHeading and asserted a preamble
// revision it never performed — a test name asserting coverage it does not have
// is the same defect class this epic is about, one layer down.
//
// The preamble is covered by TestOverwriteRevisesThePreambleAboveTheFirstH2,
// which asserts POSITION rather than presence.
func TestOverwriteRoundTripRevisesBodyAndHeadingWording(t *testing.T) {
	const project, slug = "proj", "some-task"
	vault, original := overwriteFixture(t, project, slug)

	if !strings.Contains(original, "Original framing.") {
		t.Fatalf("fixture is degenerate, missing the body text: %q", original)
	}

	// Revise body text and an H2 heading's wording. Both live BELOW the first
	// heading; the preamble is a separate test.
	revised := strings.Replace(original,
		"Filed 2026-08-23 out of a sweep.", "Filed 2026-08-23 out of a sweep. Provenance only.", 1)
	revised = strings.Replace(revised, "## The gap", "## The gap, restated", 1)
	if revised == original {
		t.Fatal("test bug: revision produced identical content")
	}

	res, err := callManageTask(t, vault, manageTaskParams{
		Project: project, Action: "overwrite", Task: slug, Content: revised,
	})
	if err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if m, ok := res.(map[string]string); !ok || m["status"] != "overwritten" {
		t.Errorf("result = %v, want status=overwritten", res)
	}

	_, got, err := vault.GetTask(project, slug)
	if err != nil {
		t.Fatalf("GetTask after overwrite: %v", err)
	}
	if got != revised {
		t.Errorf("file is not the revised content verbatim\n got: %q\nwant: %q", got, revised)
	}
	// Body text under the conventional first heading. NOT the preamble — see
	// the note on this function.
	if !strings.Contains(got, "Provenance only.") {
		t.Error("the body text under the first heading was not revised")
	}
	if !strings.Contains(got, "## The gap, restated") {
		t.Error("the H2 heading wording was not revised")
	}
}

// TestOverwriteRefusesArchivedTask pins the ACTIVE-only rule. The storage writer
// deliberately resolves all three directories and documents the archived
// question as the caller's; the refusal therefore lives in the handler, matching
// the CLI's guard rather than duplicating a rule into storage.
func TestOverwriteRefusesArchivedTask(t *testing.T) {
	const project, slug = "proj", "done-task"

	for _, tc := range []struct {
		name    string
		archive func(*storage.Vault) error
	}{
		{"retired", func(v *storage.Vault) error { return v.RetireTask(project, slug) }},
		{"cancelled", func(v *storage.Vault) error { return v.CancelTask(project, slug) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault, original := overwriteFixture(t, project, slug)
			if err := tc.archive(vault); err != nil {
				t.Fatalf("archive: %v", err)
			}
			_, archived, err := vault.GetTask(project, slug)
			if err != nil {
				t.Fatalf("GetTask after archive: %v", err)
			}

			revised := strings.Replace(archived, "Original framing.", "REWRITTEN HISTORY.", 1)
			if revised == archived {
				t.Fatal("test bug: revision produced identical content")
			}

			_, err = callManageTask(t, vault, manageTaskParams{
				Project: project, Action: "overwrite", Task: slug, Content: revised,
			})
			if err == nil {
				t.Fatal("expected a refusal for an archived task")
			}
			if !strings.Contains(err.Error(), "archived") {
				t.Errorf("refusal must say the task is archived, got %q", err)
			}

			_, after, gerr := vault.GetTask(project, slug)
			if gerr != nil {
				t.Fatalf("GetTask after refusal: %v", gerr)
			}
			if after != archived {
				t.Error("the archived file was modified despite the refusal")
			}
			if strings.Contains(after, "REWRITTEN HISTORY.") {
				t.Error("archived history was rewritten")
			}
			_ = original
		})
	}
}

// TestOverwriteRefusesHeaderSmuggling is the guard that keeps overwrite from
// becoming a second writer for fields that already have one. validateWholeTaskFile
// checks SHAPE only — it never sees the old file, so it cannot tell that a Status
// line now says something different.
func TestOverwriteRefusesHeaderSmuggling(t *testing.T) {
	const project, slug = "proj", "smuggle-task"

	for _, tc := range []struct {
		name       string
		old, new   string
		wantField  string
		wantAction string
	}{
		{"status", "**Status:** pending", "**Status:** in_progress", "**Status:**", "update_status"},
		{"priority", "**Priority:** high", "**Priority:** low", "**Priority:**", "set_meta"},
		{"title", "# Original Title", "# Smuggled Title", "title", "set_meta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault, original := overwriteFixture(t, project, slug)

			smuggled := strings.Replace(original, tc.old, tc.new, 1)
			if smuggled == original {
				t.Fatalf("test bug: %q not present in fixture", tc.old)
			}

			_, err := callManageTask(t, vault, manageTaskParams{
				Project: project, Action: "overwrite", Task: slug, Content: smuggled,
			})
			if err == nil {
				t.Fatalf("expected a refusal for a body that changes %s", tc.wantField)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("refusal must name the smuggled field %q, got %q", tc.wantField, err)
			}
			// The refusal must point at the action that DOES own the field —
			// a refusal that only says no gets worked around.
			if !strings.Contains(err.Error(), tc.wantAction) {
				t.Errorf("refusal must name %q as the owning action, got %q", tc.wantAction, err)
			}

			// 🔴 The file must be untouched. A guard that refuses after writing
			// is not a guard.
			_, after, gerr := vault.GetTask(project, slug)
			if gerr != nil {
				t.Fatalf("GetTask after refusal: %v", gerr)
			}
			if after != original {
				t.Errorf("file was modified despite the refusal\n got: %q\nwant: %q", after, original)
			}
		})
	}
}

// TestOverwriteAcceptsAnUnchangedHeader is the negative control for the guard
// above: revising only the body, with the header re-sent verbatim, must pass.
// Without this, a guard that refused everything would look correct.
func TestOverwriteAcceptsAnUnchangedHeader(t *testing.T) {
	const project, slug = "proj", "control-task"
	vault, original := overwriteFixture(t, project, slug)

	revised := strings.Replace(original, "Original framing.", "Revised framing.", 1)
	if _, err := callManageTask(t, vault, manageTaskParams{
		Project: project, Action: "overwrite", Task: slug, Content: revised,
	}); err != nil {
		t.Fatalf("an overwrite that leaves the header alone must succeed: %v", err)
	}
	_, got, _ := vault.GetTask(project, slug)
	if got != revised {
		t.Error("body revision did not land")
	}
}

// TestOverwriteRequiresContent pins the handler-side arm. The schema requires
// content on overwrite too, but the handler is what runs for a direct call.
func TestOverwriteRequiresContent(t *testing.T) {
	const project, slug = "proj", "empty-task"
	vault, _ := overwriteFixture(t, project, slug)

	_, err := callManageTask(t, vault, manageTaskParams{
		Project: project, Action: "overwrite", Task: slug, Content: "",
	})
	if err == nil {
		t.Fatal("expected a refusal for an empty overwrite body")
	}
	if !strings.Contains(err.Error(), "ENTIRE task file") {
		t.Errorf("refusal must say content is the whole file, got %q", err)
	}
}

// TestCreatedTaskHasTheConventionalFirstHeading pins DoD item 1: create emits
// the conventional first H2 UNCONDITIONALLY, so the first prose in a task file
// is addressable by amend and the region above it is provenance-only.
//
// MUTATION: remove the heading emission from CreateTask and this goes red.
func TestCreatedTaskHasTheConventionalFirstHeading(t *testing.T) {
	const project = "proj"

	for _, tc := range []struct {
		name    string
		content string
	}{
		{"body with no heading of its own", "Filed out of a sweep.\n\nSome prose.\n"},
		// The unconditional case: content already opens with its own H2. The
		// author gets an EMPTY Context section, which is the cost of the
		// guarantee. A rule that applies only when the author forgot a heading
		// is not a rule a reader can rely on.
		{"body that already opens with an H2", "## Their Own Heading\n\nSome prose.\n"},
		{"empty body", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault := storage.NewVault(t.TempDir())
			slug := "t"
			if err := vault.CreateTask(project, storage.TaskSpec{
				Slug: slug, Title: "T", Content: tc.content, Priority: "medium",
			}); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
			_, got, err := vault.GetTask(project, slug)
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}

			want := "## " + storage.ConventionalFirstHeading
			if !strings.Contains(got, want) {
				t.Fatalf("created task is missing %q:\n%s", want, got)
			}

			// It must be the FIRST H2, not merely present.
			var firstH2 string
			for _, line := range strings.Split(got, "\n") {
				if strings.HasPrefix(line, "## ") {
					firstH2 = line
					break
				}
			}
			if firstH2 != want {
				t.Errorf("first H2 = %q, want %q — the preamble must end at the conventional heading", firstH2, want)
			}
		})
	}
}

// headerAndPreamble splits a whole task file at the two boundaries that matter
// here: the end of the contiguous header-field block, and the first H2.
//
// The middle slice is the PREAMBLE — the region `create` writes once and no
// typed action but `overwrite` can reach. Deriving it positionally, rather than
// searching for a known string, is what makes the assertions below about
// LOCATION rather than about presence.
func headerAndPreamble(t *testing.T, content string) (header, preamble string) {
	t.Helper()
	h2 := strings.Index(content, "\n## ")
	if h2 < 0 {
		t.Fatalf("task file has no H2 heading:\n%s", content)
	}
	lines := strings.Split(content[:h2], "\n")
	last := -1
	for i, l := range lines {
		tl := strings.TrimSpace(l)
		if strings.HasPrefix(tl, "# ") ||
			strings.HasPrefix(tl, "**Status:**") || strings.HasPrefix(tl, "**Priority:**") ||
			strings.HasPrefix(tl, "**Parent:**") || strings.HasPrefix(tl, "**Depends:**") {
			last = i
		}
	}
	if last < 0 {
		t.Fatalf("task file has no header block:\n%s", content)
	}
	return strings.Join(lines[:last+1], "\n"), strings.TrimSpace(strings.Join(lines[last+1:], "\n"))
}

// TestOverwriteRevisesThePreambleAboveTheFirstH2 is the test the round-trip case
// above does NOT provide, and the one this whole task exists for.
//
// 🔴 Since CreateTask emits "## Context" FIRST, a created task's preamble is
// EMPTY and everything an author passes as content lands UNDER that heading.
// So a test that edits fixture body text — as the round-trip one does — proves
// heading-wording revision and proves nothing at all about the preamble. The
// region between the header block and the first H2 is reachable by no typed
// action except overwrite, and it has to be asserted POSITIONALLY.
//
// Both halves are covered: INSERTING a preamble where there was none, and then
// CHANGING the one that now exists.
func TestOverwriteRevisesThePreambleAboveTheFirstH2(t *testing.T) {
	const project, slug = "proj", "preamble-task"
	vault, original := overwriteFixture(t, project, slug)

	originalHeader, originalPreamble := headerAndPreamble(t, original)
	if originalPreamble != "" {
		t.Fatalf("fixture precondition failed: a created task's preamble must be empty, got %q", originalPreamble)
	}

	firstH2 := "## " + storage.ConventionalFirstHeading
	marker := strings.Index(original, firstH2)
	if marker < 0 {
		t.Fatalf("fixture is missing %q", firstH2)
	}

	// --- 1. INSERT a preamble where there was none. ---
	const provenance = "Filed 2026-08-23 out of the honest-instruments sweep, against 1f9595c."
	inserted := strings.Replace(original, firstH2, provenance+"\n\n"+firstH2, 1)
	if inserted == original {
		t.Fatal("test bug: insertion produced identical content")
	}

	if _, err := callManageTask(t, vault, manageTaskParams{
		Project: project, Action: "overwrite", Task: slug, Content: inserted,
	}); err != nil {
		t.Fatalf("overwrite inserting a preamble: %v", err)
	}

	_, got, err := vault.GetTask(project, slug)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	gotHeader, gotPreamble := headerAndPreamble(t, got)
	if gotPreamble != provenance {
		t.Errorf("preamble = %q, want %q", gotPreamble, provenance)
	}

	// POSITIONAL: the text must sit ABOVE the conventional first heading. A
	// containment check alone would pass if the line had landed under it, which
	// is exactly the bug this test replaces.
	pi := strings.Index(got, provenance)
	hi := strings.Index(got, firstH2)
	switch {
	case pi < 0:
		t.Fatalf("provenance line absent from the file:\n%s", got)
	case hi < 0:
		t.Fatalf("conventional heading absent from the file:\n%s", got)
	case pi > hi:
		t.Errorf("provenance line is BELOW %q (at %d vs %d) — that is body text, not the preamble",
			firstH2, pi, hi)
	}

	// The header block must be byte-identical: overwrite may revise the
	// preamble and must not touch fields other actions own.
	if gotHeader != originalHeader {
		t.Errorf("header block changed\n got: %q\nwant: %q", gotHeader, originalHeader)
	}

	// --- 2. CHANGE the preamble that now exists. ---
	const revised = "Filed 2026-08-23; premise corrected 2026-08-24. Provenance only."
	changed := strings.Replace(got, provenance, revised, 1)
	if changed == got {
		t.Fatal("test bug: revision produced identical content")
	}

	if _, err := callManageTask(t, vault, manageTaskParams{
		Project: project, Action: "overwrite", Task: slug, Content: changed,
	}); err != nil {
		t.Fatalf("overwrite revising an existing preamble: %v", err)
	}

	_, got2, err := vault.GetTask(project, slug)
	if err != nil {
		t.Fatalf("GetTask after revision: %v", err)
	}
	header2, preamble2 := headerAndPreamble(t, got2)
	if preamble2 != revised {
		t.Errorf("revised preamble = %q, want %q", preamble2, revised)
	}
	if strings.Contains(got2, provenance) {
		t.Error("the superseded preamble text survived the revision")
	}
	if header2 != originalHeader {
		t.Errorf("header block changed on the second overwrite\n got: %q\nwant: %q", header2, originalHeader)
	}
}
