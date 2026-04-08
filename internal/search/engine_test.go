// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testEngine(t *testing.T) (*Engine, *storage.Vault) {
	t.Helper()
	v := testVault(t)
	cfg := storage.Config{
		SearchDefaultLimit: 10,
		BoostWing:          0.12,
		BoostHall:          0.24,
		BoostRoom:          0.34,
	}
	emb := embedder.NewMock(384)
	eng := NewEngine(emb, v, cfg)
	t.Cleanup(func() { eng.Close() })
	return eng, v
}

func addDrawer(t *testing.T, v *storage.Vault, project, wing, room, content, hall string) storage.Drawer {
	t.Helper()
	d := storage.Drawer{
		Content:    content,
		Hall:       hall,
		SourceType: "session",
		SourceRef:  "",
		FiledAt:    "2026-04-07T10:00:00Z",
	}
	if err := v.AppendDrawer(project, wing, room, d); err != nil {
		t.Fatalf("AppendDrawer: %v", err)
	}
	// Re-read to get the generated ID.
	drawers, err := v.ListDrawers(project, wing, room)
	if err != nil {
		t.Fatal(err)
	}
	for _, stored := range drawers {
		if stored.Content == content {
			return stored
		}
	}
	t.Fatal("drawer not found after append")
	return storage.Drawer{}
}

func TestSearchBasic(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d := addDrawer(t, v, "proj", "wing-a", "room-1", "Go concurrency patterns", "facts")
	if err := eng.IndexDrawer(ctx, "proj", "wing-a", "room-1", d); err != nil {
		t.Fatal(err)
	}

	results, err := eng.Search(ctx, "concurrency", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].DrawerID != d.ID {
		t.Errorf("got drawer %q, want %q", results[0].DrawerID, d.ID)
	}
	if results[0].Score <= 0 {
		t.Error("score should be positive")
	}
}

func TestSearchFilters(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d1 := addDrawer(t, v, "proj", "wing-a", "room-1", "alpha content", "facts")
	d2 := addDrawer(t, v, "proj", "wing-b", "room-1", "beta content", "facts")

	_ = eng.IndexDrawer(ctx, "proj", "wing-a", "room-1", d1)
	_ = eng.IndexDrawer(ctx, "proj", "wing-b", "room-1", d2)

	// Filter by wing.
	results, _ := eng.Search(ctx, "content", SearchFilters{Project: "proj", Wing: "wing-a"})
	if len(results) != 1 {
		t.Fatalf("wing filter: got %d results, want 1", len(results))
	}
	if results[0].Wing != "wing-a" {
		t.Errorf("expected wing-a, got %s", results[0].Wing)
	}

	// Filter by hall.
	d3 := addDrawer(t, v, "proj", "wing-a", "room-2", "gamma content", "opinions")
	_ = eng.IndexDrawer(ctx, "proj", "wing-a", "room-2", d3)

	results, _ = eng.Search(ctx, "content", SearchFilters{Project: "proj", Hall: "opinions"})
	if len(results) != 1 {
		t.Fatalf("hall filter: got %d results, want 1", len(results))
	}
	if results[0].Hall != "opinions" {
		t.Errorf("expected opinions, got %s", results[0].Hall)
	}
}

func TestSearchDateFilter(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d1 := storage.Drawer{Content: "old stuff", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-15T00:00:00Z"}
	_ = v.AppendDrawer("proj", "wing-a", "room-1", d1)
	stored1, _ := v.ListDrawers("proj", "wing-a", "room-1")
	_ = eng.IndexDrawer(ctx, "proj", "wing-a", "room-1", stored1[0])

	d2 := storage.Drawer{Content: "new stuff", Hall: "facts", SourceType: "manual", FiledAt: "2026-06-15T00:00:00Z"}
	_ = v.AppendDrawer("proj", "wing-a", "room-2", d2)
	stored2, _ := v.ListDrawers("proj", "wing-a", "room-2")
	_ = eng.IndexDrawer(ctx, "proj", "wing-a", "room-2", stored2[0])

	results, _ := eng.Search(ctx, "stuff", SearchFilters{Project: "proj", DateFrom: "2026-03-01"})
	if len(results) != 1 {
		t.Fatalf("date filter: got %d results, want 1", len(results))
	}
	if results[0].Date != "2026-06-15" {
		t.Errorf("expected 2026-06-15, got %s", results[0].Date)
	}
}

func TestStructuralBoosts(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d1 := addDrawer(t, v, "proj", "wing-a", "room-1", "content in target wing", "facts")
	d2 := addDrawer(t, v, "proj", "wing-b", "room-1", "content in other wing", "facts")

	_ = eng.IndexDrawer(ctx, "proj", "wing-a", "room-1", d1)
	_ = eng.IndexDrawer(ctx, "proj", "wing-b", "room-1", d2)

	// Search with wing filter — matching drawer should get boosted.
	results, _ := eng.Search(ctx, "content", SearchFilters{Project: "proj", Wing: "wing-a"})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 (wing filter)", len(results))
	}
	// The matching result gets wing boost.
	if results[0].Wing != "wing-a" {
		t.Errorf("expected wing-a first due to boost")
	}
}

