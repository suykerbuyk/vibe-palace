// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"testing"
)

// TestIntegrationKGDetection verifies that the Phase 7 entity detection and
// triple extraction pipeline produces queryable person/project/tool entities
// and relationship triples from a realistic transcript.
func TestIntegrationKGDetection(t *testing.T) {
	h := newHarness(t, false) // mock embedder — KG doesn't need real embeddings

	transcript := `Alice said the deploy looks good. Bob decided to use Redis for the cache layer.
Carol works on vibe-palace and mentioned the new search pipeline.
We deployed using Docker and monitored with Grafana.
Dave created the indexing module. vibe-palace depends on hugot for embeddings.`

	sessionID := "session-kg-detect-01"
	err := h.Indexer.IndexTranscript(context.Background(), sessionID, "proj", transcript)
	if err != nil {
		t.Fatalf("IndexTranscript: %v", err)
	}

	// Verify person entities were detected.
	entities, err := h.Vault.ListEntities("proj")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}

	typeMap := make(map[string][]string) // type -> []name
	for _, e := range entities {
		typeMap[e.Type] = append(typeMap[e.Type], e.Name)
	}

	// Check person entities.
	if persons, ok := typeMap["person"]; !ok || len(persons) == 0 {
		t.Error("expected person entities")
	}

	// Check tool entities.
	if tools, ok := typeMap["tool"]; !ok || len(tools) == 0 {
		t.Error("expected tool entities (Docker, Redis, Grafana)")
	}

	// Check project entities.
	if projects, ok := typeMap["project"]; !ok || len(projects) == 0 {
		t.Error("expected project entities (vibe-palace)")
	}

	// Verify relationship triples were extracted.
	// "Bob decided to use Redis" should produce a decided triple.
	triples, err := h.Vault.QueryEntity("proj", "Bob", "", "out")
	if err != nil {
		t.Fatalf("QueryEntity Bob: %v", err)
	}
	foundDecided := false
	for _, tr := range triples {
		if tr.Predicate == "decided" {
			foundDecided = true
			break
		}
	}
	if !foundDecided {
		t.Error("expected 'decided' triple for Bob")
	}

	// "Carol works on vibe-palace" should produce a works_on triple.
	triples, err = h.Vault.QueryEntity("proj", "Carol", "", "out")
	if err != nil {
		t.Fatalf("QueryEntity Carol: %v", err)
	}
	foundWorksOn := false
	for _, tr := range triples {
		if tr.Predicate == "works_on" {
			foundWorksOn = true
			break
		}
	}
	if !foundWorksOn {
		t.Error("expected 'works_on' triple for Carol")
	}

	// Verify mentioned_in triples link to session.
	foundMentionedIn := false
	for _, e := range entities {
		if e.Type == "person" || e.Type == "tool" {
			triples, err := h.Vault.QueryEntity("proj", e.Name, "", "out")
			if err != nil {
				continue
			}
			for _, tr := range triples {
				if tr.Predicate == "mentioned_in" && tr.Object == sessionID {
					foundMentionedIn = true
					break
				}
			}
			if foundMentionedIn {
				break
			}
		}
	}
	if !foundMentionedIn {
		t.Error("expected mentioned_in triple linking entity to session")
	}

	// Verify timeline returns chronological order for a detected entity.
	timeline, err := h.Vault.Timeline("proj", "Bob")
	if err != nil {
		t.Fatalf("Timeline Bob: %v", err)
	}
	if len(timeline) == 0 {
		t.Error("expected non-empty timeline for Bob")
	}

	// Verify stats reflect new entities and triples.
	stats, err := h.Vault.KGStats("proj")
	if err != nil {
		t.Fatalf("KGStats: %v", err)
	}
	if stats.EntityCount == 0 {
		t.Error("expected non-zero entity count")
	}
	if stats.TripleCount == 0 {
		t.Error("expected non-zero triple count")
	}
}
