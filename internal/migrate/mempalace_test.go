// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// writeExportJSON marshals the given export struct and writes it to path.
func writeExportJSON(t *testing.T, path string, export memPalaceExport) {
	t.Helper()
	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		t.Fatalf("marshal export JSON: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for export: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write export JSON: %v", err)
	}
}

func testFixture() memPalaceExport {
	validTo := "2026-12-31T00:00:00Z"
	return memPalaceExport{
		ExportedAt: "2026-04-09T00:00:00Z",
		Drawers: []memPalaceDrawer{
			{
				ID:         "d1",
				Wing:       "emotions",
				Room:       "joy",
				Content:    "I felt a deep sense of happiness today.",
				SourceFile: "session-01.md",
				ChunkIndex: 0,
				AddedBy:    "claude",
				FiledAt:    "2026-04-01T10:00:00Z",
			},
			{
				ID:         "d2",
				Wing:       "technical",
				Room:       "golang",
				Content:    "Implemented a new search engine with vector embeddings.",
				SourceFile: "session-02.md",
				ChunkIndex: 0,
				AddedBy:    "claude",
				FiledAt:    "2026-04-02T10:00:00Z",
			},
			{
				ID:         "d3",
				Wing:       "memory",
				Room:       "childhood",
				Content:    "Remembered playing in the garden as a child.",
				SourceFile: "session-03.md",
				ChunkIndex: 1,
				AddedBy:    "claude",
				FiledAt:    "2026-04-03T10:00:00Z",
			},
		},
		Entities: []memPalaceEntity{
			{
				ID:         "e1",
				Name:       "Claude",
				Type:       "assistant",
				Properties: map[string]string{"role": "ai"},
				CreatedAt:  "2026-04-01T00:00:00Z",
			},
		},
		Triples: []memPalaceTriple{
			{
				Subject:       "claude",
				Predicate:     "assists",
				Object:        "john",
				ValidFrom:     "2026-01-01T00:00:00Z",
				ValidTo:       &validTo,
				Confidence:    0.95,
				SourceSession: "session-01",
			},
		},
	}
}

func setupTest(t *testing.T) (string, *storage.Vault, *search.Engine, *embedder.MockEmbedder, string) {
	t.Helper()
	tmpDir := t.TempDir()
	vault := storage.NewVault(tmpDir)
	emb := embedder.NewMock(384)
	cfg := storage.Config{
		VaultPath:     tmpDir,
		ChunkMaxChars: 2000,
		ChunkOverlap:  200,
	}
	engine := search.NewEngine(emb, vault, cfg)
	exportPath := filepath.Join(tmpDir, "export.json")
	return tmpDir, vault, engine, emb, exportPath
}

func TestImportMemPalace_Basic(t *testing.T) {
	_, vault, engine, emb, exportPath := setupTest(t)
	fixture := testFixture()
	writeExportJSON(t, exportPath, fixture)

	result, err := ImportMemPalace(context.Background(), vault, engine, emb, exportPath, ImportOptions{})
	if err != nil {
		t.Fatalf("ImportMemPalace: %v", err)
	}

	if result.DrawersCreated != 3 {
		t.Errorf("DrawersCreated = %d, want 3", result.DrawersCreated)
	}
	if result.EntitiesCreated != 1 {
		t.Errorf("EntitiesCreated = %d, want 1", result.EntitiesCreated)
	}
	if result.TriplesCreated != 1 {
		t.Errorf("TriplesCreated = %d, want 1", result.TriplesCreated)
	}
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
}

func TestImportMemPalace_Idempotent(t *testing.T) {
	_, vault, engine, emb, exportPath := setupTest(t)
	fixture := testFixture()
	writeExportJSON(t, exportPath, fixture)

	// First import.
	result1, err := ImportMemPalace(context.Background(), vault, engine, emb, exportPath, ImportOptions{})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if result1.DrawersCreated != 3 {
		t.Fatalf("first import DrawersCreated = %d, want 3", result1.DrawersCreated)
	}

	// Second import: content-addressed dedup should skip all drawers.
	result2, err := ImportMemPalace(context.Background(), vault, engine, emb, exportPath, ImportOptions{})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if result2.DrawersCreated != 0 {
		t.Errorf("second import DrawersCreated = %d, want 0", result2.DrawersCreated)
	}
}

