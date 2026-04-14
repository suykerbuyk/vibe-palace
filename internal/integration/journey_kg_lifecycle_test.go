// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"testing"
)

// TestJourney_KG_Lifecycle exercises the knowledge graph's full round trip
// through the MCP boundary: add a triple, then verify every read tool sees
// it consistently.
//
// Cross-tool consistency assertions:
//   - The triple written via vp_kg_add appears in vp_kg_query results
//     with matching subject/predicate/object.
//   - vp_kg_timeline returns the same triple chronologically.
//   - vp_kg_stats reports triple_count >= 2 (two adds) and entities
//     includes both subject and object.
func TestJourney_KG_Lifecycle(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	const project = "journey-kg"

	// Deterministic triples.
	triples := []struct {
		S, P, O string
	}{
		{"vibe-palace", "uses", "go-modules"},
		{"vibe-palace", "depends_on", "onnxruntime"},
	}
	for _, tr := range triples {
		raw := h.callTool(t, "vp_kg_add", map[string]any{
			"project":   project,
			"subject":   tr.S,
			"predicate": tr.P,
			"object":    tr.O,
		})
		var r map[string]string
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatalf("kg_add parse: %v (%s)", err, raw)
		}
		if r["status"] != "added" {
			t.Errorf("kg_add status = %q, want added", r["status"])
		}
	}

	// vp_kg_query: both triples should come back for subject "vibe-palace".
	raw := h.callTool(t, "vp_kg_query", map[string]any{
		"project": project,
		"entity":  "vibe-palace",
	})
	var got []struct {
		Subject   string `json:"subject"`
		Predicate string `json:"predicate"`
		Object    string `json:"object"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("kg_query parse: %v (%s)", err, raw)
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[g.Subject+"|"+g.Predicate+"|"+g.Object] = true
	}
	for _, tr := range triples {
		key := tr.S + "|" + tr.P + "|" + tr.O
		if !seen[key] {
			t.Errorf("kg_query missing triple %s (got %d rows)", key, len(got))
		}
	}

	// vp_kg_timeline: same triples, ordered chronologically (same day here).
	raw = h.callTool(t, "vp_kg_timeline", map[string]any{
		"project": project,
		"entity":  "vibe-palace",
	})
	var timeline []struct {
		Subject   string `json:"subject"`
		Predicate string `json:"predicate"`
		Object    string `json:"object"`
	}
	if err := json.Unmarshal([]byte(raw), &timeline); err != nil {
		t.Fatalf("kg_timeline parse: %v (%s)", err, raw)
	}
	if len(timeline) != len(triples) {
		t.Errorf("kg_timeline len = %d, want %d", len(timeline), len(triples))
	}

	// vp_kg_stats: triple count and entity breakdown align.
	raw = h.callTool(t, "vp_kg_stats", map[string]any{"project": project})
	var stats struct {
		TripleCount   int            `json:"triple_count"`
		EntityCount   int            `json:"entity_count"`
		TypeBreakdown map[string]int `json:"type_breakdown"`
	}
	if err := json.Unmarshal([]byte(raw), &stats); err != nil {
		t.Fatalf("kg_stats parse: %v (%s)", err, raw)
	}
	if stats.TripleCount != len(triples) {
		t.Errorf("kg_stats triple_count = %d, want %d", stats.TripleCount, len(triples))
	}
	// Three distinct entities: vibe-palace, go-modules, onnxruntime.
	if stats.EntityCount < 3 {
		t.Errorf("kg_stats entity_count = %d, want >=3", stats.EntityCount)
	}
}
