// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"
)

func zstdEncode(t *testing.T, b []byte) []byte {
	t.Helper()
	w, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	return w.EncodeAll(b, nil)
}

// makeZedDB builds a fresh threads.db at tmpdir containing a single
// thread (threadID) with the provided payload bytes compressed + stored.
// payloadJSON is raw (uncompressed) JSON matching Zed's thread shape.
func makeZedDB(t *testing.T, threadID, payloadJSON string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "threads.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE threads (
		id TEXT PRIMARY KEY,
		summary TEXT,
		updated_at TEXT,
		data_type TEXT DEFAULT 'zstd',
		data BLOB,
		parent_id TEXT,
		worktree_branch TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO threads (id, summary, updated_at, data) VALUES (?, ?, ?, ?)`,
		threadID, "", "2026-04-16T10:00:00Z", zstdEncode(t, []byte(payloadJSON)),
	); err != nil {
		t.Fatal(err)
	}
	return path
}

const sampleZedPayload = `{
  "title": "sample",
  "version": "0.3.0",
  "model": {"provider": "anthropic", "model": "claude-sonnet-4-5"},
  "messages": [
    {"User": {"id": "u1", "content": [{"Text": "hello"}]}},
    {"Agent": {
      "content": [
        {"Text": "sure, let me check"},
        {"ToolUse": {"id": "tu1", "name": "Read", "input": {"path": "/tmp/a"}}}
      ],
      "tool_results": {
        "tu1": {
          "tool_use_id": "tu1",
          "tool_name": "Read",
          "is_error": false,
          "content": {"Text": "file contents"}
        }
      }
    }},
    {"User": {"id": "u2", "content": [{"Text": "thanks"}]}}
  ]
}`

func TestZedAdapter_SynthesizesClaudeShapeJSONL(t *testing.T) {
	db := makeZedDB(t, "thread-A", sampleZedPayload)

	tmpVault := t.TempDir()
	res, err := Create(CreateOptions{
		Adapter:     ZedAdapterName,
		SessionID:   "thread-A",
		SourcePath:  db,
		VaultRoot:   tmpVault,
		ProjectSlug: "test",
		Now:         time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC),
		VPVersion:   "0.0.0-test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.Manifest.Adapter != ZedAdapterName {
		t.Errorf("adapter = %q, want zed", res.Manifest.Adapter)
	}
	if res.Manifest.Model != "claude-sonnet-4-5" {
		t.Errorf("model = %q, want claude-sonnet-4-5", res.Manifest.Model)
	}
	// 3 user lines (u1 + synthesized tool_result + u2) + 1 assistant = 4
	if res.Manifest.TurnCount != 4 {
		t.Errorf("turn_count = %d, want 4", res.Manifest.TurnCount)
	}

	// Decompress the archive and inspect line shape.
	lines := readArchiveLines(t, res.ArchivePath)
	if len(lines) != 4 {
		t.Fatalf("archive has %d lines, want 4", len(lines))
	}

	// Line 0: user "hello"
	requireField(t, lines[0], "type", "user")
	requireMessageRole(t, lines[0], "user")
	requireContentText(t, lines[0], 0, "hello")

	// Line 1: assistant with text + tool_use
	requireField(t, lines[1], "type", "assistant")
	requireMessageRole(t, lines[1], "assistant")
	requireMessageModel(t, lines[1], "claude-sonnet-4-5")
	content := messageContent(t, lines[1])
	if len(content) != 2 {
		t.Fatalf("assistant content len = %d, want 2", len(content))
	}
	if content[0]["type"] != "text" || content[0]["text"] != "sure, let me check" {
		t.Errorf("content[0] = %v", content[0])
	}
	if content[1]["type"] != "tool_use" || content[1]["name"] != "Read" || content[1]["id"] != "tu1" {
		t.Errorf("content[1] = %v", content[1])
	}

	// Line 2: user tool_result
	requireField(t, lines[2], "type", "user")
	trContent := messageContent(t, lines[2])
	if len(trContent) != 1 || trContent[0]["type"] != "tool_result" ||
		trContent[0]["tool_use_id"] != "tu1" || trContent[0]["text"] != "file contents" {
		t.Errorf("tool_result line malformed: %v", trContent)
	}

	// Line 3: user "thanks"
	requireField(t, lines[3], "type", "user")
	requireContentText(t, lines[3], 0, "thanks")
}

func TestZedAdapter_SynthesisDeterministic(t *testing.T) {
	db := makeZedDB(t, "det", sampleZedPayload)

	var hashes []string
	for i := range 3 {
		vault := t.TempDir()
		res, err := Create(CreateOptions{
			Adapter:     ZedAdapterName,
			SessionID:   "det",
			SourcePath:  db,
			VaultRoot:   vault,
			ProjectSlug: "p",
			Now:         time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		hashes = append(hashes, res.Manifest.SourceSHA256)
	}
	if hashes[0] != hashes[1] || hashes[1] != hashes[2] {
		t.Errorf("synthesis not deterministic: %v", hashes)
	}
	if hashes[0] == "" {
		t.Errorf("empty source hash")
	}
}

func TestZedAdapter_Idempotent(t *testing.T) {
	db := makeZedDB(t, "idem", sampleZedPayload)
	vault := t.TempDir()

	opts := CreateOptions{
		Adapter:     ZedAdapterName,
		SessionID:   "idem",
		SourcePath:  db,
		VaultRoot:   vault,
		ProjectSlug: "p",
		Now:         time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC),
	}
	first, err := Create(opts)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if first.Skipped {
		t.Errorf("first call should not be skipped")
	}
	second, err := Create(opts)
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if !second.Skipped {
		t.Errorf("second call should be skipped (idempotent)")
	}
	if second.Manifest.SourceSHA256 != first.Manifest.SourceSHA256 {
		t.Errorf("hash drifted between calls")
	}
}

func TestZedAdapter_TempFileCleanedUp(t *testing.T) {
	db := makeZedDB(t, "clean", sampleZedPayload)
	vault := t.TempDir()

	// Snapshot tempdir before + after.
	tmpdir := os.TempDir()
	before := listZedTmpFiles(t, tmpdir)

	_, err := Create(CreateOptions{
		Adapter:     ZedAdapterName,
		SessionID:   "clean",
		SourcePath:  db,
		VaultRoot:   vault,
		ProjectSlug: "p",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	after := listZedTmpFiles(t, tmpdir)
	if len(after) > len(before) {
		t.Errorf("temp files leaked: before=%d after=%d", len(before), len(after))
	}
}

func TestZedAdapter_MissingDBFailsClear(t *testing.T) {
	_, err := Create(CreateOptions{
		Adapter:     ZedAdapterName,
		SessionID:   "x",
		SourcePath:  "/nonexistent/path/threads.db",
		VaultRoot:   t.TempDir(),
		ProjectSlug: "p",
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want not-found", err)
	}
}

func TestZedAdapter_ThreadMissingFailsClear(t *testing.T) {
	db := makeZedDB(t, "exists", sampleZedPayload)
	_, err := Create(CreateOptions{
		Adapter:     ZedAdapterName,
		SessionID:   "does-not-exist",
		SourcePath:  db,
		VaultRoot:   t.TempDir(),
		ProjectSlug: "p",
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want thread-not-found", err)
	}
}

func TestZedAdapter_ToolInputPreservedVerbatim(t *testing.T) {
	// Nested object in tool input — re-encoding via map[string]any
	// would reorder keys. Using json.RawMessage preserves source order.
	payload := `{
      "title": "x", "version": "0.3.0",
      "model": {"provider": "a", "model": "m"},
      "messages": [
        {"Agent": {
          "content": [{"ToolUse": {"id": "t", "name": "N",
            "input": {"z_last": 1, "a_first": 2, "m_mid": 3}}}],
          "tool_results": {}
        }}
      ]
    }`
	db := makeZedDB(t, "t1", payload)

	res, err := Create(CreateOptions{
		Adapter:     ZedAdapterName,
		SessionID:   "t1",
		SourcePath:  db,
		VaultRoot:   t.TempDir(),
		ProjectSlug: "p",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	lines := readArchiveLines(t, res.ArchivePath)
	if len(lines) == 0 {
		t.Fatal("no lines")
	}
	// tool_use.input should serialize with keys in source order.
	content := messageContent(t, lines[0])
	if len(content) == 0 || content[0]["type"] != "tool_use" {
		t.Fatalf("unexpected content[0]: %v", content)
	}
	// We asserted on the raw JSON string ordering in the emitted line.
	if !strings.Contains(lines[0], `"input":{"z_last":1,"a_first":2,"m_mid":3}`) {
		t.Errorf("tool input key order not preserved; line=%s", lines[0])
	}
}

func TestZedAdapter_VerifyAndExtractRoundTrip(t *testing.T) {
	db := makeZedDB(t, "rt", sampleZedPayload)
	vault := t.TempDir()

	res, err := Create(CreateOptions{
		Adapter:     ZedAdapterName,
		SessionID:   "rt",
		SourcePath:  db,
		VaultRoot:   vault,
		ProjectSlug: "p",
		Now:         time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// List should surface the new entry.
	entries, err := ListEntries(vault, "p")
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}

	// Verify must pass: recomputed hash matches manifest.
	vr := VerifyWithOptions(entries[0], VerifyOptions{})
	if !vr.OK {
		t.Errorf("verify failed: %+v", vr.Problems)
	}
	if vr.RecomputedSHA != res.Manifest.SourceSHA256 {
		t.Errorf("recomputed sha mismatch: got %s want %s", vr.RecomputedSHA, res.Manifest.SourceSHA256)
	}

	// Extract must produce the exact same bytes that were hashed +
	// compressed — i.e., the synthesized JSONL.
	var extracted bytes.Buffer
	if _, err := Extract(entries[0].ArchivePath, &extracted); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// Recompute the sha and confirm it matches the manifest.
	got := sha256OfBytes(extracted.Bytes())
	if got != res.Manifest.SourceSHA256 {
		t.Errorf("extracted bytes sha mismatch: got %s want %s", got, res.Manifest.SourceSHA256)
	}

	// And extracted content must be parseable as our Claude-shape JSONL.
	sc := bufio.NewScanner(&extracted)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var count int
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Errorf("extracted line not JSON: %v", err)
		}
		count++
	}
	if count != 4 {
		t.Errorf("extracted line count = %d, want 4", count)
	}
}

func sha256OfBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// --- helpers ---

func readArchiveLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close()
	dec, err := zstd.NewReader(f)
	if err != nil {
		t.Fatalf("zstd reader: %v", err)
	}
	defer dec.Close()
	b, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

func requireField(t *testing.T, line, field, want string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := m[field].(string); got != want {
		t.Errorf("%s = %q, want %q (line=%s)", field, got, want, line)
	}
}

func requireMessageRole(t *testing.T, line, want string) {
	t.Helper()
	msg := messageMap(t, line)
	if got, _ := msg["role"].(string); got != want {
		t.Errorf("role = %q, want %q", got, want)
	}
}

func requireMessageModel(t *testing.T, line, want string) {
	t.Helper()
	msg := messageMap(t, line)
	if got, _ := msg["model"].(string); got != want {
		t.Errorf("model = %q, want %q", got, want)
	}
}

func requireContentText(t *testing.T, line string, idx int, want string) {
	t.Helper()
	content := messageContent(t, line)
	if idx >= len(content) {
		t.Fatalf("content[%d] out of range, len=%d", idx, len(content))
	}
	if got, _ := content[idx]["text"].(string); got != want {
		t.Errorf("content[%d].text = %q, want %q", idx, got, want)
	}
}

func messageMap(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msg, _ := m["message"].(map[string]any)
	return msg
}

func messageContent(t *testing.T, line string) []map[string]any {
	t.Helper()
	msg := messageMap(t, line)
	raw, _ := msg["content"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func listZedTmpFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "vp-archive-zed-") {
			out = append(out, e.Name())
		}
	}
	return out
}
