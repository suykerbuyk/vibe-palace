// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testSearchEngine(t *testing.T) (*search.Engine, *storage.Vault) {
	t.Helper()
	vault := storage.NewVault(t.TempDir())
	cfg := storage.Config{SearchDefaultLimit: 10}
	emb := embedder.NewMock(384)
	eng := search.NewEngine(emb, vault, cfg)
	t.Cleanup(func() { eng.Close() })
	return eng, vault
}

func seedDrawer(t *testing.T, v *storage.Vault, eng *search.Engine, project, wing, room, content, hall string) {
	t.Helper()
	d := storage.Drawer{
		Content:    content,
		Hall:       hall,
		SourceType: "session",
		FiledAt:    "2026-04-07T10:00:00Z",
	}
	if err := v.AppendDrawer(project, wing, room, d); err != nil {
		t.Fatal(err)
	}
	drawers, _ := v.ListDrawers(project, wing, room)
	for _, stored := range drawers {
		if stored.Content == content {
			if err := eng.IndexDrawers(context.Background(), []search.DrawerInput{{Project: project, Wing: wing, Room: room, Drawer: stored}}); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}

func TestSearchToolBasic(t *testing.T) {
	eng, vault := testSearchEngine(t)
	seedDrawer(t, vault, eng, "proj", "wing-a", "room-1", "Go concurrency patterns", "facts")

	tool := SearchTool(eng)
	result, err := tool.Handler(context.Background(),
		json.RawMessage(`{"query": "concurrency", "project": "proj"}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	results := result.([]search.SearchResult)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Content != "Go concurrency patterns" {
		t.Errorf("content = %q, want 'Go concurrency patterns'", results[0].Content)
	}
	if results[0].Score <= 0 {
		t.Error("score should be positive")
	}
}

func TestSearchToolValidation(t *testing.T) {
	eng, _ := testSearchEngine(t)
	tool := SearchTool(eng)

	// Missing query.
	_, err := tool.Handler(context.Background(),
		json.RawMessage(`{"project": "proj"}`))
	if err == nil {
		t.Error("expected error for missing query")
	}

	// Missing project.
	_, err = tool.Handler(context.Background(),
		json.RawMessage(`{"query": "test"}`))
	if err == nil {
		t.Error("expected error for missing project")
	}
}

func TestSearchToolLimitClamping(t *testing.T) {
	eng, vault := testSearchEngine(t)
	for i := range 60 {
		d := storage.Drawer{
			Content:    string(rune('A'+i%26)) + " unique content",
			Hall:       "facts",
			SourceType: "manual",
			FiledAt:    "2026-04-07T00:00:00Z",
		}
		_ = vault.AppendDrawer("proj", "wing-a", "room-1", d)
	}
	_, _ = eng.Rebuild(context.Background(), "proj")

	tool := SearchTool(eng)
	result, err := tool.Handler(context.Background(),
		json.RawMessage(`{"query": "content", "project": "proj", "limit": 100}`))
	if err != nil {
		t.Fatal(err)
	}

	results := result.([]search.SearchResult)
	if len(results) > 50 {
		t.Errorf("limit clamping failed: got %d results, max should be 50", len(results))
	}
}

func TestCrossProjectToolBasic(t *testing.T) {
	eng, vault := testSearchEngine(t)
	seedDrawer(t, vault, eng, "proj-a", "wing-1", "room-1", "alpha content", "facts")
	seedDrawer(t, vault, eng, "proj-b", "wing-1", "room-1", "beta content", "facts")

	tool := SearchCrossProjectTool(eng)
	result, err := tool.Handler(context.Background(),
		json.RawMessage(`{"query": "content"}`))
	if err != nil {
		t.Fatal(err)
	}

	results := result.([]search.SearchResult)
	if len(results) != 2 {
		t.Errorf("cross-project: got %d results, want 2", len(results))
	}
}

func TestCrossProjectToolValidation(t *testing.T) {
	eng, _ := testSearchEngine(t)
	tool := SearchCrossProjectTool(eng)

	_, err := tool.Handler(context.Background(),
		json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for missing query")
	}
}

func TestSearchToolEmptyResults(t *testing.T) {
	eng, _ := testSearchEngine(t)
	tool := SearchTool(eng)

	result, err := tool.Handler(context.Background(),
		json.RawMessage(`{"query": "nothing", "project": "proj"}`))
	if err != nil {
		t.Fatal(err)
	}

	results := result.([]search.SearchResult)
	if len(results) != 0 {
		t.Errorf("expected empty results, got %d", len(results))
	}
}
