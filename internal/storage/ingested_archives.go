// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
)

// IngestedArchivesFile returns the path to a project's LEGACY archive-ingest
// ledger: {vault}/palace/{project}/ingested-archives.jsonl
//
// The ledger itself is retired: ADR-014 moves the ingest ledger into the
// host-local store, and capture no longer writes drawers or this file. The
// path accessor is kept because the one-shot migration still has to locate and
// delete a legacy ledger left behind by a pre-ADR-014 binary
// (internal/indexstore reads it to do that); it is never written any more.
func (v *Vault) IngestedArchivesFile(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "palace", project, "ingested-archives.jsonl"), nil
}
