// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mdutil

import (
	"strings"
	"testing"
)

// ── fixture corpus ────────────────────────────────────────────────────────────
//
// These fixtures mirror the real ### Carried forward formatting observed in
// Projects/vibe-vault/agentctx/resume.md at implementation time.

// singleBullet is a minimal doc with one canonical bullet.
const singleBulletDoc = `# Test

## Open Threads

### Carried forward

- **mcp subtest** — tool-count assertion brittle

## Project History

nothing
`

// multiLineSlugDoc has a bullet whose bold slug spans two lines (as in the
// real resume.md: "iter\n  145 plan" pattern).
const multiLineSlugDoc = `# Test

## Open Threads

### Carried forward

- **Opportunity O1: delete DryRun field (iter
  145 plan, deferred)** — after the dead migration code removal, no
  migration consumes the field.
- **Phase 3 dry-run coverage gap** — the outer syncProject still
  short-circuits every migration under dry-run.

## Project History

nothing
`

// emptyCarriedDoc has a ### Carried forward section with no bullets.
const emptyCarriedDoc = `# Test

## Open Threads

### Carried forward

## Project History

nothing
`

// ── ParseCarriedForward ───────────────────────────────────────────────────────

func TestParseCarriedForward_Empty(t *testing.T) {
	bullets := ParseCarriedForward("")
	if len(bullets) != 0 {
		t.Errorf("empty body: got %d bullets, want 0", len(bullets))
	}
}

func TestParseCarriedForward_SingleCanonical(t *testing.T) {
	body := "- **mcp subtest** — tool-count assertion brittle\n"
	bullets := ParseCarriedForward(body)
	if len(bullets) != 1 {
		t.Fatalf("want 1 bullet, got %d", len(bullets))
	}
	b := bullets[0]
	if b.Slug != "mcp subtest" {
		t.Errorf("slug: got %q, want %q", b.Slug, "mcp subtest")
	}
	if !strings.Contains(b.Body, "tool-count assertion brittle") {
		t.Errorf("body missing expected text: %q", b.Body)
	}
	if b.RawForm != "em-dash" {
		t.Errorf("rawform: got %q, want em-dash", b.RawForm)
	}
}

func TestParseCarriedForward_VariantCanonical(t *testing.T) {
	body := "- **canonical-slug**\n"
	bullets := ParseCarriedForward(body)
	if len(bullets) != 1 {
		t.Fatalf("want 1 bullet, got %d", len(bullets))
	}
	b := bullets[0]
	if b.Slug != "canonical-slug" {
		t.Errorf("slug: got %q, want %q", b.Slug, "canonical-slug")
	}
	if b.RawForm != "canonical" {
		t.Errorf("rawform: got %q, want canonical", b.RawForm)
	}
}

func TestParseCarriedForward_VariantBoldColon(t *testing.T) {
	body := "- **colon-slug:**\n"
	bullets := ParseCarriedForward(body)
	if len(bullets) != 1 {
		t.Fatalf("want 1 bullet, got %d", len(bullets))
	}
	b := bullets[0]
	if b.Slug != "colon-slug" {
		t.Errorf("slug: got %q, want %q", b.Slug, "colon-slug")
	}
	if b.RawForm != "bold-colon" {
		t.Errorf("rawform: got %q, want bold-colon", b.RawForm)
	}
}

func TestParseCarriedForward_VariantBoldParen(t *testing.T) {
	body := "- **paren-slug (with note)**\n"
	bullets := ParseCarriedForward(body)
	if len(bullets) != 1 {
		t.Fatalf("want 1 bullet, got %d", len(bullets))
	}
	b := bullets[0]
	if b.Slug != "paren-slug (with note)" {
		t.Errorf("slug: got %q, want %q", b.Slug, "paren-slug (with note)")
	}
	if b.RawForm != "bold-paren" {
		t.Errorf("rawform: got %q, want bold-paren", b.RawForm)
	}
}

func TestParseCarriedForward_VariantEmDash(t *testing.T) {
	body := "- **em-dash-slug** — body text here\n"
	bullets := ParseCarriedForward(body)
	if len(bullets) != 1 {
		t.Fatalf("want 1 bullet, got %d", len(bullets))
	}
	b := bullets[0]
	if b.Slug != "em-dash-slug" {
		t.Errorf("slug: got %q, want %q", b.Slug, "em-dash-slug")
	}
	if b.Body != "body text here" {
		t.Errorf("body: got %q, want %q", b.Body, "body text here")
	}
	if b.RawForm != "em-dash" {
		t.Errorf("rawform: got %q, want em-dash", b.RawForm)
	}
}

