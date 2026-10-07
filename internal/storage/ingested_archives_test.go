// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"testing"
)

// The legacy archive-ingest ledger reader/writer (IngestedArchives,
// RecordIngestedArchive) and its IngestedArchive row type were retired with the
// capture indexer (ADR-014: the ingest ledger moved into the host-local store).
// Only the path accessor survives, so a one-shot migration can still find and
// delete a legacy ledger; this test pins its slug validation.
func TestIngestedArchivesFileInvalidSlug(t *testing.T) {
	v := testVault(t)
	if _, err := v.IngestedArchivesFile("BAD PROJECT"); err == nil {
		t.Error("IngestedArchivesFile with an invalid slug should return an error")
	}
}
