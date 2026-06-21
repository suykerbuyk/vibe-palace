// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// writeLearning drops a learning fixture (frontmatter + body) into the vault's
// learnings dir, mirroring the on-disk shape storage.GetLearning expects.
func writeLearning(t *testing.T, vault *storage.Vault, slug, name, desc, ltype, body string) {
	t.Helper()
	dir := vault.LearningsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\ntype: " + ltype + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, slug+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// getLearningCall drives the vp_get_learning handler with raw JSON params and
// returns the typed result.
func getLearningCall(t *testing.T, vault *storage.Vault, params string) getLearningResult {
	t.Helper()
	tool := GetLearningTool(vault)
	res, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	gr, ok := res.(getLearningResult)
	if !ok {
		t.Fatalf("result type = %T, want getLearningResult", res)
	}
	return gr
}

func TestListLearningsName(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if got := ListLearningsTool(vault).Name; got != "vp_list_learnings" {
		t.Errorf("name = %q, want vp_list_learnings", got)
	}
}

func TestGetLearningName(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if got := GetLearningTool(vault).Name; got != "vp_get_learning" {
		t.Errorf("name = %q, want vp_get_learning", got)
	}
}

// TestListLearningsReturnsMetadata pins that the tool returns the metadata index
// sorted by slug, with no bodies.
func TestListLearningsReturnsMetadata(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	writeLearning(t, vault, "beta", "Beta", "second", "feedback", "body b")
	writeLearning(t, vault, "alpha", "Alpha", "first", "user", "body a")

	tool := ListLearningsTool(vault)
	res, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	lr, ok := res.(listLearningsResult)
	if !ok {
		t.Fatalf("result type = %T, want listLearningsResult", res)
	}
	if len(lr.Learnings) != 2 {
		t.Fatalf("learnings = %d, want 2", len(lr.Learnings))
	}
	if lr.Learnings[0].Slug != "alpha" || lr.Learnings[1].Slug != "beta" {
		t.Errorf("slugs = [%q %q], want [alpha beta]", lr.Learnings[0].Slug, lr.Learnings[1].Slug)
	}
	if lr.Learnings[0].Type != "user" || lr.Learnings[0].Name != "Alpha" {
		t.Errorf("alpha meta = %+v", lr.Learnings[0])
	}
}

// TestListLearningsEmptyReturnsEmptySlice pins the missing-dir / empty case: a
// non-nil empty slice (so it marshals to [] not null).
func TestListLearningsEmptyReturnsEmptySlice(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ListLearningsTool(vault)
	res, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	lr := res.(listLearningsResult)
	if lr.Learnings == nil {
		t.Fatal("Learnings is nil, want non-nil empty slice")
	}
	if len(lr.Learnings) != 0 {
		t.Errorf("learnings = %d, want 0", len(lr.Learnings))
	}
}

// TestListLearningsFilterType pins that filter_type narrows to the matching
// type, and an unknown type yields an empty set.
func TestListLearningsFilterType(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	writeLearning(t, vault, "u1", "U1", "d", "user", "x")
	writeLearning(t, vault, "f1", "F1", "d", "feedback", "x")
	writeLearning(t, vault, "r1", "R1", "d", "reference", "x")

	tool := ListLearningsTool(vault)

	res, err := tool.Handler(context.Background(), json.RawMessage(`{"filter_type":"feedback"}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	lr := res.(listLearningsResult)
	if len(lr.Learnings) != 1 || lr.Learnings[0].Slug != "f1" {
		t.Errorf("filtered = %+v, want only f1", lr.Learnings)
	}

	res, err = tool.Handler(context.Background(), json.RawMessage(`{"filter_type":"nope"}`))
	if err != nil {
		t.Fatalf("handler (unknown filter): %v", err)
	}
	lr = res.(listLearningsResult)
	if len(lr.Learnings) != 0 {
		t.Errorf("unknown filter returned %d, want 0", len(lr.Learnings))
	}
}

// TestGetLearningDefaultIncludesBody pins that the body is returned inline by
// default, with metadata, ContentURI, and ContentSize populated.
func TestGetLearningDefaultIncludesBody(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	writeLearning(t, vault, "alpha", "Alpha", "first learning", "user", "the body content")

	gr := getLearningCall(t, vault, `{"slug":"alpha"}`)
	if gr.Content != "the body content" {
		t.Errorf("Content = %q", gr.Content)
	}
	if gr.Excerpt != "" {
		t.Errorf("Excerpt = %q, want empty when content included", gr.Excerpt)
	}
	if gr.Slug != "alpha" || gr.Name != "Alpha" || gr.Type != "user" || gr.Description != "first learning" {
		t.Errorf("meta = %+v", gr)
	}
	if gr.ContentURI != mcp.LearningURI("alpha") {
		t.Errorf("ContentURI = %q, want %q", gr.ContentURI, mcp.LearningURI("alpha"))
	}
	if gr.ContentSize != len("the body content") {
		t.Errorf("ContentSize = %d, want %d", gr.ContentSize, len("the body content"))
	}
}

// makeBigLearning writes a learning whose body comfortably exceeds
// learningExcerptCap so the excerpt path is exercised. Returns the full body.
func makeBigLearning(t *testing.T, vault *storage.Vault, slug string) string {
	t.Helper()
	body := strings.Repeat("This is a line of learning body content for the excerpt test.\n", 60)
	writeLearning(t, vault, slug, "Big", "big learning", "reference", body)
	got, err := vault.GetLearning(slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Content) <= learningExcerptCap {
		t.Fatalf("setup: body len %d <= learningExcerptCap %d, excerpt path unreachable",
			len(got.Content), learningExcerptCap)
	}
	return got.Content
}

// TestGetLearningExcludeContentReturnsExcerpt pins the slim path: a large body
// with include_content=false drops Content for a bounded, rune-safe Excerpt plus
// the always-set ContentURI and ContentSize.
func TestGetLearningExcludeContentReturnsExcerpt(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	body := makeBigLearning(t, vault, "big")

	gr := getLearningCall(t, vault, `{"slug":"big","include_content":false}`)

	if gr.Content != "" {
		t.Errorf("Content non-empty (len %d), want empty when include_content=false", len(gr.Content))
	}
	if gr.Excerpt == "" {
		t.Fatal("Excerpt should be non-empty when content is dropped")
	}
	if !utf8.ValidString(gr.Excerpt) {
		t.Error("Excerpt is not valid UTF-8")
	}
	if len(gr.Excerpt) > learningExcerptCap {
		t.Errorf("Excerpt len %d exceeds learningExcerptCap %d", len(gr.Excerpt), learningExcerptCap)
	}
	if !strings.HasPrefix(body, gr.Excerpt) {
		t.Error("Excerpt is not a prefix of the body")
	}
	if gr.ContentURI != mcp.LearningURI("big") {
		t.Errorf("ContentURI = %q, want %q", gr.ContentURI, mcp.LearningURI("big"))
	}
	if gr.ContentSize != len(body) {
		t.Errorf("ContentSize = %d, want %d (full body len)", gr.ContentSize, len(body))
	}
}

// TestGetLearningExcludeContentSmallBodyStaysInline pins that a small body that
// cannot truncate is still returned inline even with include_content=false.
func TestGetLearningExcludeContentSmallBodyStaysInline(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	writeLearning(t, vault, "small", "Small", "tiny", "user", "a tiny body")

	gr := getLearningCall(t, vault, `{"slug":"small","include_content":false}`)
	if gr.Content != "a tiny body" {
		t.Errorf("small body not returned inline: Content = %q", gr.Content)
	}
	if gr.Excerpt != "" {
		t.Errorf("Excerpt = %q, want empty for small inline body", gr.Excerpt)
	}
}

// TestGetLearningUnknownSlugErrors pins that an unknown slug surfaces an error.
func TestGetLearningUnknownSlugErrors(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	writeLearning(t, vault, "alpha", "Alpha", "d", "user", "x")

	tool := GetLearningTool(vault)
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"slug":"missing"}`)); err == nil {
		t.Error("expected error for unknown slug")
	}
}

// TestGetLearningEmptySlugErrors pins required-param validation.
func TestGetLearningEmptySlugErrors(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := GetLearningTool(vault)
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for empty slug")
	}
}
