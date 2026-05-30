// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"strings"
	"testing"
)

func TestAddAndGetEntity(t *testing.T) {
	v := testVault(t)
	e := Entity{
		ID:        "e1",
		Name:      "Kai",
		Type:      "person",
		CreatedAt: "2026-03-15T00:00:00Z",
	}

	if err := v.AddEntity("proj", e); err != nil {
		t.Fatalf("AddEntity: %v", err)
	}

	got, err := v.GetEntity("proj", "e1")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.Name != "Kai" {
		t.Errorf("Name = %q, want %q", got.Name, "Kai")
	}
	if got.Type != "person" {
		t.Errorf("Type = %q, want %q", got.Type, "person")
	}
}

func TestAddEntityDuplicate(t *testing.T) {
	v := testVault(t)
	e := Entity{ID: "e1", Name: "Kai", Type: "person", CreatedAt: "2026-01-01T00:00:00Z"}

	if err := v.AddEntity("proj", e); err != nil {
		t.Fatal(err)
	}
	err := v.AddEntity("proj", e)
	if err == nil {
		t.Error("duplicate entity should return error")
	}
}

func TestListEntities(t *testing.T) {
	v := testVault(t)
	for _, name := range []string{"Alpha", "Bravo", "Charlie"} {
		e := Entity{ID: name, Name: name, Type: "concept", CreatedAt: "2026-01-01T00:00:00Z"}
		if err := v.AddEntity("proj", e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := v.ListEntities("proj")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListEntities returned %d, want 3", len(got))
	}
}

func TestListEntitiesEmpty(t *testing.T) {
	v := testVault(t)
	got, err := v.ListEntities("proj")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty project, got %v", got)
	}
}

func TestGetEntityNotFound(t *testing.T) {
	v := testVault(t)
	_, err := v.GetEntity("proj", "nonexist")
	if err == nil {
		t.Error("GetEntity should return error for nonexistent entity")
	}
}

func TestAddAndGetTriple(t *testing.T) {
	v := testVault(t)
	tr := Triple{
		Subject:   "Kai",
		Predicate: "works on",
		Object:    "Orion",
		ValidFrom: "2026-01-01",
		ExtractedAt: "2026-03-15T00:00:00Z",
	}

	if err := v.AddTriple("proj", tr); err != nil {
		t.Fatalf("AddTriple: %v", err)
	}

	got, err := v.GetTriple("proj", "Kai", "works on", "Orion")
	if err != nil {
		t.Fatalf("GetTriple: %v", err)
	}
	if got.Subject != "Kai" {
		t.Errorf("Subject = %q, want %q", got.Subject, "Kai")
	}
	if got.ValidFrom != "2026-01-01" {
		t.Errorf("ValidFrom = %q, want %q", got.ValidFrom, "2026-01-01")
	}
}

func TestAddTripleDedup(t *testing.T) {
	v := testVault(t)
	first := Triple{
		Subject:     "Kai",
		Predicate:   "works on",
		Object:      "Orion",
		ValidFrom:   "2026-01-01",
		ExtractedAt: "2026-03-15T00:00:00Z",
		Confidence:  0.9,
	}
	if err := v.AddTriple("proj", first); err != nil {
		t.Fatalf("first AddTriple: %v", err)
	}

	second := first
	second.ExtractedAt = "2026-04-01T00:00:00Z"
	second.Confidence = 0.5
	err := v.AddTriple("proj", second)
	if err == nil {
		t.Fatal("second AddTriple with identical subject/predicate/object should error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error %q should contain \"already exists\" for uniform dedup signal", err)
	}

	got, err := v.GetTriple("proj", "Kai", "works on", "Orion")
	if err != nil {
		t.Fatalf("GetTriple after duplicate attempt: %v", err)
	}
	if got.ExtractedAt != first.ExtractedAt {
		t.Errorf("ExtractedAt = %q, want %q (file should not have been overwritten)",
			got.ExtractedAt, first.ExtractedAt)
	}
	if got.Confidence != first.Confidence {
		t.Errorf("Confidence = %v, want %v (file should not have been overwritten)",
			got.Confidence, first.Confidence)
	}
}

