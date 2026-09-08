// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/enrichment"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// listDecisionDrawers reads a project's decision room back through the vault
// API. It goes through ListDrawers rather than os.ReadFile on a hand-built
// path so the test asserts what a READER of the palace would actually see —
// the same route the query tool takes — instead of re-deriving the storage
// layout and passing while the layout the server uses has moved.
func listDecisionDrawers(t *testing.T, vault *storage.Vault, project string) []storage.Drawer {
	t.Helper()
	ds, err := vault.ListDrawers(project, palace.DetectWing(project, ""), DecisionRoom)
	if err != nil {
		t.Fatalf("ListDrawers(%s): %v", project, err)
	}
	return ds
}

// sourceRefs returns the drawers' source refs, sorted, so an assertion on the
// SET of refs does not depend on append order.
func sourceRefs(ds []storage.Drawer) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.SourceRef)
	}
	sort.Strings(out)
	return out
}

// breakDecisionRoom makes the decision room's drawers.jsonl unopenable as a
// file by creating a DIRECTORY at exactly that path. AppendDrawers reads the
// file before appending, and os.ReadFile on a directory fails with EISDIR,
// which is not os.IsNotExist — so the append returns an error without any
// mocking seam. Verified by TestFileDecisionDrawersReportsAppendFailure below,
// which is the guard on this trick still working.
func breakDecisionRoom(t *testing.T, vault *storage.Vault, project string) {
	t.Helper()
	path, err := vault.DrawerFile(project, palace.DetectWing(project, ""), DecisionRoom)
	if err != nil {
		t.Fatalf("DrawerFile: %v", err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir over drawers.jsonl: %v", err)
	}
}

// TestFileDecisionDrawersReportsAppendFailure pins the failure injection the
// two accumulate-don't-return tests below rely on: with a directory sitting at
// the drawers.jsonl path, the append really does error. If this ever stops
// being true those tests would pass vacuously, asserting nothing.
func TestFileDecisionDrawersReportsAppendFailure(t *testing.T) {
	vault := testVault(t)
	breakDecisionRoom(t, vault, "test-proj")

	n, err := fileDecisionDrawers(vault, "test-proj", "2026-06-21-abcd1234-01",
		"2026-06-21", []string{"a decision"})
	if err == nil {
		t.Fatalf("append over a directory returned nil error (n=%d); the failure injection no longer works", n)
	}
	if n != 0 {
		t.Errorf("appended = %d on error, want 0", n)
	}
}

// TestWriteSessionFilesDecisionDrawersWithNilIndexer is the DEFAULT case, not
// an edge case: internal/hook/hook.go calls WriteSession with a nil indexer on
// every hook-driven capture, so an ingest that hid behind an indexer guard
// would file nothing on the busiest path in the product.
func TestWriteSessionFilesDecisionDrawersWithNilIndexer(t *testing.T) {
	vault := testVault(t)

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:   "test-proj",
		Summary:   "Stopped HTML-escaping baseline JSON",
		Decisions: []string{"Use SetEscapeHTML(false)"},
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	if result.Failed() {
		t.Fatalf("unexpected failures: %+v", result.Failures)
	}

	ds := listDecisionDrawers(t, vault, "test-proj")
	if len(ds) != 1 {
		t.Fatalf("filed %d decision drawers, want 1: %+v", len(ds), ds)
	}
	d := ds[0]
	if d.Content != "Use SetEscapeHTML(false)" {
		t.Errorf("content = %q, want the decision verbatim", d.Content)
	}
	if d.SourceType != storage.SourceTypeDecision {
		t.Errorf("source_type = %q, want %q", d.SourceType, storage.SourceTypeDecision)
	}
	if d.Hall != palace.HallDecisions {
		t.Errorf("hall = %q, want %q", d.Hall, palace.HallDecisions)
	}
	assertRefShape(t, ds, result.SessionID, []int{0})
	if d.AddedBy != "capture" {
		t.Errorf("added_by = %q, want capture", d.AddedBy)
	}
	if want := result.SessionID[:10] + "T00:00:00Z"; d.FiledAt != want {
		t.Errorf("filed_at = %q, want %q (the note's day)", d.FiledAt, want)
	}
	if _, perr := time.Parse(time.RFC3339, d.FiledAt); perr != nil {
		t.Errorf("filed_at %q is not RFC3339: %v", d.FiledAt, perr)
	}
	if d.ID == "" {
		t.Error("id is empty; AppendDrawers should have stamped the content hash")
	}
}

// TestWriteSessionNoDecisionsFilesNothing: a capture with no decisions must
// leave the decision room empty AND still write its note. An ingest that
// created an empty room, or that treated "nothing to file" as an error, would
// make every summary-only capture look like a partial failure.
func TestWriteSessionNoDecisionsFilesNothing(t *testing.T) {
	vault := testVault(t)

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj",
		Summary: "Read some code",
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	if result.Failed() {
		t.Fatalf("unexpected failures: %+v", result.Failures)
	}
	if ds := listDecisionDrawers(t, vault, "test-proj"); len(ds) != 0 {
		t.Fatalf("filed %d drawers for a decision-less capture, want 0: %+v", len(ds), ds)
	}

	// The note still landed.
	if _, _, rerr := vault.ReadSession("test-proj", result.SessionID[:10],
		ParseFingerprint(result.SessionID), result.Iteration); rerr != nil {
		t.Fatalf("ReadSession: %v", rerr)
	}
}

// TestDecisionSourceRefsAreUniquePerDecision is the search-visibility
// regression. search.dedup keeps only the first result per exact SourceRef, so
// a session-level ref would make a three-decision session return exactly one
// vp_search hit and silently strand the other two.
func TestDecisionSourceRefsAreUniquePerDecision(t *testing.T) {
	vault := testVault(t)

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj",
		Summary: "Three decisions",
		Decisions: []string{
			"Pin the ratchet drop",
			"Refuse a second accept",
			"Qualify composite-literal assignments",
		},
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	ds := listDecisionDrawers(t, vault, "test-proj")
	if len(ds) != 3 {
		t.Fatalf("filed %d drawers, want 3: %+v", len(ds), ds)
	}
	assertRefShape(t, ds, result.SessionID, []int{0, 1, 2})
}

// TestBlankDecisionsSkippedWithoutRenumbering: a blank entry is skipped and its
// index is LEFT UNUSED, so a ref keeps pointing at the note's Nth decision.
// Renumbering would compact ["first", "  ", "third"] to refs 0 and 1, which
// makes "third" answer to the index of a decision it is not.
func TestBlankDecisionsSkippedWithoutRenumbering(t *testing.T) {
	vault := testVault(t)

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:   "test-proj",
		Summary:   "One blank in the middle",
		Decisions: []string{"first", "   ", "third"},
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	ds := listDecisionDrawers(t, vault, "test-proj")
	if len(ds) != 2 {
		t.Fatalf("filed %d drawers, want 2: %+v", len(ds), ds)
	}
	assertRefShape(t, ds, result.SessionID, []int{0, 2})
}

// TestWriteSessionDecisionAppendFailureIsAccumulated: the note is capture's one
// irreplaceable output. A drawers.jsonl that cannot be written must cost a
// Failures entry, never the session.
func TestWriteSessionDecisionAppendFailureIsAccumulated(t *testing.T) {
	vault := testVault(t)
	breakDecisionRoom(t, vault, "test-proj")

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:   "test-proj",
		Summary:   "Decisions cannot be filed",
		Decisions: []string{"a decision"},
	})
	if err != nil {
		t.Fatalf("WriteSession returned an error; the note must survive a drawer failure: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
	if result.NotePath == "" {
		t.Error("note_path is empty; the note should still have landed")
	}
	if _, _, rerr := vault.ReadSession("test-proj", result.SessionID[:10],
		ParseFingerprint(result.SessionID), result.Iteration); rerr != nil {
		t.Fatalf("note is not readable: %v", rerr)
	}

	var found bool
	for _, f := range result.Failures {
		if f.Stage == StagePalaceDecisionIngest {
			found = true
		}
	}
	if !found {
		t.Errorf("no %s failure recorded; got %+v", StagePalaceDecisionIngest, result.Failures)
	}
}

// TestDrainFilesDecisionDrawers is the HOOK path end to end. The hook passes no
// Decisions at all, so its notes have decisions only once an enrichment
// produces them — and when the inline enricher misses, that happens on the
// drain. The inline enricher here therefore returns an all-empty result (a
// miss that enqueues a job); a stubbed SUCCESSFUL inline enricher would file
// its drawers at WriteSession time and never exercise the drain at all.
func TestDrainFilesDecisionDrawers(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()

	missing := enrichment.NewEnricher(mockCompleter{resp: emptyEnrichment}, "test-model", 5*time.Second, "")
	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "plain heuristic summary",
		Transcript: enrichTranscript,
		Enricher:   missing,
		CWD:        cwd,
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	// Nothing filed yet: the inline enrichment produced no decisions.
	if ds := listDecisionDrawers(t, vault, "test-proj"); len(ds) != 0 {
		t.Fatalf("decision drawers exist before the drain: %+v", ds)
	}
	queue := filepath.Join(cwd, ".vibe-palace", "enrichment-queue")
	if jobs, _ := filepath.Glob(filepath.Join(queue, "*.json")); len(jobs) != 1 {
		t.Fatalf("expected 1 queued job, found %d", len(jobs))
	}

	drained, err := DrainEnrichmentQueue(context.Background(), vault, cwd, workingEnricher(), 0)
	if err != nil {
		t.Fatalf("DrainEnrichmentQueue: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained = %d, want 1", drained)
	}

	ds := listDecisionDrawers(t, vault, "test-proj")
	if len(ds) != 1 {
		t.Fatalf("filed %d drawers after the drain, want 1: %+v", len(ds), ds)
	}
	if ds[0].Content != "LLM decision one" {
		t.Errorf("content = %q, want the drained LLM decision", ds[0].Content)
	}
	if ds[0].SourceType != storage.SourceTypeDecision {
		t.Errorf("source_type = %q, want %q", ds[0].SourceType, storage.SourceTypeDecision)
	}
	assertRefShape(t, ds, result.SessionID, []int{0})

	// The drain's stamp is the NOTE's day, not the day the drain happened to
	// run — the whole reason a queued job cannot use wall-clock.
	if want := result.SessionID[:10] + "T00:00:00Z"; ds[0].FiledAt != want {
		t.Errorf("filed_at = %q, want %q (the note's day)", ds[0].FiledAt, want)
	}
}

// TestDrainDecisionAppendFailureDoesNotRequeue: the note was already rewritten
// when the drawer append fails. Requeueing would re-run enricher.Enrich — real
// LLM work — and rewrite an already-correct note, to recover a retrieval loss.
// The job must be consumed: not returned to the queue, not dead-lettered.
func TestDrainDecisionAppendFailureDoesNotRequeue(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()
	breakDecisionRoom(t, vault, "test-proj")

	missing := enrichment.NewEnricher(mockCompleter{resp: emptyEnrichment}, "test-model", 5*time.Second, "")
	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "plain heuristic summary",
		Transcript: enrichTranscript,
		Enricher:   missing,
		CWD:        cwd,
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	drained, err := DrainEnrichmentQueue(context.Background(), vault, cwd, workingEnricher(), 0)
	if err != nil {
		t.Fatalf("DrainEnrichmentQueue: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained = %d, want 1 (a drawer failure must not un-drain the job)", drained)
	}

	queue := filepath.Join(cwd, ".vibe-palace", "enrichment-queue")
	for _, pattern := range []string{"*.json", "*.processing", "*.failed"} {
		leftovers, _ := filepath.Glob(filepath.Join(queue, pattern))
		if len(leftovers) != 0 {
			t.Errorf("queue still holds %s: %v", pattern, leftovers)
		}
	}

	// The rewrite stands.
	meta, _, rerr := vault.ReadSession("test-proj", result.SessionID[:10],
		ParseFingerprint(result.SessionID), result.Iteration)
	if rerr != nil {
		t.Fatalf("ReadSession: %v", rerr)
	}
	if meta.EnrichedBy != "test-model" {
		t.Errorf("enriched_by = %q, want test-model; the note must stay enriched", meta.EnrichedBy)
	}
	if len(meta.Decisions) != 1 || meta.Decisions[0] != "LLM decision one" {
		t.Errorf("decisions = %v, want the drained LLM decision", meta.Decisions)
	}
}

// TestWriteSessionFilesWhatLandedNotTheParams is the subtle one, and it is the
// reason the ingest re-reads an UPDATED note instead of using the local meta.
//
// mergeCaptureMeta keeps the note's ENRICHED decisions when the re-capture did
// not itself enrich, so on that branch the caller's Decisions are exactly what
// was NOT written. Filing from the local meta would put a drawer in the palace
// for a decision no note ever contained — invented memory. Written against
// that naive implementation this test fails on the extra drawer.
func TestWriteSessionFilesWhatLandedNotTheParams(t *testing.T) {
	vault := testVault(t)
	const key = "retry-key-1"

	// First capture: enriched, so the note carries the LLM's decision and an
	// enriched_by stamp.
	first, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "plain heuristic summary",
		Transcript: enrichTranscript,
		Enricher:   workingEnricher(),
		SessionKey: key,
	})
	if err != nil {
		t.Fatalf("WriteSession(first): %v", err)
	}

	// Second capture of the SAME attempt, with no enricher and a different
	// decisions list. mergeCaptureMeta preserves the enriched narrative whole,
	// so the caller's list never reaches disk.
	second, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "plain heuristic summary",
		Decisions:  []string{"a decision the note never carried"},
		SessionKey: key,
	})
	if err != nil {
		t.Fatalf("WriteSession(second): %v", err)
	}
	if !second.Updated {
		t.Fatalf("second capture minted a new note (%s vs %s); the merge path was not exercised",
			second.SessionID, first.SessionID)
	}

	meta, _, err := vault.ReadSession("test-proj", second.SessionID[:10],
		ParseFingerprint(second.SessionID), second.Iteration)
	if err != nil {
		t.Fatalf("ReadSession: %v", err)
	}
	if len(meta.Decisions) != 1 || meta.Decisions[0] != "LLM decision one" {
		t.Fatalf("precondition failed: note on disk carries %v, expected the enriched decision", meta.Decisions)
	}

	ds := listDecisionDrawers(t, vault, "test-proj")
	if len(ds) != 1 {
		t.Fatalf("filed %d drawers, want 1 (only what landed on the note): %+v", len(ds), ds)
	}
	if ds[0].Content != "LLM decision one" {
		t.Errorf("content = %q, want the note's own decision", ds[0].Content)
	}
	for _, d := range ds {
		if d.Content == "a decision the note never carried" {
			t.Errorf("filed a drawer for a decision no note ever contained: %+v", d)
		}
	}
}

