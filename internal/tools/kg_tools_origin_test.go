// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func readTrackedTriple(t *testing.T, vault *storage.Vault, project, s, p, o string) storage.Triple {
	t.Helper()
	path, err := vault.KGTriplePath(project, s, p, o)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tr storage.Triple
	if err := json.Unmarshal(b, &tr); err != nil {
		t.Fatal(err)
	}
	return tr
}

// vp_kg_add over a tracked EXTRACTED triple rewrites it as authored: the
// caller's confidence and valid_from win, source_session is kept and
// extracted_at dropped. At 0eff86f the handler failed with "already exists".
func TestKGAddOverTrackedExtractedTriple(t *testing.T) {
	vault := newTestVault(t)
	if err := vault.AddTriple("test", storage.Triple{Subject: "Alice", Predicate: "works_on", Object: "atlas",
		Confidence: 0.6, ValidFrom: "2026-01-01", SourceSession: "s1", ExtractedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(kgAddParams{Project: "test", Subject: "Alice", Predicate: "works_on", Object: "atlas",
		Confidence: 0.9, ValidFrom: "2026-02-02"})
	if _, err := KGAddTool(vault).Handler(context.Background(), params); err != nil {
		t.Fatalf("vp_kg_add over a tracked extracted triple: %v", err)
	}
	got := readTrackedTriple(t, vault, "test", "Alice", "works_on", "atlas")
	if got.Origin != storage.OriginAuthored || got.ExtractedAt != "" || got.SourceSession != "s1" ||
		got.Confidence != 0.9 || got.ValidFrom != "2026-02-02" {
		t.Errorf("after vp_kg_add: %+v, want authored, no extracted_at, source_session s1, 0.9, valid_from 2026-02-02", got)
	}
}

// vp_kg_invalidate on a tracked triple writes an authored overlay without
// extracted_at. At 0eff86f the handler kept extracted_at.
func TestKGInvalidateTrackedTripleDropsExtractedAt(t *testing.T) {
	vault := newTestVault(t)
	if err := vault.AddTriple("test", storage.Triple{Subject: "Alice", Predicate: "works_on", Object: "atlas",
		SourceSession: "s1", ExtractedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(kgInvalidateParams{Project: "test", Subject: "Alice", Predicate: "works_on", Object: "atlas", Ended: "2026-03-01"})
	if _, err := KGInvalidateTool(vault).Handler(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	got := readTrackedTriple(t, vault, "test", "Alice", "works_on", "atlas")
	if got.Origin != storage.OriginAuthored || got.ValidTo != "2026-03-01" || got.SourceSession != "s1" || got.ExtractedAt != "" {
		t.Errorf("after vp_kg_invalidate: %+v, want authored, valid_to 2026-03-01, source_session s1, no extracted_at", got)
	}
}

// vp_kg_add's entity lines are written as authored.
func TestKGAddWritesAuthoredEntityLines(t *testing.T) {
	vault := newTestVault(t)
	params, _ := json.Marshal(kgAddParams{Project: "test", Subject: "Alice", Predicate: "works_on", Object: "atlas"})
	if _, err := KGAddTool(vault).Handler(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	entities, _, err := vault.ListEntities("test")
	if err != nil || len(entities) != 2 {
		t.Fatalf("ListEntities = %+v, err %v; want the two entity lines", entities, err)
	}
	for _, e := range entities {
		if e.Origin != storage.OriginAuthored {
			t.Errorf("entity %s has origin %q, want authored", e.ID, e.Origin)
		}
	}
}
