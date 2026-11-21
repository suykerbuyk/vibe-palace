// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import "github.com/suykerbuyk/vibe-palace/internal/atomicfile"

// WriteManifest is a TEST-ONLY unsigned manifest write for this package's tests
// (production has no unsigned manifest writer: every write goes through
// writeManifestBytes). vaultRoot "" writes an UNSTAMPED manifest outside a vault.
func WriteManifest(vaultRoot, path string, m *Manifest) error {
	b, err := MarshalManifest(m)
	if err != nil {
		return err
	}
	return atomicfile.Write(vaultRoot, path, b)
}
