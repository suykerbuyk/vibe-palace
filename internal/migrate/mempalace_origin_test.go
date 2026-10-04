// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// A mempalace import's KG records are extracted, never authored (ADR-014
// decision 5), whatever their valid_to: every entity and triple payload in
// the local store says origin "extracted".
func TestImportMemPalaceWritesExtractedOrigin(t *testing.T) {
	_, vault, engine, emb, exportPath := setupTest(t)
	writeExportJSON(t, exportPath, testFixture())
	if _, err := ImportMemPalace(context.Background(), vault, "alpha", engine, emb, mustLoadExport(t, exportPath), ImportOptions{}); err != nil {
		t.Fatalf("ImportMemPalace: %v", err)
	}
	st, err := indexstore.ReadStore(vault, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	recs := st.KG(true)
	if len(recs) != 2 {
		t.Fatalf("%d local KG records, want 2 (one entity, one triple)", len(recs))
	}
	for _, r := range recs {
		var p struct {
			Origin string `json:"origin"`
		}
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Origin != storage.OriginExtracted {
			t.Errorf("record %s has origin %q, want extracted", r.ID, p.Origin)
		}
	}
}
