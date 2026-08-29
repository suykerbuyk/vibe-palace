// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package zed

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// DefaultDBPath returns the per-OS default location of Zed's
// threads.db SQLite file. Empty string when $HOME / $LOCALAPPDATA is
// unresolvable.
//
// ZED_THREADS_DB overrides the default on all platforms, mirroring
// the CLAUDE_HOME escape hatch on the Claude adapter.
func DefaultDBPath() string {
	if v := os.Getenv("ZED_THREADS_DB"); v != "" {
		return v
	}
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "Zed", "threads", "threads.db")
	case "windows":
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "Zed", "threads", "threads.db")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, "AppData", "Local", "Zed", "threads", "threads.db")
	default: // linux, freebsd, etc.
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, ".local", "share", "zed", "threads", "threads.db")
	}
}

// ErrSchemaDrift is returned when the threads table is missing one or
// more expected columns, or when the database lacks the table
// entirely. The archive adapter treats this as fatal: it would rather
// refuse than emit a mis-synthesized manifest.
var ErrSchemaDrift = errors.New("zed threads schema drift")

// ErrUnsupportedVersion is returned when a thread payload's version
// field does not match SupportedThreadVersion. Mirrors ErrSchemaDrift
// semantics: fail loud rather than silently synthesize against an
// unknown shape.
var ErrUnsupportedVersion = errors.New("unsupported zed thread version")

// openDB opens the threads database read-only and verifies its
// schema. Returns an error tagged with ErrSchemaDrift on any missing
// expected column; extra columns are tolerated.
func openDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	if err := verifySchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func verifySchema(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(threads)`)
	if err != nil {
		return fmt.Errorf("pragma table_info: %w", err)
	}
	defer rows.Close()

	present := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan pragma row: %w", err)
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate pragma: %w", err)
	}

	if len(present) == 0 {
		return fmt.Errorf("%w: threads table not found", ErrSchemaDrift)
	}

	var missing []string
	for _, col := range ExpectedSchemaColumns {
		if !present[col] {
			missing = append(missing, col)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: missing columns %v", ErrSchemaDrift, missing)
	}
	return nil
}

// QueryThread opens dbPath, validates the schema, and returns the
// parsed thread identified by threadID. Returns sql.ErrNoRows wrapped
// when the thread does not exist, and ErrSchemaDrift /
// ErrUnsupportedVersion for drift conditions.
func QueryThread(dbPath, threadID string) (*Thread, error) {
	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var id, summary, updatedAt string
	var data []byte
	err = db.QueryRow(
		`SELECT id, COALESCE(summary, ''), COALESCE(updated_at, ''), data FROM threads WHERE id = ?`,
		threadID,
	).Scan(&id, &summary, &updatedAt, &data)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("thread %q not found in %s", threadID, dbPath)
		}
		return nil, fmt.Errorf("query thread: %w", err)
	}
	return parseThread(id, summary, updatedAt, data)
}

// ThreadListRow is one native-panel thread as listed from threads.db
// without decompressing the payload. The CLI uses this so an operator
// can DECLARE a thread id; the agent must not pick the newest row.
type ThreadListRow struct {
	ID        string `json:"id"`
	Summary   string `json:"summary"`
	UpdatedAt string `json:"updated_at"`
}

// ListThreads returns native-panel threads in reverse-chronological
// order (most recently updated first). limit <= 0 means no limit.
// Listing does not decompress the data BLOB.
func ListThreads(dbPath string, limit int) ([]ThreadListRow, error) {
	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	q := `SELECT id, COALESCE(summary, ''), COALESCE(updated_at, '') FROM threads ORDER BY updated_at DESC`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query threads: %w", err)
	}
	defer rows.Close()

	var out []ThreadListRow
	for rows.Next() {
		var r ThreadListRow
		if err := rows.Scan(&r.ID, &r.Summary, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan thread: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []ThreadListRow{}
	}
	return out, nil
}

// ListThreadIDs returns thread IDs in reverse-chronological order
// (most recently updated first). Convenience wrapper over ListThreads.
func ListThreadIDs(dbPath string, limit int) ([]string, error) {
	rows, err := ListThreads(dbPath, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, nil
}

func parseThread(id, summary, updatedAt string, data []byte) (*Thread, error) {
	decoded, err := decompressZstd(data)
	if err != nil {
		return nil, fmt.Errorf("decompress thread %s: %w", id, err)
	}
	var t Thread
	if err := json.Unmarshal(decoded, &t); err != nil {
		return nil, fmt.Errorf("unmarshal thread %s: %w", id, err)
	}

	// Belt-and-suspenders: version-field gate (schema-level check
	// happened in verifySchema on DB open).
	if t.Version != "" && t.Version != SupportedThreadVersion {
		return nil, fmt.Errorf("%w: thread %s has version %q, supported %q",
			ErrUnsupportedVersion, id, t.Version, SupportedThreadVersion)
	}

	msgs, err := UnmarshalMessages(t.RawMessages)
	if err != nil {
		return nil, fmt.Errorf("parse messages: %w", err)
	}
	t.Messages = msgs
	t.RawMessages = nil

	t.ID = id
	t.Summary = summary
	if updatedAt != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
			t.UpdatedAt = parsed
		} else if parsed, err := time.Parse(time.RFC3339, updatedAt); err == nil {
			t.UpdatedAt = parsed
		}
	}
	return &t, nil
}

func decompressZstd(data []byte) ([]byte, error) {
	r, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.DecodeAll(data, nil)
}
