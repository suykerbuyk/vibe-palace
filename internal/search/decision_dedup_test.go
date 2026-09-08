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
