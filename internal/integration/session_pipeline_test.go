// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/search"
)

// TestIntegrationSessionCaptureToSearch proves the full workflow:
// vp_capture_session → chunk transcript → embed → store → index → search finds content.
func TestIntegrationSessionCaptureToSearch(t *testing.T) {
	h := newHarness(t, true) // real ONNX
	h.registerAllTools(t)

	transcript := `## Human

We need to implement a database migration system for PostgreSQL.
The system should support both up and down migrations with version tracking.

## Assistant

I recommend a simple approach using sequential SQL files:

1. Each migration gets a timestamp-based filename like 001_create_users.up.sql
2. A migrations table tracks which migrations have been applied
3. The up command applies pending migrations in order
4. The down command reverts the last N migrations

For the implementation, we should use database/sql with the pgx driver
since it has good PostgreSQL support and connection pooling built in.

## Human

What about handling migration failures?

## Assistant

We should wrap each migration in a transaction. If any statement fails,
the entire migration is rolled back. The migrations table is only updated
after a successful commit. This ensures atomicity — either the migration
fully applies or it has no effect.`

	result := h.callTool(t, "vp_capture_session", map[string]any{
		"project":    "test-proj",
		"summary":    "Discussed database migration system design.",
		"tag":        "planning",
		"transcript": transcript,
		"decisions":  []string{"Use sequential SQL files with timestamp names"},
	})

	// Parse the capture result.
	var captureResult struct {
		Status    string `json:"status"`
		SessionID string `json:"session_id"`
		Iteration int    `json:"iteration"`
		Project   string `json:"project"`
	}
	if err := json.Unmarshal([]byte(result), &captureResult); err != nil {
		t.Fatalf("parse capture result: %v (raw: %s)", err, result)
	}
	if captureResult.Status != "ok" {
		t.Errorf("status = %q, want %q", captureResult.Status, "ok")
	}
	if captureResult.SessionID == "" {
		t.Error("session_id is empty")
	}
	if captureResult.Iteration < 1 {
		t.Errorf("iteration = %d, want >= 1", captureResult.Iteration)
	}

	// Now search for content from the transcript.
	results, err := h.Engine.Search(context.Background(), "PostgreSQL database migration system",
		search.SearchFilters{Project: "test-proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results for migration query after session capture")
	}

	// Top result should be about database migrations.
	top := strings.ToLower(results[0].Content)
	if !strings.Contains(top, "migration") && !strings.Contains(top, "database") {
		t.Errorf("top result should mention migration/database, got: %s",
			truncate(results[0].Content, 100))
	}

	// All results should reference the session.
	for i, r := range results {
		if r.SourceType != "session" {
			t.Errorf("result[%d] source_type = %q, want %q", i, r.SourceType, "session")
		}
		if r.SourceRef != captureResult.SessionID {
			t.Errorf("result[%d] source_ref = %q, want %q", i, r.SourceRef, captureResult.SessionID)
		}
	}

	// Search for unrelated content — should score lower.
	unrelated, err := h.Engine.Search(context.Background(), "quantum physics particle accelerator",
		search.SearchFilters{Project: "test-proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) > 0 && unrelated[0].Score >= results[0].Score {
		t.Errorf("unrelated query score (%f) should be lower than relevant (%f)",
			unrelated[0].Score, results[0].Score)
	}
}

// TestIntegrationSessionCaptureWithoutTranscript proves that capturing a
// session without a transcript writes the session file but creates no drawers.
func TestIntegrationSessionCaptureWithoutTranscript(t *testing.T) {
	h := newHarness(t, false) // mock embedder — no ONNX needed
	h.registerAllTools(t)

	result := h.callTool(t, "vp_capture_session", map[string]any{
		"project": "test-proj",
		"summary": "Quick debugging session, no transcript captured.",
		"tag":     "debugging",
	})

	var captureResult struct {
		Status    string `json:"status"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(result), &captureResult); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if captureResult.Status != "ok" {
		t.Errorf("status = %q, want ok", captureResult.Status)
	}

	// Verify no drawers were created.
	wings, _ := h.Vault.ListWings("test-proj")
	for _, wing := range wings {
		drawers, _ := h.Vault.ListDrawersByWing("test-proj", wing)
		if len(drawers) > 0 {
			t.Errorf("expected no drawers without transcript, found %d in wing %s", len(drawers), wing)
		}
	}
}

// TestIntegrationSessionIterationAcrossSessions proves that multiple
// session captures for the same project+date auto-increment iterations.
func TestIntegrationSessionIterationAcrossSessions(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	var iterations []int
	for i := 0; i < 3; i++ {
		result := h.callTool(t, "vp_capture_session", map[string]any{
			"project": "test-proj",
			"summary": "Session number " + string(rune('1'+i)),
		})

		var r struct {
			Iteration int `json:"iteration"`
		}
		json.Unmarshal([]byte(result), &r)
		iterations = append(iterations, r.Iteration)
	}

	// Verify auto-increment.
	for i := 1; i < len(iterations); i++ {
		if iterations[i] != iterations[i-1]+1 {
			t.Errorf("iteration %d → %d (expected +1)", iterations[i-1], iterations[i])
		}
	}

	// Verify all sessions are listable.
	sessions, err := h.Vault.ListSessions("test-proj", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(sessions))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
