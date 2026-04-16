// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LinkSessionNote updates an existing manifest's VaultRelSessionNote
// field in place. Used by vp_capture_session after writing a session
// note so the archive -> note link is bidirectional (ADR-001 Phase 4).
//
// The write is non-atomic in the general sense (no fsync contract),
// but it is crash-safe at the manifest level: a failed write leaves
// the prior manifest intact because we rename-over via a temp file.
func LinkSessionNote(manifestPath, vaultRelSessionNote string) error {
	m, err := ReadManifest(manifestPath)
	if err != nil {
		return err
	}
	if m.VaultRelSessionNote == vaultRelSessionNote {
		return nil // idempotent no-op
	}
	m.VaultRelSessionNote = vaultRelSessionNote
	return atomicWriteManifest(manifestPath, m)
}

// atomicWriteManifest writes via a sibling temp file and rename so a
// crash mid-write cannot corrupt the on-disk manifest.
func atomicWriteManifest(path string, m *Manifest) error {
	tmp := path + ".tmp"
	if err := WriteManifest(tmp, m); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename manifest: %w", err)
	}
	return nil
}

// VaultRelPath returns the vault-relative form of an absolute path,
// with forward slashes regardless of OS. Returns the input unchanged
// if it is not inside vaultRoot.
func VaultRelPath(vaultRoot, abs string) string {
	rel, err := filepath.Rel(vaultRoot, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return filepath.ToSlash(rel)
}
