// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package zed

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"
)

func compressJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	defer w.Close()
	return w.EncodeAll(b, nil)
}

// makeTestDB creates a fresh threads.db with the expected schema and
// the provided rows inserted. Returns the path.
func makeTestDB(t *testing.T, rows ...testRow) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "threads.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE threads (
		id TEXT PRIMARY KEY,
		summary TEXT,
		updated_at TEXT,
		data_type TEXT DEFAULT 'zstd',
		data BLOB,
		parent_id TEXT,
		worktree_branch TEXT
	)`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	for _, r := range rows {
		_, err := db.Exec(
			`INSERT INTO threads (id, summary, updated_at, data) VALUES (?, ?, ?, ?)`,
			r.ID, r.Summary, r.UpdatedAt, r.Data,
		)
		if err != nil {
			t.Fatalf("insert %s: %v", r.ID, err)
		}
	}
	return path
}

type testRow struct {
	ID        string
	Summary   string
	UpdatedAt string
	Data      []byte
}

// minimalThread builds a well-formed thread payload with one user
// turn, one assistant turn with a tool_use, and a tool_result. Used
// by round-trip + synthesis tests.
func minimalThread(t *testing.T, version string) []byte {
	t.Helper()
	payload := map[string]any{
		"title":   "Hello",
		"version": version,
		"model": map[string]any{
			"provider": "anthropic",
			"model":    "claude-opus-4-6",
		},
		"messages": []any{
			map[string]any{
				"User": map[string]any{
					"id": "u1",
					"content": []any{
						map[string]any{"Text": "list files"},
					},
				},
			},
			map[string]any{
				"Agent": map[string]any{
					"content": []any{
						map[string]any{
							"ToolUse": map[string]any{
								"id":    "tu1",
								"name":  "list_dir",
								"input": map[string]any{"path": "/tmp"},
							},
						},
					},
					"tool_results": map[string]any{
						"tu1": map[string]any{
							"tool_use_id": "tu1",
							"tool_name":   "list_dir",
							"is_error":    false,
							"content":     map[string]any{"Text": "a.txt\nb.txt"},
						},
					},
				},
			},
		},
	}
	return compressJSON(t, payload)
}

func TestQueryThread_RoundTrip(t *testing.T) {
	db := makeTestDB(t, testRow{
		ID:        "thread-1",
		Summary:   "Hello",
		UpdatedAt: "2026-04-16T10:00:00Z",
		Data:      minimalThread(t, SupportedThreadVersion),
	})

	th, err := QueryThread(db, "thread-1")
	if err != nil {
		t.Fatalf("QueryThread: %v", err)
	}
	if th.ID != "thread-1" {
		t.Errorf("ID = %q, want thread-1", th.ID)
	}
	if th.ModelString() != "claude-opus-4-6" {
		t.Errorf("ModelString = %q, want claude-opus-4-6", th.ModelString())
	}
	if len(th.Messages) != 2 {
		t.Fatalf("Messages = %d, want 2", len(th.Messages))
	}
	if th.Messages[0].Role != "user" || th.Messages[1].Role != "assistant" {
		t.Errorf("roles = %v / %v", th.Messages[0].Role, th.Messages[1].Role)
	}
	// Assistant message should carry the tool_use and flattened tool_result.
	var seenToolUse, seenToolResult bool
	for _, c := range th.Messages[1].Content {
		if c.Type == "tool_use" && c.ToolName == "list_dir" {
			seenToolUse = true
		}
		if c.Type == "tool_result" && c.ToolUseID == "tu1" && strings.Contains(c.ToolResultText, "a.txt") {
			seenToolResult = true
		}
	}
	if !seenToolUse {
		t.Errorf("tool_use block missing from assistant content")
	}
	if !seenToolResult {
		t.Errorf("tool_result block missing from assistant content")
	}
}

func TestQueryThread_NotFound(t *testing.T) {
	db := makeTestDB(t)
	_, err := QueryThread(db, "missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want not-found", err)
	}
}

func TestQueryThread_UnsupportedVersion(t *testing.T) {
	db := makeTestDB(t, testRow{
		ID:        "thread-v",
		UpdatedAt: "2026-04-16T10:00:00Z",
		Data:      minimalThread(t, "99.0.0"),
	})
	_, err := QueryThread(db, "thread-v")
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Errorf("err = %v, want ErrUnsupportedVersion", err)
	}
}

func TestOpenDB_SchemaDrift_MissingColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Create a threads table missing the `data` column.
	_, err = db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, summary TEXT, updated_at TEXT)`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	db.Close()

	_, err = QueryThread(path, "anything")
	if !errors.Is(err, ErrSchemaDrift) {
		t.Errorf("err = %v, want ErrSchemaDrift", err)
	}
	if !strings.Contains(err.Error(), "data") {
		t.Errorf("err = %v, want missing-column listing including 'data'", err)
	}
}

func TestOpenDB_SchemaDrift_NoTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = QueryThread(path, "anything")
	if !errors.Is(err, ErrSchemaDrift) {
		t.Errorf("err = %v, want ErrSchemaDrift", err)
	}
}

func TestOpenDB_ExtraColumnsTolerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extra.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE threads (
		id TEXT PRIMARY KEY,
		summary TEXT,
		updated_at TEXT,
		data_type TEXT,
		data BLOB,
		parent_id TEXT,
		worktree_branch TEXT,
		future_field TEXT
	)`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err = db.Exec(
		`INSERT INTO threads (id, data) VALUES (?, ?)`,
		"t", minimalThread(t, SupportedThreadVersion),
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	db.Close()

	if _, err := QueryThread(path, "t"); err != nil {
		t.Errorf("extra columns should be tolerated, got: %v", err)
	}
}

func TestListThreadIDs_OrdersByUpdatedDesc(t *testing.T) {
	db := makeTestDB(t,
		testRow{ID: "old", UpdatedAt: "2026-01-01T00:00:00Z", Data: minimalThread(t, SupportedThreadVersion)},
		testRow{ID: "new", UpdatedAt: "2026-04-01T00:00:00Z", Data: minimalThread(t, SupportedThreadVersion)},
		testRow{ID: "mid", UpdatedAt: "2026-03-01T00:00:00Z", Data: minimalThread(t, SupportedThreadVersion)},
	)
	ids, err := ListThreadIDs(db, 0)
	if err != nil {
		t.Fatalf("ListThreadIDs: %v", err)
	}
	want := []string{"new", "mid", "old"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], want[i])
		}
	}
}

func TestDefaultDBPath_PerOS(t *testing.T) {
	t.Setenv("ZED_THREADS_DB", "")
	got := DefaultDBPath()
	if got == "" {
		t.Skip("no home dir resolvable in this env")
	}
	var wantContains string
	switch runtime.GOOS {
	case "darwin":
		wantContains = "Library/Application Support/Zed/threads/threads.db"
	case "windows":
		wantContains = filepath.Join("Zed", "threads", "threads.db")
	default:
		wantContains = ".local/share/zed/threads/threads.db"
	}
	if !strings.Contains(filepath.ToSlash(got), filepath.ToSlash(wantContains)) {
		t.Errorf("DefaultDBPath() = %q, want contains %q", got, wantContains)
	}
}

func TestDefaultDBPath_EnvOverride(t *testing.T) {
	t.Setenv("ZED_THREADS_DB", "/custom/path/threads.db")
	if got := DefaultDBPath(); got != "/custom/path/threads.db" {
		t.Errorf("DefaultDBPath() = %q, want /custom/path/threads.db", got)
	}
}
