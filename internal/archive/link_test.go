// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"path/filepath"
	"testing"
)

func TestLinkSessionNote_WritesAndPersists(t *testing.T) {
	vaultRoot, res := seedArchive(t, "sess-link")

	rel := "Projects/demo/sessions/2026-04-15-01.md"
	if err := LinkSessionNote(vaultRoot, res.ManifestPath, rel); err != nil {
		t.Fatalf("LinkSessionNote: %v", err)
	}
	m, err := ReadManifest(res.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.VaultRelSessionNote != rel {
		t.Errorf("vault_rel_session_note = %q, want %q", m.VaultRelSessionNote, rel)
	}

	// Idempotent: second call with same value is a no-op success.
	if err := LinkSessionNote(vaultRoot, res.ManifestPath, rel); err != nil {
		t.Errorf("second LinkSessionNote: %v", err)
	}
}

func TestVaultRelPath(t *testing.T) {
	root := filepath.FromSlash("/vault")
	tests := []struct {
		abs, want string
	}{
		{filepath.FromSlash("/vault/Projects/demo/transcripts/x.manifest.json"), "Projects/demo/transcripts/x.manifest.json"},
		{filepath.FromSlash("/elsewhere/x"), filepath.FromSlash("/elsewhere/x")},
	}
	for _, tt := range tests {
		if got := VaultRelPath(root, tt.abs); got != tt.want {
			t.Errorf("VaultRelPath(%q) = %q, want %q", tt.abs, got, tt.want)
		}
	}
}
