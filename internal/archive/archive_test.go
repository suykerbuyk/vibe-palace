// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// sampleClaudeJSONL is a minimal fixture resembling a Claude Code
// session log: a permission-mode header followed by user/assistant
// message records with a model string.
const sampleClaudeJSONL = `{"type":"permission-mode","permissionMode":"bypassPermissions","sessionId":"test-session"}
{"type":"user","message":{"role":"user","content":"hello"}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-6","content":"hi"}}
{"type":"user","message":{"role":"user","content":"ok"}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-6","content":"bye"}}
`

func writeSource(t *testing.T, dir string) (path, sum string, size int64) {
	t.Helper()
	path = filepath.Join(dir, "source.jsonl")
	if err := os.WriteFile(path, []byte(sampleClaudeJSONL), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	h := sha256.Sum256([]byte(sampleClaudeJSONL))
	return path, hex.EncodeToString(h[:]), int64(len(sampleClaudeJSONL))
}

func TestCreate_WritesPairAndHashesPreCompression(t *testing.T) {
	tmp := t.TempDir()
	vault := filepath.Join(tmp, "vault")
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcPath, wantSum, wantSize := writeSource(t, srcDir)

	res, err := Create(CreateOptions{
		Adapter:     ClaudeCodeAdapterName,
		SessionID:   "sess-1",
		SourcePath:  srcPath,
		VaultRoot:   vault,
		ProjectSlug: "demo",
		Now:         time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		VPVersion:   "test",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.Skipped {
		t.Fatal("first Create should not be Skipped")
	}

	wantDir := filepath.Join(vault, "Projects", "demo", "transcripts")
	if !strings.HasPrefix(res.ArchivePath, wantDir) {
		t.Errorf("archive path not under transcripts dir: %s", res.ArchivePath)
	}
	if !strings.HasSuffix(res.ArchivePath, "2026-04-15-sess-1.jsonl.zst") {
		t.Errorf("unexpected archive filename: %s", res.ArchivePath)
	}

	// Manifest contents
	m := res.Manifest
	if m.SchemaVersion != ManifestSchemaVersion {
		t.Errorf("schema version = %d, want %d", m.SchemaVersion, ManifestSchemaVersion)
	}
	if m.Adapter != ClaudeCodeAdapterName {
		t.Errorf("adapter = %q", m.Adapter)
	}
	if m.SourceSHA256 != wantSum {
		t.Errorf("source_sha256 = %q, want %q", m.SourceSHA256, wantSum)
	}
	if m.SourceBytes != wantSize {
		t.Errorf("source_bytes = %d, want %d", m.SourceBytes, wantSize)
	}
	if m.TurnCount != 4 { // two user + two assistant
		t.Errorf("turn_count = %d, want 4", m.TurnCount)
	}
	if m.Model != "claude-opus-4-6" {
		t.Errorf("model = %q", m.Model)
	}
	if m.CapturedAt != "2026-04-15T12:00:00Z" {
		t.Errorf("captured_at = %q", m.CapturedAt)
	}

	// Roundtrip the compressed file and verify it matches the source.
	f, err := os.Open(res.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec, err := zstd.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	got, err := io.ReadAll(dec)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sampleClaudeJSONL {
		t.Errorf("decompressed content mismatch")
	}

	// Manifest should be readable back.
	if _, err := ReadManifest(res.ManifestPath); err != nil {
		t.Errorf("ReadManifest: %v", err)
	}
}

func TestCreate_IdempotentOnSameSource(t *testing.T) {
	tmp := t.TempDir()
	vault := filepath.Join(tmp, "vault")
	srcPath, _, _ := writeSource(t, tmp)

	opts := CreateOptions{
		Adapter:     ClaudeCodeAdapterName,
		SessionID:   "sess-2",
		SourcePath:  srcPath,
		VaultRoot:   vault,
		ProjectSlug: "demo",
		Now:         time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
	}
	if _, err := Create(opts); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	// Bump the clock to ensure the date prefix would change if a new
	// file were written — but the idempotency check must short-circuit
	// before any filesystem mutation.
	opts.Now = time.Date(2026, 4, 16, 12, 0, 0, 0, time.UTC)
	res2, err := Create(opts)
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	// Second call sees the existing manifest at the original date prefix.
	// Because date is recomputed from opts.Now, it looks for 2026-04-16-*
	// and will write a new archive unless we fix the date. The current
	// contract is "idempotent on the same target filename"; a different
	// clock day is treated as a separate archive.
	if res2.Skipped {
		t.Fatal("different date prefix should not be treated as idempotent")
	}

	// Now repeat with identical opts -> must skip.
	res3, err := Create(opts)
	if err != nil {
		t.Fatalf("third Create: %v", err)
	}
	if !res3.Skipped {
		t.Fatal("identical opts must be Skipped=true")
	}
}

func TestCreate_SourceChangePreservesPriorManifest(t *testing.T) {
	tmp := t.TempDir()
	vault := filepath.Join(tmp, "vault")
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcPath, _, _ := writeSource(t, srcDir)

	opts := CreateOptions{
		Adapter:     ClaudeCodeAdapterName,
		SessionID:   "sess-3",
		SourcePath:  srcPath,
		VaultRoot:   vault,
		ProjectSlug: "demo",
		Now:         time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
	}
	first, err := Create(opts)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	firstHash := first.Manifest.SourceSHA256

	// Simulate Claude Code appending more turns.
	appended := sampleClaudeJSONL + `{"type":"user","message":{"role":"user","content":"more"}}` + "\n"
	if err := os.WriteFile(srcPath, []byte(appended), 0o644); err != nil {
		t.Fatal(err)
	}

	second, err := Create(opts)
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if second.Skipped {
		t.Fatal("changed source must not be Skipped")
	}
	if second.Manifest.SourceSHA256 == firstHash {
		t.Fatal("source hash did not update after append")
	}

	// Prior manifest must be preserved as .bak.<short-hash>.
	matches, err := filepath.Glob(first.ManifestPath + "." + firstHash[:12] + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Errorf("expected preserved .bak manifest, got %d matches", len(matches))
	}
}

func TestEncodeProjectDir(t *testing.T) {
	// Claude Code replaces every non-(ASCII-alnum or '-') character with '-':
	// path separators, dots, underscores, spaces, and non-ASCII all collapse.
	cases := []struct{ in, want string }{
		{"/home/johns/code/vibe-palace", "-home-johns-code-vibe-palace"},
		{"/home/alice/code/my.app_v2", "-home-alice-code-my-app-v2"},
		{"/home/user/path with spaces", "-home-user-path-with-spaces"},
		{"/home/user/project-123", "-home-user-project-123"},
		{`C:\Users\bob\proj`, "C--Users-bob-proj"},
		{"/home/josé/café", "-home-jos--caf-"},
	}
	for _, c := range cases {
		if got := EncodeProjectDir(c.in); got != c.want {
			t.Errorf("EncodeProjectDir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInspectClaudeJSONL(t *testing.T) {
	tmp := t.TempDir()
	srcPath, _, _ := writeSource(t, tmp)

	turns, model, err := InspectClaudeJSONL(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if turns != 4 {
		t.Errorf("turns = %d, want 4", turns)
	}
	if model != "claude-opus-4-6" {
		t.Errorf("model = %q", model)
	}
}
