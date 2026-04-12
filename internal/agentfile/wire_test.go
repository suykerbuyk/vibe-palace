// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package agentfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeHost(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestWireAddsBlockToNonEmptyFile(t *testing.T) {
	path := writeHost(t, "# CLAUDE.md\n\nExisting user content.\n")
	res, err := Wire(Target{Path: path, DisplayName: "CLAUDE.md"})
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	if res.Kind != Added {
		t.Errorf("Kind = %v, want Added", res.Kind)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if !strings.HasPrefix(s, "# CLAUDE.md\n\nExisting user content.\n") {
		t.Errorf("pre-existing content not preserved verbatim at head: %q", s)
	}
	if !strings.Contains(s, "<!-- vibe-palace:begin ") {
		t.Error("opening delim missing")
	}
	if !strings.Contains(s, blockCloseDelim) {
		t.Error("closing delim missing")
	}
	if !strings.HasSuffix(s, blockCloseDelim+"\n") {
		t.Errorf("file should end with closing delim + newline, got tail %q", s[len(s)-30:])
	}
}

func TestWireIdempotentUnchanged(t *testing.T) {
	path := writeHost(t, "# hello\n")
	if _, err := Wire(Target{Path: path}); err != nil {
		t.Fatalf("first Wire: %v", err)
	}
	info1, _ := os.Stat(path)
	data1, _ := os.ReadFile(path)

	res, err := Wire(Target{Path: path})
	if err != nil {
		t.Fatalf("second Wire: %v", err)
	}
	if res.Kind != Unchanged {
		t.Errorf("Kind = %v, want Unchanged", res.Kind)
	}
	info2, _ := os.Stat(path)
	data2, _ := os.ReadFile(path)

	// Unchanged must not rewrite the file — mtime preserved, contents identical.
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Errorf("mtime changed on Unchanged run: %v -> %v", info1.ModTime(), info2.ModTime())
	}
	if string(data1) != string(data2) {
		t.Errorf("content changed on Unchanged run")
	}
}

func TestWireUpdatesStaleBlock(t *testing.T) {
	// Simulate a file that has a block with a different content hash and body.
	stale := "<!-- vibe-palace:begin v=1 sha=deadbee -->\nOld body text.\n<!-- vibe-palace:end -->"
	path := writeHost(t, "# head\n\n"+stale+"\n\n# tail\n")

	res, err := Wire(Target{Path: path})
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	if res.Kind != Updated {
		t.Errorf("Kind = %v, want Updated", res.Kind)
	}
	if res.PrevSha != "deadbee" {
		t.Errorf("PrevSha = %q, want deadbee", res.PrevSha)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if strings.Contains(s, "Old body text.") {
		t.Error("stale content not removed")
	}
	if !strings.Contains(s, "# head\n") || !strings.Contains(s, "# tail\n") {
		t.Errorf("content outside block was corrupted: %q", s)
	}
	if !strings.Contains(s, "vp_bootstrap_context") {
		t.Error("fresh block body missing")
	}
}

func TestWirePreservesExternalContent(t *testing.T) {
	// Content before and after where the block lands must be byte-identical.
	before := "# Project\n\nLine A\nLine B\n"
	after := "\n## Appendix\nLine C\n"
	path := writeHost(t, before+after)

	if _, err := Wire(Target{Path: path}); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	data, _ := os.ReadFile(path)
	s := string(data)
	if !strings.HasPrefix(s, before) {
		t.Errorf("prefix differs: %q", s[:len(before)])
	}
	// The 'after' region appears once the block is appended — since this file
	// had no existing block, Wire appends at the end. Verify original content
	// is still verbatim at the start.
	if !strings.Contains(s, after) {
		t.Errorf("appendix content lost: %q", s)
	}
}

func TestWirePreservesCRLFLineEndings(t *testing.T) {
	path := writeHost(t, "# CRLF host\r\n\r\nBody.\r\n")
	if _, err := Wire(Target{Path: path}); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	data, _ := os.ReadFile(path)
	// The block we wrote must use CRLF, matching the host's convention.
	if !strings.Contains(string(data), "<!-- vibe-palace:begin") {
		t.Fatal("block not written")
	}
	if strings.Contains(string(data), "\n") && !strings.Contains(string(data), "\r\n") {
		t.Error("CRLF file rewritten with LF-only endings")
	}
	// Round-trip: a second Wire must report Unchanged (proving the CRLF
	// version bytes-equals on re-read).
	res, err := Wire(Target{Path: path})
	if err != nil {
		t.Fatalf("second Wire: %v", err)
	}
	if res.Kind != Unchanged {
		t.Errorf("second Wire Kind = %v, want Unchanged (CRLF not round-tripped)", res.Kind)
	}
}

func TestWireRestoresManuallyAlteredBlock(t *testing.T) {
	// Wire once, then a user hand-edits inside the delimiters. Re-wiring
	// must restore the canonical content without touching outside.
	path := writeHost(t, "# host\n")
	if _, err := Wire(Target{Path: path}); err != nil {
		t.Fatalf("first Wire: %v", err)
	}
	data, _ := os.ReadFile(path)
	tampered := strings.Replace(string(data), "vp_bootstrap_context", "vp_TAMPERED", 1)
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	res, err := Wire(Target{Path: path})
	if err != nil {
		t.Fatalf("re-Wire: %v", err)
	}
	if res.Kind != Updated {
		t.Errorf("Kind = %v, want Updated (tampered block)", res.Kind)
	}
	restored, _ := os.ReadFile(path)
	if strings.Contains(string(restored), "vp_TAMPERED") {
		t.Error("tampered content not replaced")
	}
	if !strings.Contains(string(restored), "vp_bootstrap_context") {
		t.Error("canonical content not restored")
	}
	if !strings.HasPrefix(string(restored), "# host\n") {
		t.Error("content outside block corrupted")
	}
}

func TestWireMissingFileErrors(t *testing.T) {
	_, err := Wire(Target{Path: filepath.Join(t.TempDir(), "nope.md")})
	if err == nil {
		t.Error("expected error for missing file")
	}
}
