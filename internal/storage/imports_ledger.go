// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// The host-local ledgers (today: the import marker under palace/.local/imports)
// are git-ignored hints, not vault content, but they are written under the
// vault root, so they go through the same funnel as every other vault write:
// the per-path lock (ADR-003) and appendUnderLock's under-test tripwire.
//
// Both methods hold the lock around the append. A build callback must not take
// the same lock (ADR-003, "RMW callers must not double-acquire").

// AppendHostLocalLine appends data (the newline included) to the host-local
// ledger at absPath under the per-path lock, creating its directory first.
func (v *Vault) AppendHostLocalLine(absPath string, data []byte) error {
	// 🔴 FIRST: the under-test tripwire (EnsureVaultDir), before the lock and before
	// any directory exists, so a refused write leaves nothing behind.
	if err := v.EnsureVaultDir(filepath.Dir(absPath)); err != nil {
		return err
	}
	release, err := vaultlock.Acquire(v.Root, absPath)
	if err != nil {
		return fmt.Errorf("storage: lock %s: %w", absPath, err)
	}
	defer release()
	return v.appendUnderLock(absPath, data)
}

// UpdateHostLocalLedger is the read-then-append form: under the lock it reads
// the ledger (absent is empty), calls build with its bytes, and appends what
// build returns. A nil result appends nothing. build's error is returned as is.
func (v *Vault) UpdateHostLocalLedger(absPath string, build func(current []byte) ([]byte, error)) error {
	if err := v.EnsureVaultDir(filepath.Dir(absPath)); err != nil {
		return err
	}
	release, err := vaultlock.Acquire(v.Root, absPath)
	if err != nil {
		return fmt.Errorf("storage: lock %s: %w", absPath, err)
	}
	defer release()
	current, err := os.ReadFile(absPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("storage: read %s: %w", absPath, err)
	}
	add, err := build(current)
	if err != nil {
		return err
	}
	if len(add) == 0 {
		return nil
	}
	return v.appendUnderLock(absPath, add)
}
