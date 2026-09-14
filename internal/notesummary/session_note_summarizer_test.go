// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package notesummary

import (
	"context"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/summarize"
)

// mockCompleter is a tiny in-test llm.Completer, mirroring
// internal/itersummary/iteration_summarizer_test.go's own mockCompleter. It
// records the user prompt it was last called with, so tests can assert which
// note content actually drove the LLM call.
type mockCompleter struct {
	resp string
	err  error

	lastSystem string
	lastUser   string
}

func (m *mockCompleter) Complete(ctx context.Context, system, user string) (string, error) {
	m.lastSystem = system
	m.lastUser = user
	if m.err != nil {
		return "", m.err
	}
	return m.resp, nil
}

func (m *mockCompleter) Name() string { return "mock" }

const cannedSummaryJSON = `{"search_summary":"Implemented the widget and wired it into the pipeline."}`

// longBody pads a narrative string past LengthGateBytes so tests exercising
// the LLM path don't trip the belt-and-suspenders length gate.
func longBody(narrative string) string {
	pad := strings.Repeat("x", LengthGateBytes+1-len(narrative))
	if pad == "" {
		return narrative
	}
	// Trailing newline so the written body matches exactly on read-back:
	// marshalSessionFile appends one only when the body lacks it.
	return narrative + "\n\n<!-- padding -->\n" + pad + "\n"
}

// writeSession writes a session note via the vault's real writer and returns
// its identifying coordinates, mirroring how internal/capture actually
// populates a summarize.SummaryItem.
func writeSession(t *testing.T, v *storage.Vault, project string, meta storage.SessionMeta, body string) summarize.SummaryItem {
	t.Helper()
	ref, err := v.WriteSessionRef(project, meta, body)
	if err != nil {
		t.Fatalf("WriteSessionRef: %v", err)
	}
	return summarize.SummaryItem{
		Kind:        summarize.KindSessionNote,
		Project:     project,
		Date:        ref.Date,
		Fingerprint: ref.Fingerprint,
		Iteration:   ref.Iteration,
		NotePath:    ref.NotePath,
	}
}

// TestSummarize_HappyPath proves the full read-generate-write path: a real
// storage.Vault backed by a temp dir, a real session note, a stub completer
// returning canned JSON. Summarize must return KindSessionNote and the note
// rewritten in the vault must carry the new SearchSummary fields, with its
// body left untouched.
func TestSummarize_HappyPath(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	const project = "proj"
	body := longBody("## Transcript\n\nDid the widget work today.")

	item := writeSession(t, v, project, storage.SessionMeta{
		Date:         "2026-07-10",
		Title:        "Widget session",
		Summary:      "worked on widget",
		Decisions:    []string{"Use a struct"},
		FilesChanged: []string{"widget.go"},
		OpenThreads:  []string{"add tests"},
	}, body)

	mc := &mockCompleter{resp: cannedSummaryJSON}
	client := NewSummarizer(mc, "test-model", 0, "")
	is := &SessionNoteSummarizer{client: client, vault: v}

	res, err := is.Summarize(context.Background(), item)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if res == nil || res.Kind != summarize.KindSessionNote {
		t.Fatalf("Summarize result = %+v, want Kind=KindSessionNote", res)
	}

	gotMeta, gotBody, err := v.ReadSession(project, item.Date, item.Fingerprint, item.Iteration)
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if gotMeta.SearchSummary != "Implemented the widget and wired it into the pipeline." {
		t.Errorf("SearchSummary = %q", gotMeta.SearchSummary)
	}
	if gotMeta.SearchSummaryModel != "test-model" {
		t.Errorf("SearchSummaryModel = %q, want %q", gotMeta.SearchSummaryModel, "test-model")
	}
	if gotMeta.SearchSummaryAt == "" {
		t.Error("SearchSummaryAt is empty, want an RFC3339 timestamp")
	}
	if gotBody != body {
		t.Errorf("body was modified by Summarize:\ngot:  %q\nwant: %q", gotBody, body)
	}
	// Human-facing Summary must be untouched.
	if gotMeta.Summary != "worked on widget" {
		t.Errorf("Summary = %q, want unchanged %q", gotMeta.Summary, "worked on widget")
	}

	// The LLM prompt must have carried the structured fields.
	if !strings.Contains(mc.lastUser, "Use a struct") {
		t.Errorf("prompt missing decisions: %q", mc.lastUser)
	}
	if !strings.Contains(mc.lastUser, "widget.go") {
		t.Errorf("prompt missing files changed: %q", mc.lastUser)
	}
	if !strings.Contains(mc.lastUser, "add tests") {
		t.Errorf("prompt missing open threads: %q", mc.lastUser)
	}
}

// TestSummarize_ShortBodyNoOp proves the belt-and-suspenders length gate: a
// note whose body is at or under LengthGateBytes must produce a benign
// success with no LLM call and no rewrite.
func TestSummarize_ShortBodyNoOp(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	const project = "proj"
	body := "short body"
	if len(body) > LengthGateBytes {
		t.Fatalf("test fixture invariant broken: body length %d exceeds gate %d", len(body), LengthGateBytes)
	}

	item := writeSession(t, v, project, storage.SessionMeta{Date: "2026-07-11", Title: "short"}, body)

	mc := &mockCompleter{resp: cannedSummaryJSON}
	client := NewSummarizer(mc, "test-model", 0, "")
	is := &SessionNoteSummarizer{client: client, vault: v}

	res, err := is.Summarize(context.Background(), item)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if res == nil || res.Kind != summarize.KindSessionNote {
		t.Fatalf("Summarize result = %+v, want Kind=KindSessionNote", res)
	}
	if mc.lastUser != "" {
		t.Errorf("expected no LLM call for a short body, but one was made: %q", mc.lastUser)
	}

	gotMeta, _, err := v.ReadSession(project, item.Date, item.Fingerprint, item.Iteration)
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if gotMeta.SearchSummary != "" {
		t.Errorf("SearchSummary = %q, want empty (no-op)", gotMeta.SearchSummary)
	}
}

