// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

// TestIntegrationWeightedRoomScoring and TestIntegrationScoringOverrides used to
// capture a transcript and assert that capture filed drawers into the rooms the
// weighted scorer chose (including per-project [palace.scoring] overrides). ADR-014
// decision 7 removes transcript indexing from capture entirely — capture files no
// drawers — so that end-to-end-through-capture path no longer exists. The room
// classifier and the scoring-override behaviour they exercised now run in
// palace.Prepare (used by the pending-archive ingester) and are covered at the
// unit level in internal/palace: TestRoomClassifier_* (metadata_test.go),
// TestRoomClassifierIsDeterministic / _Digest (indexing_test.go),
// TestRunAudit_RoomDistribution / _WithOverrides (audit_test.go), and the
// chunk+classify golden TestPrepareMatchesTheGolden (prepare_test.go). The two
// integration tests are therefore removed with capture's indexer.

// TestIntegrationDrawerIDStableAcrossRooms seeds drawers directly (not through
// capture) and pins that a drawer's id is content-addressed, identical across
// rooms. It does not depend on capture-time indexing, so it stands unchanged.
func TestIntegrationDrawerIDStableAcrossRooms(t *testing.T) {
	h := newHarness(t, false)

	// Add the same content to two different rooms.
	var d1, d2 storage.Drawer
	h.seedProject(t, "proj")
	h.Seed(t,
		testinfra.WithDrawerOut("proj", "wing-a", "testing", "shared content here", "facts", "2026-01-01T10:00:00Z", &d1),
		testinfra.WithDrawerOut("proj", "wing-a", "debugging", "shared content here", "facts", "2026-01-01T10:00:00Z", &d2),
	)

	if d1.ID != d2.ID {
		t.Errorf("drawer IDs should be identical across rooms: %q vs %q", d1.ID, d2.ID)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
