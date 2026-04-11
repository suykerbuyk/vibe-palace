// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestIntegrationWeightedRoomScoring proves that the weighted keyword scoring
// system correctly classifies session transcripts end-to-end through the
// capture pipeline. It verifies:
//   - Ambiguous content (lone low-weight keywords) falls to "general"
//   - Domain-specific content classifies to the correct room
//   - Dominant topic wins when multiple rooms compete
func TestIntegrationWeightedRoomScoring(t *testing.T) {
	h := newHarness(t, false) // mock embedder — scoring doesn't need real vectors
	h.registerAllTools(t)

	tests := []struct {
		name       string
		transcript string
		wantRooms  map[string]bool // rooms that MUST appear
		denyRooms  map[string]bool // rooms that MUST NOT appear (from ambiguous content)
	}{
		{
			name: "kubernetes devops content",
			transcript: `## Human

We need to set up the kubernetes cluster with terraform.
Deploy the docker containers to the staging environment.

## Assistant

I'll configure the terraform modules for the kubernetes deployment.
The docker images should be built in the CI/CD pipeline first.`,
			wantRooms: map[string]bool{"devops": true},
		},
		{
			name: "ambiguous lone test keyword",
			transcript: `## Human

Just test it and see what happens.

## Assistant

Sure, let me test that quickly.`,
			// "test" alone (0.3) is below minRoomScore (0.6), should fall to general
			wantRooms: map[string]bool{"general": true},
			denyRooms: map[string]bool{"testing": true},
		},
		{
			name: "strong testing signal",
			transcript: `## Human

We need to write a test spec with full assertion coverage.
Add mock objects for the external service dependencies.

## Assistant

I'll create the test spec with comprehensive assertions and mock the
HTTP client. Coverage should reach 90% with these fixtures.`,
			wantRooms: map[string]bool{"testing": true},
		},
		{
			name: "security with mixed signals",
			transcript: `## Human

There's a vulnerability in the auth token validation.
Check the credential storage and fix the CVE.

## Assistant

The CVE affects the access token refresh flow. I'll patch the
credential validation and add permission checks.`,
			wantRooms: map[string]bool{"security": true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h2 := newHarness(t, false)
			h2.registerAllTools(t)

			result := h2.callTool(t, "vp_capture_session", map[string]any{
				"project":    "score-test",
				"summary":    "Testing room scoring: " + tt.name,
				"tag":        "implementation",
				"transcript": tt.transcript,
			})

			var captureResult struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(result), &captureResult); err != nil {
				t.Fatalf("parse capture result: %v", err)
			}
			if captureResult.Status != "ok" {
				t.Fatalf("capture status = %q, want ok", captureResult.Status)
			}

			// Collect rooms that drawers were filed into.
			gotRooms := make(map[string]bool)
			wings, _ := h2.Vault.ListWings("score-test")
			for _, wing := range wings {
				rooms, _ := h2.Vault.ListRooms("score-test", wing)
				for _, room := range rooms {
					drawers, _ := h2.Vault.ListDrawers("score-test", wing, room)
					if len(drawers) > 0 {
						gotRooms[room] = true
					}
				}
			}

			for room := range tt.wantRooms {
				if !gotRooms[room] {
					t.Errorf("expected room %q in results, got rooms: %v", room, keys(gotRooms))
				}
			}
			for room := range tt.denyRooms {
				if gotRooms[room] {
					t.Errorf("room %q should NOT appear (ambiguous content), got rooms: %v", room, keys(gotRooms))
				}
			}
		})
	}
}

// TestIntegrationScoringOverrides proves that [palace.scoring] config overrides
// flow through the full capture pipeline: config → NewIndexer → RoomClassifier →
// drawer classification.
func TestIntegrationScoringOverrides(t *testing.T) {
	// Add a new "ml" room via scoring overrides and lower the threshold.
	h := newHarness(t, false, func(cfg *storage.Config) {
		cfg.PalaceScoringOverrides = map[string]storage.ScoringRoomOverride{
			"ml": {
				High:   []string{"neural network", "transformer"},
				Medium: []string{"training"},
				Low:    []string{"epoch"},
			},
		}
		cfg.PalaceMinScore = 0.3
	})
	h.registerAllTools(t)

	tests := []struct {
		name       string
		transcript string
		wantRooms  map[string]bool
	}{
		{
			name: "new ml room via override",
			transcript: `## Human

Train the neural network on the dataset.
Set transformer attention heads to 8.

## Assistant

I'll configure the transformer architecture and start training.
The neural network should converge within 50 epochs.`,
			wantRooms: map[string]bool{"ml": true},
		},
		{
			name: "lowered threshold classifies lone low keyword",
			transcript: `## Human

Just test the output.

## Assistant

Testing the output now.`,
			// With default threshold 0.6, lone "test" (0.3) → general.
			// With threshold 0.3, "test" classifies as testing.
			wantRooms: map[string]bool{"testing": true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h2 := newHarness(t, false, func(cfg *storage.Config) {
				cfg.PalaceScoringOverrides = map[string]storage.ScoringRoomOverride{
					"ml": {
						High:   []string{"neural network", "transformer"},
						Medium: []string{"training"},
						Low:    []string{"epoch"},
					},
				}
				cfg.PalaceMinScore = 0.3
			})
			h2.registerAllTools(t)

			result := h2.callTool(t, "vp_capture_session", map[string]any{
				"project":    "override-test",
				"summary":    "Testing scoring overrides: " + tt.name,
				"tag":        "implementation",
				"transcript": tt.transcript,
			})

			var captureResult struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(result), &captureResult); err != nil {
				t.Fatalf("parse capture result: %v", err)
			}
			if captureResult.Status != "ok" {
				t.Fatalf("capture status = %q, want ok", captureResult.Status)
			}

			gotRooms := make(map[string]bool)
			wings, _ := h2.Vault.ListWings("override-test")
			for _, wing := range wings {
				rooms, _ := h2.Vault.ListRooms("override-test", wing)
				for _, room := range rooms {
					drawers, _ := h2.Vault.ListDrawers("override-test", wing, room)
					if len(drawers) > 0 {
						gotRooms[room] = true
					}
				}
			}

			for room := range tt.wantRooms {
				if !gotRooms[room] {
					t.Errorf("expected room %q in results, got rooms: %v", room, keys(gotRooms))
				}
			}
		})
	}
}

// TestIntegrationDrawerIDStableAcrossRooms proves that drawer IDs are
// independent of room classification — the same content in different rooms
// produces the same drawer ID (Task 12.0a).
func TestIntegrationDrawerIDStableAcrossRooms(t *testing.T) {
	h := newHarness(t, false)

	// Add the same content to two different rooms.
	d1 := h.addDrawer(t, "proj", "wing-a", "testing", "shared content here", "facts", "2026-01-01")
	d2 := h.addDrawer(t, "proj", "wing-a", "debugging", "shared content here", "facts", "2026-01-01")

	if d1.ID != d2.ID {
		t.Errorf("drawer IDs should be identical across rooms: %q vs %q", d1.ID, d2.ID)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
