// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"path/filepath"
	"strings"
)

// LinkSessionNote updates an existing manifest's VaultRelSessionNote
// field in place. Used by vp_capture_session after writing a session
// note so the archive -> note link is bidirectional (ADR-001 Phase 4).
//
// The write is crash-safe at the manifest level — a failed write leaves the
// prior manifest intact — because WriteManifest goes through the shared
// temp-plus-rename primitive. There is still no fsync contract.
//
// vaultRoot reaches the primitive's surface stamp; pass "" to skip stamping
// (e.g. manifests outside any vault).
func LinkSessionNote(vaultRoot, manifestPath, vaultRelSessionNote string) error {
	m, err := ReadManifest(manifestPath)
	if err != nil {
		return err
	}
	if m.VaultRelSessionNote == vaultRelSessionNote {
		return nil // idempotent no-op
	}
	m.VaultRelSessionNote = vaultRelSessionNote
	// Was atomicWriteManifest: a hand-rolled WriteManifest-to-".tmp" plus
	// os.Rename. WriteManifest now owns the temp file and the rename, so the
	// wrapper had nothing left to do, and it stamps, so the stampVault call
	// that used to follow this line is gone with it.
	return WriteManifest(vaultRoot, manifestPath, m)
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
