// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	zedreader "github.com/suykerbuyk/vibe-palace/internal/archive/zed"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	_ "modernc.org/sqlite"
)

func makeThreadsDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "threads.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
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
		t.Fatalf("create: %v", err)
	}
	_, err = db.Exec(`INSERT INTO threads (id, summary, updated_at, data) VALUES
		('old', 'first', '2026-01-01T00:00:00Z', X''),
		('new', 'latest', '2026-04-01T00:00:00Z', X''),
		('mid', 'middle', '2026-03-01T00:00:00Z', X'')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	return path
}

func TestRunArchiveThreadsJSON(t *testing.T) {
	db := makeThreadsDB(t)
	var buf bytes.Buffer
	code := runArchiveThreads(archive.ZedAdapterName, db, 0, true, &buf)
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, buf.String())
	}
	var rows []zedreader.ThreadListRow
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("json: %v\n%s", err, buf.String())
	}
	if len(rows) != 3 || rows[0].ID != "new" || rows[0].Summary != "latest" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestRunArchiveThreadsLimitAndTable(t *testing.T) {
	db := makeThreadsDB(t)
	var buf bytes.Buffer
	code := runArchiveThreads("zed", db, 2, false, &buf)
	if code != cli.ExitOK {
		t.Fatalf("exit %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "new") || !strings.Contains(out, "latest") {
		t.Errorf("missing newest row:\n%s", out)
	}
	if strings.Contains(out, "old") {
		t.Errorf("limit 2 still listed old:\n%s", out)
	}
}

func TestRunArchiveThreadsRejectsOtherAdapter(t *testing.T) {
	code := runArchiveThreads("claude-code", "/nope", 0, true, ioDiscard())
	if code != cli.ExitUser {
		t.Errorf("exit %d, want ExitUser", code)
	}
}

func TestRunArchiveThreadsMissingDB(t *testing.T) {
	code := runArchiveThreads("zed", filepath.Join(t.TempDir(), "nope.db"), 0, true, ioDiscard())
	if code != cli.ExitUser {
		t.Errorf("exit %d, want ExitUser", code)
	}
}

func TestCmdArchiveThreadsIsReadOnly(t *testing.T) {
	c := cmdArchiveThreads()
	if c.MutatesVault {
		t.Error("archive threads must not be vault-mutating")
	}
}

func ioDiscard() *bytes.Buffer { return &bytes.Buffer{} }

func TestRunArchiveThreadsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "threads.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE threads (
		id TEXT PRIMARY KEY, summary TEXT, updated_at TEXT,
		data_type TEXT, data BLOB, parent_id TEXT, worktree_branch TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	var buf bytes.Buffer
	code := runArchiveThreads("zed", path, 0, false, &buf)
	if code != cli.ExitOK {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(buf.String(), "No native Zed threads") {
		t.Errorf("got %q", buf.String())
	}
}

func TestDefaultDBPathHonoredWhenSourceEmpty(t *testing.T) {
	db := makeThreadsDB(t)
	t.Setenv("ZED_THREADS_DB", db)
	var buf bytes.Buffer
	code := runArchiveThreads("zed", "", 1, true, &buf)
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "new") {
		t.Errorf("env override not used: %s", buf.String())
	}
}

func TestCmdArchiveParentListsThreads(t *testing.T) {
	parent := registeredCommand(t, "archive")
	found := false
	for _, s := range parent.Subcommands {
		if s == "archive threads" {
			found = true
		}
	}
	if !found {
		t.Errorf("parent Subcommands = %v, want archive threads", parent.Subcommands)
	}
}
