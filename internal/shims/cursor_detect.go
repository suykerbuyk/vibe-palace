// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"os"
	"path/filepath"
)

// CursorPresent reports whether the given project root looks like a
// Cursor-enabled project for the purpose of emitting Cursor rule shims.
//
// Strict rule per the vps-skill-artifacts-cross-ide plan: the CursorRule
// shim is emitted when either `.cursor/rules/` or `.cursor/` exists at
// projectRoot. The presence of `.cursorrules` alone (the flat-file Cursor
// surface, already handled by agentfile.Detect) is **not** a trigger —
// bootstrapping a new `.cursor/rules/` tree from a `.cursorrules`-only
// project presumes a Cursor surface the user never opted into.
//
// Returns true when:
//   - projectRoot/.cursor/rules/ exists and is a directory (primary signal).
//   - projectRoot/.cursor/ exists and is a directory (weaker — still true).
//
// Returns false when only `.cursorrules` (flat file) exists, or when
// neither `.cursor/` nor `.cursor/rules/` is present.
func CursorPresent(projectRoot string) bool {
	if info, err := os.Stat(filepath.Join(projectRoot, CursorRulesDir)); err == nil && info.IsDir() {
		return true
	}
	if info, err := os.Stat(filepath.Join(projectRoot, ".cursor")); err == nil && info.IsDir() {
		return true
	}
	return false
}
