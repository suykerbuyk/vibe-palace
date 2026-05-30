// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testVault(t *testing.T) *storage.Vault {
	t.Helper()
	dir := t.TempDir()
	v := storage.NewVault(dir)
	// Ensure palace structure exists.
	if err := storage.EnsureDir(dir + "/palace/test-proj/drawers/test-proj/general"); err != nil {
		t.Fatal(err)
	}
	return v
}

func testEngine(t *testing.T, v *storage.Vault) (*search.Engine, embedder.Embedder) {
	t.Helper()
	emb := embedder.NewMock(384)
	cfg := storage.Config{SearchDefaultLimit: 10}
	eng := search.NewEngine(emb, v, cfg)
	t.Cleanup(func() { eng.Close() })
	return eng, emb
}

func TestIndexTranscriptBasic(t *testing.T) {
	v := testVault(t)
	eng, emb := testEngine(t, v)
	idx := NewIndexer(v, eng, emb, storage.Config{})

	transcript := "We decided to use brute-force vector search. " +
		"The test coverage is now at 96%. " +
		"We discovered that HNSW has a recall bug."

	_, err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", transcript)
	if err != nil {
		t.Fatalf("IndexTranscript: %v", err)
	}

	// Verify drawers were written.
	wing := palace.DetectWing("test-proj", "")
	rooms := []string{"general", "testing", "debugging", "decisions", "discoveries"}
	var totalDrawers int
	for _, room := range rooms {
		drawers, _ := v.ListDrawers("test-proj", wing, room)
		totalDrawers += len(drawers)
	}
	if totalDrawers == 0 {
		t.Error("expected at least one drawer to be written")
	}
}

func TestIndexTranscriptEmpty(t *testing.T) {
	v := testVault(t)
	eng, emb := testEngine(t, v)
	idx := NewIndexer(v, eng, emb, storage.Config{})

	_, err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", "")
	if err != nil {
		t.Fatalf("expected no error for empty transcript, got: %v", err)
	}

	_, err = idx.IndexTranscript(context.Background(), "session-01", "test-proj", "   \n\n  ")
	if err != nil {
		t.Fatalf("expected no error for whitespace transcript, got: %v", err)
	}
}

func TestIndexTranscriptNoEngine(t *testing.T) {
	v := testVault(t)
	idx := NewIndexer(v, nil, nil, storage.Config{})

	transcript := "Some content to chunk and store without embeddings."

	_, err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", transcript)
	if err != nil {
		t.Fatalf("IndexTranscript without engine: %v", err)
	}

	// Drawers should still be stored.
	wing := palace.DetectWing("test-proj", "")
	drawers, _ := v.ListDrawers("test-proj", wing, "general")
	if len(drawers) == 0 {
		t.Error("expected drawers to be written even without engine")
	}
}

func TestIndexTranscriptDuplicateIdempotent(t *testing.T) {
	v := testVault(t)
	eng, emb := testEngine(t, v)
	idx := NewIndexer(v, eng, emb, storage.Config{})

	transcript := "This is a unique chunk of text for dedup testing."

	// First index.
	if _, err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", transcript); err != nil {
		t.Fatalf("first IndexTranscript: %v", err)
	}

	// Second index with same content should not error (duplicates are skipped).
	if _, err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", transcript); err != nil {
		t.Fatalf("second IndexTranscript should be idempotent: %v", err)
	}
}

// TestIndexTranscriptStats locks in the IndexStats contract: a fresh transcript
// produces non-zero drawer/entity/triple counts; a re-index of identical
// content produces zero counts (every artifact dedup-skips). The third counter
// (Triples) depends on AddTriple's O_CREATE|O_EXCL dedup error introduced
// alongside this contract.
func TestIndexTranscriptStats(t *testing.T) {
	v := testVault(t)
	eng, emb := testEngine(t, v)
	idx := NewIndexer(v, eng, emb, storage.Config{})

	// Each entity must clear kg.DefaultMinMentions (=2) to be extracted, so
	// reference internal/capture/indexer.go and https://example.com twice.
	transcript := "We modified internal/capture/indexer.go and visited https://example.com for docs. " +
		"Reviewing internal/capture/indexer.go once more; see also https://example.com."

	first, err := idx.IndexTranscript(context.Background(), "session-stats", "test-proj", transcript)
	if err != nil {
		t.Fatalf("first IndexTranscript: %v", err)
	}
	if first.Drawers == 0 {
		t.Error("first call: stats.Drawers should be > 0 for a fresh transcript")
	}
	if first.Entities == 0 {
		t.Error("first call: stats.Entities should be > 0 (file path + URL clear MinMentions)")
	}
	if first.Triples == 0 {
		t.Error("first call: stats.Triples should be > 0 (mentioned_in triples per entity)")
	}

	// Re-index the same transcript — every artifact should dedup-skip.
	second, err := idx.IndexTranscript(context.Background(), "session-stats", "test-proj", transcript)
	if err != nil {
		t.Fatalf("second IndexTranscript: %v", err)
	}
	if second.Drawers != 0 {
		t.Errorf("second call: stats.Drawers = %d, want 0 (every drawer should dedup-skip)", second.Drawers)
	}
	if second.Entities != 0 {
		t.Errorf("second call: stats.Entities = %d, want 0 (every entity should dedup-skip)", second.Entities)
	}
	if second.Triples != 0 {
		t.Errorf("second call: stats.Triples = %d, want 0 (every triple should dedup-skip)", second.Triples)
	}
}

