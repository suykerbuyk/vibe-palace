// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"fmt"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// DecisionRoom is the palace room every decision drawer is filed into. It is a
// PATH SEGMENT — the {room} in palace/{project}/drawers/{wing}/{room}/drawers.jsonl
// — and it is FIXED rather than classified from the drawer's content: a decision
// is filed here because the writer knows it is a decision, not because a
// classifier put it here.
//
// It is one exported symbol precisely because it would otherwise be a bare
// "decisions" literal at three call sites across three tasks — the writer that
// files the drawer, the resolver that defaults an unqualified query to this
// room, and the query that reads it back. A drift in any ONE of them does not
// fail: it yields an empty default query, which is indistinguishable at the
// wire from an honestly-empty palace. The reader sees "no decisions recorded"
// and believes it. A constant makes the three agree by construction.
//
// The symbol lives on the capture side rather than on the drawer type because
// storage.Drawer has no Room field at all: the room reaches storage only as an
// argument to AppendDrawers, so there is no drawer-shaped place to hang it.
const DecisionRoom = "decisions"

// decisionSourceRef builds the search identity of ONE decision:
// "session/{id}#decision/{n}".
//
// It is per DECISION, not per session, and that is the whole reason the
// function exists rather than the session id being passed straight through.
// search.dedup (internal/search/engine.go) keeps only the FIRST result it sees
// for an exact SourceRef string and silently drops the rest — it is what stops
// eight adjacent transcript chunks of one session from filling a result page.
// Hand it a session-level ref for every decision of a session and it applies
// that same rule to them: a note carrying five decisions returns exactly one
// vp_search hit, chosen by score, and the other four are unreachable through
// search forever. Nothing errors; the drawers are on disk and simply never
// surface.
//
// internal/search/iterations.go:31 is the existing precedent, arrived at from
// the other direction: iterationSourceRef is per ENTRY and deliberately NOT per
// chunk, because there the sub-chunks of one iteration entry ARE the same
// retrievable thing and collapsing them is the desired behaviour, with
// ChunkIndex carrying the distinction. The rule both obey is the same one —
// the SourceRef must name exactly the unit a searcher wants back, one hit at a
// time. For a decision that unit is the decision.
//
// The "#" is not decoration: it keeps the decision ref lexically disjoint from
// the bare session id that capture's transcript chunks file under
// (internal/capture/indexer.go:95), so a decision drawer can never collide in
// dedup with a tape drawer of the very session it was recorded on.
func decisionSourceRef(sessionID string, n int) string {
	return fmt.Sprintf("session/%s#decision/%d", sessionID, n)
}

// DecisionDrawer builds the drawer for one decision. It is exported and it is
// the ONLY place a decision drawer is constructed, so the field set below is
// the definition of what a decision drawer IS — the writer here, the query
// that defaults to SourceTypeDecision, and any later reader all describe the
// same record because there is only one that makes it.
//
// Two omissions are deliberate:
//
//   - ID is left zero. storage.AppendDrawers overwrites it unconditionally with
//     DrawerID(wing, content) (internal/storage/drawers.go:146), and that
//     content hash is what makes the append idempotent: re-filing the same
//     decision of the same session is a no-op rather than a duplicate. Setting
//     an ID here would be dead code that reads like a contract.
//
//   - ChunkIndex is left zero and MEANS zero. It exists for the chunker, which
//     splits one long source into an ordered run; a decision is not chunked,
//     it is filed verbatim as one drawer, and its position among the note's
//     decisions already lives in SourceRef where dedup can see it.
//
// Content is stored VERBATIM. A decision is the operator's own sentence and the
// palace's job is to hand it back unaltered; nothing here trims, truncates or
// reformats it.
func DecisionDrawer(sessionID, content, filedAt string, n int) storage.Drawer {
	return storage.Drawer{
		// The hall is HARDCODED, never classified. palace.Classify would happily
		// read "we should use X" and file this under advice; the writer already
		// KNOWS it is a decision, and a classifier can only turn that certainty
		// back into a guess.
		Hall:       palace.HallDecisions,
		SourceType: storage.SourceTypeDecision,
		Content:    content,
		SourceRef:  decisionSourceRef(sessionID, n),
		AddedBy:    "capture",
		FiledAt:    filedAt,
	}
}

// fileDecisionDrawers files a session note's decisions into DecisionRoom, one
// drawer per decision, and returns how many were appended.
//
// # Indices are positions in the slice, and skipped blanks leave a hole
//
// n is the index of the decision in the SLICE PASSED IN, not a counter of
// drawers written. An entry that is empty after TrimSpace is skipped and its
// index is simply not used: ["a", "  ", "c"] files refs .../0 and .../2, never
// .../0 and .../1. That costs nothing and buys a real property — the ref is a
// stable pointer back to the note's Nth decision, so a reader holding a drawer
// can go to the note and find the line it came from. Renumbering around the
// blank would break that silently, and would also make the SAME decision file
// under a DIFFERENT ref depending on whether some unrelated earlier entry
// happened to be blank on that capture, which turns a re-file into a duplicate
// drawer rather than the intended no-op.
//
// # One batched append
//
// The whole slice goes through a single AppendDrawers call. AppendDrawers pays
// its duplicate scan — a full read and unmarshal of the room's JSONL — ONCE per
// call, so a loop over AppendDrawer here would be O(decisions × drawers already
// in the room) for no benefit; see the batch-shape rationale on AppendDrawers
// itself. It also means the note's decisions land atomically: all of them or,
// on an error, none.
//
// The room is not created here. AppendDrawers ensures its own parent directory.
func fileDecisionDrawers(vault *storage.Vault, project, sessionID, filedAt string, decisions []string) (int, error) {
	if len(decisions) == 0 {
		return 0, nil
	}

	ds := make([]storage.Drawer, 0, len(decisions))
	for n, d := range decisions {
		if strings.TrimSpace(d) == "" {
			continue
		}
		ds = append(ds, DecisionDrawer(sessionID, d, filedAt, n))
	}
	if len(ds) == 0 {
		return 0, nil
	}

	// DetectWing(project, "") is the project's own wing — the same wing
	// capture's transcript indexer files into, so a session's decisions and its
	// tape sit under one roof and a wing-scoped query sees both.
	wing := palace.DetectWing(project, "")
	return vault.AppendDrawers(project, wing, DecisionRoom, ds)
}
