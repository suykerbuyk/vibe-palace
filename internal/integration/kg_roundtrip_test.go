// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"testing"
)

// TestIntegrationKGEntityRoundTrip proves that entities extracted during
// transcript indexing are written to the KG and queryable via the storage layer.
func TestIntegrationKGEntityRoundTrip(t *testing.T) {
	h := newHarness(t, false) // mock embedder — KG doesn't need real embeddings

	// Each entity is mentioned twice to clear the default
	// kg.DefaultMinMentions (=2) frequency filter.
	transcript := `We refactored internal/storage/vault.go to improve error handling.
Re-reading internal/storage/vault.go confirmed the fix.
The changes were reviewed at https://github.com/suykerbuyk/vibe-palace/pull/42
and again at https://github.com/suykerbuyk/vibe-palace/pull/42 after CI.
We also updated internal/search/engine.go with the new API.
Follow-up edits to internal/search/engine.go landed shortly after.`

	sessionID := "session-kg-01"
	_, err := h.Indexer.IndexTranscript(context.Background(), sessionID, "proj", transcript)
	if err != nil {
		t.Fatalf("IndexTranscript: %v", err)
	}

	// Verify entities were extracted.
	entities, err := h.Vault.ListEntities("proj")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(entities) == 0 {
		t.Fatal("expected entities from transcript")
	}

	// Check for specific file entities.
	foundVault := false
	foundEngine := false
	foundURL := false
	for _, e := range entities {
		switch {
		case e.Type == "file" && e.Name == "internal/storage/vault.go":
			foundVault = true
		case e.Type == "file" && e.Name == "internal/search/engine.go":
			foundEngine = true
		case e.Type == "url":
			foundURL = true
		}
	}
	if !foundVault {
		t.Error("expected file entity for internal/storage/vault.go")
	}
	if !foundEngine {
		t.Error("expected file entity for internal/search/engine.go")
	}
	if !foundURL {
		t.Error("expected URL entity")
	}

	// Verify triples: file/URL entities should have "mentioned_in" triples
	// whose confidence mirrors the originating extractor. Under the
	// unified kg.ExtractAll pass, file/URL regex matches are treated as
	// deterministic (confidence 1.0), replacing the previous hard-coded
	// 0.8 that the pre-refactor entity extractor emitted.
	for _, e := range entities {
		if e.Type != "file" && e.Type != "url" {
			continue // Phase 7 entities have varying confidence
		}
		triples, err := h.Vault.QueryEntity("proj", e.Name, "", "out")
		if err != nil {
			continue
		}
		for _, tr := range triples {
			if tr.Predicate == "mentioned_in" && tr.Object == sessionID {
				if tr.Confidence != 1.0 {
					t.Errorf("triple confidence for %s = %f, want 1.0", e.Name, tr.Confidence)
				}
				if tr.SourceSession != sessionID {
					t.Errorf("triple source_session = %q, want %q", tr.SourceSession, sessionID)
				}
			}
		}
	}
}

// TestIntegrationKGEntityDeduplication proves that re-indexing the same
// transcript does not create duplicate entities.
func TestIntegrationKGEntityDeduplication(t *testing.T) {
	h := newHarness(t, false)

	transcript := "We modified internal/capture/chunker.go and visited https://example.com for docs."
	sessionID := "session-dedup-01"

	// Index twice.
	for i := 0; i < 2; i++ {
		_, _ = h.Indexer.IndexTranscript(context.Background(), sessionID, "proj", transcript)
	}

	entities, err := h.Vault.ListEntities("proj")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}

	// Count entities by name — each should appear exactly once.
	counts := make(map[string]int)
	for _, e := range entities {
		counts[e.Name]++
	}
	for name, count := range counts {
		if count > 1 {
			t.Errorf("entity %q appeared %d times (expected 1)", name, count)
		}
	}
}
