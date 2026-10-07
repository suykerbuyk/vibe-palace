// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/ingest"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

// TestIntegrationSessionCaptureToSearch proves the ADR-014 workflow: capture no
// longer indexes the transcript it is given (decision 7). On a hook-less host a
// capture with archive_transcript writes an inline ARCHIVE, the pending-archive
// ingester turns that archive into host-local chunks, and only then is the
// transcript searchable. A capture with NO archive leaves the project's chunk
// store absent.
func TestIntegrationSessionCaptureToSearch(t *testing.T) {
	h := newHarness(t, true) // real ONNX
	h.registerAllTools(t)
	h.seedProject(t, "test-proj")
	h.seedProject(t, "note-only-proj")

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

	// Capture WITH archive_transcript, so a hook-less host writes an inline
	// archive of the transcript (capture itself indexes nothing now).
	var result string
	h.Seed(t, testinfra.WithCapturedSession(map[string]any{
		"project":            "test-proj",
		"summary":            "Discussed database migration system design.",
		"tag":                "planning",
		"transcript":         transcript,
		"archive_transcript": true,
		"decisions":          []string{"Use sequential SQL files with timestamp names"},
	}, &result))

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

	// Ingest the inline archive in-process, exactly as the detached `vp drain
	// archives` the capture spawned would. Explicit so the freshly-created
	// archive is ingested regardless of this fresh host's baseline set.
	if _, err := ingest.Run(context.Background(),
		ingest.Deps{Vault: h.Vault, Engine: h.Engine, Embedder: h.Embedder},
		ingest.RunOptions{VaultRoot: h.Vault.Root, Project: "test-proj", Explicit: true}); err != nil {
		t.Fatalf("ingest the inline archive: %v", err)
	}

	// Now the transcript is searchable through its archive's chunks.
	results, err := h.Engine.Search(context.Background(), "PostgreSQL database migration system",
		search.SearchFilters{Project: "test-proj"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected search results for migration query after archive ingest")
	}
	top := strings.ToLower(results[0].Content)
	if !strings.Contains(top, "migration") && !strings.Contains(top, "database") {
		t.Errorf("top result should mention migration/database, got: %s",
			truncate(results[0].Content, 100))
	}
	// At least one transcript chunk of this session is now present (source_ref
	// is the session id for transcript chunks).
	sawTranscript := false
	for _, r := range results {
		if r.Project != "test-proj" {
			t.Errorf("result leaked from another project: %+v", r)
		}
		if r.SourceType == "session" && strings.Contains(r.SourceRef, captureResult.SessionID) {
			sawTranscript = true
		}
	}
	if !sawTranscript {
		t.Errorf("no transcript chunk of session %s among the results after ingest: %+v",
			captureResult.SessionID, results)
	}

	// A capture WITHOUT an archive leaves the project's host-local chunk store
	// absent: nothing to ingest, so no chunks.jsonl is ever written.
	var noteOnly string
	h.Seed(t, testinfra.WithCapturedSession(map[string]any{
		"project": "note-only-proj",
		"summary": "A note with no transcript and no archive.",
		"tag":     "planning",
	}, &noteOnly))
	if !strings.Contains(noteOnly, `"status":"ok"`) && !strings.Contains(noteOnly, `"status": "ok"`) {
		t.Fatalf("note-only capture: %s", noteOnly)
	}
	assertChunkStoreAbsent(t, h, "note-only-proj")
}

// assertChunkStoreAbsent fails if the project's host-local chunk store
// (palace/.local/index/<p>/chunks.jsonl) exists.
func assertChunkStoreAbsent(t *testing.T, h *testHarness, project string) {
	t.Helper()
	dir, err := h.Vault.IndexDir(project)
	if err != nil {
		t.Fatalf("IndexDir(%s): %v", project, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "chunks.jsonl")); !os.IsNotExist(err) {
		t.Errorf("host-local chunk store for %s should be absent, stat chunks.jsonl err = %v", project, err)
	}
}

// TestIntegrationSessionCaptureWithoutTranscript proves that capturing a
// session without a transcript writes the session file but creates no drawers.
func TestIntegrationSessionCaptureWithoutTranscript(t *testing.T) {
	h := newHarness(t, false) // mock embedder — no ONNX needed
	h.registerAllTools(t)
	h.seedProject(t, "test-proj")

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

	// No tracked drawers (trivially true now that capture writes none).
	if total, _ := countAllDrawers(t, h.Vault, "test-proj"); total > 0 {
		t.Errorf("expected no drawers without transcript, found %d", total)
	}
	// The project's host-local chunk store is absent: a capture with no
	// transcript archives nothing, so there is nothing for the ingester to turn
	// into chunks (ADR-014 decision 7).
	assertChunkStoreAbsent(t, h, "test-proj")
	// And no ingester was launched: no archive means no `vp drain archives`
	// trigger fired from the capture handler.
	if launches := h.RecordedLaunches(); len(launches) != 0 {
		t.Errorf("expected no ingester launch without an archive, got %d: %+v", len(launches), launches)
	}
}

// TestIntegrationSessionIterationAcrossSessions proves that multiple
// session captures for the same project+date auto-increment iterations.
func TestIntegrationSessionIterationAcrossSessions(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)
	h.seedProject(t, "test-proj")

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
