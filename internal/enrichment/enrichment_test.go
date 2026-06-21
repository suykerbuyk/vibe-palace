// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package enrichment

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// mockCompleter is a scripted llm.Completer. Each call to Complete pops the
// next scripted response from responses; if responses is exhausted it
// returns errExhausted. A non-nil err short-circuits and is returned on
// every call.
type mockCompleter struct {
	responses []string
	err       error
	calls     int
	gotSystem []string
	gotUser   []string
}

func (m *mockCompleter) Name() string { return "mock" }

func (m *mockCompleter) Complete(_ context.Context, system, user string) (string, error) {
	m.calls++
	m.gotSystem = append(m.gotSystem, system)
	m.gotUser = append(m.gotUser, user)
	if m.err != nil {
		return "", m.err
	}
	if len(m.responses) == 0 {
		return "", fmt.Errorf("mock: no scripted response for call %d", m.calls)
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return resp, nil
}

func samplePromptInput() PromptInput {
	return PromptInput{
		UserText:      "implement enrichment",
		AssistantText: "done",
		FilesChanged:  []string{"client.go"},
		ToolCounts:    map[string]int{"Edit": 2},
		Duration:      5,
		UserMessages:  2,
		AsstMessages:  2,
	}
}

func TestGenerate_HappyPath(t *testing.T) {
	mock := &mockCompleter{responses: []string{
		`{"summary":"Built enrichment pipeline.","decisions":["Raw HTTP over SDK — fewer deps"],"open_threads":["Add retry logic"],"tag":"implementation"}`,
	}}

	result, err := Generate(context.Background(), mock, samplePromptInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result, got nil")
	}
	if result.Summary != "Built enrichment pipeline." {
		t.Errorf("summary: got %q", result.Summary)
	}
	if len(result.Decisions) != 1 || result.Decisions[0] != "Raw HTTP over SDK — fewer deps" {
		t.Errorf("decisions: got %v", result.Decisions)
	}
	if len(result.OpenThreads) != 1 || result.OpenThreads[0] != "Add retry logic" {
		t.Errorf("open_threads: got %v", result.OpenThreads)
	}
	if result.Tag != "implementation" {
		t.Errorf("tag: got %q", result.Tag)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call, got %d", mock.calls)
	}
	// Sanity: the default system prompt was used.
	if len(mock.gotSystem) == 0 || !strings.Contains(mock.gotSystem[0], "structured JSON summaries") {
		t.Errorf("expected default system prompt, got %q", mock.gotSystem)
	}
}

func TestGenerate_FencedJSON(t *testing.T) {
	mock := &mockCompleter{responses: []string{
		"```json\n{\"summary\":\"Fenced.\",\"tag\":\"docs\"}\n```",
	}}

	result, err := Generate(context.Background(), mock, samplePromptInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary != "Fenced." {
		t.Errorf("summary: got %q", result.Summary)
	}
	if result.Tag != "docs" {
		t.Errorf("tag: got %q", result.Tag)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call (no reprompt), got %d", mock.calls)
	}
}

func TestGenerate_BareFencedJSON(t *testing.T) {
	mock := &mockCompleter{responses: []string{
		"```\n{\"summary\":\"Bare fence.\",\"tag\":\"review\"}\n```",
	}}

	result, err := Generate(context.Background(), mock, samplePromptInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary != "Bare fence." || result.Tag != "review" {
		t.Errorf("got %+v", result)
	}
}

func TestGenerate_NilCompleter(t *testing.T) {
	result, err := Generate(context.Background(), nil, PromptInput{})
	if result != nil || err != nil {
		t.Errorf("nil completer: got result=%v, err=%v", result, err)
	}
}

func TestGenerate_CompleterError(t *testing.T) {
	mock := &mockCompleter{err: fmt.Errorf("connection refused")}
	_, err := Generate(context.Background(), mock, samplePromptInput())
	if err == nil {
		t.Fatal("expected error from completer")
	}
}

func TestGenerate_RepromptRecovers(t *testing.T) {
	mock := &mockCompleter{responses: []string{
		"Sure! Here is the summary you asked for.",
		`{"summary":"Recovered on retry.","tag":"debugging"}`,
	}}

	result, err := Generate(context.Background(), mock, samplePromptInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary != "Recovered on retry." || result.Tag != "debugging" {
		t.Errorf("got %+v", result)
	}
	if mock.calls != 2 {
		t.Fatalf("expected 2 calls (reprompt), got %d", mock.calls)
	}
	// The reprompt should carry the corrective reminder.
	if !strings.Contains(mock.gotSystem[1], "valid JSON only") {
		t.Errorf("reprompt missing corrective instruction: %q", mock.gotSystem[1])
	}
}

func TestGenerate_RepromptStillFails(t *testing.T) {
	mock := &mockCompleter{responses: []string{
		"not json",
		"still not json",
	}}

	_, err := Generate(context.Background(), mock, samplePromptInput())
	if err == nil {
		t.Fatal("expected error after failed reprompt")
	}
	if mock.calls != 2 {
		t.Errorf("expected 2 calls, got %d", mock.calls)
	}
}

func TestGenerate_RepromptCompleterError(t *testing.T) {
	// First call returns unparseable content; the reprompt itself errors.
	mock := &mockCompleter{responses: []string{"not json"}}
	_, err := Generate(context.Background(), mock, samplePromptInput())
	if err == nil {
		t.Fatal("expected error when reprompt completer fails")
	}
}

func TestValidateTag(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"implementation", "implementation"},
		{"debugging", "debugging"},
		{"refactor", "refactor"},
		{"exploration", "exploration"},
		{"review", "review"},
		{"docs", "docs"},
		{"planning", "planning"},
		{"DOCS", "docs"},
		{"  planning  ", "planning"},
		{"research", ""}, // vv tag, not allowed here
		{"invalid", ""},
		{"", ""},
	}

	for _, tt := range tests {
		if got := validateTag(tt.input); got != tt.want {
			t.Errorf("validateTag(%q): got %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseResponse_InvalidTagEmptied(t *testing.T) {
	result, err := parseResponse(`{"summary":"x","tag":"bogus"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Tag != "" {
		t.Errorf("expected empty tag for invalid input, got %q", result.Tag)
	}
}

func TestBuildUserPrompt_AllSections(t *testing.T) {
	activities := make([]string, 25)
	for i := range activities {
		activities[i] = fmt.Sprintf("activity-%d", i)
	}
	in := PromptInput{
		UserText:         "did things",
		AssistantText:    "ok",
		FilesChanged:     []string{"a.go", "b.go"},
		ToolCounts:       map[string]int{"Read": 3, "Edit": 1},
		Duration:         12,
		UserMessages:     4,
		AsstMessages:     3,
		NarrativeSummary: "heuristic summary",
		NarrativeTag:     "implementation",
		Activities:       activities,
	}

	out := buildUserPrompt(in)

	for _, want := range []string{
		"Duration: 12 minutes",
		"## Tool Usage",
		"## Files Changed",
		"## Heuristic Analysis",
		"Summary: heuristic summary",
		"Tag: implementation",
		"activity-0",
		"... and 5 more",
		"## User Messages",
		"## Assistant Messages",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("buildUserPrompt missing %q", want)
		}
	}

	// Tool counts must be sorted (Edit before Read).
	if strings.Index(out, "Edit: 1") > strings.Index(out, "Read: 3") {
		t.Error("tool counts not sorted alphabetically")
	}
	// Activities are capped at 20 lines.
	if strings.Contains(out, "activity-20") {
		t.Error("activities not capped at 20")
	}
}

func TestEnricher_EnrichHappyPath(t *testing.T) {
	mock := &mockCompleter{responses: []string{
		`{"summary":"Enriched.","tag":"planning"}`,
	}}
	e := NewEnricher(mock, "test-model", 2*time.Second, "")

	if e.Model() != "test-model" {
		t.Errorf("Model: got %q", e.Model())
	}

	result, err := e.Enrich(context.Background(), samplePromptInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.Summary != "Enriched." || result.Tag != "planning" {
		t.Errorf("got %+v", result)
	}
}

func TestEnricher_CustomSystemPrompt(t *testing.T) {
	mock := &mockCompleter{responses: []string{`{"summary":"ok","tag":"docs"}`}}
	e := NewEnricher(mock, "m", time.Second, "CUSTOM SYSTEM PROMPT")

	if _, err := e.Enrich(context.Background(), samplePromptInput()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.gotSystem) == 0 || mock.gotSystem[0] != "CUSTOM SYSTEM PROMPT" {
		t.Errorf("custom system prompt not threaded: %q", mock.gotSystem)
	}
}

func TestEnricher_EmptySystemPromptFallsBack(t *testing.T) {
	e := NewEnricher(nil, "m", 0, "")
	if !strings.Contains(e.systemPrompt, "structured JSON summaries") {
		t.Errorf("empty system prompt did not fall back to default")
	}
}

func TestEnricher_NilReceiver(t *testing.T) {
	var e *Enricher
	result, err := e.Enrich(context.Background(), samplePromptInput())
	if result != nil || err != nil {
		t.Errorf("nil enricher: got result=%v, err=%v", result, err)
	}
}

func TestEnricher_NilCompleter(t *testing.T) {
	e := NewEnricher(nil, "m", time.Second, "")
	result, err := e.Enrich(context.Background(), samplePromptInput())
	if result != nil || err != nil {
		t.Errorf("nil completer: got result=%v, err=%v", result, err)
	}
}

func TestEnricher_ZeroTimeoutUsesDefault(t *testing.T) {
	mock := &mockCompleter{responses: []string{`{"summary":"ok","tag":"review"}`}}
	e := NewEnricher(mock, "m", 0, "")
	if _, err := e.Enrich(context.Background(), samplePromptInput()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