func TestIndexTranscriptEntityExtraction(t *testing.T) {
	v := testVault(t)
	eng, emb := testEngine(t, v)
	idx := NewIndexer(v, eng, emb, storage.Config{})

	// Each entity is mentioned twice so it clears the default
	// kg.DefaultMinMentions (=2) frequency filter introduced by the
	// unified kg.ExtractAll pass.
	transcript := "We modified internal/capture/indexer.go and visited https://example.com for docs. " +
		"Reviewing internal/capture/indexer.go once more; see also https://example.com."

	_, err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", transcript)
	if err != nil {
		t.Fatalf("IndexTranscript: %v", err)
	}

	// Check that entities were extracted to KG.
	entities, err := v.ListEntities("test-proj")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(entities) == 0 {
		t.Error("expected at least one entity from transcript")
	}
}

func TestIndexTranscriptPerformance(t *testing.T) {
	v := testVault(t)
	idx := NewIndexer(v, nil, nil, storage.Config{}) // no embedder = fast

	// Generate ~100KB transcript.
	var b strings.Builder
	for b.Len() < 100_000 {
		b.WriteString("This is a test sentence for performance benchmarking. ")
		if b.Len()%500 < 55 {
			b.WriteString("\n\n")
		}
	}

	_, err := idx.IndexTranscript(context.Background(), "perf-session", "test-proj", b.String())
	if err != nil {
		t.Fatalf("IndexTranscript 100KB: %v", err)
	}
}

func TestChunkConfigFromConfig(t *testing.T) {
	v := testVault(t)

	// Custom chunk settings via config.
	cfg := storage.Config{ChunkMaxChars: 200, ChunkOverlap: 30}
	idx := NewIndexer(v, nil, nil, cfg)

	got := idx.chunkConfig()
	if got.MaxChars != 200 {
		t.Errorf("MaxChars = %d, want 200", got.MaxChars)
	}
	if got.Overlap != 30 {
		t.Errorf("Overlap = %d, want 30", got.Overlap)
	}

	// Zero values fall back to defaults.
	idx2 := NewIndexer(v, nil, nil, storage.Config{})
	got2 := idx2.chunkConfig()
	if got2.MaxChars != 800 {
		t.Errorf("default MaxChars = %d, want 800", got2.MaxChars)
	}
	if got2.Overlap != 100 {
		t.Errorf("default Overlap = %d, want 100", got2.Overlap)
	}
}

func TestNewIndexerWithScoringOverrides(t *testing.T) {
	v := testVault(t)
	cfg := storage.Config{
		PalaceScoringOverrides: map[string]storage.ScoringRoomOverride{
			"ml": {
				High:   []string{"neural network"},
				Medium: []string{"training"},
				Low:    []string{"epoch"},
			},
		},
		PalaceMinScore: 0.3,
	}
	idx := NewIndexer(v, nil, nil, cfg)

	// Verify the classifier was constructed with overrides.
	// Neural network content should classify to "ml" via the override.
	transcript := "Train the neural network on the transformer architecture dataset."
	_, err := idx.IndexTranscript(context.Background(), "ml-session", "test-proj", transcript)
	if err != nil {
		t.Fatalf("IndexTranscript: %v", err)
	}

	wing := palace.DetectWing("test-proj", "")
	drawers, _ := v.ListDrawers("test-proj", wing, "ml")
	if len(drawers) == 0 {
		t.Error("expected drawers in 'ml' room via scoring override, got none")
	}
}

