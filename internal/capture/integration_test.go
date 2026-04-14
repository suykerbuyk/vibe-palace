// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// newIntegrationEmbedder creates a real ONNX embedder, skipping in short mode.
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

// TestIntegrationCaptureAndSearch exercises the full end-to-end pipeline:
// capture a session with a transcript → chunk → embed with real ONNX →
// store drawers → index vectors → search finds the content.
func TestIntegrationCaptureAndSearch(t *testing.T) {
	emb := newIntegrationEmbedder(t)
	vault := storage.NewVault(t.TempDir())
	cfg := storage.Config{
		SearchDefaultLimit: 10,
		BoostWing:          0.12,
		BoostHall:          0.24,
		BoostRoom:          0.34,
	}
	eng := search.NewEngine(emb, vault, cfg)
	defer eng.Close()

	idx := NewIndexer(vault, eng, emb, cfg)
	ctx := context.Background()

	transcript := `## Human

How should we implement the chunking engine for our knowledge management system?
I want it to handle markdown transcripts and plain text.

## Assistant

I'd recommend a sliding-window approach with sentence-boundary awareness.
The key design decisions are:

1. Use 800-character chunks with 100-character overlap
2. Detect the transcript format (plain text, JSON-RPC, or markdown)
3. Never split mid-sentence — scan backward for punctuation boundaries
4. Keep exchange pairs together when possible

For the implementation, we should create an internal/capture package with
a Chunk function that accepts configurable MaxChars and Overlap parameters.

## Human

What about classifying the chunks into rooms and halls?

## Assistant

We can use keyword-based heuristics for room detection. For example,
chunks mentioning "test", "assert", or "coverage" map to the testing room.
Chunks about "deploy", "CI", or "Docker" go to the devops room.

For hall classification, we detect memory types: decisions (keywords like
"decided", "chose"), discoveries ("found that", "realized"), and so on.
The default hall is "facts" for anything that doesn't match a specific pattern.`

	// Run the full pipeline.
	err := idx.IndexTranscript(ctx, "session-integration-01", "test-proj", transcript)
	if err != nil {
		t.Fatalf("IndexTranscript: %v", err)
	}

	// Search for chunking-related content — should find it.
	results, err := eng.Search(ctx, "sliding window chunking with sentence boundaries", search.SearchFilters{
		Project: "test-proj",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results for chunking query, got none")
	}

	// The top result should contain content about chunking.
	topContent := strings.ToLower(results[0].Content)
	if !strings.Contains(topContent, "chunk") && !strings.Contains(topContent, "sliding") {
		t.Errorf("top result should be about chunking, got: %s", truncate(results[0].Content, 100))
	}

	// All results should reference the session.
	for i, r := range results {
		if r.SourceType != "session" {
			t.Errorf("result[%d] source_type = %q, want %q", i, r.SourceType, "session")
		}
		if r.SourceRef != "session-integration-01" {
			t.Errorf("result[%d] source_ref = %q, want %q", i, r.SourceRef, "session-integration-01")
		}
	}

	// Search for keyword-heuristics content — should find the room/hall discussion.
	results2, err := eng.Search(ctx, "keyword heuristics for room classification", search.SearchFilters{
		Project: "test-proj",
	})
	if err != nil {
		t.Fatalf("Search for heuristics: %v", err)
	}
	if len(results2) == 0 {
		t.Fatal("expected results for heuristics query, got none")
	}

	// Search for something NOT in the transcript — should return results
	// but with lower scores.
	results3, err := eng.Search(ctx, "quantum physics particle accelerator", search.SearchFilters{
		Project: "test-proj",
	})
	if err != nil {
		t.Fatalf("Search for unrelated: %v", err)
	}
	if len(results3) > 0 && len(results) > 0 {
		if results3[0].Score >= results[0].Score {
			t.Errorf("unrelated query score (%f) should be lower than relevant query score (%f)",
				results3[0].Score, results[0].Score)
		}
	}
}

// TestIntegrationCaptureRoomClassification verifies that the chunker and
// detector correctly classify chunks into different rooms based on content.
func TestIntegrationCaptureRoomClassification(t *testing.T) {
	emb := newIntegrationEmbedder(t)
	vault := storage.NewVault(t.TempDir())
	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := search.NewEngine(emb, vault, cfg)
	defer eng.Close()

	idx := NewIndexer(vault, eng, emb, cfg)
	ctx := context.Background()

	// Use separate transcripts so each topic gets its own chunk and room.
	testingTranscript := `We wrote comprehensive unit tests with good coverage for the
chunking engine. The test suite includes assertion checks for sentence
boundaries and edge cases. Each test function validates a specific behavior
of the chunker, detector, and entity extractor. We verified that all 422
tests pass with coverage above 80 percent across all packages.`

	devopsTranscript := `We deployed the new Docker container with the updated CI pipeline
configuration for GitHub Actions. The Kubernetes cluster was updated with
new pod scheduling rules and the deployment rollout completed successfully.
The CI pipeline now runs linting and building in parallel stages.`

	err := idx.IndexTranscript(ctx, "session-rooms-01", "test-proj", testingTranscript)
	if err != nil {
		t.Fatalf("IndexTranscript testing: %v", err)
	}
	err = idx.IndexTranscript(ctx, "session-rooms-02", "test-proj", devopsTranscript)
	if err != nil {
		t.Fatalf("IndexTranscript devops: %v", err)
	}

	// Search with room filter for testing content.
	testResults, err := eng.Search(ctx, "unit test coverage assertions", search.SearchFilters{
		Project: "test-proj",
		Room:    "testing",
	})
	if err != nil {
		t.Fatalf("Search testing room: %v", err)
	}
	if len(testResults) == 0 {
		t.Error("expected results in testing room for test-related query")
	}

	// Search with room filter for devops content.
	devopsResults, err := eng.Search(ctx, "Docker CI pipeline deployment", search.SearchFilters{
		Project: "test-proj",
		Room:    "devops",
	})
	if err != nil {
		t.Fatalf("Search devops room: %v", err)
	}
	if len(devopsResults) == 0 {
		t.Error("expected results in devops room for deployment-related query")
	}
}

// TestIntegrationCaptureEntityExtraction verifies that entities extracted
// from a transcript are written to the knowledge graph and can be queried.
func TestIntegrationCaptureEntityExtraction(t *testing.T) {
	emb := newIntegrationEmbedder(t)
	vault := storage.NewVault(t.TempDir())
	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := search.NewEngine(emb, vault, cfg)
	defer eng.Close()

	idx := NewIndexer(vault, eng, emb, cfg)
	ctx := context.Background()

	transcript := `We modified internal/capture/chunker.go to add sentence boundary
detection. The change in internal/capture/chunker.go follows the patterns from
https://github.com/suykerbuyk/vibe-palace in the search package, and
https://github.com/suykerbuyk/vibe-palace again for the chunker design.
We also updated internal/storage/config.go with the new chunker settings,
and internal/storage/config.go was cross-checked against its defaults.`

	err := idx.IndexTranscript(ctx, "session-entities-01", "test-proj", transcript)
	if err != nil {
		t.Fatalf("IndexTranscript: %v", err)
	}

	// Verify entities were extracted.
	entities, err := vault.ListEntities("test-proj")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(entities) == 0 {
		t.Fatal("expected entities extracted from transcript")
	}

	// Should have file entities.
	foundFile := false
	foundURL := false
	for _, e := range entities {
		if e.Type == "file" {
			foundFile = true
		}
		if e.Type == "url" {
			foundURL = true
		}
	}
	if !foundFile {
		t.Error("expected at least one file entity")
	}
	if !foundURL {
		t.Error("expected at least one URL entity")
	}

	// Verify triples were written (mentioned_in relationships).
	for _, e := range entities {
		triples, err := vault.QueryEntity("test-proj", e.Name, "", "out")
		if err != nil {
			continue
		}
		for _, tr := range triples {
			if tr.Predicate == "mentioned_in" && tr.Object == "session-entities-01" {
				return // found at least one — pass
			}
		}
	}
	t.Error("expected at least one 'mentioned_in' triple linking an entity to the session")
}

// TestIntegrationCaptureLargeTranscript verifies that a 100KB transcript
// is chunked, embedded, and indexed within a reasonable time using the
// real ONNX embedder.
func TestIntegrationCaptureLargeTranscript(t *testing.T) {
	emb := newIntegrationEmbedder(t)
	vault := storage.NewVault(t.TempDir())
	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := search.NewEngine(emb, vault, cfg)
	defer eng.Close()

	idx := NewIndexer(vault, eng, emb, cfg)
	ctx := context.Background()

	// Build a ~100KB transcript with varied content.
	var b strings.Builder
	topics := []string{
		"We discussed the architecture of the search engine and decided to use brute-force cosine similarity.",
		"The test suite now has 422 tests passing with coverage above 80 percent across all packages.",
		"We deployed the new version to production using Docker containers and GitHub Actions CI pipeline.",
		"The chunking algorithm uses a sliding window with 800 character chunks and 100 character overlap.",
		"We discovered that the HNSW library has a critical recall bug in the replenish function.",
		"The configuration system uses a three-tier TOML precedence: embedded defaults, vault, and project.",
	}
	for b.Len() < 100_000 {
		for _, topic := range topics {
			b.WriteString(topic)
			b.WriteString(" ")
		}
		b.WriteString("\n\n")
	}

	err := idx.IndexTranscript(ctx, "session-large-01", "test-proj", b.String())
	if err != nil {
		t.Fatalf("IndexTranscript 100KB: %v", err)
	}

	// Verify searchability.
	results, err := eng.Search(ctx, "HNSW recall bug", search.SearchFilters{Project: "test-proj"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected search results from 100KB transcript")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
