// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHandlerReturnsMockResponse verifies the handler echoes MOCK_RESPONSE_FILE
// contents back as the assistant message's .content field.
func TestHandlerReturnsMockResponse(t *testing.T) {
	dir := t.TempDir()
	respPath := filepath.Join(dir, "resp.json")
	if err := os.WriteFile(respPath, []byte("[]"), 0644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "mock.log")
	t.Setenv("MOCK_RESPONSE_FILE", respPath)
	t.Setenv("MOCK_LOG", logPath)
	t.Setenv("MOCK_PROMPT_TOKENS", "7")
	t.Setenv("MOCK_COMPLETION_TOKENS", "3")

	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/chat/completions", "application/json",
		strings.NewReader(`{"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	choices := got["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(choices))
	}
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "[]" {
		t.Errorf("content = %q, want %q", msg["content"], "[]")
	}
	usage := got["usage"].(map[string]any)
	if usage["total_tokens"].(float64) != 10 {
		t.Errorf("total_tokens = %v, want 10", usage["total_tokens"])
	}

	// Log file should have one JSONL line.
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	s := bufio.NewScanner(bytes.NewReader(b))
	lines := 0
	for s.Scan() {
		var row map[string]any
		if err := json.Unmarshal(s.Bytes(), &row); err != nil {
			t.Fatalf("log line parse: %v", err)
		}
		if row["path"] != "/chat/completions" {
			t.Errorf("log path = %v", row["path"])
		}
		lines++
	}
	if lines != 1 {
		t.Errorf("log lines = %d, want 1", lines)
	}
}

// TestMockShapeMatchesRealFixture is the drift trip-wire: the mock's
// response shape must be a superset of the fields the real LLM API
// returns (as captured in testdata/real-response.json). If llm.Client
// starts consuming new fields, either update the fixture or update the
// mock — this test forces the conversation.
func TestMockShapeMatchesRealFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/real-response.json")
	if err != nil {
		t.Fatal(err)
	}
	var real map[string]any
	if err := json.Unmarshal(fixture, &real); err != nil {
		t.Fatal(err)
	}

	// Spin up the mock and capture its response.
	dir := t.TempDir()
	respPath := filepath.Join(dir, "resp.json")
	if err := os.WriteFile(respPath, []byte("[]"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOCK_RESPONSE_FILE", respPath)
	t.Setenv("MOCK_LOG", "")

	srv := httptest.NewServer(newHandler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/chat/completions", "application/json",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var mock map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&mock); err != nil {
		t.Fatal(err)
	}

	// Real fixture fields must exist in mock response.
	rc := real["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"]
	mc := mock["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"]
	if rc == nil || mc == nil {
		t.Errorf("missing choices[0].message.content in real=%v mock=%v", rc, mc)
	}
	if _, ok := real["usage"].(map[string]any)["total_tokens"]; !ok {
		t.Error("real fixture missing usage.total_tokens")
	}
	if _, ok := mock["usage"].(map[string]any)["total_tokens"]; !ok {
		t.Error("mock response missing usage.total_tokens")
	}
}
