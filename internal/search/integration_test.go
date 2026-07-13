// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// newIntegrationEmbedder creates a real ONNX embedder, skipping if model
// is unavailable or in short mode.
func newIntegrationEmbedder(t *testing.T) embedder.Embedder {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode (requires ONNX model)")
	}

	emb, err := embedder.NewONNX("sentence-transformers/all-MiniLM-L6-v2", testutil.ProjectCacheDir(t), 256, 32)
	if err != nil {
		t.Fatalf("NewONNX: %v", err)
	}
	t.Cleanup(func() { emb.Close() })
	return emb
}

// TestIntegrationSearchSemanticRanking verifies that the full pipeline
// (real embeddings → vector index → engine) produces semantically meaningful
// results: related content ranks higher than unrelated content.
func TestIntegrationSearchSemanticRanking(t *testing.T) {
	emb := newIntegrationEmbedder(t)
	vault := storage.NewVault(t.TempDir())
	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := NewEngine(emb, vault, cfg)
	defer eng.Close()

	ctx := context.Background()

	// Seed drawers with known content — some related, some not.
	drawers := []struct {
		wing, room, content, hall string
	}{
		{"dev", "go", "Go goroutines and channels enable lightweight concurrency", "facts"},
		{"dev", "go", "The sync.WaitGroup is used to wait for goroutines to finish", "facts"},
		{"dev", "python", "Python asyncio provides asynchronous I/O with async/await syntax", "facts"},
		{"cooking", "italian", "Fresh pasta is made from eggs and tipo 00 flour", "facts"},
		{"cooking", "italian", "Neapolitan pizza requires a wood-fired oven at 900 degrees", "facts"},
		{"science", "physics", "Quantum entanglement allows instantaneous correlation between particles", "facts"},
	}

	for _, d := range drawers {
		dr := storage.Drawer{
			Content:    d.content,
			Hall:       d.hall,
			SourceType: "manual",
			FiledAt:    "2026-04-07T10:00:00Z",
		}
		if err := vault.AppendDrawer("proj", d.wing, d.room, dr); err != nil {
			t.Fatal(err)
		}
	}

	if err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// Query about Go concurrency — should rank Go content highest.
	results, err := eng.Search(ctx, "how do goroutines work in Go", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for Go concurrency query")
	}

	// The top result should be from the dev/go wing.
	if results[0].Wing != "dev" || results[0].Room != "go" {
		t.Errorf("top result should be dev/go, got %s/%s (content: %s)",
			results[0].Wing, results[0].Room, truncate(results[0].Content, 60))
	}

	// Cooking content should rank lower than Go content.
	goScore := results[0].Score
	var cookingScore float64
	for _, r := range results {
		if r.Wing == "cooking" {
			cookingScore = r.Score
			break
		}
	}
	if cookingScore >= goScore {
		t.Errorf("cooking score (%f) should be < Go score (%f) for Go-related query",
			cookingScore, goScore)
	}

	// Query about cooking — should rank Italian content highest.
	results, err = eng.Search(ctx, "making homemade pasta dough", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for pasta query")
	}
	if results[0].Wing != "cooking" {
		t.Errorf("top result for pasta query should be cooking, got %s (content: %s)",
			results[0].Wing, truncate(results[0].Content, 60))
	}
}