// TestDecisionRoomDoesNotDependOnContent pins the room as UNCONDITIONAL.
//
// The room a decision lands in is asserted by this writer, never inferred from
// what the decision says. The two fixtures below are chosen to be exactly the
// inputs that would move a classified drawer somewhere else:
//
//   - the decision text is dense with tier-3 room keywords ("kubernetes",
//     "terraform", "deploy" all score the devops room in defaultRoomKeywords);
//   - files_changed names a path the filename tier maps to a room outright
//     (Dockerfile -> devops, foo_test.go -> testing).
//
// Both must be inert. A drawer that landed in devops here would be invisible
// to the palace query's default, which prunes to DecisionRoom — and invisible
// in the silent way, returning an empty result indistinguishable from a palace
// that honestly holds no decisions.
//
// This is the regression test for re-introducing a classifier on this path. The
// contract deletes the classification step rather than calling Classify with an
// empty content string to defeat it, so there is no seam to assert against;
// asserting the OUTCOME is what survives a future refactor.
func TestDecisionRoomDoesNotDependOnContent(t *testing.T) {
	vault := testVault(t)

	res, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:      "test-proj",
		Summary:      "a session that touches deployment plumbing",
		Decisions:    []string{"Deploy the kubernetes cluster with terraform rather than by hand"},
		FilesChanged: []string{"Dockerfile", "internal/capture/decisions_test.go"},
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	ds := listDecisionDrawers(t, vault, "test-proj")
	if len(ds) != 1 {
		t.Fatalf("filed %d drawers into %q, want 1 — a classifier would have "+
			"routed this text to devops", len(ds), DecisionRoom)
	}
	if ds[0].Hall != palace.HallDecisions {
		t.Errorf("hall = %q, want %q — the hall is hardcoded, never DetectHall'd",
			ds[0].Hall, palace.HallDecisions)
	}
	assertRefShape(t, ds, res.SessionID, []int{0})

	// And nothing was filed into the room the classifier would have chosen.
	devops, err := vault.ListDrawers("test-proj", palace.DetectWing("test-proj", ""), "devops")
	if err != nil {
		t.Fatalf("ListDrawers(devops): %v", err)
	}
	if len(devops) != 0 {
		t.Errorf("devops room holds %d drawers, want 0: %+v", len(devops), devops)
	}
}

