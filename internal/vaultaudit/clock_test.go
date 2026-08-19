// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"testing"
	"time"
)

// TestWriterCalendarDay_IsUTCUntilTheVaultTimezoneConfigExists pins option 2b's
// stated DEFAULT.
//
// The instant chosen is the one the whole finding was measured on: 22:57 in
// America/Denver, which is already the NEXT day in UTC. That six-hour evening window
// is where the two clocks disagreed, so it is the only instant worth pinning.
//
// This test says "UTC today" and it says WHY: the vault timezone is not configurable
// yet. When 2b's config lands, this expectation changes to the configured zone and
// this test becomes the place that records the switch — it must not be deleted then,
// it must be re-pointed.
func TestWriterCalendarDay_IsUTCUntilTheVaultTimezoneConfigExists(t *testing.T) {
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skipf("no tzdata for America/Denver on this host: %v", err)
	}

	// 2026-08-12 22:57 MDT == 2026-08-13 04:57 UTC.
	evening := time.Date(2026, 8, 12, 22, 57, 0, 0, denver)

	if got, want := WriterCalendarDay(evening), "2026-08-13"; got != want {
		t.Errorf("WriterCalendarDay(%s) = %q, want %q — the calendar day is derived in the "+
			"vault's timezone, which is UTC until the 2b config exists; a process-local "+
			"answer here is the second clock this ruling removed", evening, got, want)
	}
}

// TestWriterCalendarDay_IgnoresTheInstantsOwnZone proves the derivation does not
// simply echo whatever zone the caller's time.Time happens to carry.
//
// Without this, WriterCalendarDay could be a no-op wrapper over Format and still pass
// the test above by accident of the host's TZ. Two zones, one instant, one answer.
func TestWriterCalendarDay_IgnoresTheInstantsOwnZone(t *testing.T) {
	instant := time.Date(2026, 8, 13, 4, 57, 0, 0, time.UTC)

	zones := []*time.Location{time.UTC, time.FixedZone("minus6", -6*3600), time.FixedZone("plus14", 14*3600)}
	for _, z := range zones {
		if got, want := WriterCalendarDay(instant.In(z)), "2026-08-13"; got != want {
			t.Errorf("WriterCalendarDay(same instant, zone %s) = %q, want %q — the SAME instant "+
				"must yield the SAME calendar day regardless of which zone the caller's "+
				"time.Time is wearing", z, got, want)
		}
	}
}
