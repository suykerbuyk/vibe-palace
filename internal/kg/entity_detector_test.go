// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package kg

import (
	"testing"
)

func TestDetectPersonVerb(t *testing.T) {
	text := "John said the deploy looks good. Sarah mentioned the new API."
	entities := DetectEntities(text)

	found := entityByName(entities, "John")
	if found == nil {
		t.Fatal("expected to detect John")
	}
	if found.Type != EntityPerson {
		t.Errorf("John type = %q, want person", found.Type)
	}
	if found.Confidence < 0.6 {
		t.Errorf("John confidence = %.1f, want >= 0.6", found.Confidence)
	}

	found = entityByName(entities, "Sarah")
	if found == nil {
		t.Fatal("expected to detect Sarah")
	}
	if found.Type != EntityPerson {
		t.Errorf("Sarah type = %q, want person", found.Type)
	}
}

func TestDetectPersonMultiWord(t *testing.T) {
	text := "John Smith works on the backend. Alice Johnson reviewed the PR."
	entities := DetectEntities(text)

	found := entityByName(entities, "John Smith")
	if found == nil {
		t.Fatal("expected to detect John Smith")
	}
	if found.Type != EntityPerson {
		t.Errorf("type = %q, want person", found.Type)
	}
}

func TestDetectTool(t *testing.T) {
	text := "We deployed using Docker and monitored with Grafana."
	entities := DetectEntities(text)

	found := entityByName(entities, "Docker")
	if found == nil {
		t.Fatal("expected to detect Docker")
	}
	if found.Type != EntityTool {
		t.Errorf("Docker type = %q, want tool", found.Type)
	}
	if found.Confidence < 0.7 {
		t.Errorf("Docker confidence = %.1f, want >= 0.7", found.Confidence)
	}

	found = entityByName(entities, "Grafana")
	if found == nil {
		t.Fatal("expected to detect Grafana")
	}
}

func TestDetectProject(t *testing.T) {
	text := "We need to deploy vibe-palace to production. The PR for data-pipeline is ready."
	entities := DetectEntities(text)

	found := entityByName(entities, "vibe-palace")
	if found == nil {
		t.Fatal("expected to detect vibe-palace")
	}
	if found.Type != EntityProject {
		t.Errorf("type = %q, want project", found.Type)
	}
}

func TestDetectProjectVersion(t *testing.T) {
	text := "We updated MyLib v2.0 with the new API."
	entities := DetectEntities(text)

	found := entityByName(entities, "MyLib")
	if found == nil {
		t.Fatal("expected to detect MyLib")
	}
	if found.Type != EntityProject {
		t.Errorf("type = %q, want project", found.Type)
	}
}

func TestDetectConcept(t *testing.T) {
	text := "We adopted the Palace architecture for the memory system."
	entities := DetectEntities(text)

	found := entityByName(entities, "Palace")
	if found == nil {
		t.Fatal("expected to detect Palace concept")
	}
	if found.Type != EntityConcept {
		t.Errorf("type = %q, want concept", found.Type)
	}
}

func TestFalsePositiveRejection(t *testing.T) {
	text := "The system works well. However, this is good. Perhaps we should try."
	entities := DetectEntities(text)

	for _, e := range entities {
		if commonFalsePositives[e.Name] {
			t.Errorf("false positive not rejected: %q", e.Name)
		}
	}
}

func TestEmptyInput(t *testing.T) {
	if entities := DetectEntities(""); entities != nil {
		t.Errorf("expected nil for empty input, got %d entities", len(entities))
	}
	if entities := DetectEntities("   "); entities != nil {
		t.Errorf("expected nil for whitespace input, got %d entities", len(entities))
	}
}

func TestConfidenceStacking(t *testing.T) {
	// "Alice" appears with verb pattern AND as capitalized name.
	text := "Alice said the tests pass. Alice mentioned the deploy."
	entities := DetectEntities(text)

	found := entityByName(entities, "Alice")
	if found == nil {
		t.Fatal("expected to detect Alice")
	}
	if found.Confidence < 0.7 {
		t.Errorf("confidence = %.1f, want >= 0.7 (stacked)", found.Confidence)
	}
}

func TestToolNotMisclassifiedAsPerson(t *testing.T) {
	text := "We use Docker for containerization."
	entities := DetectEntities(text)

	found := entityByName(entities, "Docker")
	if found == nil {
		t.Fatal("expected to detect Docker")
	}
	if found.Type != EntityTool {
		t.Errorf("Docker classified as %q, want tool", found.Type)
	}
}

func entityByName(entities []DetectedEntity, name string) *DetectedEntity {
	for i, e := range entities {
		if e.Name == name {
			return &entities[i]
		}
	}
	return nil
}
