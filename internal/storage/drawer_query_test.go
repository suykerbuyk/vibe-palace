// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"testing"
)

// seedDrawers files ds into one room and fails the test if any of them was
// deduped away — a silently-skipped duplicate would make an assertion below
// pass or fail for a reason that has nothing to do with ScanDrawers.
func seedDrawers(t *testing.T, v *Vault, project, wing, room string, ds ...Drawer) {
	t.Helper()
	n, err := v.AppendDrawers(project, wing, room, ds)
	if err != nil {
		t.Fatalf("AppendDrawers(%s/%s): %v", wing, room, err)
	}
	if n != len(ds) {
		t.Fatalf("AppendDrawers(%s/%s) filed %d of %d drawers", wing, room, n, len(ds))
	}
}

// contents projects hits down to the field the fixtures identify them by.
func contents(hits []DrawerHit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Content)
	}
	return out
}

func wantContents(t *testing.T, hits []DrawerHit, want ...string) {
	t.Helper()
	got := contents(hits)
	if len(got) != len(want) {
		t.Fatalf("got %d hits %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hit %d = %q, want %q (full: %v, want %v)", i, got[i], want[i], got, want)
		}
	}
}

func TestScanDrawersRequiresProject(t *testing.T) {
	v := testVault(t)
	if _, err := v.ScanDrawers(DrawerQuery{}); err == nil {
		t.Fatal("ScanDrawers with empty Project: want error, got nil")
	}
}

// filterVault seeds one wing/room with drawers that differ along every filter
// axis at once, so a filter test can assert that exactly one axis moved.
func filterVault(t *testing.T) *Vault {
	t.Helper()
	v := testVault(t)
	seedDrawers(t, v, "proj", "wing-a", "decisions",
		Drawer{Hall: "facts", Content: "chose SQLite for the index", SourceType: "session", FiledAt: "2026-09-01T09:00:00Z"},
		Drawer{Hall: "facts", Content: "chose Go for the CLI", SourceType: "commit", FiledAt: "2026-09-02T09:00:00Z"},
		Drawer{Hall: "decisions-hall", Content: "REJECTED a sidecar index file", SourceType: "iteration", FiledAt: "2026-09-03T09:00:00Z"},
		Drawer{Hall: "decisions-hall", Content: "kept the append-only writer", SourceType: "session", FiledAt: "2026-09-04T09:00:00Z"},
	)
	return v
}

func TestScanDrawersFilterHall(t *testing.T) {
	v := filterVault(t)

	hits, err := v.ScanDrawers(DrawerQuery{Project: "proj", Hall: "facts"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "chose Go for the CLI", "chose SQLite for the index")

	// An empty Hall must not filter at all.
	hits, err = v.ScanDrawers(DrawerQuery{Project: "proj"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	if len(hits) != 4 {
		t.Fatalf("unfiltered scan returned %d hits, want 4: %v", len(hits), contents(hits))
	}

	// A hall nobody used is an empty result, not an error.
	hits, err = v.ScanDrawers(DrawerQuery{Project: "proj", Hall: "no-such-hall"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("unknown hall returned %d hits: %v", len(hits), contents(hits))
	}
}

func TestScanDrawersFilterSourceType(t *testing.T) {
	v := filterVault(t)

	// Single-value set.
	hits, err := v.ScanDrawers(DrawerQuery{Project: "proj", SourceTypes: []string{"commit"}})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "chose Go for the CLI")

	// Multi-value set: membership, not the first entry only.
	hits, err = v.ScanDrawers(DrawerQuery{Project: "proj", SourceTypes: []string{"commit", "iteration"}})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "REJECTED a sidecar index file", "chose Go for the CLI")

	// A source type nobody filed under matches nothing — as distinct from the
	// empty set, which matches everything.
	hits, err = v.ScanDrawers(DrawerQuery{Project: "proj", SourceTypes: []string{"archive"}})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("unknown source type returned %d hits: %v", len(hits), contents(hits))
	}
}

func TestScanDrawersFilterQSubstringIsCaseInsensitive(t *testing.T) {
	v := filterVault(t)

	// Lowercase needle against an UPPERCASE stored word, and the reverse:
	// folding only one side would pass one of these and fail the other.
	hits, err := v.ScanDrawers(DrawerQuery{Project: "proj", Q: "rejected"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "REJECTED a sidecar index file")

	hits, err = v.ScanDrawers(DrawerQuery{Project: "proj", Q: "SQLITE"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "chose SQLite for the index")

	// Substring, not whole word.
	hits, err = v.ScanDrawers(DrawerQuery{Project: "proj", Q: "chose "})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "chose Go for the CLI", "chose SQLite for the index")
}

func TestScanDrawersFiltersAreANDed(t *testing.T) {
	v := filterVault(t)

	// hall + source type + q + date window all at once: each of the four
	// fixtures fails at least one predicate except the one expected back.
	hits, err := v.ScanDrawers(DrawerQuery{
		Project:     "proj",
		Hall:        "facts",
		SourceTypes: []string{"session", "commit"},
		Q:           "chose",
		DateFrom:    "2026-09-02",
		DateTo:      "2026-09-02",
	})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "chose Go for the CLI")

	// One predicate contradicting the rest empties the result.
	hits, err = v.ScanDrawers(DrawerQuery{
		Project:     "proj",
		Hall:        "facts",
		SourceTypes: []string{"iteration"},
	})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("contradictory filters returned %d hits: %v", len(hits), contents(hits))
	}
}

