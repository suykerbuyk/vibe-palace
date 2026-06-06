// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"log/slog"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// stamp records the MCP surface version for a successful vault write at path.
//
// Whole-file writers stamp implicitly via atomicfile.Write; this helper is for
// the append-style writers (JSONL appends, O_EXCL triple creates) that cannot
// route through the whole-file primitive. Best-effort: a stamp failure is
// logged and never propagated, so it can never fail the underlying write.
func (v *Vault) stamp(path string) {
	if err := surface.StampForPath(v.Root, path); err != nil {
		slog.Warn("surface stamp failed", "path", path, "err", err)
	}
}
