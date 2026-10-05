// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// listOne seeds one archive and returns its listed entry.
func listOne(t *testing.T) (string, *Entry) {
	t.Helper()
	vault, _ := seedArchive(t, "sess-rv")
	entries, err := ListEntries(vault, "demo")
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListEntries: %v, %d entries", err, len(entries))
	}
	return vault, entries[0]
}

// countReads counts the reads of the compressed file for the rest of the test.
func countReads(t *testing.T) *int {
	t.Helper()
	n := 0
	old := readArchiveFile
	readArchiveFile = func(p string) ([]byte, error) { n++; return old(p) }
	t.Cleanup(func() { readArchiveFile = old })
	return &n
}

// rewriteManifest edits the manifest on disk.
func rewriteManifest(t *testing.T, vault string, e *Entry, edit func(*Manifest)) {
	t.Helper()
	m := *e.Manifest
	edit(&m)
	if err := WriteManifest(vault, e.ManifestPath, &m); err != nil {
		t.Fatal(err)
	}
}

// TestReadVerifiedReadsTheWholeArchiveOnce: the bytes are the source bytes,
// keyed by the manifest's source_sha256, and the compressed file is read
// exactly once (one os.ReadFile, so it is closed before ReadVerified returns).
func TestReadVerifiedReadsTheWholeArchiveOnce(t *testing.T) {
	_, e := listOne(t)
	reads := countReads(t)
	got, err := ReadVerified(e)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Bytes) != sampleClaudeJSONL || got.SHA != e.Manifest.SourceSHA256 || got.Fallback {
		t.Fatalf("ReadVerified = %d bytes, sha %s, fallback %v", len(got.Bytes), got.SHA, got.Fallback)
	}
	if *reads != 1 {
		t.Fatalf("%d reads of the compressed file, want 1", *reads)
	}
}

// TestReadVerifiedRefusesALengthMismatch: a compressed file whose length is
// not the manifest's compressed_bytes is corrupt.
func TestReadVerifiedRefusesALengthMismatch(t *testing.T) {
	_, e := listOne(t)
	f, err := os.OpenFile(e.ArchivePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("junk"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := ReadVerified(e); !errors.Is(err, ErrArchiveCorrupt) || !strings.Contains(err.Error(), "compressed_bytes") {
		t.Fatalf("ReadVerified = %v, want ErrArchiveCorrupt on compressed_bytes", err)
	}
}

// TestReadVerifiedRefusesAHashMismatch: other bytes under the listed manifest
// (its lengths edited to match, so only the hash can tell) are corrupt.
func TestReadVerifiedRefusesAHashMismatch(t *testing.T) {
	vault, _ := seedArchive(t, "sess-rv")
	entries, _ := ListEntries(vault, "demo")
	e := entries[0]
	other := strings.Replace(sampleClaudeJSONL, "a", "b", 1)
	src := filepath.Join(t.TempDir(), "o.jsonl")
	if err := os.WriteFile(src, []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := compressFile(vault, src, e.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	rewriteManifest(t, vault, e, func(m *Manifest) { m.CompressedBytes = n; m.SourceBytes = int64(len(other)) })
	entries, _ = ListEntries(vault, "demo")
	if _, err := ReadVerified(entries[0]); !errors.Is(err, ErrArchiveCorrupt) || !strings.Contains(err.Error(), "source_sha256") {
		t.Fatalf("ReadVerified = %v, want ErrArchiveCorrupt on source_sha256", err)
	}
}

// TestReadVerifiedKeysAManifestWithNoHashByItsBytes: a manifest with no
// source_sha256 is checked by length only and keyed by the sha256 of the
// decompressed bytes.
func TestReadVerifiedKeysAManifestWithNoHashByItsBytes(t *testing.T) {
	vault, e := listOne(t)
	rewriteManifest(t, vault, e, func(m *Manifest) { m.SourceSHA256 = "" })
	entries, _ := ListEntries(vault, "demo")
	got, err := ReadVerified(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(sampleClaudeJSONL))
	if !got.Fallback || got.SHA != hex.EncodeToString(sum[:]) {
		t.Fatalf("fallback %v, sha %s; want the sha256 of the bytes", got.Fallback, got.SHA)
	}
}

// TestReadVerifiedReportsAManifestChangedSinceTheListing: a manifest rewritten
// or moved aside (Create renames it to a .bak first) after the listing is a
// change to retry, not a corrupt archive.
func TestReadVerifiedReportsAManifestChangedSinceTheListing(t *testing.T) {
	vault, e := listOne(t)
	rewriteManifest(t, vault, e, func(m *Manifest) { m.CapturedAt = "2026-04-16T12:00:00Z" })
	if _, err := ReadVerified(e); !errors.Is(err, ErrArchiveChanged) {
		t.Fatalf("rewritten manifest: %v, want ErrArchiveChanged", err)
	}
	_, e = listOne(t)
	if err := os.Rename(e.ManifestPath, e.ManifestPath+".x.bak"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadVerified(e); !errors.Is(err, ErrArchiveChanged) {
		t.Fatalf("moved manifest: %v, want ErrArchiveChanged", err)
	}
}

// TestReadVerifiedTellsARewriteMidReadFromCorruption: the hook rewrites the
// pair while the file is being read (the read returns the new bytes, the
// manifest is new by the time they are checked): ErrArchiveChanged, never
// ErrArchiveCorrupt, so no failure is counted against the archive.
func TestReadVerifiedTellsARewriteMidReadFromCorruption(t *testing.T) {
	vault, e := listOne(t)
	other := sampleClaudeJSONL + "\nmore\n"
	src := filepath.Join(t.TempDir(), "o.jsonl")
	if err := os.WriteFile(src, []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	old := readArchiveFile
	readArchiveFile = func(p string) ([]byte, error) {
		n, err := compressFile(vault, src, e.ArchivePath)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(other))
		rewriteManifest(t, vault, e, func(m *Manifest) {
			m.CompressedBytes, m.SourceBytes, m.SourceSHA256 = n, int64(len(other)), hex.EncodeToString(sum[:])
		})
		return old(p)
	}
	t.Cleanup(func() { readArchiveFile = old })
	if _, err := ReadVerified(e); !errors.Is(err, ErrArchiveChanged) {
		t.Fatalf("rewrite mid-read: %v, want ErrArchiveChanged", err)
	}
}

// TestReadVerifiedRefusesASourceLengthMismatch: a manifest whose source_bytes
// does not describe the bytes is corrupt, even where the hash would match.
func TestReadVerifiedRefusesASourceLengthMismatch(t *testing.T) {
	vault, e := listOne(t)
	rewriteManifest(t, vault, e, func(m *Manifest) { m.SourceBytes++ })
	entries, _ := ListEntries(vault, "demo")
	if _, err := ReadVerified(entries[0]); !errors.Is(err, ErrArchiveCorrupt) || !strings.Contains(err.Error(), "source_bytes") {
		t.Fatalf("ReadVerified = %v, want ErrArchiveCorrupt on source_bytes", err)
	}
}

// TestReadVerifiedTreatsAMissingFileAsAChange: the compressed file is gone
// while its listed manifest is still there (a rewrite renamed it away and has
// not written the new manifest yet): ErrArchiveChanged, retried next run,
// never a failure.
func TestReadVerifiedTreatsAMissingFileAsAChange(t *testing.T) {
	_, e := listOne(t)
	if err := os.Rename(e.ArchivePath, e.ArchivePath+".tmp"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadVerified(e); !errors.Is(err, ErrArchiveChanged) {
		t.Fatalf("ReadVerified = %v, want ErrArchiveChanged", err)
	}
}