func TestNewIndexerWithLoweredThreshold(t *testing.T) {
	v := testVault(t)
	cfg := storage.Config{
		PalaceMinScore: 0.3, // Lower threshold: lone "test" (0.3) should now classify
	}
	idx := NewIndexer(v, nil, nil, cfg)

	transcript := "Just test it quickly."
	_, err := idx.IndexTranscript(context.Background(), "threshold-session", "test-proj", transcript)
	if err != nil {
		t.Fatalf("IndexTranscript: %v", err)
	}

	wing := palace.DetectWing("test-proj", "")
	drawers, _ := v.ListDrawers("test-proj", wing, "testing")
	if len(drawers) == 0 {
		t.Error("expected drawers in 'testing' room with lowered threshold, got none")
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"file-internal/capture/indexer.go", "file-internal-capture-indexer-go"},
		{"url-https://example.com", "url-https-example-com"},
		{"UPPER Case", "upper-case"},
		{"---multi---dashes---", "multi-dashes"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := slug.Slugify(tt.input)
			if got != tt.want {
				t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestIndexTranscriptKGDedup verifies that the unified kg.ExtractAll pass
// writes exactly one KG entity per (type, normalized-name) even when the
// same token appears in multiple places in the transcript and could be
// surfaced by more than one extractor pass. It also verifies the
// frequency filter admits doubletons and drops singletons.
func TestIndexTranscriptKGDedup(t *testing.T) {
	v := testVault(t)
	eng, emb := testEngine(t, v)
	idx := NewIndexer(v, eng, emb, storage.Config{})

	// internal/foo/bar.go is mentioned twice (should pass the >=2 filter,
	// and should dedup to exactly one entity row). onetime/file.go is
	// mentioned once (should be filtered out).
	transcript := "First: edit internal/foo/bar.go to fix the bug. " +
		"Then: retest internal/foo/bar.go end-to-end. " +
		"We briefly glanced at onetime/file.go but did not change it."

	if _, err := idx.IndexTranscript(context.Background(), "session-dedup", "test-proj", transcript); err != nil {
		t.Fatalf("IndexTranscript: %v", err)
	}

	entities, err := v.ListEntities("test-proj")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}

	countBar := 0
	foundOnetime := false
	for _, e := range entities {
		if e.Name == "internal/foo/bar.go" {
			countBar++
		}
		if e.Name == "onetime/file.go" {
			foundOnetime = true
		}
	}
	if countBar != 1 {
		t.Errorf("internal/foo/bar.go KG entity count = %d, want exactly 1 (dedup)", countBar)
	}
	if foundOnetime {
		t.Error("onetime/file.go should have been dropped by frequency filter (mention_count=1 < 2)")
	}
}

// TestIndexDrawersParityAcrossPaths verifies the collapsed engine API
// produces identical searchable results whether:
//   (A) a single drawer is indexed via the convenience Engine.IndexDrawer
//       (engine embeds the content), or
//   (B) the capture batch path calls Engine.IndexDrawers with pre-computed
//       vectors from EmbedBatch.
// Both paths must yield the same DrawerID in the search result for the
// same content.
func TestIndexDrawersParityAcrossPaths(t *testing.T) {
	content := "distinctive parity marker for collapsed engine index API"
	wing := palace.DetectWing("test-proj", "")
	room := "general"

	ctx := context.Background()
	hall := palace.DetectHall(content)

	// ---------- Path A: single-drawer IndexDrawer ----------
	vaultA := testVault(t)
	engA, _ := testEngine(t, vaultA)

	dA := storage.Drawer{
		Hall:       hall,
		Content:    content,
		SourceType: "session",
		SourceRef:  "session-A",
		ChunkIndex: 0,
		FiledAt:    "2026-04-14T10:00:00Z",
		AddedBy:    "test",
	}
	dA.ID = storage.DrawerID(wing, content)
	if err := vaultA.AppendDrawer("test-proj", wing, room, dA); err != nil {
		t.Fatalf("vaultA.AppendDrawer: %v", err)
	}
	if err := engA.IndexDrawer(ctx, "test-proj", wing, room, dA); err != nil {
		t.Fatalf("engA.IndexDrawer: %v", err)
	}

	resA, err := engA.Search(ctx, "parity marker", search.SearchFilters{Project: "test-proj"})
	if err != nil {
		t.Fatalf("engA.Search: %v", err)
	}
	if len(resA) == 0 {
		t.Fatalf("path A: no results")
	}

	// ---------- Path B: capture batch IndexDrawers via Indexer ----------
	vaultB := testVault(t)
	engB, embB := testEngine(t, vaultB)
	idx := NewIndexer(vaultB, engB, embB, storage.Config{
		ChunkMaxChars: 4000, // ensure single chunk
		ChunkOverlap:  0,
	})
	if _, err := idx.IndexTranscript(ctx, "session-B", "test-proj", content); err != nil {
		t.Fatalf("IndexTranscript: %v", err)
	}

	resB, err := engB.Search(ctx, "parity marker", search.SearchFilters{Project: "test-proj"})
	if err != nil {
		t.Fatalf("engB.Search: %v", err)
	}
	if len(resB) == 0 {
		t.Fatalf("path B: no results")
	}

	// Both should surface the same content and same derived DrawerID
	// (since DrawerID is content-addressed via storage.DrawerID).
	if resA[0].DrawerID != resB[0].DrawerID {
		t.Errorf("DrawerID mismatch: pathA=%q pathB=%q", resA[0].DrawerID, resB[0].DrawerID)
	}
	if resA[0].Content != resB[0].Content {
		t.Errorf("Content mismatch:\n  A=%q\n  B=%q", resA[0].Content, resB[0].Content)
	}
}
