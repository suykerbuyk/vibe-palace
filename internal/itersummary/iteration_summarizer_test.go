// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package itersummary

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/summarize"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// mockCompleter is a tiny in-test llm.Completer, mirroring
// internal/capture/session_test.go's mockCompleter pattern. It also records
// the user prompt it was last called with, so tests can assert which
// iterations.md entry actually drove the LLM call.
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

// writeIterationsFile writes content as project's iterations.md inside v,
// creating the Projects/{project}/ directory as needed.
func writeIterationsFile(t *testing.T, v *storage.Vault, project, content string) {
	t.Helper()
	path, err := v.IterationsFile(project)
	if err != nil {
		t.Fatalf("IterationsFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

const cannedSummaryJSON = `{"summary":"Implemented the widget and wired it into the pipeline.","decisions":["Use a struct — simpler than a map"],"unblocks":"Downstream consumers can now call Widget()."}`

// TestSummarize_HappyPath proves the full read-generate-write path: a real
// storage.Vault backed by a temp dir, a real iterations.md fixture, a stub
// completer returning canned JSON. Summarize must return KindIteration and
// the cache written to the vault must be readable back with the expected
// content.
func TestSummarize_HappyPath(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	const project = "proj"
	writeIterationsFile(t, v, project, "## Iteration 5 — Add widget\n\nDid the widget work.\n")

	mc := &mockCompleter{resp: cannedSummaryJSON}
	client := NewSummarizer(mc, "test-model", 0, "")
	is := &IterationSummarizer{client: client, vault: v}

	res, err := is.Summarize(context.Background(), summarize.SummaryItem{
		Kind:      summarize.KindIteration,
		Project:   project,
		Iteration: 5,
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if res == nil || res.Kind != summarize.KindIteration {
		t.Fatalf("Summarize result = %+v, want Kind=KindIteration", res)
	}

	got, ok, err := v.ReadIterationSummary(project, 5)
	if err != nil {
		t.Fatalf("ReadIterationSummary: %v", err)
	}
	if !ok {
		t.Fatal("ReadIterationSummary: ok = false, want true")
	}
	if got.Summary != "Implemented the widget and wired it into the pipeline." {
		t.Errorf("Summary = %q", got.Summary)
	}
	if len(got.Decisions) != 1 || got.Decisions[0] != "Use a struct — simpler than a map" {
		t.Errorf("Decisions = %v", got.Decisions)
	}
	if got.Unblocks != "Downstream consumers can now call Widget()." {
		t.Errorf("Unblocks = %q", got.Unblocks)
	}
	if got.Model != "test-model" {
		t.Errorf("Model = %q, want %q", got.Model, "test-model")
	}
	if got.MatchIndex != 0 {
		t.Errorf("MatchIndex = %d, want 0", got.MatchIndex)
	}
}

// TestSummarize_DroppedContentProof seeds the fixture entry's Body with a
// distinctive code snippet and file:line citation, and has the stub
// completer's canned response deliberately NOT include that text. It then
// asserts the CACHED summary does not contain the distinctive string — a
// concrete proxy for "the written cache stores the LLM's structured result,
// not the raw entry body verbatim."
func TestSummarize_DroppedContentProof(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	const project = "proj"
	const distinctive = "ZZZ_UNIQUE_SNIPPET_MARKER_998877"
	body := "Refactored the parser.\n\n```go\nfunc " + distinctive + "() {}\n```\n\nSee internal/foo/bar.go:123 for details.\n"
	writeIterationsFile(t, v, project, "## Iteration 9 — Refactor parser\n\n"+body)

	mc := &mockCompleter{resp: cannedSummaryJSON} // deliberately does not mention `distinctive`
	client := NewSummarizer(mc, "test-model", 0, "")
	is := &IterationSummarizer{client: client, vault: v}

	if _, err := is.Summarize(context.Background(), summarize.SummaryItem{
		Kind:      summarize.KindIteration,
		Project:   project,
		Iteration: 9,
	}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	got, ok, err := v.ReadIterationSummary(project, 9)
	if err != nil || !ok {
		t.Fatalf("ReadIterationSummary: ok=%v err=%v", ok, err)
	}

	rendered := got.Summary + strings.Join(got.Decisions, " ") + got.Unblocks
	if strings.Contains(rendered, distinctive) {
		t.Errorf("cached summary contains verbatim snippet marker %q, rendered=%q", distinctive, rendered)
	}
	if strings.Contains(rendered, "bar.go:123") {
		t.Errorf("cached summary contains verbatim file:line citation, rendered=%q", rendered)
	}
}

// TestSummarize_MultiEntryPerN proves that when TWO entries share the same
// N, Summarize resolves the LAST file-order match (per
// wrapstate.LastEntryByN's established convention): the prompt sent to the
// LLM must be built from the last entry's Header/Body, and the cached
// MatchIndex must equal len(wrapstate.EntriesByN(content, N)) - 1.
func TestSummarize_MultiEntryPerN(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	const project = "proj"
	const firstMarker = "FIRST_ENTRY_MARKER_AAA"
	const lastMarker = "LAST_ENTRY_MARKER_ZZZ"
	content := "## Iteration 12 — First pass\n\n" + firstMarker + " body text.\n\n---\n\n" +
		"## Iteration 12 — Second pass\n\n" + lastMarker + " body text.\n"
	writeIterationsFile(t, v, project, content)

	mc := &mockCompleter{resp: cannedSummaryJSON}
	client := NewSummarizer(mc, "test-model", 0, "")
	is := &IterationSummarizer{client: client, vault: v}

	if _, err := is.Summarize(context.Background(), summarize.SummaryItem{
		Kind:      summarize.KindIteration,
		Project:   project,
		Iteration: 12,
	}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	if !strings.Contains(mc.lastUser, lastMarker) {
		t.Errorf("prompt sent to LLM does not contain last entry's marker %q; got %q", lastMarker, mc.lastUser)
	}
	if strings.Contains(mc.lastUser, firstMarker) {
		t.Errorf("prompt sent to LLM unexpectedly contains first entry's marker %q; got %q", firstMarker, mc.lastUser)
	}

	got, ok, err := v.ReadIterationSummary(project, 12)
	if err != nil || !ok {
		t.Fatalf("ReadIterationSummary: ok=%v err=%v", ok, err)
	}

	wantIdx := len(wrapstate.EntriesByN(content, 12)) - 1
	if wantIdx != 1 {
		t.Fatalf("test fixture invariant broken: expected 2 entries sharing N=12, got matchIndex-should-be %d", wantIdx)
	}
	if got.MatchIndex != wantIdx {
		t.Errorf("MatchIndex = %d, want %d (last of %d matches)", got.MatchIndex, wantIdx, wantIdx+1)
	}
}

// TestSummarize_MissingEntry proves Summarize returns a non-nil error for an
// Iteration number with no matching entry, and does NOT write a cache file.
func TestSummarize_MissingEntry(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	const project = "proj"
	writeIterationsFile(t, v, project, "## Iteration 5 — Add widget\n\nDid the widget work.\n")

	mc := &mockCompleter{resp: cannedSummaryJSON}
	client := NewSummarizer(mc, "test-model", 0, "")
	is := &IterationSummarizer{client: client, vault: v}

	_, err := is.Summarize(context.Background(), summarize.SummaryItem{
		Kind:      summarize.KindIteration,
		Project:   project,
		Iteration: 999,
	})
	if err == nil {
		t.Fatal("expected error for missing iteration entry, got nil")
	}

	_, ok, rerr := v.ReadIterationSummary(project, 999)
	if rerr != nil {
		t.Fatalf("ReadIterationSummary: %v", rerr)
	}
	if ok {
		t.Error("expected no cache file to be written for a missing entry, but one was found")
	}
}

// TestSummarize_NilClient proves that if an IterationSummarizer is
// constructed with a nil LLM client (a programming-contract violation, per
// the doc comment on Summarize), Summarize returns a clear error rather than
// silently no-op'ing.
func TestSummarize_NilClient(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	const project = "proj"
	writeIterationsFile(t, v, project, "## Iteration 5 — Add widget\n\nDid the widget work.\n")

	is := &IterationSummarizer{client: nil, vault: v}

	_, err := is.Summarize(context.Background(), summarize.SummaryItem{
		Kind:      summarize.KindIteration,
		Project:   project,
		Iteration: 5,
	})
	if err == nil {
		t.Fatal("expected error when client is nil, got nil")
	}
}

func TestNewIterationSummarizerFromConfig_Disabled(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	is, err := NewIterationSummarizerFromConfig(storage.SummarizationConfig{Enabled: false}, v)
	if err != nil {
		t.Fatalf("disabled config: unexpected error: %v", err)
	}
	if is != nil {
		t.Errorf("disabled config: expected nil summarizer, got %v", is)
	}
}

func TestNewIterationSummarizerFromConfig_MissingAPIKeyEnvName(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	is, err := NewIterationSummarizerFromConfig(storage.SummarizationConfig{
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

func TestNewIterationSummarizerFromConfig_APIKeyEnvUnset(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	t.Setenv("VP_TEST_ITERSUMMARY_KEY", "")
	is, err := NewIterationSummarizerFromConfig(storage.SummarizationConfig{
		Enabled:   true,
		Provider:  "anthropic",
		Model:     "claude-test",
		APIKeyEnv: "VP_TEST_ITERSUMMARY_KEY",
	}, v)
	if err == nil {
		t.Fatal("expected error when api key env is empty")
	}
	if is != nil {
		t.Errorf("expected nil summarizer on error, got %v", is)
	}
}

func TestNewIterationSummarizerFromConfig_Resolvable(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	t.Setenv("VP_TEST_ITERSUMMARY_KEY", "sk-test-123")
	is, err := NewIterationSummarizerFromConfig(storage.SummarizationConfig{
		Enabled:        true,
		Provider:       "anthropic",
		Model:          "claude-test",
		APIKeyEnv:      "VP_TEST_ITERSUMMARY_KEY",
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
