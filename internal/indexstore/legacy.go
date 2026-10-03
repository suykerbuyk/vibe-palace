// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"errors"
	"fmt"
	"io/fs"
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
//   - index-fingerprints-project-lifecycle-and-migration-marker adds the
//     second condition, the migration marker.
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
	n, known, err := vault.TrackedDrawerFiles(project)
	if err != nil {
		return false, fmt.Errorf("indexstore: list tracked drawers of %s: %w", project, err)
	}
	if !known || n > 0 {
		return false, nil
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
