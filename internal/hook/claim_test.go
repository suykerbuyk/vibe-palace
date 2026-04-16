// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaimPath_Format(t *testing.T) {
	got := ClaimPath("/tmp/proj/.vibe-palace", "abc-123")
	want := filepath.Join("/tmp/proj/.vibe-palace", "claimed-abc-123")
	if got != want {
		t.Errorf("ClaimPath = %q, want %q", got, want)
	}
}

func TestIsClaimed_NoClaim(t *testing.T) {
	dir := t.TempDir()
	if IsClaimed(dir, "no-such-session") {
		t.Error("IsClaimed returned true for non-existent claim")
	}
}

func TestWriteClaim_And_IsClaimed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".vibe-palace")

	if err := WriteClaim(dir, "sess-1", "note-2026-04-15-01"); err != nil {
		t.Fatalf("WriteClaim: %v", err)
	}

	if !IsClaimed(dir, "sess-1") {
		t.Error("IsClaimed returned false after WriteClaim")
	}

	// Verify content contains the note ID.
	data, err := os.ReadFile(ClaimPath(dir, "sess-1"))
	if err != nil {
		t.Fatalf("read claim file: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "note-2026-04-15-01" {
		t.Errorf("claim content = %q, want %q", got, "note-2026-04-15-01")
	}
}

func TestWriteClaim_CreatesDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "deep", ".vibe-palace")

	if err := WriteClaim(dir, "sess-2", "note-id"); err != nil {
		t.Fatalf("WriteClaim on non-existent dir: %v", err)
	}

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("WriteClaim did not create the claim directory")
	}

	if !IsClaimed(dir, "sess-2") {
		t.Error("IsClaimed returned false after WriteClaim on new dir")
	}
}