// assertRefShape checks the drawers' source refs against the ref contract:
// "session/{id}#decision/{n}/{drawerID}", one per expected index, in index
// order.
//
// The last segment is asserted to be the drawer's OWN id rather than a literal,
// which is the point of that segment: decisionSourceRef derives it with the
// same storage.DrawerID(wing, content) that AppendDrawers stamps onto the
// drawer, so a search hit names the drawer it came from. Spelling the hash out
// as a constant here would assert only that md5 is md5.
func assertRefShape(t *testing.T, ds []storage.Drawer, sessionID string, wantIdx []int) {
	t.Helper()
	if len(ds) != len(wantIdx) {
		t.Fatalf("got %d drawers, want %d: %+v", len(ds), len(wantIdx), ds)
	}
	byRef := make(map[string]storage.Drawer, len(ds))
	for _, d := range ds {
		byRef[d.SourceRef] = d
	}
	got := sourceRefs(ds)
	sort.Strings(got)
	for i, n := range wantIdx {
		prefix := fmt.Sprintf("session/%s#decision/%d/", sessionID, n)
		if !strings.HasPrefix(got[i], prefix) {
			t.Errorf("source_ref[%d] = %q, want prefix %q", i, got[i], prefix)
			continue
		}
		d := byRef[got[i]]
		if suffix := strings.TrimPrefix(got[i], prefix); suffix != d.ID {
			t.Errorf("source_ref[%d] discriminator = %q, want the drawer's own id %q",
				i, suffix, d.ID)
		}
	}
}

