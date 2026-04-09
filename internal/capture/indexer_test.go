// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
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

	err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", transcript)
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

	err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", "")
	if err != nil {
		t.Fatalf("expected no error for empty transcript, got: %v", err)
	}

	err = idx.IndexTranscript(context.Background(), "session-01", "test-proj", "   \n\n  ")
	if err != nil {
		t.Fatalf("expected no error for whitespace transcript, got: %v", err)
	}
}

func TestIndexTranscriptNoEngine(t *testing.T) {
	v := testVault(t)
	idx := NewIndexer(v, nil, nil, storage.Config{})

	transcript := "Some content to chunk and store without embeddings."

	err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", transcript)
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
	if err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", transcript); err != nil {
		t.Fatalf("first IndexTranscript: %v", err)
	}

	// Second index with same content should not error (duplicates are skipped).
	if err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", transcript); err != nil {
		t.Fatalf("second IndexTranscript should be idempotent: %v", err)
	}
}

func TestIndexTranscriptEntityExtraction(t *testing.T) {
	v := testVault(t)
	eng, emb := testEngine(t, v)
	idx := NewIndexer(v, eng, emb, storage.Config{})

	transcript := "We modified internal/capture/indexer.go and visited https://example.com for docs."

	err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", transcript)
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

	err := idx.IndexTranscript(context.Background(), "perf-session", "test-proj", b.String())
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
			got := slugify(tt.input)
			if got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
