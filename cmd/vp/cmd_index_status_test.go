// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/ingest"
	"github.com/suykerbuyk/vibe-palace/internal/search"
)

// TestCoverageFailureLimitMatchesIngest guards the coverage path's local copy
// of the ingester's failure limit against ingest.DefaultFailureLimit. The
// search package cannot import ingest (ingest imports search), so this test —
// in a package that imports both — is where the two are pinned together.
func TestCoverageFailureLimitMatchesIngest(t *testing.T) {
	if search.CoverageFailureLimit() != ingest.DefaultFailureLimit {
		t.Fatalf("coverage failure limit = %d, ingest.DefaultFailureLimit = %d — they must match, "+
			"or coverage counts the wrong archives as failing",
			search.CoverageFailureLimit(), ingest.DefaultFailureLimit)
	}
}
