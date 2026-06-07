// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package agentfile

import (
	"os"
	"path/filepath"
)

// EnsureManaged guarantees the agent file at path exists and carries the
// managed vibe-palace block, creating an empty file first when missing
// (Wire requires the file to exist). displayName is used in the wired block.
func EnsureManaged(path, displayName string) (Result, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
			return Result{}, err
		}
	} else if err != nil {
		return Result{}, err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		canonical = path
	}
	return Wire(Target{Path: canonical, DisplayName: displayName})
}
