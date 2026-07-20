// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedArchive creates a vault+transcripts layout and returns the
// Create result for use in the verify/extract/list tests.
func seedArchive(t *testing.T, sessionID string) (vaultRoot string, res *CreateResult) {
	t.Helper()
	tmp := t.TempDir()
	vaultRoot = filepath.Join(tmp, "vault")
	src := filepath.Join(tmp, "src.jsonl")
	if err := os.WriteFile(src, []byte(sampleClaudeJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Create(CreateOptions{
		Adapter:     ClaudeCodeAdapterName,
		SessionID:   sessionID,
		SourcePath:  src,
		VaultRoot:   vaultRoot,
		ProjectSlug: "demo",
		Now:         time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return vaultRoot, r
}

func TestListEntries_SortedByCapturedAt(t *testing.T) {
	tmp := t.TempDir()
	vaultRoot := filepath.Join(tmp, "vault")
	src := filepath.Join(tmp, "src.jsonl")
	if err := os.WriteFile(src, []byte(sampleClaudeJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	// Two archives, different capture times and IDs.
	for i, when := range []time.Time{
		time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC),
	} {
		if _, err := Create(CreateOptions{
			Adapter: ClaudeCodeAdapterName, SessionID: []string{"s-new", "s-old"}[i],
			SourcePath: src, VaultRoot: vaultRoot, ProjectSlug: "demo", Now: when,
		}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := ListEntries(vaultRoot, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Manifest.SessionID != "s-old" {
		t.Errorf("oldest-first ordering violated: first = %q", entries[0].Manifest.SessionID)
	}
}

// An UNREADABLE transcripts directory must produce an error, never an empty
// listing. filepath.Glob swallowed EACCES and returned nil,nil -- "I could not
// look" reported as "there was nothing there". os.ReadDir surfaces it.
func TestListEntries_UnreadableDirIsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; directory permission bits do not bind")
	}
	vault, _ := seedArchive(t, "sess-unreadable")
	dir := filepath.Join(vault, "Projects", "demo", "transcripts")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	entries, err := ListEntries(vault, "demo")
	if err == nil {
		t.Fatalf("want an error for an unreadable transcripts dir; got %d entries, nil error", len(entries))
	}
	if entries != nil {
		t.Errorf("entries must be nil on error, got %v", entries)
	}
}

// An ABSENT transcripts directory is the common no-transcripts-yet project: it
// must stay a non-erroring empty listing, matching Glob's old behaviour, so
// `vp archive list` does not begin erroring for every project without an
// archive.
func TestListEntries_AbsentDirIsEmptyNotError(t *testing.T) {
	vaultRoot := filepath.Join(t.TempDir(), "vault")
	// Nothing under Projects/demo/transcripts -- the directory never existed.
	entries, err := ListEntries(vaultRoot, "demo")
	if err != nil {
		t.Fatalf("absent transcripts dir must not error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("absent transcripts dir must be an empty listing, got %d entries", len(entries))
	}
}

func TestVerify_OK(t *testing.T) {
	vault, res := seedArchive(t, "sess-verify-ok")
	entries, err := ListEntries(vault, "demo")
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListEntries: %v len=%d", err, len(entries))
	}
	r := VerifyWithOptions(entries[0], VerifyOptions{})
	if !r.OK {
		t.Fatalf("expected OK, problems: %v", r.Problems)
	}
	if r.RecomputedSHA != res.Manifest.SourceSHA256 {
		t.Errorf("recomputed SHA mismatch")
	}
}

func TestVerify_DetectsTamperedArchive(t *testing.T) {
	vault, res := seedArchive(t, "sess-tamper")
	// Tamper: overwrite the compressed archive with a different
	// zstd-compressed payload (empty-ish). Easiest: rewrite by
	// compressing different content.
	other := []byte(sampleClaudeJSONL + "\ntampered\n")
	tmp := t.TempDir()
	otherSrc := filepath.Join(tmp, "o.jsonl")
	if err := os.WriteFile(otherSrc, other, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := compressFile(otherSrc, res.ArchivePath); err != nil {
		t.Fatal(err)
	}

	entries, _ := ListEntries(vault, "demo")
	r := VerifyWithOptions(entries[0], VerifyOptions{})
	if r.OK {
		t.Fatal("tampered archive must fail verification")
	}
	found := false
	for _, p := range r.Problems {
		if strings.Contains(p, "source_sha256 mismatch") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected source_sha256 mismatch problem, got: %v", r.Problems)
	}
}

func TestVerify_DetectsMissingArchive(t *testing.T) {
	vault, res := seedArchive(t, "sess-missing")
	if err := os.Remove(res.ArchivePath); err != nil {
		t.Fatal(err)
	}
	entries, _ := ListEntries(vault, "demo")
	r := VerifyWithOptions(entries[0], VerifyOptions{})
	if r.OK {
		t.Fatal("missing archive must fail verification")
	}
}

func TestExtract_RoundTripsOriginalBytes(t *testing.T) {
	_, res := seedArchive(t, "sess-extract")
	var buf bytes.Buffer
	n, err := Extract(res.ArchivePath, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if int(n) != len(sampleClaudeJSONL) {
		t.Errorf("byte count = %d, want %d", n, len(sampleClaudeJSONL))
	}
	if buf.String() != sampleClaudeJSONL {
		t.Errorf("extracted content does not match original")
	}
}

func TestResolveEntry_BySessionID(t *testing.T) {
	vault, res := seedArchive(t, "sess-resolve-id")
	e, err := ResolveEntry(vault, "demo", "sess-resolve-id")
	if err != nil {
		t.Fatal(err)
	}
	if e.ManifestPath != res.ManifestPath {
		t.Errorf("ResolveEntry path mismatch: %s vs %s", e.ManifestPath, res.ManifestPath)
	}
}

func TestResolveEntry_ByManifestPath(t *testing.T) {
	_, res := seedArchive(t, "sess-resolve-path")
	e, err := ResolveEntry("", "", res.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if e.Manifest.SessionID != "sess-resolve-path" {
		t.Errorf("got session id %q", e.Manifest.SessionID)
	}
}

func TestResolveEntry_ByArchivePath(t *testing.T) {
	_, res := seedArchive(t, "sess-resolve-archive")
	e, err := ResolveEntry("", "", res.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	if e.Manifest.SessionID != "sess-resolve-archive" {
		t.Errorf("got session id %q", e.Manifest.SessionID)
	}
}

func TestResolveEntry_UnknownSessionID(t *testing.T) {
	vault, _ := seedArchive(t, "present")
	if _, err := ResolveEntry(vault, "demo", "absent"); err == nil {
		t.Fatal("expected error for unknown session id")
	}
}
