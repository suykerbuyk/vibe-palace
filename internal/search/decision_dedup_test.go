// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestDedupKeepsBothDecisionsAndTheTranscriptOfOneSession pins BOTH collision
// modes that the decision SourceRef scheme has to survive, in one result set.
//
// dedup keeps only the FIRST result per exact SourceRef string, so the shape of
// the ref is the whole of a decision's search visibility:
//
//   - decision vs decision: two decisions recorded on ONE note must not share a
//     ref, or the second is unreachable through vp_search forever. This is why
//     capture's ref carries the decision's index and is not the session id.
//   - decision vs transcript: capture's transcript chunks file under the bare
//     session id (internal/capture/indexer.go), so a decision ref that were
//     merely the session id would also collide with the very tape it was
//     recorded on — whichever scored higher would erase the other.
//
// Nothing errors in either case: the drawers sit on disk and simply never
// surface, which is why this is pinned by a test rather than left to review.
//
// The refs below are the REAL strings the two writers produce; they are spelled
// literally because internal/capture imports this package and cannot be
// imported back.
func TestDedupKeepsBothDecisionsAndTheTranscriptOfOneSession(t *testing.T) {
	eng, _ := testEngine(t)
	ctx := context.Background()

	const sessionID = "2026-06-21-abcd1234-01"
	const (
		decisionRef0  = "session/" + sessionID + "#decision/0"
		decisionRef1  = "session/" + sessionID + "#decision/1"
		transcriptRef = sessionID
	)

	inputs := []DrawerInput{
		{Project: "proj", Wing: "proj", Room: "decisions", Drawer: storage.Drawer{
			ID: "dec-0", Content: "We decided to use brute-force vector search",
			Hall: "decisions", SourceType: storage.SourceTypeDecision,
			SourceRef: decisionRef0, FiledAt: "2026-06-21T00:00:00Z", AddedBy: "capture",
		}},
		{Project: "proj", Wing: "proj", Room: "decisions", Drawer: storage.Drawer{
			ID: "dec-1", Content: "We decided to keep brute-force search behind a flag",
			Hall: "decisions", SourceType: storage.SourceTypeDecision,
			SourceRef: decisionRef1, FiledAt: "2026-06-21T00:00:00Z", AddedBy: "capture",
		}},
		{Project: "proj", Wing: "proj", Room: "general", Drawer: storage.Drawer{
			ID: "tape-0", Content: "transcript talking about brute-force search all afternoon",
			Hall: "facts", SourceType: "session",
			SourceRef: transcriptRef, FiledAt: "2026-06-21T00:00:00Z", AddedBy: "capture",
		}},
	}
	if err := eng.IndexDrawers(ctx, inputs); err != nil {
		t.Fatalf("IndexDrawers: %v", err)
	}

	results, err := eng.Search(ctx, "brute-force search", SearchFilters{Project: "proj", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	got := make(map[string]int, len(results))
	for _, r := range results {
		got[r.SourceRef]++
	}
	for _, ref := range []string{decisionRef0, decisionRef1, transcriptRef} {
		if got[ref] != 1 {
			t.Errorf("SourceRef %q survived %d times, want exactly 1 (results=%d, refs=%v)",
				ref, got[ref], len(results), got)
		}
	}
	if len(results) != 3 {
		t.Errorf("got %d results, want 3 (two decisions and one transcript chunk of one session)", len(results))
	}
}

// TestDedupKeepsBothTheSupersededAndRevisedDecisionAtOneIndex pins the content
// discriminator in capture's decision SourceRef.
//
// A recapture that REVISES the decision at position 0 files a second drawer:
// different content means a different DrawerID, so the append cannot dedup it
// away, and the superseded drawer stays on disk. That much is accepted — an old
// drawer existing is a stale record of something that was once true.
//
// What is NOT acceptable is what happens next if both refs read "#decision/0".
// Engine.Rebuild indexes every drawer in the palace, so both reach the index in
// the same dedup bucket, and dedup hands back exactly one of them — whichever
// scored higher for the query. That can be the SUPERSEDED text, with the
// current decision absent from the result entirely. Accepting a stale drawer is
// not the same as accepting that search answers with it.
//
// The refs below carry capture's discriminator — the drawer's own
// storage.DrawerID(wing, content) — which differs precisely because the content
// does. Both survive, and a reader can see the decision was revised.
func TestDedupKeepsBothTheSupersededAndRevisedDecisionAtOneIndex(t *testing.T) {
	eng, _ := testEngine(t)
	ctx := context.Background()

	const sessionID = "2026-06-21-abcd1234-01"
	const (
		supersededRef = "session/" + sessionID + "#decision/0/aaaa1111"
		revisedRef    = "session/" + sessionID + "#decision/0/bbbb2222"
	)

	inputs := []DrawerInput{
		{Project: "proj", Wing: "proj", Room: "decisions", Drawer: storage.Drawer{
			ID: "aaaa1111", Content: "Stamp decision drawers with the wall clock",
			Hall: "decisions", SourceType: storage.SourceTypeDecision,
			SourceRef: supersededRef, FiledAt: "2026-06-21T00:00:00Z", AddedBy: "capture",
		}},
		{Project: "proj", Wing: "proj", Room: "decisions", Drawer: storage.Drawer{
			ID: "bbbb2222", Content: "Stamp decision drawers with the note's own day",
			Hall: "decisions", SourceType: storage.SourceTypeDecision,
			SourceRef: revisedRef, FiledAt: "2026-06-21T00:00:00Z", AddedBy: "capture",
		}},
	}
	if err := eng.IndexDrawers(ctx, inputs); err != nil {
		t.Fatalf("IndexDrawers: %v", err)
	}

	results, err := eng.Search(ctx, "stamp decision drawers", SearchFilters{Project: "proj", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	got := make(map[string]int, len(results))
	for _, r := range results {
		got[r.SourceRef]++
	}
	// The revised one is the answer a reader needs; without the discriminator
	// it is the one that can vanish.
	if got[revisedRef] != 1 {
		t.Errorf("revised decision survived %d times, want 1 (refs=%v)", got[revisedRef], got)
	}
	if got[supersededRef] != 1 {
		t.Errorf("superseded decision survived %d times, want 1 — both should be "+
			"visible so a reader can see the decision changed (refs=%v)",
			got[supersededRef], got)
	}
}
