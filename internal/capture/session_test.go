// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestWriteSessionHappyPath(t *testing.T) {
	vault := testVault(t)

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj",
		Summary: "Implemented feature X",
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	if result.Status != "ok" {
		t.Errorf("status = %q, want %q", result.Status, "ok")
	}
	if result.Project != "test-proj" {
		t.Errorf("project = %q, want %q", result.Project, "test-proj")
	}
	if result.SessionID == "" {
		t.Error("session_id is empty")
	}
	if result.Iteration < 1 {
		t.Errorf("iteration = %d, want >= 1", result.Iteration)
	}
}

func TestWriteSessionTitleDefaulting(t *testing.T) {
	vault := testVault(t)

	// No title: should default to summary (truncated at 80).
	long := "This is a very long summary that exceeds eighty characters and should be truncated by the title defaulting logic in WriteSession"
	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj",
		Summary: long,
	})
	if err != nil {
		t.Fatal(err)
	}

	meta, _, err := vault.ReadSession("test-proj", result.SessionID[:10], result.Iteration)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Title) > 80 {
		t.Errorf("title length = %d, want <= 80", len(meta.Title))
	}
}

func TestWriteSessionWithFriction(t *testing.T) {
	vault := testVault(t)

	// Transcript with friction signals: corrections and rework.
	transcript := "wrong wrong wrong undo revert go back start over try again scratch that"
	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "Session with friction",
		Transcript: transcript,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FrictionScore == 0 {
		t.Error("expected non-zero friction score for transcript with friction signals")
	}
}

func TestWriteSessionWithArchiveLink(t *testing.T) {
	vault := testVault(t)

	// Seed a real archive.
	srcPath := filepath.Join(t.TempDir(), "src.jsonl")
	jsonl := `{"type":"permission-mode","permissionMode":"bypassPermissions","sessionId":"link-session"}
{"type":"user","message":{"role":"user","content":"hi"}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-6","content":"hello"}}
`
	if err := os.WriteFile(srcPath, []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}
	archRes, err := archive.Create(archive.CreateOptions{
		Adapter:     archive.ClaudeCodeAdapterName,
		SessionID:   "link-session",
		SourcePath:  srcPath,
		VaultRoot:   vault.Root,
		ProjectSlug: "test-proj",
	})
	if err != nil {
		t.Fatalf("seed archive: %v", err)
	}

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:          "test-proj",
		Summary:          "Session linked to archive",
		ArchiveSessionID: "link-session",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Session frontmatter should carry archive: field.
	meta, _, err := vault.ReadSession("test-proj", result.SessionID[:10], result.Iteration)
	if err != nil {
		t.Fatal(err)
	}
	wantArchive := archive.VaultRelPath(vault.Root, archRes.ManifestPath)
	if meta.Archive != wantArchive {
		t.Errorf("session.archive = %q, want %q", meta.Archive, wantArchive)
	}

	// Manifest should have the back-link.
	m, err := archive.ReadManifest(archRes.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.VaultRelSessionNote == "" {
		t.Error("manifest vault_rel_session_note was not updated")
	}
}

func TestWriteSessionNeedsIndexingTrue(t *testing.T) {
	vault := testVault(t)
	indexer := NewIndexer(vault, nil, nil, storage.Config{})

	result, err := WriteSession(context.Background(), vault, indexer, SessionParams{
		Project:       "test-proj",
		Summary:       "Session with deferred indexing",
		Transcript:    "Some transcript text that would normally be indexed.",
		NeedsIndexing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" {
		t.Errorf("status = %q, want %q", result.Status, "ok")
	}
	// With NeedsIndexing=true, no drawers should be created.
	// (The indexer runs but writes no drawers only because embedder is nil;
	// the key assertion is that WriteSession does not error and returns ok.)
}

func TestWriteSessionNeedsIndexingFalse(t *testing.T) {
	vault := testVault(t)
	indexer := NewIndexer(vault, nil, nil, storage.Config{})

	result, err := WriteSession(context.Background(), vault, indexer, SessionParams{
		Project:       "test-proj",
		Summary:       "Session with immediate indexing",
		Transcript:    "Some transcript text that should be indexed right away.",
		NeedsIndexing: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" {
		t.Errorf("status = %q, want %q", result.Status, "ok")
	}
}

func TestWriteSessionMissingProject(t *testing.T) {
	vault := testVault(t)

	_, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Summary: "No project specified",
	})
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestWriteSessionMissingSummary(t *testing.T) {
	vault := testVault(t)

	_, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj",
	})
	if err == nil {
		t.Fatal("expected error for missing summary")
	}
}

func TestWriteSessionAllFields(t *testing.T) {
	vault := testVault(t)

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:      "test-proj",
		Summary:      "Full session with all metadata",
		Title:        "Test Session",
		Tag:          "implementation",
		Model:        "claude-opus-4-6",
		Decisions:    []string{"Use brute-force search", "Skip HNSW"},
		FilesChanged: []string{"internal/capture/session.go"},
		OpenThreads:  []string{"Phase 6 next"},
	})
	if err != nil {
		t.Fatal(err)
	}

	meta, body, err := vault.ReadSession("test-proj", result.SessionID[:10], result.Iteration)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "Test Session" {
		t.Errorf("title = %q, want %q", meta.Title, "Test Session")
	}
	if meta.Tag != "implementation" {
		t.Errorf("tag = %q, want %q", meta.Tag, "implementation")
	}
	if meta.Model != "claude-opus-4-6" {
		t.Errorf("model = %q, want %q", meta.Model, "claude-opus-4-6")
	}
	if len(meta.Decisions) != 2 {
		t.Errorf("decisions count = %d, want 2", len(meta.Decisions))
	}
	if len(meta.FilesChanged) != 1 {
		t.Errorf("files_changed count = %d, want 1", len(meta.FilesChanged))
	}
	if len(meta.OpenThreads) != 1 {
		t.Errorf("open_threads count = %d, want 1", len(meta.OpenThreads))
	}
	// Body should contain the summary section.
	if body == "" {
		t.Error("body is empty")
	}
}

func TestParseIteration(t *testing.T) {
	tests := []struct {
		id   string
		want int
	}{
		{"2026-04-15-01", 1},
		{"2026-04-15-03", 3},
		{"2026-04-15-12", 12},
		{"bad", 0},
		{"", 0},
	}
	for _, tt := range tests {
		got := ParseIteration(tt.id)
		if got != tt.want {
			t.Errorf("ParseIteration(%q) = %d, want %d", tt.id, got, tt.want)
		}
	}
}
