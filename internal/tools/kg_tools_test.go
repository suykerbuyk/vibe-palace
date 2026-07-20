// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// bornCurrentTestVault returns an ephemeral vault stamped at the current data
// format so KG reads pass the armed format gate — the same "stamp on creation"
// semantics a freshly-created real vault gets. Shared by the tools test helpers.
func bornCurrentTestVault(t *testing.T, root string) *storage.Vault {
	t.Helper()
	if err := surface.WriteFormat(root, surface.RequiredDataFormat); err != nil {
		t.Fatalf("stamp born-current vault: %v", err)
	}
	return storage.NewVault(root)
}

func newTestVault(t *testing.T) *storage.Vault {
	t.Helper()
	return bornCurrentTestVault(t, t.TempDir())
}

func TestKGAddAndQuery(t *testing.T) {
	vault := newTestVault(t)
	ctx := context.Background()

	// Add a fact.
	addTool := KGAddTool(vault)
	params, _ := json.Marshal(kgAddParams{
		Project:   "test",
		Subject:   "Alice",
		Predicate: "works_on",
		Object:    "backend",
	})
	result, err := addTool.Handler(ctx, params)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	status := result.(map[string]string)
	if status["status"] != "added" {
		t.Errorf("status = %q, want added", status["status"])
	}

	// Query the fact.
	queryTool := KGQueryTool(vault)
	qParams, _ := json.Marshal(kgQueryParams{
		Project: "test",
		Entity:  "Alice",
	})
	qResult, err := queryTool.Handler(ctx, qParams)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	triples := qResult.([]storage.Triple)
	if len(triples) == 0 {
		t.Fatal("expected at least 1 triple")
	}
	if triples[0].Predicate != "works_on" {
		t.Errorf("predicate = %q, want works_on", triples[0].Predicate)
	}
}

func TestKGAddDuplicateEntity(t *testing.T) {
	vault := newTestVault(t)
	ctx := context.Background()
	addTool := KGAddTool(vault)

	params, _ := json.Marshal(kgAddParams{
		Project:   "test",
		Subject:   "Alice",
		Predicate: "works_on",
		Object:    "backend",
	})

	// First add succeeds.
	if _, err := addTool.Handler(ctx, params); err != nil {
		t.Fatalf("first add: %v", err)
	}

	// Second add with same entities should also succeed (catches "already exists").
	params2, _ := json.Marshal(kgAddParams{
		Project:   "test",
		Subject:   "Alice",
		Predicate: "uses",
		Object:    "backend",
	})
	if _, err := addTool.Handler(ctx, params2); err != nil {
		t.Fatalf("second add with duplicate entities: %v", err)
	}
}

func TestKGInvalidate(t *testing.T) {
	vault := newTestVault(t)
	ctx := context.Background()

	// Add a fact first.
	addTool := KGAddTool(vault)
	params, _ := json.Marshal(kgAddParams{
		Project:   "test",
		Subject:   "Alice",
		Predicate: "member_of",
		Object:    "team-alpha",
		ValidFrom: "2026-01-01",
	})
	if _, err := addTool.Handler(ctx, params); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Invalidate the fact.
	invTool := KGInvalidateTool(vault)
	invParams, _ := json.Marshal(kgInvalidateParams{
		Project:   "test",
		Subject:   "Alice",
		Predicate: "member_of",
		Object:    "team-alpha",
		Ended:     "2026-04-01",
	})
	result, err := invTool.Handler(ctx, invParams)
	if err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	status := result.(map[string]string)
	if status["status"] != "invalidated" {
		t.Errorf("status = %q, want invalidated", status["status"])
	}

	// Query with as_of after ended date should return empty.
	queryTool := KGQueryTool(vault)
	qParams, _ := json.Marshal(kgQueryParams{
		Project: "test",
		Entity:  "Alice",
		AsOf:    "2026-05-01",
	})
	qResult, err := queryTool.Handler(ctx, qParams)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	triples := qResult.([]storage.Triple)
	if len(triples) != 0 {
		t.Errorf("expected 0 triples after invalidation, got %d", len(triples))
	}
}

func TestKGTimeline(t *testing.T) {
	vault := newTestVault(t)
	ctx := context.Background()
	addTool := KGAddTool(vault)

	// Add two facts with different dates.
	for _, fact := range []kgAddParams{
		{Project: "test", Subject: "Alice", Predicate: "joined", Object: "team-alpha", ValidFrom: "2026-01-01"},
		{Project: "test", Subject: "Alice", Predicate: "works_on", Object: "api", ValidFrom: "2026-02-01"},
	} {
		params, _ := json.Marshal(fact)
		if _, err := addTool.Handler(ctx, params); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	tlTool := KGTimelineTool(vault)
	tlParams, _ := json.Marshal(kgEntityParams{Project: "test", Entity: "Alice"})
	result, err := tlTool.Handler(ctx, tlParams)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}

	triples := result.([]storage.Triple)
	if len(triples) < 2 {
		t.Fatalf("expected >= 2 triples, got %d", len(triples))
	}
	// Should be sorted by ValidFrom.
	if triples[0].ValidFrom > triples[1].ValidFrom {
		t.Error("timeline not sorted by ValidFrom")
	}
}

func TestKGStats(t *testing.T) {
	vault := newTestVault(t)
	ctx := context.Background()

	addTool := KGAddTool(vault)
	params, _ := json.Marshal(kgAddParams{
		Project:   "test",
		Subject:   "Alice",
		Predicate: "works_on",
		Object:    "backend",
	})
	if _, err := addTool.Handler(ctx, params); err != nil {
		t.Fatalf("add: %v", err)
	}

	statsTool := KGStatsTool(vault)
	sParams, _ := json.Marshal(projectParams{Project: "test"})
	result, err := statsTool.Handler(ctx, sParams)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	stats := result.(kgStatsResult)
	if stats.EntityCount < 2 {
		t.Errorf("entity count = %d, want >= 2", stats.EntityCount)
	}
	if stats.TripleCount < 1 {
		t.Errorf("triple count = %d, want >= 1", stats.TripleCount)
	}
	if len(stats.TypeBreakdown) == 0 {
		t.Error("expected non-empty type breakdown")
	}
}

func TestKGQueryEmpty(t *testing.T) {
	vault := newTestVault(t)
	ctx := context.Background()

	queryTool := KGQueryTool(vault)
	params, _ := json.Marshal(kgQueryParams{Project: "test", Entity: "nobody"})
	result, err := queryTool.Handler(ctx, params)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	triples := result.([]storage.Triple)
	if len(triples) != 0 {
		t.Errorf("expected empty result, got %d", len(triples))
	}
}

func TestKGRequiredParams(t *testing.T) {
	vault := newTestVault(t)
	ctx := context.Background()

	// Missing project.
	addTool := KGAddTool(vault)
	params, _ := json.Marshal(kgAddParams{Subject: "Alice", Predicate: "uses", Object: "Docker"})
	if _, err := addTool.Handler(ctx, params); err == nil {
		t.Error("expected error for missing project")
	}

	// Missing entity on query.
	queryTool := KGQueryTool(vault)
	qParams, _ := json.Marshal(kgQueryParams{Project: "test"})
	if _, err := queryTool.Handler(ctx, qParams); err == nil {
		t.Error("expected error for missing entity")
	}
}