func TestImportMemPalace_DryRun(t *testing.T) {
	_, vault, engine, emb, exportPath := setupTest(t)
	fixture := testFixture()
	writeExportJSON(t, exportPath, fixture)

	result, err := ImportMemPalace(context.Background(), vault, engine, emb, exportPath, ImportOptions{DryRun: true})
	if err != nil {
		t.Fatalf("ImportMemPalace dry run: %v", err)
	}

	if result.DrawersCreated != 3 {
		t.Errorf("DryRun DrawersCreated = %d, want 3", result.DrawersCreated)
	}
	if result.EntitiesCreated != 1 {
		t.Errorf("DryRun EntitiesCreated = %d, want 1", result.EntitiesCreated)
	}
	if result.TriplesCreated != 1 {
		t.Errorf("DryRun TriplesCreated = %d, want 1", result.TriplesCreated)
	}

	// Verify nothing was actually written: a real import should still create 3.
	result2, err := ImportMemPalace(context.Background(), vault, engine, emb, exportPath, ImportOptions{})
	if err != nil {
		t.Fatalf("post-dry-run import: %v", err)
	}
	if result2.DrawersCreated != 3 {
		t.Errorf("post-dry-run DrawersCreated = %d, want 3", result2.DrawersCreated)
	}
}

func TestImportMemPalace_WingRoomMapping(t *testing.T) {
	_, vault, _, emb, exportPath := setupTest(t)

	export := memPalaceExport{
		ExportedAt: "2026-04-09T00:00:00Z",
		Drawers: []memPalaceDrawer{
			{
				ID:      "w1",
				Wing:    "emotions",
				Room:    "joy",
				Content: "Known wing with room.",
				FiledAt: "2026-04-01T00:00:00Z",
			},
			{
				ID:      "w2",
				Wing:    "Custom Wing",
				Room:    "",
				Content: "Unknown wing with empty room.",
				FiledAt: "2026-04-02T00:00:00Z",
			},
		},
	}
	writeExportJSON(t, exportPath, export)

	result, err := ImportMemPalace(context.Background(), vault, nil, emb, exportPath, ImportOptions{})
	if err != nil {
		t.Fatalf("ImportMemPalace: %v", err)
	}
	if result.DrawersCreated != 2 {
		t.Errorf("DrawersCreated = %d, want 2", result.DrawersCreated)
	}

	// Verify wing mapping: "emotions" stays "emotions", "Custom Wing" -> slugified.
	if mapped := mapWing("emotions"); mapped != "emotions" {
		t.Errorf("mapWing(emotions) = %q, want %q", mapped, "emotions")
	}
	if mapped := mapWing("Custom Wing"); mapped == "Custom Wing" {
		t.Errorf("mapWing(Custom Wing) should be slugified, got %q", mapped)
	}

	// Verify empty room defaults to "general" by checking no error occurred.
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
}

func TestImportMemPalace_BadJSON(t *testing.T) {
	_, vault, engine, emb, exportPath := setupTest(t)

	if err := os.WriteFile(exportPath, []byte(`{bad json`), 0o644); err != nil {
		t.Fatalf("write bad JSON: %v", err)
	}

	_, err := ImportMemPalace(context.Background(), vault, engine, emb, exportPath, ImportOptions{})
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

func TestImportMemPalace_NonExistentFile(t *testing.T) {
	_, vault, engine, emb, _ := setupTest(t)

	_, err := ImportMemPalace(context.Background(), vault, engine, emb, "/no/such/file.json", ImportOptions{})
	if err == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}
}

func TestImportMemPalace_CancelledContext(t *testing.T) {
	_, vault, engine, emb, exportPath := setupTest(t)

	// Create an export with enough drawers to trigger the embed batch loop.
	fixture := testFixture()
	writeExportJSON(t, exportPath, fixture)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := ImportMemPalace(ctx, vault, engine, emb, exportPath, ImportOptions{})
	if err == nil {
		t.Fatal("expected context cancellation error, got nil")
	}
}

func TestImportMemPalace_EmptyExport(t *testing.T) {
	_, vault, engine, emb, exportPath := setupTest(t)

	export := memPalaceExport{
		ExportedAt: "2026-04-09T00:00:00Z",
		Drawers:    []memPalaceDrawer{},
		Entities:   []memPalaceEntity{},
		Triples:    []memPalaceTriple{},
	}
	writeExportJSON(t, exportPath, export)

	result, err := ImportMemPalace(context.Background(), vault, engine, emb, exportPath, ImportOptions{})
	if err != nil {
		t.Fatalf("ImportMemPalace empty: %v", err)
	}
	if result.DrawersCreated != 0 {
		t.Errorf("DrawersCreated = %d, want 0", result.DrawersCreated)
	}
	if result.EntitiesCreated != 0 {
		t.Errorf("EntitiesCreated = %d, want 0", result.EntitiesCreated)
	}
	if result.TriplesCreated != 0 {
		t.Errorf("TriplesCreated = %d, want 0", result.TriplesCreated)
	}
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
}
