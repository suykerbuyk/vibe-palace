// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// DeleteLegacyLedgerIfUntracked deletes the legacy ingest ledger,
// palace/<p>/ingested-archives.jsonl, once no drawer is tracked under
// palace/<p>/drawers/ (ADR-014 decision 2). It reports whether it deleted one.
//
// The legacy ledger lists archives whose chunks live in tracked drawers. Once
// those drawers have left git (a pull deleted them), a surviving ledger would
// make the backfill skip every archive it lists, and nothing would ever
// rebuild them. So:
//   - it is keyed on git (storage.Vault.TrackedDrawerFiles), never on the
//     host-local store being absent, which is true on every host until capture
//     is redirected;
//   - a vault that is not a git repository keeps its ledger;
//   - a vault carrying the migration marker (storage.VaultMigrated) deletes it
//     whatever git tracks: a migrated vault has no legacy ledger to honour. A
//     malformed marker keeps the ledger, whatever git tracks, and logs a
//     warning naming vault.toml.
//
// The one caller is the top of the refresh backfill, before it reads the
// ledger (Chair ruling C4). capture-and-backfill-write-host-local-index-only
// deletes that call together with the legacy reader and writer.
func DeleteLegacyLedgerIfUntracked(vault *storage.Vault, project string) (bool, error) {
	path, err := vault.IngestedArchivesFile(project)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("indexstore: stat legacy ledger: %w", err)
	}
	migrated, err := storage.VaultMigrated(vault.Root)
	if err != nil {
		// Kept, whatever the drawers say: a vault.toml nobody can read must not
		// decide a deletion either way.
		slog.Warn("legacy ledger kept: .vibe-palace/vault.toml has a malformed migration marker",
			"project", project, "err", err)
		return false, nil
	}
	if !migrated {
		n, known, err := vault.TrackedDrawerFiles(project)
		if err != nil {
			return false, fmt.Errorf("indexstore: list tracked drawers of %s: %w", project, err)
		}
		if !known || n > 0 {
			return false, nil
		}
	}
	rel, err := filepath.Rel(vault.Root, path)
	if err != nil {
		return false, err
	}
	if _, err := vaultfs.Delete(vault.Root, filepath.ToSlash(rel), ""); err != nil {
		if errors.Is(err, vaultfs.ErrFileNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("indexstore: delete legacy ledger: %w", err)
	}
	return true, nil
}
