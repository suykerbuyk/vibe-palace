// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package kg

import (
	"slices"
	"testing"
)

func TestExtractWorksOn(t *testing.T) {
	entities := []DetectedEntity{
		{Name: "Alice", Type: EntityPerson, Confidence: 0.8},
	}
	text := "Alice works on the backend."
	triples := ExtractTriples(text, entities, "2026-04-09")

	found := tripleByPredicate(triples, "works_on")
	if found == nil {
		t.Fatal("expected works_on triple")
	}
	if found.Subject != "Alice" {
		t.Errorf("subject = %q, want Alice", found.Subject)
	}
	if found.Confidence < 0.6 {
		t.Errorf("confidence = %.1f, want >= 0.6", found.Confidence)
	}
}

func TestExtractDecided(t *testing.T) {
	entities := []DetectedEntity{
		{Name: "Bob", Type: EntityPerson, Confidence: 0.8},
		{Name: "Redis", Type: EntityTool, Confidence: 0.8},
	}
	text := "Bob decided to use Redis for caching."
	triples := ExtractTriples(text, entities, "2026-04-09")

	found := tripleByPredicate(triples, "decided")
	if found == nil {
		t.Fatal("expected decided triple")
	}
	if found.Subject != "Bob" {
		t.Errorf("subject = %q, want Bob", found.Subject)
	}
	if !containsFlag(found.Flags, "DECISION") {
		t.Error("expected DECISION flag")
	}
}

func TestExtractMemberOf(t *testing.T) {
	entities := []DetectedEntity{
		{Name: "Carol", Type: EntityPerson, Confidence: 0.8},
	}
	text := "Carol joined the platform team."
	triples := ExtractTriples(text, entities, "2026-04-09")

	found := tripleByPredicate(triples, "member_of")
	if found == nil {
		t.Fatal("expected member_of triple")
	}
	if found.ValidFrom != "2026-04-09" {
		t.Errorf("valid_from = %q, want 2026-04-09", found.ValidFrom)
	}
}

func TestExtractDependsOn(t *testing.T) {
	entities := []DetectedEntity{
		{Name: "vibe-palace", Type: EntityProject, Confidence: 0.6},
	}
	text := "vibe-palace depends on hugot for embeddings."
	triples := ExtractTriples(text, entities, "2026-04-09")

	found := tripleByPredicate(triples, "depends_on")
	if found == nil {
		t.Fatal("expected depends_on triple")
	}
}

func TestExtractCreated(t *testing.T) {
	entities := []DetectedEntity{
		{Name: "Dave", Type: EntityPerson, Confidence: 0.8},
	}
	text := "Dave created the indexing pipeline."
	triples := ExtractTriples(text, entities, "2026-04-09")

	found := tripleByPredicate(triples, "created")
	if found == nil {
		t.Fatal("expected created triple")
	}
}

func TestExtractUses(t *testing.T) {
	entities := []DetectedEntity{
		{Name: "vibe-palace", Type: EntityProject, Confidence: 0.6},
		{Name: "Docker", Type: EntityTool, Confidence: 0.8},
	}
	text := "vibe-palace uses Docker for deployment."
	triples := ExtractTriples(text, entities, "2026-04-09")

	found := tripleByPredicate(triples, "uses")
	if found == nil {
		t.Fatal("expected uses triple")
	}
}

func TestExtractFiltersNonEntities(t *testing.T) {
	// No known entities — triples should be filtered out.
	entities := []DetectedEntity{}
	text := "Alice works on the backend."
	triples := ExtractTriples(text, entities, "2026-04-09")

	if len(triples) != 0 {
		t.Errorf("expected 0 triples (no entities), got %d", len(triples))
	}
}

func TestExtractDedup(t *testing.T) {
	entities := []DetectedEntity{
		{Name: "Alice", Type: EntityPerson, Confidence: 0.8},
	}
	text := "Alice works on the backend. Alice works on the frontend."
	triples := ExtractTriples(text, entities, "2026-04-09")

	count := 0
	for _, tr := range triples {
		if tr.Predicate == "works_on" && tr.Subject == "Alice" {
			count++
		}
	}
	// Could be 2 (different objects) or 1 if same normalized.
	// The point is there shouldn't be exact duplicates.
	for i := range triples {
		for j := i + 1; j < len(triples); j++ {
			if triples[i].Subject == triples[j].Subject &&
				triples[i].Predicate == triples[j].Predicate &&
				triples[i].Object == triples[j].Object {
				t.Errorf("duplicate triple: %v", triples[i])
			}
		}
	}
}

func TestExtractEmpty(t *testing.T) {
	if triples := ExtractTriples("", nil, ""); triples != nil {
		t.Errorf("expected nil for empty input, got %d", len(triples))
	}
}

func TestExtractMultiSentence(t *testing.T) {
	entities := []DetectedEntity{
		{Name: "Alice", Type: EntityPerson, Confidence: 0.8},
		{Name: "Bob", Type: EntityPerson, Confidence: 0.8},
		{Name: "Redis", Type: EntityTool, Confidence: 0.8},
	}
	text := "Alice works on the API. Bob decided to use Redis. Alice created the search module."
	triples := ExtractTriples(text, entities, "2026-04-09")

	if len(triples) < 2 {
		t.Errorf("expected at least 2 triples from multi-sentence, got %d", len(triples))
	}
}

func tripleByPredicate(triples []ExtractedTriple, predicate string) *ExtractedTriple {
	for i, t := range triples {
		if t.Predicate == predicate {
			return &triples[i]
		}
	}
	return nil
}

func containsFlag(flags []string, flag string) bool {
	return slices.Contains(flags, flag)
}
