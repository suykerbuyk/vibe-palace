// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package archive

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteManifest_ReplacesRatherThanTruncatingInPlace is the proof that
// routing WriteManifest through atomicfile.Write is a real atomicity change and
// not an incidental refactor. Without a test that DISCRIMINATES, "it is atomic
// now" is a claim about the call graph rather than about behaviour.
//
// The discriminator is POSIX rename(2), the semantics
// internal/atomicfile/rename_other.go already documents: a rename replaces the
// DIRECTORY ENTRY, and the old inode stays alive until the last descriptor
// closes. A descriptor opened before the rewrite therefore still reads the OLD
// bytes.
//
// The os.WriteFile this replaced opened the SAME inode O_TRUNC and wrote in
// place, so the retained descriptor would have observed the new bytes — and, at
// any instant mid-write, a truncated prefix of them. That window is the torn
// manifest this change closes, on the one file that indexes the transcript it
// accompanies. Verified to fail against the pre-routing implementation before
// this test was kept.
//
// Windows is excluded because the PROOF is shaped by those semantics, not
// because the behaviour is unpinned there: atomicfile's own rename tests cover
// the MoveFileEx replace path and its retry classifier.
func TestWriteManifest_ReplacesRatherThanTruncatingInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-04-15-sess.manifest.json")

	if err := WriteManifest("", path, &Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Adapter:       ClaudeCodeAdapterName,
		SessionID:     "before",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	retained, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer retained.Close()

	if err := WriteManifest("", path, &Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Adapter:       ClaudeCodeAdapterName,
		SessionID:     "after",
	}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	viaOldInode, err := io.ReadAll(retained)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(viaOldInode, []byte(`"session_id": "before"`)) {
		t.Fatalf("the descriptor opened before the rewrite sees %q — the write went "+
			"through the old inode in place, which is NOT an atomic replace", viaOldInode)
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(onDisk, []byte(`"session_id": "after"`)) {
		t.Fatalf("path holds %q, want the rewritten manifest", onDisk)
	}

	// The temp file is the primitive's business and must not outlive the call.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".vp-atomic-") || strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file %q left behind", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("%d entries in the dir, want exactly the manifest", len(entries))
	}
}