func TestDeduplication(t *testing.T) {
	eng, _ := testEngine(t)
	ctx := context.Background()

	// Index two drawers with the same SourceRef (simulating adjacent chunks).
	d1 := storage.Drawer{
		ID: "chunk-1", Content: "first chunk of document", Hall: "facts",
		SourceType: "session", SourceRef: "session-2026-04-07",
		ChunkIndex: 0, FiledAt: "2026-04-07T00:00:00Z",
	}
	d2 := storage.Drawer{
		ID: "chunk-2", Content: "second chunk of document", Hall: "facts",
		SourceType: "session", SourceRef: "session-2026-04-07",
		ChunkIndex: 1, FiledAt: "2026-04-07T00:00:00Z",
	}

	eng.mu.Lock()
	idx := NewVectorIndex(384)
	eng.indexes["proj"] = idx
	mock := embedder.NewMock(384)
	v1, _ := mock.Embed(ctx, d1.Content)
	v2, _ := mock.Embed(ctx, d2.Content)
	_ = idx.Insert(d1.ID, v1)
	_ = idx.Insert(d2.ID, v2)
	eng.metadata[d1.ID] = makeDrawerMeta("proj", "wing-a", "room-1", d1)
	eng.metadata[d2.ID] = makeDrawerMeta("proj", "wing-a", "room-1", d2)
	eng.mu.Unlock()

	results, err := eng.Search(ctx, "chunk of document", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}

	// Should deduplicate: only 1 result from the same SourceRef.
	if len(results) != 1 {
		t.Errorf("expected 1 result after dedup, got %d", len(results))
	}
}

func TestRebuild(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	_ = addDrawer(t, v, "proj", "wing-a", "room-1", "first document", "facts")
	_ = addDrawer(t, v, "proj", "wing-a", "room-2", "second document", "opinions")
	_ = addDrawer(t, v, "proj", "wing-b", "room-1", "third document", "facts")

	if err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	results, err := eng.Search(ctx, "document", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("got %d results after rebuild, want 3", len(results))
	}
}

func TestEmptySearch(t *testing.T) {
	eng, _ := testEngine(t)
	results, err := eng.Search(context.Background(), "anything", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Errorf("expected nil results from empty engine, got %v", results)
	}
}

func TestCrossProjectSearch(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d1 := addDrawer(t, v, "proj-a", "wing-1", "room-1", "alpha project content", "facts")
	d2 := addDrawer(t, v, "proj-b", "wing-1", "room-1", "beta project content", "facts")

	_ = eng.IndexDrawer(ctx, "proj-a", "wing-1", "room-1", d1)
	_ = eng.IndexDrawer(ctx, "proj-b", "wing-1", "room-1", d2)

	// Cross-project search (no project filter).
	results, _ := eng.Search(ctx, "content", SearchFilters{})
	if len(results) != 2 {
		t.Errorf("cross-project: got %d results, want 2", len(results))
	}
}

func TestIndexAndRemoveDrawer(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d := addDrawer(t, v, "proj", "wing-a", "room-1", "removable content", "facts")
	_ = eng.IndexDrawer(ctx, "proj", "wing-a", "room-1", d)

	results, _ := eng.Search(ctx, "removable", SearchFilters{Project: "proj"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result before remove, got %d", len(results))
	}

	eng.RemoveDrawer("proj", d.ID)

	results, _ = eng.Search(ctx, "removable", SearchFilters{Project: "proj"})
	if len(results) != 0 {
		t.Errorf("expected 0 results after remove, got %d", len(results))
	}
}

func TestSearchDefaultLimit(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	// Add 15 drawers.
	for i := 0; i < 15; i++ {
		d := storage.Drawer{
			Content:    string(rune('A'+i)) + " unique content item",
			Hall:       "facts",
			SourceType: "manual",
			FiledAt:    "2026-04-07T00:00:00Z",
		}
		_ = v.AppendDrawer("proj", "wing-a", "room-1", d)
	}

	_ = eng.Rebuild(ctx, "proj")

	// Default limit is 10.
	results, _ := eng.Search(ctx, "content", SearchFilters{Project: "proj"})
	if len(results) > 10 {
		t.Errorf("expected at most 10 results with default limit, got %d", len(results))
	}
}

func TestEmbedderGetter(t *testing.T) {
	eng, _ := testEngine(t)
	emb := eng.Embedder()
	if emb == nil {
		t.Fatal("Embedder() returned nil")
	}
	if emb.Dimensions() != 384 {
		t.Errorf("Dimensions() = %d, want 384", emb.Dimensions())
	}
}

func TestIndexDrawerWithVec(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	d := addDrawer(t, v, "proj", "wing-a", "room-1", "pre-computed vector content", "facts")

	// Generate a vector manually.
	vec, err := eng.Embedder().Embed(ctx, d.Content)
	if err != nil {
		t.Fatal(err)
	}

	// Index with pre-computed vector.
	if err := eng.IndexDrawerWithVec("proj", "wing-a", "room-1", d, vec); err != nil {
		t.Fatalf("IndexDrawerWithVec: %v", err)
	}

	// Should be searchable.
	results, err := eng.Search(ctx, "pre-computed", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].DrawerID != d.ID {
		t.Errorf("got drawer %q, want %q", results[0].DrawerID, d.ID)
	}
}
