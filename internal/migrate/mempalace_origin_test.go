// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// A mempalace import marks its entity lines and triples as extracted: they
// are derived records, never authored ones (ADR-014 decision 5), whatever
// their valid_to.
func TestImportMemPalaceWritesExtractedOrigin(t *testing.T) {
	_, vault, engine, emb, exportPath := setupTest(t)
	writeExportJSON(t, exportPath, testFixture())
	if _, err := ImportMemPalace(context.Background(), vault, engine, emb, mustLoadExport(t, exportPath), ImportOptions{}); err != nil {
		t.Fatalf("ImportMemPalace: %v", err)
	}
	entities, _, err := vault.ListEntities("mempalace")
	if err != nil || len(entities) == 0 {
		t.Fatalf("ListEntities = %d, err %v", len(entities), err)
	}
	for _, e := range entities {
		if e.Origin != storage.OriginExtracted {
			t.Errorf("entity %s has origin %q, want extracted", e.ID, e.Origin)
		}
	}
	dir, err := vault.KGTriplesDir("mempalace")
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) == 0 {
		t.Fatal("the import wrote no triples")
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
		if tr.Origin != storage.OriginExtracted || storage.ClassifyTriple(tr) != storage.OriginExtracted {
			t.Errorf("triple %s: origin %q, classified %q; want extracted", filepath.Base(f), tr.Origin, storage.ClassifyTriple(tr))
		}
	}
}
