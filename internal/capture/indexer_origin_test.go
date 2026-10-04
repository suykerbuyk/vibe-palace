// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The extractor marks every entity line and triple it writes as extracted
// (ADR-014 decision 5), so the migration's classifier never has to guess.
func TestIndexTranscriptWritesExtractedOrigin(t *testing.T) {
	v := testVault(t)
	idx := NewIndexer(v, nil, nil, storage.Config{})
	transcript := "We modified internal/capture/indexer.go and visited https://example.com for docs. " +
		"Reviewing internal/capture/indexer.go once more; see also https://example.com."
	if _, err := idx.IndexTranscript(context.Background(), "session-01", "test-proj", transcript); err != nil {
		t.Fatalf("IndexTranscript: %v", err)
	}
	entities, _, err := v.ListEntities("test-proj")
	if err != nil || len(entities) == 0 {
		t.Fatalf("ListEntities = %d entities, err %v; want some", len(entities), err)
	}
	for _, e := range entities {
		if e.Origin != storage.OriginExtracted {
			t.Errorf("entity %s has origin %q, want extracted", e.ID, e.Origin)
		}
	}
	dir, err := v.KGTriplesDir("test-proj")
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) == 0 {
		t.Fatal("the extractor wrote no triples")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var tr storage.Triple
		if err := json.Unmarshal(b, &tr); err != nil {
			t.Fatal(err)
		}
		if tr.Origin != storage.OriginExtracted {
			t.Errorf("triple %s has origin %q, want extracted", filepath.Base(f), tr.Origin)
		}
	}
}