func TestParseCarriedForward_VariantPlain(t *testing.T) {
	// Plain text bullet: slug derived from first sentence.
	body := "- plain text bullet. Extra text here.\n"
	bullets := ParseCarriedForward(body)
	if len(bullets) != 1 {
		t.Fatalf("want 1 bullet, got %d", len(bullets))
	}
	b := bullets[0]
	if b.Slug != "plain text bullet" {
		t.Errorf("slug: got %q, want %q", b.Slug, "plain text bullet")
	}
	if b.RawForm != "plain" {
		t.Errorf("rawform: got %q, want plain", b.RawForm)
	}
}

func TestParseCarriedForward_VariantPlainNoSentenceMark(t *testing.T) {
	// Plain text with no sentence-ending punctuation: first 6 words.
	body := "- one two three four five six seven eight\n"
	bullets := ParseCarriedForward(body)
	if len(bullets) != 1 {
		t.Fatalf("want 1 bullet, got %d", len(bullets))
	}
	b := bullets[0]
	// Slug derived: first 6 words
	if b.Slug != "one two three four five six" {
		t.Errorf("slug: got %q, want first 6 words", b.Slug)
	}
}

func TestParseCarriedForward_MultilineBoldSlug(t *testing.T) {
	// Bold slug spans two lines, closing ** on second line.
	body := "- **Opportunity O1: delete DryRun field (iter\n  145 plan, deferred)** — body text\n"
	bullets := ParseCarriedForward(body)
	if len(bullets) != 1 {
		t.Fatalf("want 1 bullet, got %d", len(bullets))
	}
	b := bullets[0]
	if !strings.Contains(b.Slug, "Opportunity O1") {
		t.Errorf("slug should contain 'Opportunity O1', got %q", b.Slug)
	}
	if b.RawForm != "em-dash" {
		t.Errorf("rawform: got %q, want em-dash", b.RawForm)
	}
	if !strings.Contains(b.Body, "body text") {
		t.Errorf("body missing expected text: %q", b.Body)
	}
}

func TestParseCarriedForward_MultiBullet(t *testing.T) {
	// Two bullets: counts correctly.
	body := "- **alpha** — first\n- **beta** — second\n"
	bullets := ParseCarriedForward(body)
	if len(bullets) != 2 {
		t.Fatalf("want 2 bullets, got %d", len(bullets))
	}
	if bullets[0].Slug != "alpha" {
		t.Errorf("bullet 0 slug: got %q, want alpha", bullets[0].Slug)
	}
	if bullets[1].Slug != "beta" {
		t.Errorf("bullet 1 slug: got %q, want beta", bullets[1].Slug)
	}
}

func TestParseCarriedForward_ContinuationLines(t *testing.T) {
	// Bullet with continuation lines.
	body := "- **slug** — first line\n  continuation one\n  continuation two\n"
	bullets := ParseCarriedForward(body)
	if len(bullets) != 1 {
		t.Fatalf("want 1 bullet, got %d", len(bullets))
	}
	b := bullets[0]
	if !strings.Contains(b.Body, "first line") {
		t.Errorf("body missing first line text: %q", b.Body)
	}
	if !strings.Contains(b.Body, "continuation one") {
		t.Errorf("body missing continuation: %q", b.Body)
	}
}

// ── round-trip preservation ───────────────────────────────────────────────────

func TestParseCarriedForward_RoundTrip_SingleBullet(t *testing.T) {
	body := "- **mcp subtest** — tool-count assertion brittle\n"
	bullets := ParseCarriedForward(body)
	emitted := EmitCarriedBullets(bullets)
	if emitted != body {
		t.Errorf("round-trip failed:\ngot:  %q\nwant: %q", emitted, body)
	}
}

func TestParseCarriedForward_RoundTrip_MultilineBold(t *testing.T) {
	// Multi-line bold slug should be preserved verbatim.
	body := "- **Opportunity O1: delete DryRun (iter\n  145 plan)** — body\n"
	bullets := ParseCarriedForward(body)
	emitted := EmitCarriedBullets(bullets)
	if emitted != body {
		t.Errorf("round-trip failed:\ngot:  %q\nwant: %q", emitted, body)
	}
}

func TestParseCarriedForward_RoundTrip_MultiBullet(t *testing.T) {
	// Two bullets: both preserved.
	body := "- **alpha** — first\n- **beta** — second\n"
	bullets := ParseCarriedForward(body)
	emitted := EmitCarriedBullets(bullets)
	if emitted != body {
		t.Errorf("round-trip failed:\ngot:  %q\nwant: %q", emitted, body)
	}
}

func TestParseCarriedForward_RoundTrip_WithContinuation(t *testing.T) {
	body := "- **slug** — first line\n  continuation one\n  continuation two\n"
	bullets := ParseCarriedForward(body)
	emitted := EmitCarriedBullets(bullets)
	if emitted != body {
		t.Errorf("round-trip failed:\ngot:  %q\nwant: %q", emitted, body)
	}
}

