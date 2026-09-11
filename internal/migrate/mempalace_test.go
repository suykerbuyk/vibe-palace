// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// mustLoadExport loads the export at path, failing the test on error.
func mustLoadExport(t *testing.T, path string) *MemPalaceExport {
	t.Helper()
	export, err := LoadMemPalaceExport(path)
	if err != nil {
		t.Fatalf("LoadMemPalaceExport: %v", err)
	}
	return export
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

	result, err := ImportMemPalace(context.Background(), vault, engine, emb, mustLoadExport(t, exportPath), ImportOptions{})
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
	result1, err := ImportMemPalace(context.Background(), vault, engine, emb, mustLoadExport(t, exportPath), ImportOptions{})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if result1.DrawersCreated != 3 {
		t.Fatalf("first import DrawersCreated = %d, want 3", result1.DrawersCreated)
	}

	// Second import: content-addressed dedup should skip all drawers.
	result2, err := ImportMemPalace(context.Background(), vault, engine, emb, mustLoadExport(t, exportPath), ImportOptions{})
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

	result, err := ImportMemPalace(context.Background(), vault, engine, emb, mustLoadExport(t, exportPath), ImportOptions{DryRun: true})
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
	result2, err := ImportMemPalace(context.Background(), vault, engine, emb, mustLoadExport(t, exportPath), ImportOptions{})
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

	result, err := ImportMemPalace(context.Background(), vault, nil, emb, mustLoadExport(t, exportPath), ImportOptions{})
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

func TestImportMemPalace_CancelledContext(t *testing.T) {
	_, vault, engine, emb, exportPath := setupTest(t)

	// Create an export with enough drawers to trigger the embed batch loop.
	fixture := testFixture()
	writeExportJSON(t, exportPath, fixture)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := ImportMemPalace(ctx, vault, engine, emb, mustLoadExport(t, exportPath), ImportOptions{})
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

	result, err := ImportMemPalace(context.Background(), vault, engine, emb, mustLoadExport(t, exportPath), ImportOptions{})
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

// TestLoadMemPalaceExport pins the loader's error classes. The CLI maps them
// to exit codes (cmd/vp/cmd_migrate.go's migrateInputExit): not-found, a
// directory, and malformed JSON are the operator's to retype, so each must be
// recognisable through errors.Is / errors.As rather than by message.
func TestLoadMemPalaceExport(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("ok", func(t *testing.T) {
		p := filepath.Join(dir, "ok.json")
		writeExportJSON(t, p, testFixture())
		export, err := LoadMemPalaceExport(p)
		if err != nil {
			t.Fatalf("LoadMemPalaceExport: %v", err)
		}
		if got := len(export.data.Drawers); got != 3 {
			t.Errorf("drawers = %d, want 3", got)
		}
		if export.path != p {
			t.Errorf("path = %q, want %q", export.path, p)
		}
	})

	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	cases := []struct {
		name   string
		path   string
		prefix string
		class  func(error) bool
	}{
		{"missing", filepath.Join(dir, "nope.json"), "read export file:",
			func(err error) bool { return errors.Is(err, fs.ErrNotExist) }},
		{"directory", dir, "read export file:",
			func(err error) bool { return errors.Is(err, fs.ErrInvalid) }},
		{"malformed", write("bad.json", "{"), "parse export JSON:",
			func(err error) bool { return errors.As(err, &syn) }},
		{"wrong type", write("array.json", "[]"), "parse export JSON:",
			func(err error) bool { return errors.As(err, &typ) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			export, err := LoadMemPalaceExport(tc.path)
			if err == nil {
				t.Fatalf("LoadMemPalaceExport(%q) = %v, want an error", tc.path, export)
			}
			if !strings.HasPrefix(err.Error(), tc.prefix) {
				t.Errorf("error %q lacks prefix %q", err, tc.prefix)
			}
			if !tc.class(err) {
				t.Errorf("error %q (%T) is not of the expected class", err, errors.Unwrap(err))
			}
		})
	}
}

func TestMemPalaceExportEmbeddableDrawers(t *testing.T) {
	cases := []struct {
		name    string
		drawers []memPalaceDrawer
		want    int
	}{
		{"none", nil, 0},
		{"blank only", []memPalaceDrawer{{ID: "a", Content: ""}, {ID: "b", Content: " \n\t"}}, 0},
		{"mixed", []memPalaceDrawer{{ID: "a", Content: "text"}, {ID: "b", Content: "  "}, {ID: "c", Content: " more "}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			export := &MemPalaceExport{data: memPalaceExport{Drawers: tc.drawers}}
			if got := export.EmbeddableDrawers(); got != tc.want {
				t.Errorf("EmbeddableDrawers() = %d, want %d", got, tc.want)
			}
		})
	}
}

// countingEmbedder wraps an embedder and counts EmbedBatch calls.
type countingEmbedder struct {
	embedder.Embedder
	batches atomic.Int32
}

func (c *countingEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	c.batches.Add(1)
	return c.Embedder.EmbedBatch(ctx, texts)
}

// TestImportMemPalace_DryRunEmbedsNothing pins that a dry run never calls the
// embedder, even when handed one, and that its counts match a real run's.
//
// The count equality holds ONLY on a fresh vault with unique IDs: dry-run
// counts are len(work)/len(entities)/len(triples) and have never reflected ID
// dedupe, so a dry run over an already-imported vault over-reports.
func TestImportMemPalace_DryRunEmbedsNothing(t *testing.T) {
	tmpDir := t.TempDir()
	vault := storage.NewVault(tmpDir)
	emb := &countingEmbedder{Embedder: embedder.NewMock(384)}
	engine := search.NewEngine(emb, vault, storage.Config{VaultPath: tmpDir})
	exportPath := filepath.Join(tmpDir, "export.json")
	writeExportJSON(t, exportPath, testFixture())
	export := mustLoadExport(t, exportPath)

	dry, err := ImportMemPalace(context.Background(), vault, engine, emb, export, ImportOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if n := emb.batches.Load(); n != 0 {
		t.Fatalf("dry run called EmbedBatch %d times, want 0", n)
	}

	live, err := ImportMemPalace(context.Background(), vault, engine, emb, export, ImportOptions{})
	if err != nil {
		t.Fatalf("real run: %v", err)
	}
	if emb.batches.Load() == 0 {
		t.Error("real run never called EmbedBatch; the counter proves nothing")
	}
	if dry.DrawersCreated != live.DrawersCreated ||
		dry.EntitiesCreated != live.EntitiesCreated ||
		dry.TriplesCreated != live.TriplesCreated {
		t.Errorf("dry-run counts %d/%d/%d != real-run counts %d/%d/%d (fresh vault, unique IDs)",
			dry.DrawersCreated, dry.EntitiesCreated, dry.TriplesCreated,
			live.DrawersCreated, live.EntitiesCreated, live.TriplesCreated)
	}
}

// TestImportMemPalace_DryRunNilEngineAndEmbedder pins the contract the CLI
// relies on: a dry run takes nil for both and still reports full counts.
func TestImportMemPalace_DryRunNilEngineAndEmbedder(t *testing.T) {
	tmpDir := t.TempDir()
	vault := storage.NewVault(tmpDir)
	exportPath := filepath.Join(tmpDir, "export.json")
	writeExportJSON(t, exportPath, testFixture())

	result, err := ImportMemPalace(context.Background(), vault, nil, nil, mustLoadExport(t, exportPath), ImportOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry run with nil engine/embedder: %v", err)
	}
	if result.DrawersCreated != 3 || result.EntitiesCreated != 1 || result.TriplesCreated != 1 {
		t.Errorf("counts = %d/%d/%d, want 3/1/1",
			result.DrawersCreated, result.EntitiesCreated, result.TriplesCreated)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "palace", "mempalace")); !os.IsNotExist(err) {
		t.Errorf("dry run wrote palace/mempalace (stat err %v)", err)
	}
}