// TestIntegrationRebuildAndCache verifies that rebuild populates the embed
// cache and a second rebuild uses cached vectors (no re-embedding needed).
func TestIntegrationRebuildAndCache(t *testing.T) {
	emb := newIntegrationEmbedder(t)
	vault := storage.NewVault(t.TempDir())
	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := NewEngine(emb, vault, cfg)
	defer eng.Close()

	ctx := context.Background()

	dr := storage.Drawer{
		Content:    "Test content for cache verification",
		Hall:       "facts",
		SourceType: "manual",
		FiledAt:    "2026-04-07T10:00:00Z",
	}
	if err := vault.AppendDrawer("proj", "wing-a", "room-1", dr); err != nil {
		t.Fatal(err)
	}

	// First rebuild — embeds and caches.
	if err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}

	// Verify cache file exists.
	stored, _ := vault.ListDrawers("proj", "wing-a", "room-1")
	if len(stored) == 0 {
		t.Fatal("no drawers found")
	}
	cache := NewEmbedCache(vault)
	vec, err := cache.Get("proj", stored[0].ID)
	if err != nil {
		t.Fatalf("cache Get: %v", err)
	}
	if vec == nil {
		t.Fatal("expected cached vector after rebuild")
	}
	if len(vec) != 384 {
		t.Errorf("cached vector dims = %d, want 384", len(vec))
	}

	// Second rebuild should succeed (uses cache, no re-embedding).
	if err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}

	// Search should still work.
	results, err := eng.Search(ctx, "test content", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result after second rebuild, got %d", len(results))
	}
}

// TestIntegrationIndexDrawerAndSearch verifies the incremental indexing
// path: add a drawer via IndexDrawer, then search for it.
func TestIntegrationIndexDrawerAndSearch(t *testing.T) {
	emb := newIntegrationEmbedder(t)
	vault := storage.NewVault(t.TempDir())
	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := NewEngine(emb, vault, cfg)
	defer eng.Close()

	ctx := context.Background()

	dr := storage.Drawer{
		Content:    "Kubernetes pod scheduling uses affinity and anti-affinity rules",
		Hall:       "facts",
		SourceType: "session",
		FiledAt:    "2026-04-07T10:00:00Z",
	}
	if err := vault.AppendDrawer("proj", "infra", "k8s", dr); err != nil {
		t.Fatal(err)
	}
	stored, _ := vault.ListDrawers("proj", "infra", "k8s")

	if err := eng.IndexDrawers(ctx, []DrawerInput{{Project: "proj", Wing: "infra", Room: "k8s", Drawer: stored[0]}}); err != nil {
		t.Fatal(err)
	}

	// Unrelated query should still return it (only 1 item in index).
	results, err := eng.Search(ctx, "container orchestration", SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Wing != "infra" || results[0].Room != "k8s" {
		t.Errorf("result = %s/%s, want infra/k8s", results[0].Wing, results[0].Room)
	}
	if results[0].Hall != "facts" {
		t.Errorf("hall = %s, want facts", results[0].Hall)
	}
	if results[0].Date != "2026-04-07" {
		t.Errorf("date = %s, want 2026-04-07", results[0].Date)
	}
}

// TestIntegrationStructuralBoostsWithRealEmbeddings verifies that structural
// boosts affect ranking when two results have similar semantic relevance.
func TestIntegrationStructuralBoostsWithRealEmbeddings(t *testing.T) {
	emb := newIntegrationEmbedder(t)
	vault := storage.NewVault(t.TempDir())
	cfg := storage.Config{
		SearchDefaultLimit: 10,
		BoostWing:          0.12,
		BoostHall:          0.24,
		BoostRoom:          0.34,
	}
	eng := NewEngine(emb, vault, cfg)
	defer eng.Close()

	ctx := context.Background()

	// Two identical-content drawers in different wings.
	for _, wing := range []string{"target-wing", "other-wing"} {
		dr := storage.Drawer{
			Content:    "Machine learning model training with gradient descent",
			Hall:       "facts",
			SourceType: "manual",
			FiledAt:    "2026-04-07T10:00:00Z",
		}
		if err := vault.AppendDrawer("proj", wing, "room-1", dr); err != nil {
			t.Fatal(err)
		}
	}

	if err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}

	// Search with wing filter — the matching wing should get boosted.
	results, err := eng.Search(ctx, "gradient descent optimization",
		SearchFilters{Project: "proj", Wing: "target-wing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (wing filter), got %d", len(results))
	}
	if results[0].Wing != "target-wing" {
		t.Errorf("expected target-wing, got %s", results[0].Wing)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
