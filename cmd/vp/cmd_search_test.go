// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testEngine(t *testing.T) (*search.Engine, *storage.Vault) {
	t.Helper()
	v := testVault(t)
	cfg := storage.Config{
		SearchDefaultLimit: 10,
		BoostWing:          0.12,
		BoostHall:          0.24,
		BoostRoom:          0.34,
	}
	emb := embedder.NewMock(384)
	eng := search.NewEngine(emb, v, cfg)
	t.Cleanup(func() { eng.Close() })
	return eng, v
}

func TestRunSearchNoResults(t *testing.T) {
	eng, _ := testEngine(t)
	var buf bytes.Buffer
	code := runSearch(eng, "test-proj", "nonexistent query", "", "", 10, false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(buf.String(), "No results found") {
		t.Errorf("expected no results message: %s", buf.String())
	}
}

func TestRunSearchWithResults(t *testing.T) {
	eng, v := testEngine(t)

	// Add content to the vault.
	v.AppendDrawer("test-proj", "code", "auth", storage.Drawer{
		Content:    "Authentication middleware handles JWT token validation and refresh",
		Hall:       "facts",
		SourceType: "session",
		SourceRef:  "session-2026-04-01-01",
		FiledAt:    "2026-04-01T10:00:00Z",
	})
	v.AppendDrawer("test-proj", "code", "database", storage.Drawer{
		Content:    "Database migration system uses versioned SQL files",
		Hall:       "facts",
		SourceType: "session",
		SourceRef:  "session-2026-04-01-02",
		FiledAt:    "2026-04-01T10:00:00Z",
	})

	var buf bytes.Buffer
	code := runSearch(eng, "test-proj", "authentication", "", "", 10, false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	// With mock embedder, results are deterministic but non-semantic.
	// We should get results (both drawers will have some similarity).
	if strings.Contains(out, "No results found") {
		t.Error("expected some results from search")
	}
}

func TestRunSearchJSON(t *testing.T) {
	eng, v := testEngine(t)

	v.AppendDrawer("test-proj", "code", "api", storage.Drawer{
		Content:    "REST API endpoint for user management",
		Hall:       "facts",
		SourceType: "session",
		FiledAt:    "2026-04-01T10:00:00Z",
	})

	var buf bytes.Buffer
	code := runSearch(eng, "test-proj", "api endpoint", "", "", 10, true, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var results []search.SearchResult
	if err := json.Unmarshal(buf.Bytes(), &results); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// Should have at least one result.
	if len(results) == 0 {
		t.Error("expected at least one result in JSON")
	}
}

func TestRunSearchLimit(t *testing.T) {
	eng, v := testEngine(t)

	// Add several drawers.
	for i := 0; i < 5; i++ {
		v.AppendDrawer("test-proj", "code", "misc", storage.Drawer{
			Content:    "Some content for search testing purposes",
			Hall:       "facts",
			SourceType: "session",
			FiledAt:    "2026-04-01T10:00:00Z",
		})
	}

	var buf bytes.Buffer
	code := runSearch(eng, "test-proj", "content testing", "", "", 2, true, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var results []search.SearchResult
	json.Unmarshal(buf.Bytes(), &results)
	if len(results) > 2 {
		t.Errorf("expected max 2 results, got %d", len(results))
	}
}

func TestRunSearchWingFilter(t *testing.T) {
	eng, v := testEngine(t)

	v.AppendDrawer("test-proj", "code", "api", storage.Drawer{
		Content: "Code API content", Hall: "facts", SourceType: "session",
		FiledAt: "2026-04-01T10:00:00Z",
	})
	v.AppendDrawer("test-proj", "docs", "api", storage.Drawer{
		Content: "Docs API content", Hall: "facts", SourceType: "session",
		FiledAt: "2026-04-01T10:00:00Z",
	})

	var buf bytes.Buffer
	code := runSearch(eng, "test-proj", "api content", "code", "", 10, true, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var results []search.SearchResult
	json.Unmarshal(buf.Bytes(), &results)
	for _, r := range results {
		if r.Wing != "code" {
			t.Errorf("expected wing=code, got %q", r.Wing)
		}
	}
}

func TestSearchMissingQueryExitCode(t *testing.T) {
	cmd := cmdSearch()
	code := cmd.Run(nil)
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

func TestSearchBadFlags(t *testing.T) {
	cmd := cmdSearch()
	code := cmd.Run([]string{"--unknown-flag"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}