func TestQueryEntityOut(t *testing.T) {
	v := testVault(t)
	v.AddTriple("proj", Triple{Subject: "Kai", Predicate: "works on", Object: "Orion"})
	v.AddTriple("proj", Triple{Subject: "Kai", Predicate: "knows", Object: "Mika"})
	v.AddTriple("proj", Triple{Subject: "Mika", Predicate: "works on", Object: "Kai"})

	got, err := v.QueryEntity("proj", "Kai", "", "out")
	if err != nil {
		t.Fatalf("QueryEntity: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("QueryEntity out returned %d, want 2", len(got))
	}
}

func TestQueryEntityIn(t *testing.T) {
	v := testVault(t)
	v.AddTriple("proj", Triple{Subject: "Kai", Predicate: "works on", Object: "Orion"})
	v.AddTriple("proj", Triple{Subject: "Mika", Predicate: "knows", Object: "Orion"})
	v.AddTriple("proj", Triple{Subject: "Mika", Predicate: "works on", Object: "Kai"})

	got, err := v.QueryEntity("proj", "Orion", "", "in")
	if err != nil {
		t.Fatalf("QueryEntity: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("QueryEntity in returned %d, want 2", len(got))
	}
}

func TestQueryEntityBoth(t *testing.T) {
	v := testVault(t)
	v.AddTriple("proj", Triple{Subject: "Kai", Predicate: "works on", Object: "Orion"})
	v.AddTriple("proj", Triple{Subject: "Mika", Predicate: "knows", Object: "Kai"})

	got, err := v.QueryEntity("proj", "Kai", "", "both")
	if err != nil {
		t.Fatalf("QueryEntity: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("QueryEntity both returned %d, want 2", len(got))
	}
}

func TestQueryEntityTemporal(t *testing.T) {
	v := testVault(t)
	v.AddTriple("proj", Triple{
		Subject: "Kai", Predicate: "works on", Object: "Orion",
		ValidFrom: "2025-01-01", ValidTo: "2025-12-31",
	})
	v.AddTriple("proj", Triple{
		Subject: "Kai", Predicate: "works on", Object: "Vega",
		ValidFrom: "2026-01-01",
	})

	// Query at 2025-06-01: only Orion should be valid.
	got, err := v.QueryEntity("proj", "Kai", "2025-06-01", "out")
	if err != nil {
		t.Fatalf("QueryEntity: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("temporal query returned %d, want 1", len(got))
	}
	if got[0].Object != "Orion" {
		t.Errorf("Object = %q, want Orion", got[0].Object)
	}

	// Query at 2026-06-01: only Vega should be valid.
	got, err = v.QueryEntity("proj", "Kai", "2026-06-01", "out")
	if err != nil {
		t.Fatalf("QueryEntity: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("temporal query returned %d, want 1", len(got))
	}
	if got[0].Object != "Vega" {
		t.Errorf("Object = %q, want Vega", got[0].Object)
	}
}

func TestQueryEntityInvalidDirection(t *testing.T) {
	v := testVault(t)
	_, err := v.QueryEntity("proj", "Kai", "", "sideways")
	if err == nil {
		t.Error("QueryEntity with invalid direction should return error")
	}
}

func TestInvalidateTriple(t *testing.T) {
	v := testVault(t)
	tr := Triple{Subject: "Kai", Predicate: "works on", Object: "Orion", ValidFrom: "2025-01-01"}
	if err := v.AddTriple("proj", tr); err != nil {
		t.Fatal(err)
	}

	if err := v.InvalidateTriple("proj", "Kai", "works on", "Orion", "2025-12-31"); err != nil {
		t.Fatalf("InvalidateTriple: %v", err)
	}

	got, err := v.GetTriple("proj", "Kai", "works on", "Orion")
	if err != nil {
		t.Fatalf("GetTriple: %v", err)
	}
	if got.ValidTo != "2025-12-31" {
		t.Errorf("ValidTo = %q, want %q", got.ValidTo, "2025-12-31")
	}
}

func TestTimeline(t *testing.T) {
	v := testVault(t)
	v.AddTriple("proj", Triple{Subject: "Kai", Predicate: "works on", Object: "Vega", ValidFrom: "2026-01-01"})
	v.AddTriple("proj", Triple{Subject: "Kai", Predicate: "works on", Object: "Orion", ValidFrom: "2025-01-01"})
	v.AddTriple("proj", Triple{Subject: "Mika", Predicate: "knows", Object: "Kai", ValidFrom: "2024-06-01"})

	got, err := v.Timeline("proj", "Kai")
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Timeline returned %d, want 3", len(got))
	}
	// Should be sorted by ValidFrom ascending.
	if got[0].ValidFrom > got[1].ValidFrom || got[1].ValidFrom > got[2].ValidFrom {
		t.Errorf("Timeline not sorted: %v, %v, %v", got[0].ValidFrom, got[1].ValidFrom, got[2].ValidFrom)
	}
}

func TestKGStats(t *testing.T) {
	v := testVault(t)
	v.AddEntity("proj", Entity{ID: "e1", Name: "Kai", Type: "person", CreatedAt: "2026-01-01T00:00:00Z"})
	v.AddEntity("proj", Entity{ID: "e2", Name: "Orion", Type: "project", CreatedAt: "2026-01-01T00:00:00Z"})
	v.AddTriple("proj", Triple{Subject: "Kai", Predicate: "works on", Object: "Orion"})
	v.AddTriple("proj", Triple{Subject: "Kai", Predicate: "knows", Object: "Mika", ValidTo: "2025-12-31"})

	stats, err := v.KGStats("proj")
	if err != nil {
		t.Fatalf("KGStats: %v", err)
	}
	if stats.EntityCount != 2 {
		t.Errorf("EntityCount = %d, want 2", stats.EntityCount)
	}
	if stats.TripleCount != 2 {
		t.Errorf("TripleCount = %d, want 2", stats.TripleCount)
	}
	if stats.CurrentFacts != 1 {
		t.Errorf("CurrentFacts = %d, want 1", stats.CurrentFacts)
	}
	if stats.ExpiredFacts != 1 {
		t.Errorf("ExpiredFacts = %d, want 1", stats.ExpiredFacts)
	}
	if len(stats.PredicateTypes) != 2 {
		t.Errorf("PredicateTypes count = %d, want 2", len(stats.PredicateTypes))
	}
}

func TestKGStatsEmpty(t *testing.T) {
	v := testVault(t)
	stats, err := v.KGStats("proj")
	if err != nil {
		t.Fatalf("KGStats: %v", err)
	}
	if stats.EntityCount != 0 || stats.TripleCount != 0 {
		t.Errorf("expected zero counts, got entities=%d triples=%d", stats.EntityCount, stats.TripleCount)
	}
}

func TestEntityWithProperties(t *testing.T) {
	v := testVault(t)
	e := Entity{
		ID:         "e1",
		Name:       "Kai",
		Type:       "person",
		Properties: map[string]string{"role": "engineer", "team": "platform"},
		CreatedAt:  "2026-01-01T00:00:00Z",
	}

	if err := v.AddEntity("proj", e); err != nil {
		t.Fatal(err)
	}

	got, err := v.GetEntity("proj", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Properties["role"] != "engineer" {
		t.Errorf("Properties[role] = %q, want %q", got.Properties["role"], "engineer")
	}
}
