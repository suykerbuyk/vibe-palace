// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
)

// writeTestManifest is a TEST-ONLY unsigned manifest write (archive has no
// unsigned production writer; see archive.writeManifestBytes).
func writeTestManifest(vaultRoot, path string, m *archive.Manifest) error {
	b, err := archive.MarshalManifest(m)
	if err != nil {
		return err
	}
	return atomicfile.Write(vaultRoot, path, b)
}
