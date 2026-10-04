// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
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

	// A decoy: another project's session about the same subject. A
	// project-scoped search must never return any of it. Session ids are per
	// project and can coincide, so the decoy is told apart by its project and
	// by a marker in every one of its texts.
	const decoyMarker = "ZEBRA_DECOY_MARKER"
	var decoyResult string
	h.Seed(t, testinfra.WithCapturedSession(map[string]any{
		"project":    "decoy-proj",
		"summary":    "Also discussed PostgreSQL database migrations " + decoyMarker + ".",
		"tag":        "planning",
		"transcript": "## Human\n\nHow should PostgreSQL database migrations be versioned? " + decoyMarker + "\n\n## Assistant\n\nUse a migrations table and run each migration in a transaction. " + decoyMarker,
		"decisions":  []string{"Version database migrations in a migrations table " + decoyMarker},
	}, &decoyResult))
	if !strings.Contains(decoyResult, `"status":"ok"`) && !strings.Contains(decoyResult, `"status": "ok"`) {
		t.Fatalf("decoy capture: %s", decoyResult)
	}

	var result string
	h.Seed(t, testinfra.WithCapturedSession(map[string]any{
		"project":    "test-proj",
		"summary":    "Discussed database migration system design.",
		"tag":        "planning",
		"transcript": transcript,
		"decisions":  []string{"Use sequential SQL files with timestamp names"},
	}, &result))

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

	// Every result comes from the captured session: its transcript chunks
	// (source_ref = the session id), its session note ("sessions/<id>.md") or
	// its decision ("session/<id>#decision/..."). The first search builds the
	// whole project, notes and decisions included: capture's IndexDrawers on a
	// cold engine no longer makes the project look built with the transcript
	// alone (task search-index-completeness-and-build-serialization, defect 1).
	sawTranscript, sawNote := false, false
	for i, r := range results {
		if !strings.Contains(r.SourceRef, captureResult.SessionID) {
			t.Errorf("result[%d] source_ref = %q, want one of session %s", i, r.SourceRef, captureResult.SessionID)
		}
		if r.Project != "test-proj" || strings.Contains(r.Content, decoyMarker) {
			t.Errorf("result[%d] comes from the decoy project's session: %+v", i, r)
		}
		if r.SourceType == "session" && r.SourceRef == captureResult.SessionID {
			sawTranscript = true
		}
		if r.SourceType == "session-note" && r.SourceRef == "sessions/"+captureResult.SessionID+".md" {
			sawNote = true
		}
	}
	if !sawTranscript {
		t.Errorf("no transcript chunk of session %s among the results: %+v", captureResult.SessionID, results)
	}
	// The session note is indexed by the first search's build. Before the
	// cold-insert fix, capture's IndexDrawers made the project look built with
	// the transcript alone, and the note never appeared.
	if !sawNote {
		t.Errorf("the session note row of %s is not among the results: %+v", captureResult.SessionID, results)
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

	var result string
	h.Seed(t, testinfra.WithCapturedSession(map[string]any{
		"project": "test-proj",
		"summary": "Quick debugging session, no transcript captured.",
		"tag":     "debugging",
	}, &result))

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
	if total, _ := countAllDrawers(t, h.Vault, "test-proj"); total > 0 {
		t.Errorf("expected no drawers without transcript, found %d", total)
	}
}

// TestIntegrationSessionIterationAcrossSessions proves that multiple
// session captures for the same project+date auto-increment iterations.
func TestIntegrationSessionIterationAcrossSessions(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	var iterations []int
	for i := range 3 {
		var result string
		h.Seed(t, testinfra.WithCapturedSession(map[string]any{
			"project": "test-proj",
			"summary": "Session number " + string(rune('1'+i)),
		}, &result))

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
	sessions, _, err := h.Vault.ListSessions("test-proj", "", "", 0)
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
