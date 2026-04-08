// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testSessionVault(t *testing.T) *storage.Vault {
	t.Helper()
	dir := t.TempDir()
	return storage.NewVault(dir)
}

func TestCaptureSessionSchema(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)
	if tool.Name != "vp_capture_session" {
		t.Errorf("tool name = %q, want %q", tool.Name, "vp_capture_session")
	}
	if tool.Description == "" {
		t.Error("tool description is empty")
	}
	// Verify schema is valid JSON.
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Fatalf("invalid schema JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
}

func TestCaptureSessionBasic(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)

	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Implemented chunking engine for Phase 5."
	}`)

	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	r, ok := result.(captureSessionResult)
	if !ok {
		t.Fatalf("unexpected result type: %T", result)
	}
	if r.Status != "ok" {
		t.Errorf("status = %q, want %q", r.Status, "ok")
	}
	if r.Project != "test-proj" {
		t.Errorf("project = %q, want %q", r.Project, "test-proj")
	}
	if r.SessionID == "" {
		t.Error("session_id is empty")
	}
	if r.Iteration < 1 {
		t.Errorf("iteration = %d, want >= 1", r.Iteration)
	}
}

func TestCaptureSessionWithAllFields(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)

	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Full session with all metadata.",
		"title": "Test Session",
		"tag": "implementation",
		"model": "claude-opus-4-6",
		"decisions": ["Use brute-force search", "Skip HNSW"],
		"files_changed": ["internal/capture/chunker.go"],
		"open_threads": ["Phase 6 next"]
	}`)

	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	r := result.(captureSessionResult)
	if r.Status != "ok" {
		t.Errorf("status = %q, want %q", r.Status, "ok")
	}
}

func TestCaptureSessionWithTranscript(t *testing.T) {
	vault := testSessionVault(t)
	indexer := capture.NewIndexer(vault, nil, nil, storage.Config{}) // no engine = no embedding
	tool := CaptureSessionTool(vault, indexer)

	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Session with transcript.",
		"transcript": "We discussed the chunking algorithm and decided on sliding window with 800 char chunks."
	}`)

	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	r := result.(captureSessionResult)
	if r.Status != "ok" {
		t.Errorf("status = %q, want %q", r.Status, "ok")
	}
}

func TestCaptureSessionValidationMissingProject(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)

	params := json.RawMessage(`{"summary": "No project specified."}`)

	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestCaptureSessionValidationMissingSummary(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)

	params := json.RawMessage(`{"project": "test-proj"}`)

	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for missing summary")
	}
}

func TestCaptureSessionResult(t *testing.T) {
	// Verify the result struct serializes correctly.
	r := captureSessionResult{
		Status:    "ok",
		Project:   "my-proj",
		NotePath:  "/path/to/session.md",
		Iteration: 3,
		SessionID: "2026-04-07-03",
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if decoded["status"] != "ok" {
		t.Errorf("status = %v, want ok", decoded["status"])
	}
	if decoded["session_id"] != "2026-04-07-03" {
		t.Errorf("session_id = %v, want 2026-04-07-03", decoded["session_id"])
	}
}

func TestCaptureSessionIterationAutoIncrements(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)

	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "First session."
	}`)

	r1, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("first session: %v", err)
	}

	params2 := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Second session."
	}`)

	r2, err := tool.Handler(context.Background(), params2)
	if err != nil {
		t.Fatalf("second session: %v", err)
	}

	iter1 := r1.(captureSessionResult).Iteration
	iter2 := r2.(captureSessionResult).Iteration
	if iter2 != iter1+1 {
		t.Errorf("iteration did not auto-increment: %d → %d", iter1, iter2)
	}
}