// TestSummarize_MissingNote proves Summarize returns a non-nil error when the
// session note does not exist, and this is a real, retry-worthy failure.
func TestSummarize_MissingNote(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	mc := &mockCompleter{resp: cannedSummaryJSON}
	client := NewSummarizer(mc, "test-model", 0, "")
	is := &SessionNoteSummarizer{client: client, vault: v}

	_, err := is.Summarize(context.Background(), summarize.SummaryItem{
		Kind:      summarize.KindSessionNote,
		Project:   "proj",
		Date:      "2026-07-12",
		Iteration: 1,
	})
	if err == nil {
		t.Fatal("expected error for missing session note, got nil")
	}
}

// TestSummarize_NilClient proves that if a SessionNoteSummarizer is
// constructed with a nil LLM client (a programming-contract violation, per
// the doc comment on Summarize), Summarize returns a clear error rather than
// silently no-op'ing — but ONLY once the note is past the length gate (a
// short note never reaches the client-nil check at all).
func TestSummarize_NilClient(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	const project = "proj"
	body := longBody("## Transcript\n\nDid stuff.")
	item := writeSession(t, v, project, storage.SessionMeta{Date: "2026-07-13", Title: "t"}, body)

	is := &SessionNoteSummarizer{client: nil, vault: v}

	_, err := is.Summarize(context.Background(), item)
	if err == nil {
		t.Fatal("expected error when client is nil, got nil")
	}
}

// TestSummarize_DroppedContentProof seeds the note's Body with a distinctive
// code snippet and file:line citation, and has the stub completer's canned
// response deliberately NOT include that text. It then asserts the WRITTEN
// SearchSummary does not contain the distinctive string — a concrete proxy
// for "the written summary stores the LLM's own text, not the raw body
// verbatim."
func TestSummarize_DroppedContentProof(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	const project = "proj"
	const distinctive = "ZZZ_UNIQUE_SNIPPET_MARKER_998877"
	body := longBody("Refactored the parser.\n\n```go\nfunc " + distinctive + "() {}\n```\n\nSee internal/foo/bar.go:123 for details.\n")

	item := writeSession(t, v, project, storage.SessionMeta{Date: "2026-07-14", Title: "t"}, body)

	mc := &mockCompleter{resp: cannedSummaryJSON} // deliberately does not mention `distinctive`
	client := NewSummarizer(mc, "test-model", 0, "")
	is := &SessionNoteSummarizer{client: client, vault: v}

	if _, err := is.Summarize(context.Background(), item); err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	gotMeta, _, err := v.ReadSession(project, item.Date, item.Fingerprint, item.Iteration)
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if strings.Contains(gotMeta.SearchSummary, distinctive) {
		t.Errorf("written SearchSummary contains verbatim snippet marker %q: %q", distinctive, gotMeta.SearchSummary)
	}
	if strings.Contains(gotMeta.SearchSummary, "bar.go:123") {
		t.Errorf("written SearchSummary contains verbatim file:line citation: %q", gotMeta.SearchSummary)
	}
}

func TestNewSessionNoteSummarizerFromConfig_Disabled(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	is, err := NewSessionNoteSummarizerFromConfig(storage.SummarizationConfig{Enabled: false}, v)
	if err != nil {
		t.Fatalf("disabled config: unexpected error: %v", err)
	}
	if is != nil {
		t.Errorf("disabled config: expected nil summarizer, got %v", is)
	}
}

func TestNewSessionNoteSummarizerFromConfig_MissingAPIKeyEnvName(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	is, err := NewSessionNoteSummarizerFromConfig(storage.SummarizationConfig{
		Enabled:  true,
		Provider: "anthropic",
		Model:    "claude-test",
	}, v)
	if err == nil {
		t.Fatal("expected error when api key env name is empty")
	}
	if is != nil {
		t.Errorf("expected nil summarizer on error, got %v", is)
	}
}

func TestNewSessionNoteSummarizerFromConfig_APIKeyEnvUnset(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	t.Setenv("VP_TEST_NOTESUMMARY_KEY", "")
	is, err := NewSessionNoteSummarizerFromConfig(storage.SummarizationConfig{
		Enabled:   true,
		Provider:  "anthropic",
		Model:     "claude-test",
		APIKeyEnv: "VP_TEST_NOTESUMMARY_KEY",
	}, v)
	if err == nil {
		t.Fatal("expected error when api key env is empty")
	}
	if is != nil {
		t.Errorf("expected nil summarizer on error, got %v", is)
	}
}

func TestNewSessionNoteSummarizerFromConfig_Resolvable(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	t.Setenv("VP_TEST_NOTESUMMARY_KEY", "sk-test-123")
	is, err := NewSessionNoteSummarizerFromConfig(storage.SummarizationConfig{
		Enabled:        true,
		Provider:       "anthropic",
		Model:          "claude-test",
		APIKeyEnv:      "VP_TEST_NOTESUMMARY_KEY",
		TimeoutSeconds: 5,
	}, v)
	if err != nil {
		t.Fatalf("resolvable config: unexpected error: %v", err)
	}
	if is == nil {
		t.Fatal("resolvable config: expected non-nil summarizer")
	}
	if is.client == nil {
		t.Error("resolvable config: expected non-nil client")
	}
	if is.vault != v {
		t.Error("resolvable config: expected vault to be threaded through")
	}
}