func TestParseCarriedForward_RoundTrip_TwelveBullets(t *testing.T) {
	// Real 12-bullet body from resume.md (representative sample).
	body := "- **Opportunity O1: delete DryRun field (iter\n" +
		"  145 plan, deferred at iter 146 ship)** — after the dead code removal.\n" +
		"- **Harness plan mode pattern** — non-code harness follow-up.\n" +
		"- **mcp subtest tool-count assertion brittle** — brittle length check.\n" +
		"- **Phase 3 dry-run coverage gap** — outer path never reached.\n" +
		"- **Phase 6.1 cwd semantics revisit** — stamp CWD vs opts.CWD.\n" +
		"- **Session synthesis agent** — enabled by default, inert without LLM.\n" +
		"- **Zed Track C (ACP proxy)** — decision point after 2 weeks usage.\n" +
		"- **Root cause of case-sibling shell creation is unpatched.**\n" +
		"- **`00_Regulatory Monitor` has a space in the directory name.**\n" +
		"- **`cortana-obsidian` references** — 3 scaffold template files.\n" +
		"- **`agentctx/notes/` subdirectory not yet in templates schema (iter 149)**\n" +
		"  — the iter-149 L2 fix moved improvements.md.\n" +
		"- **Vault `.gitattributes` for jsonl files (iter 151\n" +
		"  observation)** — adjacent to vault-push-parallelism task.\n"
	bullets := ParseCarriedForward(body)
	if len(bullets) != 12 {
		t.Fatalf("want 12 bullets, got %d", len(bullets))
	}
	emitted := EmitCarriedBullets(bullets)
	if emitted != body {
		t.Errorf("round-trip failed:\ngot:\n%s\nwant:\n%s", emitted, body)
	}
}

// ── AddCarriedBullet ──────────────────────────────────────────────────────────

func TestAddCarriedBullet_ToEmpty(t *testing.T) {
	got, err := AddCarriedBullet(emptyCarriedDoc, "Open Threads", "new-slug", "new title", "new body")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "**new-slug**") {
		t.Error("missing new slug")
	}
	if !strings.Contains(got, "new title") {
		t.Error("missing new title")
	}
}

func TestAddCarriedBullet_ToSingle(t *testing.T) {
	got, err := AddCarriedBullet(singleBulletDoc, "Open Threads", "new-slug", "title", "body")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "**new-slug**") {
		t.Error("missing new slug")
	}
	// Original bullet preserved.
	if !strings.Contains(got, "**mcp subtest**") {
		t.Error("original bullet missing")
	}
	// New slug appears after original.
	origIdx := strings.Index(got, "**mcp subtest**")
	newIdx := strings.Index(got, "**new-slug**")
	if newIdx <= origIdx {
		t.Errorf("new slug should appear after original: orig=%d new=%d", origIdx, newIdx)
	}
}

func TestAddCarriedBullet_ToMulti(t *testing.T) {
	got, err := AddCarriedBullet(multiLineSlugDoc, "Open Threads", "new-slug", "title", "body")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "**new-slug**") {
		t.Error("missing new slug")
	}
	// Both original bullets preserved.
	if !strings.Contains(got, "Opportunity O1") {
		t.Error("first original bullet missing")
	}
	if !strings.Contains(got, "Phase 3 dry-run") {
		t.Error("second original bullet missing")
	}
}

func TestAddCarriedBullet_SlugAlreadyExists(t *testing.T) {
	_, err := AddCarriedBullet(singleBulletDoc, "Open Threads", "mcp subtest", "title", "body")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want already-exists error, got %v", err)
	}
}

func TestAddCarriedBullet_SlugAlreadyExists_CaseInsensitive(t *testing.T) {
	_, err := AddCarriedBullet(singleBulletDoc, "Open Threads", "MCP SUBTEST", "title", "body")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want already-exists error (case-insensitive), got %v", err)
	}
}

func TestAddCarriedBullet_ParentNotFound(t *testing.T) {
	_, err := AddCarriedBullet(singleBulletDoc, "Missing Section", "slug", "title", "body")
	if err == nil || !strings.Contains(err.Error(), "Missing Section") {
		t.Fatalf("want parent-not-found error, got %v", err)
	}
}

func TestAddCarriedBullet_CarriedForwardNotFound(t *testing.T) {
	doc := "## Open Threads\n\n### alpha\n\nalpha body\n"
	_, err := AddCarriedBullet(doc, "Open Threads", "slug", "title", "body")
	if err == nil || !strings.Contains(err.Error(), "Carried forward") {
		t.Fatalf("want carried-forward-not-found error, got %v", err)
	}
}