// TestScanDrawersDateBoundsAreInclusiveOfTheDay pins the truncation in
// matchesDateRange. Every fixture stamp here carries a NON-ZERO time of day on
// purpose: with a midnight-only or date-only fixture, comparing the raw
// RFC3339 FiledAt against a YYYY-MM-DD bound would still pass, and the
// regression this test exists for would ship. "2026-09-08T14:23:00Z" is
// lexically GREATER than "2026-09-08", so an untruncated DateTo drops it.
func TestScanDrawersDateBoundsAreInclusiveOfTheDay(t *testing.T) {
	v := testVault(t)
	seedDrawers(t, v, "proj", "wing-a", "decisions",
		Drawer{Hall: "facts", Content: "day before, late", SourceType: "session", FiledAt: "2026-09-07T23:59:59Z"},
		Drawer{Hall: "facts", Content: "target day, early", SourceType: "session", FiledAt: "2026-09-08T00:00:01Z"},
		Drawer{Hall: "facts", Content: "target day, afternoon", SourceType: "session", FiledAt: "2026-09-08T14:23:00Z"},
		Drawer{Hall: "facts", Content: "day after, early", SourceType: "session", FiledAt: "2026-09-09T00:00:01Z"},
	)

	// DateTo is inclusive of the whole closing day.
	hits, err := v.ScanDrawers(DrawerQuery{Project: "proj", DateTo: "2026-09-08"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "target day, afternoon", "target day, early", "day before, late")

	// DateFrom is inclusive of the whole opening day, including a drawer filed
	// one second past midnight and one filed mid-afternoon.
	hits, err = v.ScanDrawers(DrawerQuery{Project: "proj", DateFrom: "2026-09-08"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "day after, early", "target day, afternoon", "target day, early")

	// A single-day window is both bounds on the same date.
	hits, err = v.ScanDrawers(DrawerQuery{Project: "proj", DateFrom: "2026-09-08", DateTo: "2026-09-08"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "target day, afternoon", "target day, early")
}

// TestScanDrawersToleratesShortFiledAt covers the len(date) >= 10 guard: a
// stamp too short to truncate must fail a bounded query rather than panic, and
// must still come back from an unbounded one.
func TestScanDrawersToleratesShortFiledAt(t *testing.T) {
	v := testVault(t)
	seedDrawers(t, v, "proj", "wing-a", "decisions",
		Drawer{Hall: "facts", Content: "no stamp at all", SourceType: "session", FiledAt: ""},
		Drawer{Hall: "facts", Content: "properly stamped", SourceType: "session", FiledAt: "2026-09-08T14:23:00Z"},
	)

	hits, err := v.ScanDrawers(DrawerQuery{Project: "proj"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("unbounded scan returned %d hits, want 2: %v", len(hits), contents(hits))
	}

	hits, err = v.ScanDrawers(DrawerQuery{Project: "proj", DateFrom: "2026-09-01"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "properly stamped")
}

// TestScanDrawersSortsNewestFirstWithIDTiebreak pins BOTH halves of the sort.
//
// The two same-stamp fixtures are seeded in the order (alpha, beta) while their
// deterministic IDs run the other way — DrawerID("wing-a", "same stamp beta")
// is 9dc05683 and DrawerID("wing-a", "same stamp alpha") is f90e965d — so a
// sort that drops the ID tiebreak leaves them in file order and goes red here.
func TestScanDrawersSortsNewestFirstWithIDTiebreak(t *testing.T) {
	v := testVault(t)
	seedDrawers(t, v, "proj", "wing-a", "decisions",
		Drawer{Hall: "facts", Content: "oldest", SourceType: "session", FiledAt: "2026-09-01T08:00:00Z"},
		Drawer{Hall: "facts", Content: "same stamp alpha", SourceType: "session", FiledAt: "2026-09-05T12:00:00Z"},
		Drawer{Hall: "facts", Content: "same stamp beta", SourceType: "session", FiledAt: "2026-09-05T12:00:00Z"},
		Drawer{Hall: "facts", Content: "newest", SourceType: "session", FiledAt: "2026-09-09T08:00:00Z"},
	)

	hits, err := v.ScanDrawers(DrawerQuery{Project: "proj"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "newest", "same stamp beta", "same stamp alpha", "oldest")

	if hits[1].ID >= hits[2].ID {
		t.Fatalf("tie not broken by ascending ID: %q then %q", hits[1].ID, hits[2].ID)
	}
}

// TestScanDrawersLimitAppliesAfterTheSort seeds across two rooms so the walk
// order and the sorted order genuinely differ: a limit applied during the walk
// would keep whichever room was read first, not the newest N overall.
func TestScanDrawersLimitAppliesAfterTheSort(t *testing.T) {
	v := testVault(t)
	seedDrawers(t, v, "proj", "wing-a", "aaa-first-room",
		Drawer{Hall: "facts", Content: "oldest of all", SourceType: "session", FiledAt: "2026-09-01T08:00:00Z"},
		Drawer{Hall: "facts", Content: "second oldest", SourceType: "session", FiledAt: "2026-09-02T08:00:00Z"},
	)
	seedDrawers(t, v, "proj", "wing-a", "zzz-last-room",
		Drawer{Hall: "facts", Content: "newest of all", SourceType: "session", FiledAt: "2026-09-09T08:00:00Z"},
		Drawer{Hall: "facts", Content: "second newest", SourceType: "session", FiledAt: "2026-09-08T08:00:00Z"},
	)

	hits, err := v.ScanDrawers(DrawerQuery{Project: "proj", Limit: 2})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "newest of all", "second newest")

	// A limit at or above the match count is a no-op, and a non-positive limit
	// means no limit at all.
	for _, limit := range []int{0, -1, 4, 99} {
		hits, err := v.ScanDrawers(DrawerQuery{Project: "proj", Limit: limit})
		if err != nil {
			t.Fatalf("ScanDrawers(limit=%d): %v", limit, err)
		}
		if len(hits) != 4 {
			t.Fatalf("limit=%d returned %d hits, want 4: %v", limit, len(hits), contents(hits))
		}
	}
}

// scopeVault seeds two wings, each with two rooms, so wing and room pruning can
// be told apart from filtering.
func scopeVault(t *testing.T) *Vault {
	t.Helper()
	v := testVault(t)
	seedDrawers(t, v, "proj", "wing-a", "decisions",
		Drawer{Hall: "facts", Content: "a/decisions", SourceType: "session", FiledAt: "2026-09-04T08:00:00Z"})
	seedDrawers(t, v, "proj", "wing-a", "general",
		Drawer{Hall: "facts", Content: "a/general", SourceType: "session", FiledAt: "2026-09-03T08:00:00Z"})
	seedDrawers(t, v, "proj", "wing-b", "decisions",
		Drawer{Hall: "facts", Content: "b/decisions", SourceType: "session", FiledAt: "2026-09-02T08:00:00Z"})
	seedDrawers(t, v, "proj", "wing-b", "general",
		Drawer{Hall: "facts", Content: "b/general", SourceType: "session", FiledAt: "2026-09-01T08:00:00Z"})
	return v
}

func TestScanDrawersWingAndRoomScoping(t *testing.T) {
	v := scopeVault(t)

	// Empty wing and empty room: every wing, every room.
	hits, err := v.ScanDrawers(DrawerQuery{Project: "proj"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "a/decisions", "a/general", "b/decisions", "b/general")

	// Pinned wing, empty room: every room in that wing only.
	hits, err = v.ScanDrawers(DrawerQuery{Project: "proj", Wing: "wing-a"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "a/decisions", "a/general")

	// Pinned room, empty wing: that room in EVERY wing.
	hits, err = v.ScanDrawers(DrawerQuery{Project: "proj", Room: "decisions"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "a/decisions", "b/decisions")

	// Both pinned: exactly one room.
	hits, err = v.ScanDrawers(DrawerQuery{Project: "proj", Wing: "wing-b", Room: "decisions"})
	if err != nil {
		t.Fatalf("ScanDrawers: %v", err)
	}
	wantContents(t, hits, "b/decisions")

	// The hits carry the segments the Drawer record does not.
	if hits[0].Wing != "wing-b" || hits[0].Room != "decisions" {
		t.Fatalf("hit segments = %q/%q, want wing-b/decisions", hits[0].Wing, hits[0].Room)
	}
}

// TestScanDrawersHonestEmptyIsNotAnError pins the contract that makes an empty
// result readable: ListWings, ListRooms and readDrawerFile all return (nil,
// nil) for an absent directory or file, so "there is nothing filed" reaches the
// caller as an empty slice rather than as a failure it would have to classify.
func TestScanDrawersHonestEmptyIsNotAnError(t *testing.T) {
	// A vault with no palace store at all.
	empty := testVault(t)
	for _, q := range []DrawerQuery{
		{Project: "proj"},
		{Project: "proj", Wing: "wing-a"},
		{Project: "proj", Wing: "wing-a", Room: "decisions"},
		{Project: "proj", Room: "decisions"},
	} {
		hits, err := empty.ScanDrawers(q)
		if err != nil {
			t.Fatalf("ScanDrawers(%+v) on storeless vault: %v", q, err)
		}
		if len(hits) != 0 {
			t.Fatalf("ScanDrawers(%+v) on storeless vault returned %d hits", q, len(hits))
		}
	}

	// A populated vault whose wing has no `decisions` room.
	v := testVault(t)
	seedDrawers(t, v, "proj", "wing-a", "general",
		Drawer{Hall: "facts", Content: "only a general drawer", SourceType: "session", FiledAt: "2026-09-08T14:23:00Z"})

	hits, err := v.ScanDrawers(DrawerQuery{Project: "proj", Wing: "wing-a", Room: "decisions"})
	if err != nil {
		t.Fatalf("ScanDrawers on a wing with no decisions room: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("missing room returned %d hits: %v", len(hits), contents(hits))
	}

	// And a wing that does not exist in a project that does.
	hits, err = v.ScanDrawers(DrawerQuery{Project: "proj", Wing: "wing-z"})
	if err != nil {
		t.Fatalf("ScanDrawers on a missing wing: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("missing wing returned %d hits: %v", len(hits), contents(hits))
	}
}

// TestScanDrawersRejectsBadSlugs proves the walk still routes through the
// existing slug validation rather than reaching for a path of its own: a wing
// pinned to an invalid slug skips ListWings, so ListRooms is the gate.
func TestScanDrawersRejectsBadSlugs(t *testing.T) {
	v := scopeVault(t)

	if _, err := v.ScanDrawers(DrawerQuery{Project: "proj", Wing: "../escape"}); err == nil {
		t.Fatal("ScanDrawers with a traversal wing: want error, got nil")
	}
	if _, err := v.ScanDrawers(DrawerQuery{Project: "proj", Wing: "wing-a", Room: "../escape"}); err == nil {
		t.Fatal("ScanDrawers with a traversal room: want error, got nil")
	}
	if _, err := v.ScanDrawers(DrawerQuery{Project: "../escape"}); err == nil {
		t.Fatal("ScanDrawers with a traversal project: want error, got nil")
	}
}