// TestDecisionFiledAtIsTheNoteDayNotWallClock pins filed_at to the note's
// calendar day. The stamp is what a date-bounded palace query filters on, and
// the note's day is what the session id is built from, so the two must agree —
// a drawer stamped with the moment it happened to be written is unreachable
// through the obvious query for its own session.
func TestDecisionFiledAtIsTheNoteDayNotWallClock(t *testing.T) {
	vault := testVault(t)

	res, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:   "test-proj",
		Summary:   "one decision",
		Decisions: []string{"Stamp drawers with the note's day"},
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	ds := listDecisionDrawers(t, vault, "test-proj")
	if len(ds) != 1 {
		t.Fatalf("filed %d drawers, want 1", len(ds))
	}
	// The session id opens with the note's calendar day; the drawer's stamp
	// must be midnight UTC on exactly that day.
	day := res.SessionID[:10]
	if want := day + "T00:00:00Z"; ds[0].FiledAt != want {
		t.Errorf("filed_at = %q, want %q (the note's day, not wall-clock)", ds[0].FiledAt, want)
	}
}

// TestDecisionFiledAtRejectsAMalformedDay pins the refusal rather than a
// fallback. Falling back to time.Now() on an unparseable day would reintroduce
// the wall-clock stamp DecisionFiledAt exists to prevent, on the one input
// nobody is watching.
func TestDecisionFiledAtRejectsAMalformedDay(t *testing.T) {
	for _, bad := range []string{"", "2026-6-1", "2026-06-21T00:00:00Z", "not-a-day"} {
		if got, err := DecisionFiledAt(bad); err == nil {
			t.Errorf("DecisionFiledAt(%q) = %q, want an error", bad, got)
		}
	}
	got, err := DecisionFiledAt("2026-06-21")
	if err != nil {
		t.Fatalf("DecisionFiledAt(valid): %v", err)
	}
	if got != "2026-06-21T00:00:00Z" {
		t.Errorf("DecisionFiledAt = %q, want 2026-06-21T00:00:00Z", got)
	}
}

