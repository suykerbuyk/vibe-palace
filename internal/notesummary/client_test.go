// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package notesummary

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// scriptedCompleter is a scripted llm.Completer mirroring
// internal/itersummary/client_test.go's own scriptedCompleter exactly: each
// call to Complete pops the next scripted response; a non-nil err
// short-circuits and is returned on every call.
type scriptedCompleter struct {
	responses []string
	err       error
	calls     int
	gotSystem []string
}

func (m *scriptedCompleter) Name() string { return "mock" }

func (m *scriptedCompleter) Complete(_ context.Context, system, _ string) (string, error) {
	m.calls++
	m.gotSystem = append(m.gotSystem, system)
	if m.err != nil {
		return "", m.err
	}
	if len(m.responses) == 0 {
		return "", fmt.Errorf("scriptedCompleter: no scripted response for call %d", m.calls)
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	return resp, nil
}

func TestGenerate_HappyPath(t *testing.T) {
	mock := &scriptedCompleter{responses: []string{
		`{"search_summary":"Fixed the parser and added a regression test."}`,
	}}

	result, err := generate(context.Background(), mock, defaultSystemPrompt, PromptInput{Summary: "old summary", Body: "did stuff"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary != "Fixed the parser and added a regression test." {
		t.Errorf("summary: got %q", result.Summary)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call, got %d", mock.calls)
	}
}

func TestGenerate_FencedJSON(t *testing.T) {
	mock := &scriptedCompleter{responses: []string{
		"```json\n{\"search_summary\":\"Fenced.\"}\n```",
	}}
	result, err := generate(context.Background(), mock, defaultSystemPrompt, PromptInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary != "Fenced." {
		t.Errorf("summary: got %q", result.Summary)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call (no reprompt), got %d", mock.calls)
	}
}

func TestGenerate_BareFencedJSON(t *testing.T) {
	mock := &scriptedCompleter{responses: []string{
		"```\n{\"search_summary\":\"Bare fence.\"}\n```",
	}}
	result, err := generate(context.Background(), mock, defaultSystemPrompt, PromptInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary != "Bare fence." {
		t.Errorf("got %+v", result)
	}
}

func TestGenerate_NilCompleter(t *testing.T) {
	result, err := generate(context.Background(), nil, defaultSystemPrompt, PromptInput{})
	if result != nil || err != nil {
		t.Errorf("nil completer: got result=%v, err=%v", result, err)
	}
}

func TestGenerate_CompleterError(t *testing.T) {
	mock := &scriptedCompleter{err: fmt.Errorf("connection refused")}
	_, err := generate(context.Background(), mock, defaultSystemPrompt, PromptInput{})
	if err == nil {
		t.Fatal("expected error from completer")
	}
}

func TestGenerate_RepromptRecovers(t *testing.T) {
	mock := &scriptedCompleter{responses: []string{
		"Sure! Here is the summary you asked for.",
		`{"search_summary":"Recovered on retry."}`,
	}}
	result, err := generate(context.Background(), mock, defaultSystemPrompt, PromptInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary != "Recovered on retry." {
		t.Errorf("got %+v", result)
	}
	if mock.calls != 2 {
		t.Fatalf("expected 2 calls (reprompt), got %d", mock.calls)
	}
	if !strings.Contains(mock.gotSystem[1], "valid JSON only") {
		t.Errorf("reprompt missing corrective instruction: %q", mock.gotSystem[1])
	}
}

func TestGenerate_RepromptStillFails(t *testing.T) {
	mock := &scriptedCompleter{responses: []string{
		"not json",
		"still not json",
	}}
	_, err := generate(context.Background(), mock, defaultSystemPrompt, PromptInput{})
	if err == nil {
		t.Fatal("expected error after failed reprompt")
	}
	if mock.calls != 2 {
		t.Errorf("expected 2 calls, got %d", mock.calls)
	}
}

func TestGenerate_RepromptCompleterError(t *testing.T) {
	mock := &scriptedCompleter{responses: []string{"not json"}}
	_, err := generate(context.Background(), mock, defaultSystemPrompt, PromptInput{})
	if err == nil {
		t.Fatal("expected error: first call parses fine as unparseable, reprompt call exhausts scripted responses")
	}
	if mock.calls != 2 {
		t.Errorf("expected 2 calls (initial + reprompt attempt), got %d", mock.calls)
	}
}

func TestSummarizer_Generate_TimeoutDefault(t *testing.T) {
	mock := &scriptedCompleter{responses: []string{`{"search_summary":"ok"}`}}
	s := NewSummarizer(mock, "test-model", 0, "")
	result, err := s.Generate(context.Background(), PromptInput{Summary: "s", Body: "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary != "ok" {
		t.Errorf("got %+v", result)
	}
	if s.Model() != "test-model" {
		t.Errorf("Model() = %q", s.Model())
	}
}

func TestSummarizer_Generate_NilReceiverAndNilCompleter(t *testing.T) {
	var nilSummarizer *Summarizer
	if result, err := nilSummarizer.Generate(context.Background(), PromptInput{}); result != nil || err != nil {
		t.Errorf("nil receiver: got result=%v, err=%v", result, err)
	}

	s := NewSummarizer(nil, "m", 0, "")
	if result, err := s.Generate(context.Background(), PromptInput{}); result != nil || err != nil {
		t.Errorf("nil completer: got result=%v, err=%v", result, err)
	}
}

// TestBuildUserPrompt_SectionsAndOrdering proves buildUserPrompt renders each
// populated section, omits empty ones, and includes the note body.
func TestBuildUserPrompt_SectionsAndOrdering(t *testing.T) {
	in := PromptInput{
		Summary:      "existing summary text",
		Decisions:    []string{"decision one"},
		FilesChanged: []string{"foo.go"},
		OpenThreads:  []string{"thread one"},
		Body:         "narrative body text",
	}
	got := buildUserPrompt(in)

	for _, want := range []string{
		"## Existing Summary\nexisting summary text",
		"## Decisions\n- decision one",
		"## Files Changed\n- foo.go",
		"## Open Threads\n- thread one",
		"## Session Note Body\nnarrative body text",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("buildUserPrompt output missing %q; got:\n%s", want, got)
		}
	}

	// Ordering: Summary before Decisions before FilesChanged before
	// OpenThreads before Body.
	idxSummary := strings.Index(got, "## Existing Summary")
	idxDecisions := strings.Index(got, "## Decisions")
	idxFiles := strings.Index(got, "## Files Changed")
	idxThreads := strings.Index(got, "## Open Threads")
	idxBody := strings.Index(got, "## Session Note Body")
	if !(idxSummary < idxDecisions && idxDecisions < idxFiles && idxFiles < idxThreads && idxThreads < idxBody) {
		t.Errorf("buildUserPrompt sections out of expected order:\n%s", got)
	}
}

// TestBuildUserPrompt_EmptyFieldsOmitted proves that unset optional fields
// produce no corresponding section header.
func TestBuildUserPrompt_EmptyFieldsOmitted(t *testing.T) {
	got := buildUserPrompt(PromptInput{Body: "just a body"})
	for _, unwanted := range []string{"## Existing Summary", "## Decisions", "## Files Changed", "## Open Threads"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("buildUserPrompt output unexpectedly contains %q for empty field; got:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "## Session Note Body\njust a body") {
		t.Errorf("buildUserPrompt missing body section; got:\n%s", got)
	}
}
