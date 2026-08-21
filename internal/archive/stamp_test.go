// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// assertStamped fails unless stampDir/.surface records the current surface.
func assertStamped(t *testing.T, stampDir string) {
	t.Helper()
	s, err := surface.ReadStamp(stampDir)
	if err != nil {
		t.Fatalf("ReadStamp(%s): %v", stampDir, err)
	}
	if s.Surface != surface.MCPSurfaceVersion {
		t.Fatalf("stamp at %s = surface %d, want %d", stampDir, s.Surface, surface.MCPSurfaceVersion)
	}
}

// TestArchiveCreateStamps proves archive.Create leaves a .surface stamp at the
// project root (both the manifest and the .jsonl.zst land under
// Projects/<slug>/transcripts/, which maps to Projects/<slug>).
func TestArchiveCreateStamps(t *testing.T) {
	vaultRoot, _ := seedArchive(t, "sess-stamp")
	assertStamped(t, filepath.Join(vaultRoot, "Projects", "demo"))
}

// TestLinkSessionNoteStamps proves LinkSessionNote stamps independently of
// Create: the manifest is written directly (no stamp), then the link call must
// produce the stamp. The fresh temp vault gives a unique stamp-dir path so the
// per-process cache can't mask a missing stamp.
func TestLinkSessionNoteStamps(t *testing.T) {
	vaultRoot := t.TempDir()
	transcripts := filepath.Join(vaultRoot, "Projects", "demo", "transcripts")
	if err := os.MkdirAll(transcripts, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(transcripts, "2026-04-15-sess-link.manifest.json")
	// "" on purpose: this seed must NOT stamp, so the stamp asserted below can
	// only have come from LinkSessionNote.
	if err := WriteManifest("", manifestPath, &Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Adapter:       ClaudeCodeAdapterName,
		SessionID:     "sess-link",
	}); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	if err := LinkSessionNote(vaultRoot, manifestPath, "Projects/demo/sessions/2026-04-15-01.md"); err != nil {
		t.Fatalf("LinkSessionNote: %v", err)
	}
	assertStamped(t, filepath.Join(vaultRoot, "Projects", "demo"))
}
