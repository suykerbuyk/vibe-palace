// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
)

// WriteHostLocalWithBackup replaces a file that lives OUTSIDE the vault (the
// global config, a checkout's .vibe-palace.toml) with next, keeping old as
// <path>.bak. It returns the backup's path.
//
// Host-local files are not vault files, so this takes no vaultlock and does
// not use atomicfile's permission/fsync semantics. It keeps a .bak because,
// unlike a vault file, there may be no committed copy standing behind the
// file, so the backup is the only pre-image it has. Every file it writes is
// mode 0644. The write is temp-then-rename, so a reader sees the old bytes or
// the new ones, never a torn mix.
//
// It is the one implementation behind `vp config upgrade` (reconcile's
// host-local branch) and the checkout rebind; the error texts are theirs.
func WriteHostLocalWithBackup(path string, old, next []byte) (string, error) {
	backupPath := path + ".bak"
	if err := os.WriteFile(backupPath, old, 0o644); err != nil {
		return "", fmt.Errorf("create backup: %w", err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, next, 0o644); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("rename: %w", err)
	}
	return backupPath, nil
}
