// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package itersummary

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// scriptedCompleter is a scripted llm.Completer mirroring
// internal/enrichment/enrichment_test.go's own mockCompleter exactly: each
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
		`{"summary":"Fixed the parser.","decisions":["Regex over hand-rolled scan — simpler"],"unblocks":"Enables the next entry type"}`,
	}}

	result, err := generate(context.Background(), mock, defaultSystemPrompt, PromptInput{Header: "## Iteration 1", Body: "did stuff"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary != "Fixed the parser." {
		t.Errorf("summary: got %q", result.Summary)
	}
	if len(result.Decisions) != 1 || result.Decisions[0] != "Regex over hand-rolled scan — simpler" {
		t.Errorf("decisions: got %v", result.Decisions)
	}
	if result.Unblocks != "Enables the next entry type" {
		t.Errorf("unblocks: got %q", result.Unblocks)
	}
	if mock.calls != 1 {
		t.Errorf("expected 1 call, got %d", mock.calls)
	}
}

func TestGenerate_FencedJSON(t *testing.T) {
	mock := &scriptedCompleter{responses: []string{
		"```json\n{\"summary\":\"Fenced.\"}\n```",
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
		"```\n{\"summary\":\"Bare fence.\"}\n```",
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
		`{"summary":"Recovered on retry."}`,
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
	mock := &scriptedCompleter{responses: []string{`{"summary":"ok"}`}}
	s := NewSummarizer(mock, "test-model", 0, "")
	result, err := s.Generate(context.Background(), PromptInput{Header: "h", Body: "b"})
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