func TestAddCarriedBullet_CanonicalForm(t *testing.T) {
	got, err := AddCarriedBullet(emptyCarriedDoc, "Open Threads", "my-slug", "my title", "my body")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Must use canonical form "- **slug** — title body"
	if !strings.Contains(got, "- **my-slug**") {
		t.Errorf("not in canonical form, doc:\n%s", got)
	}
}

// ── RemoveCarriedBullet ───────────────────────────────────────────────────────

func TestRemoveCarriedBullet_Single(t *testing.T) {
	got, err := RemoveCarriedBullet(singleBulletDoc, "Open Threads", "mcp subtest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "**mcp subtest**") {
		t.Error("removed bullet should be gone")
	}
	// Section heading must still exist.
	if !strings.Contains(got, "### Carried forward") {
		t.Error("Carried forward heading should be preserved")
	}
}

func TestRemoveCarriedBullet_Multi_RemoveFirst(t *testing.T) {
	got, err := RemoveCarriedBullet(multiLineSlugDoc, "Open Threads", "Opportunity O1: delete DryRun field (iter 145 plan, deferred)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "Opportunity O1") {
		t.Error("first bullet should be removed")
	}
	if !strings.Contains(got, "Phase 3 dry-run") {
		t.Error("second bullet should be preserved")
	}
}

func TestRemoveCarriedBullet_Multi_RemoveSecond(t *testing.T) {
	got, err := RemoveCarriedBullet(multiLineSlugDoc, "Open Threads", "Phase 3 dry-run coverage gap")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "Phase 3 dry-run") {
		t.Error("second bullet should be removed")
	}
	if !strings.Contains(got, "Opportunity O1") {
		t.Error("first bullet should be preserved")
	}
}

func TestRemoveCarriedBullet_CaseInsensitive(t *testing.T) {
	// Slug match is case-insensitive.
	got, err := RemoveCarriedBullet(singleBulletDoc, "Open Threads", "MCP SUBTEST")
	if err != nil {
		t.Fatalf("case-insensitive remove failed: %v", err)
	}
	if strings.Contains(got, "**mcp subtest**") {
		t.Error("removed bullet should be gone")
	}
}

func TestRemoveCarriedBullet_SlugNotFound(t *testing.T) {
	_, err := RemoveCarriedBullet(singleBulletDoc, "Open Threads", "ghost-slug")
	if err == nil || !strings.Contains(err.Error(), "ghost-slug") {
		t.Fatalf("want not-found error, got %v", err)
	}
	// Error should list available slugs.
	if !strings.Contains(err.Error(), "mcp subtest") {
		t.Errorf("error should list available slugs: %v", err)
	}
}

func TestRemoveCarriedBullet_SlugNotFoundEmpty(t *testing.T) {
	_, err := RemoveCarriedBullet(emptyCarriedDoc, "Open Threads", "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

// ── GetCarriedBullet ──────────────────────────────────────────────────────────

func TestGetCarriedBullet_Found(t *testing.T) {
	b, err := GetCarriedBullet(singleBulletDoc, "Open Threads", "mcp subtest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Slug != "mcp subtest" {
		t.Errorf("slug: got %q, want %q", b.Slug, "mcp subtest")
	}
}

func TestGetCarriedBullet_NotFound(t *testing.T) {
	_, err := GetCarriedBullet(singleBulletDoc, "Open Threads", "ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

// ── BuildCarriedBullet ────────────────────────────────────────────────────────

func TestBuildCarriedBullet_NoBody(t *testing.T) {
	s := BuildCarriedBullet("my-slug", "my title", "")
	if s != "- **my-slug** — my title\n" {
		t.Errorf("got %q", s)
	}
}

func TestBuildCarriedBullet_SingleParaBody(t *testing.T) {
	s := BuildCarriedBullet("my-slug", "my title", "extra body text")
	// Single-para body: title and body joined on same line.
	if !strings.Contains(s, "- **my-slug**") {
		t.Error("missing canonical form")
	}
	if !strings.Contains(s, "my title") {
		t.Error("missing title")
	}
	if !strings.Contains(s, "extra body text") {
		t.Error("missing body")
	}
}

// ── liberal variants doc ──────────────────────────────────────────────────────

func TestParseLiberalVariants_AllFive(t *testing.T) {
	body := `- **canonical-slug**
- **colon-slug:**
- **paren-slug (with note)**
- **em-dash-slug** — body text here
- plain text bullet. Extra text here.
`
	bullets := ParseCarriedForward(body)
	if len(bullets) != 5 {
		t.Fatalf("want 5 bullets, got %d: %v", len(bullets), bullets)
	}
	forms := map[string]bool{}
	for _, b := range bullets {
		forms[b.RawForm] = true
	}
	for _, want := range []string{"canonical", "bold-colon", "bold-paren", "em-dash", "plain"} {
		if !forms[want] {
			t.Errorf("missing rawform %q in parsed bullets", want)
		}
	}
}
