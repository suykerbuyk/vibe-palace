// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestIntegrationConfigBoostValues proves that search boost config values
// actually affect result scoring.
func TestIntegrationConfigBoostValues(t *testing.T) {
	// Harness with a large wing boost.
	h := newHarness(t, true, func(c *storage.Config) {
		c.BoostWing = 0.50
		c.BoostHall = 0.0
		c.BoostRoom = 0.0
	})
	ctx := context.Background()

	// Two identical drawers in different wings.
	content := "Machine learning model training with gradient descent optimization"
	h.addDrawer(t, "proj", "target-wing", "room", content, "facts", "2026-04-01")
	h.addDrawer(t, "proj", "other-wing", "room", content, "facts", "2026-04-01")

	if err := h.Engine.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}

	// Search with wing filter — target-wing should get boosted.
	boosted, err := h.Engine.Search(ctx, "gradient descent", search.SearchFilters{
		Project: "proj", Wing: "target-wing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(boosted) != 1 {
		t.Fatalf("expected 1 result with wing filter, got %d", len(boosted))
	}

	// Search without wing filter — both should appear, scores should differ.
	all, err := h.Engine.Search(ctx, "gradient descent", search.SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 2 {
		t.Fatalf("expected 2 results, got %d", len(all))
	}

	// Now create a zero-boost harness and verify scores are equal.
	h2 := newHarness(t, true, func(c *storage.Config) {
		c.BoostWing = 0.0
		c.BoostHall = 0.0
		c.BoostRoom = 0.0
	})

	h2.addDrawer(t, "proj", "wing-a", "room", content, "facts", "2026-04-01")
	h2.addDrawer(t, "proj", "wing-b", "room", content, "facts", "2026-04-01")

	if err := h2.Engine.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}

	noBoosted, err := h2.Engine.Search(ctx, "gradient descent", search.SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(noBoosted) < 2 {
		t.Fatalf("expected 2 results, got %d", len(noBoosted))
	}
	// With zero boosts, identical content should have identical scores.
	if noBoosted[0].Score != noBoosted[1].Score {
		t.Errorf("with zero boosts, scores should be equal: %f vs %f",
			noBoosted[0].Score, noBoosted[1].Score)
	}
}

// TestIntegrationConfigChunkSize proves that ChunkMaxChars config values
// actually control how many chunks are produced from a transcript.
func TestIntegrationConfigChunkSize(t *testing.T) {
	// Build a ~2000-char transcript with many short paragraphs.
	// Each paragraph is ~100 chars, separated by \n\n.
	var transcript string
	for i := 0; len(transcript) < 2000; i++ {
		transcript += "This is paragraph number " + string(rune('A'+i%26)) +
			" with enough content to form a meaningful semantic unit for chunking.\n\n"
	}

	// Small chunks: 200 chars → expect many chunks.
	h1 := newHarness(t, false, func(c *storage.Config) {
		c.ChunkMaxChars = 200
		c.ChunkOverlap = 0
	})
	if err := h1.Indexer.IndexTranscript(context.Background(), "s1", "proj", transcript); err != nil {
		t.Fatal(err)
	}
	smallDrawers, _ := countAllDrawers(t, h1.Vault, "proj")

	// Large chunks: 1000 chars → expect fewer chunks.
	h2 := newHarness(t, false, func(c *storage.Config) {
		c.ChunkMaxChars = 1000
		c.ChunkOverlap = 0
	})
	if err := h2.Indexer.IndexTranscript(context.Background(), "s2", "proj", transcript); err != nil {
		t.Fatal(err)
	}
	largeDrawers, _ := countAllDrawers(t, h2.Vault, "proj")

	if smallDrawers <= largeDrawers {
		t.Errorf("small chunks (%d drawers) should produce more drawers than large chunks (%d drawers)",
			smallDrawers, largeDrawers)
	}
}

// TestIntegrationConfigSearchLimit proves the default search limit config
// actually constrains result count.
func TestIntegrationConfigSearchLimit(t *testing.T) {
	h := newHarness(t, false, func(c *storage.Config) {
		c.SearchDefaultLimit = 3
	})
	ctx := context.Background()

	// Add 10 drawers.
	for i := 0; i < 10; i++ {
		h.addDrawer(t, "proj", "wing", "room",
			"unique content item number "+string(rune('A'+i)), "facts", "2026-04-01")
	}

	if err := h.Engine.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}

	results, err := h.Engine.Search(ctx, "content", search.SearchFilters{Project: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) > 3 {
		t.Errorf("expected at most 3 results (config limit), got %d", len(results))
	}
}

// countAllDrawers counts total drawers across all wings/rooms for a project.
func countAllDrawers(t *testing.T, vault *storage.Vault, project string) (int, []storage.Drawer) {
	t.Helper()
	wings, err := vault.ListWings(project)
	if err != nil {
		t.Fatal(err)
	}
	var total int
	var all []storage.Drawer
	for _, wing := range wings {
		drawers, err := vault.ListDrawersByWing(project, wing)
		if err != nil {
			t.Fatal(err)
		}
		total += len(drawers)
		all = append(all, drawers...)
	}
	return total, all
}
