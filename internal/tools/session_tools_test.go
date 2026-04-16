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

	"github.com/suykerbuyk/vibe-palace/internal/archive"
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

func TestCaptureSessionArchiveLink(t *testing.T) {
	vault := testSessionVault(t)

	// Seed a real archive under the project's transcripts dir.
	ctx := context.Background()
	srcPath := filepath.Join(t.TempDir(), "src.jsonl")
	if err := os.WriteFile(srcPath, []byte(sampleClaudeJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := archive.Create(archive.CreateOptions{
		Adapter:     archive.ClaudeCodeAdapterName,
		SessionID:   "link-session",
		SourcePath:  srcPath,
		VaultRoot:   vault.Root,
		ProjectSlug: "test-proj",
	})
	if err != nil {
		t.Fatalf("seed archive: %v", err)
	}

	tool := CaptureSessionTool(vault, nil)
	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Session linked to a pre-existing archive.",
		"archive_session_id": "link-session"
	}`)
	r, err := tool.Handler(ctx, params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := r.(captureSessionResult)

	// Session frontmatter should carry archive: <vault-rel-manifest>.
	meta, _, err := vault.ReadSession("test-proj", got.SessionID[:10], got.Iteration)
	if err != nil {
		t.Fatalf("read session back: %v", err)
	}
	wantArchive := archive.VaultRelPath(vault.Root, res.ManifestPath)
	if meta.Archive != wantArchive {
		t.Errorf("session.archive = %q, want %q", meta.Archive, wantArchive)
	}

	// Manifest should carry vault_rel_session_note.
	m, err := archive.ReadManifest(res.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.VaultRelSessionNote == "" {
		t.Error("manifest vault_rel_session_note was not updated")
	}
	if !strings.HasSuffix(m.VaultRelSessionNote, ".md") {
		t.Errorf("vault_rel_session_note = %q, expected a .md path", m.VaultRelSessionNote)
	}
}

const sampleClaudeJSONL = `{"type":"permission-mode","permissionMode":"bypassPermissions","sessionId":"link-session"}
{"type":"user","message":{"role":"user","content":"hi"}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-6","content":"hello"}}
`

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
