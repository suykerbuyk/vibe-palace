// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// lockedWrite serializes a blind whole-file write of absPath against any other
// vault writer of the same path (CLI vs MCP, or concurrent goroutines) by
// holding the per-path advisory lock across the write. RMW callers that already
// hold the lock must call atomicfile.Write directly instead.
func (v *Vault) lockedWrite(absPath string, data []byte, opts ...atomicfile.Option) error {
	release, err := vaultlock.Acquire(v.Root, absPath)
	if err != nil {
		return fmt.Errorf("storage: lock %s: %w", absPath, err)
	}
	defer release()
	return atomicfile.Write(v.Root, absPath, data, opts...)
}