// TestRevisedDecisionAtSameIndexGetsItsOwnRef is the regression for the ref's
// content discriminator. A recapture that REVISES the decision at position 0
// files a second drawer — different content, so the append cannot dedup it —
// and both are indexed by Engine.Rebuild. Sharing "#decision/0" would put them
// in the same dedup bucket, where search hands back whichever scores higher:
// possibly the superseded text, with the current decision missing entirely.
func TestRevisedDecisionAtSameIndexGetsItsOwnRef(t *testing.T) {
	vault := testVault(t)
	const key = "revise-key-1"

	first, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "first pass",
		Decisions:  []string{"Use the wall clock for filed_at"},
		SessionKey: key,
	})
	if err != nil {
		t.Fatalf("WriteSession(first): %v", err)
	}

	second, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "revised pass",
		Decisions:  []string{"Use the note's day for filed_at"},
		SessionKey: key,
	})
	if err != nil {
		t.Fatalf("WriteSession(second): %v", err)
	}
	if !second.Updated {
		t.Fatalf("second capture minted a new note; the revision path was not exercised")
	}

	ds := listDecisionDrawers(t, vault, "test-proj")
	if len(ds) != 2 {
		t.Fatalf("filed %d drawers, want 2 (superseded + revised): %+v", len(ds), ds)
	}
	refs := map[string]bool{}
	for _, d := range ds {
		if refs[d.SourceRef] {
			t.Fatalf("two drawers share source_ref %q; search dedup would hide one", d.SourceRef)
		}
		refs[d.SourceRef] = true
		// Both are at index 0 of their own capture, so the index alone would
		// have collided — the discriminator is what separates them.
		prefix := fmt.Sprintf("session/%s#decision/0/", first.SessionID)
		if !strings.HasPrefix(d.SourceRef, prefix) {
			t.Errorf("source_ref %q lacks prefix %q", d.SourceRef, prefix)
		}
	}
}
