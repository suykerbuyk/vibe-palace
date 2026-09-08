// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"fmt"
	"strings"
	"time"

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
// "session/{id}#decision/{n}/{drawerID}".
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
// # Why the index is not enough on its own
//
// The index alone is unique only WITHIN one call. A recapture that revises the
// decision at position 0 files a SECOND drawer — different content, therefore a
// different DrawerID, therefore not deduped by the append — and the superseded
// one stays on disk, which is accepted. What is not acceptable is that both
// would then carry "#decision/0". Engine.Rebuild indexes every drawer in the
// palace, so both reach the index, and dedup would hand back whichever scored
// higher — quite possibly the SUPERSEDED text, with the current decision
// silently missing from the result. Accepting that an old drawer exists is not
// the same as accepting that search returns the wrong one of the two.
//
// So the ref carries a content discriminator: storage.DrawerID(wing, content),
// which is exactly the id AppendDrawers will compute for this same drawer
// (internal/storage/drawers.go). Two consequences fall out, both wanted:
//
//   - Same text at the same position produces the same ref, so a re-file is
//     still one drawer and one search identity — the append dedups it away and
//     nothing changes.
//   - Different text at the same position produces different refs, so the
//     revision and the superseded original are both visible to vp_search and a
//     reader can see that the decision changed.
//
// Reusing DrawerID rather than hashing the content again keeps one definition
// of "which decision is this", and it means the ref's last segment IS the
// drawer's own id — so a search hit names the drawer it came from.
//
// internal/search/iterations.go:31 is the existing precedent, arrived at from
// the other direction: iterationSourceRef is per ENTRY and deliberately NOT per
// chunk, because there the sub-chunks of one iteration entry ARE the same
// retrievable thing and collapsing them is the desired behaviour, with
// ChunkIndex carrying the distinction. The rule both obey is the same one —
// the SourceRef must name exactly the unit a searcher wants back, one hit at a
// time. For a decision that unit is the decision, as revised.
//
// The "#" is not decoration: it keeps the decision ref lexically disjoint from
// the bare session id that capture's transcript chunks file under
// (internal/capture/indexer.go:95), so a decision drawer can never collide in
// dedup with a tape drawer of the very session it was recorded on.
func decisionSourceRef(wing, sessionID, content string, n int) string {
	return fmt.Sprintf("session/%s#decision/%d/%s", sessionID, n, storage.DrawerID(wing, content))
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
func DecisionDrawer(wing, sessionID, content, filedAt string, n int) storage.Drawer {
	return storage.Drawer{
		// The hall is HARDCODED, never classified. palace.Classify would happily
		// read "we should use X" and file this under advice; the writer already
		// KNOWS it is a decision, and a classifier can only turn that certainty
		// back into a guess.
		Hall:       palace.HallDecisions,
		SourceType: storage.SourceTypeDecision,
		Content:    content,
		SourceRef:  decisionSourceRef(wing, sessionID, content, n),
		AddedBy:    "capture",
		FiledAt:    filedAt,
	}
}

// DecisionFiledAt renders a session note's calendar day as the RFC3339 stamp
// its decision drawers carry: midnight UTC on that day.
//
// # Why the note's day and never time.Now()
//
// filed_at answers "which day's work is this decision from", not "when did the
// process happen to write the row". Those come apart on the path that matters
// most. A capture whose inline enrichment misses enqueues a job, and that job
// drains on a LATER run — the hook drains at SessionEnd, which can be days
// after the note was written. Stamping wall-clock there gives Monday's decision
// Thursday's date, and a vp_palace_query bounded by the session's own date then
// does not return it. The drawer is on disk, dated wrong, and invisible to the
// obvious query.
//
// The same rule is why this is not now.UTC() at the live site either. A note's
// day is its CALENDAR day (storage.CalendarDay, the writer's local zone), and
// it is what the session id and the note filename are built from. Stamping UTC
// instead would put every decision captured after local midnight-minus-offset
// on the neighbouring day, so the query's date prefix would disagree with the
// session id the drawer's own source_ref names.
//
// The sibling backfill task rules the same way for historical notes, which is
// why this is exported: one widening rule, one implementation, so a backfilled
// decision and a live one of the same day sort together instead of the
// backfilled one landing under a date-only stamp below every live one.
//
// A malformed day is an ERROR rather than a silent fallback to now. Falling
// back would reintroduce exactly the wall-clock stamp this function exists to
// prevent, on the one input where nobody is watching.
func DecisionFiledAt(noteDate string) (string, error) {
	day, err := time.Parse("2006-01-02", noteDate)
	if err != nil {
		return "", fmt.Errorf("decision filed_at: note date %q is not YYYY-MM-DD: %w", noteDate, err)
	}
	return day.UTC().Format(time.RFC3339), nil
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
func fileDecisionDrawers(vault *storage.Vault, project, sessionID, noteDate string, decisions []string) (int, error) {
	if len(decisions) == 0 {
		return 0, nil
	}

	// Widen here rather than at each call site, so the two writers and the
	// backfill cannot drift into three different notions of filed_at.
	filedAt, err := DecisionFiledAt(noteDate)
	if err != nil {
		return 0, err
	}

	// DetectWing(project, "") is the project's own wing — the same wing
	// capture's transcript indexer files into, so a session's decisions and its
	// tape sit under one roof and a wing-scoped query sees both. It is resolved
	// before the drawers are built because the source_ref discriminator is keyed
	// on it, exactly as the drawer id will be.
	wing := palace.DetectWing(project, "")

	ds := make([]storage.Drawer, 0, len(decisions))
	for n, d := range decisions {
		if strings.TrimSpace(d) == "" {
			continue
		}
		ds = append(ds, DecisionDrawer(wing, sessionID, d, filedAt, n))
	}
	if len(ds) == 0 {
		return 0, nil
	}

	return vault.AppendDrawers(project, wing, DecisionRoom, ds)
}
