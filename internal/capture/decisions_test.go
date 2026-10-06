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
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// storedDecisions reads a project's decision chunks back from the HOST-LOCAL
// store (decision-chunks-in-the-host-local-store), sorted by source_ref so an
// assertion on the SET does not depend on write order. Decisions live in
// palace/.local/index/<p>/chunks.jsonl now, never in a tracked drawers.jsonl.
func storedDecisions(t *testing.T, vault *storage.Vault, project string) []indexstore.StoredChunk {
	t.Helper()
	st, err := indexstore.ReadStore(vault, project)
	if err != nil {
		t.Fatalf("ReadStore(%s): %v", project, err)
	}
	var out []indexstore.StoredChunk
	for _, c := range st.Chunks(false) {
		if c.SourceType == storage.SourceTypeDecision {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceRef < out[j].SourceRef })
	return out
}

// assertNoTrackedDecisionDrawers pins that decisions no longer touch the tracked
// tree: the decision room holds nothing (Scope 1, "Never to the tracked tree").
func assertNoTrackedDecisionDrawers(t *testing.T, vault *storage.Vault, project string) {
	t.Helper()
	ds, err := vault.ListDrawers(project, palace.DetectWing(project, ""), search.DecisionRoom)
	if err == nil && len(ds) != 0 {
		t.Errorf("tracked decision drawers exist (%d); decisions must go to the store only: %+v", len(ds), ds)
	}
}

// breakDecisionStore makes the project's chunks.jsonl unopenable by putting a
// DIRECTORY at exactly that path, so the decision write errors (readLines on a
// directory fails). It is the store-side successor to the old breakDecisionRoom,
// and TestFileDecisionDrawersReportsStoreFailure guards that it still works.
func breakDecisionStore(t *testing.T, vault *storage.Vault, project string) {
	t.Helper()
	dir, err := vault.IndexDir(project)
	if err != nil {
		t.Fatalf("IndexDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir index dir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "chunks.jsonl"), 0o755); err != nil {
		t.Fatalf("mkdir over chunks.jsonl: %v", err)
	}
}

// TestFileDecisionDrawersReportsStoreFailure pins the failure injection the
// accumulate-don't-return tests rely on: with a directory at chunks.jsonl, the
// store write really errors.
func TestFileDecisionDrawersReportsStoreFailure(t *testing.T) {
	vault := testVault(t)
	// The project must exist for the commit lock's ProjectExists check.
	if _, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj", Summary: "seed",
	}); err != nil {
		t.Fatalf("seed WriteSession: %v", err)
	}
	breakDecisionStore(t, vault, "test-proj")

	n, err := fileDecisionDrawers(context.Background(), vault, "test-proj", "2026-06-21-abcd1234-01",
		"2026-06-21-abcd1234-01", "2026-06-21", []string{"a decision"})
	if err == nil {
		t.Fatalf("store write over a directory returned nil error (n=%d); the failure injection no longer works", n)
	}
}

// TestDecisionWriteSkippedWhenCommitLockBusy pins the timeout contract (Scope
// 1): a busy commit lock makes the decision write skip — not fail — so a capture
// is never lost behind an ingest. The next notes-tier build restores the
// decision. The timeout is driven by a zero wait against a held lock, never by
// wall-clock (X6).
func TestDecisionWriteSkippedWhenCommitLockBusy(t *testing.T) {
	vault := testVault(t)
	if _, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj", Summary: "seed",
	}); err != nil {
		t.Fatalf("seed WriteSession: %v", err)
	}

	// Hold the project's commit lock, then make the decision write try once.
	held, err := indexstore.Lock(context.Background(), vault, "test-proj", indexstore.NoTimeout)
	if err != nil {
		t.Fatalf("hold commit lock: %v", err)
	}
	defer func() { _ = held.Release() }()

	restore := decisionWriteTimeout
	decisionWriteTimeout = 0
	defer func() { decisionWriteTimeout = restore }()

	n, err := fileDecisionDrawers(context.Background(), vault, "test-proj", "2026-06-21-abcd1234-01",
		"2026-06-21-abcd1234-01", "2026-06-21", []string{"a skipped decision"})
	if err != nil {
		t.Fatalf("a busy lock must SKIP, not fail: %v", err)
	}
	if n != 0 {
		t.Errorf("wrote %d chunks while the lock was held, want 0 (skipped)", n)
	}
	// Nothing was written, so no decision chunk exists yet.
	if ds := storedDecisions(t, vault, "test-proj"); len(ds) != 0 {
		t.Errorf("a skipped write left %d decision chunks, want 0: %+v", len(ds), ds)
	}
}

// TestDecisionWriteWaitsForTheCommitLockThenWrites is the positive half of R1:
// a capture whose decision write finds the commit lock HELD waits for it, then
// writes once it is released. Contention is driven by holding the lock and
// releasing it on the waiter's own CommitWait event (not by wall-clock, X6): the
// release is triggered deterministically the moment the capture is known to be
// about to wait.
func TestDecisionWriteWaitsForTheCommitLockThenWrites(t *testing.T) {
	vault := testVault(t)
	if _, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj", Summary: "seed",
	}); err != nil {
		t.Fatalf("seed WriteSession: %v", err)
	}

	held, err := indexstore.Lock(context.Background(), vault, "test-proj", indexstore.NoTimeout)
	if err != nil {
		t.Fatalf("hold commit lock: %v", err)
	}

	// Signal (once) when the capture is about to wait for the commit lock.
	waiting := make(chan struct{}, 1)
	restore := indexstore.ObserveCommitLocks(func(project string, ev indexstore.CommitLockEvent) {
		if project == "test-proj" && ev == indexstore.CommitWait {
			select {
			case waiting <- struct{}{}:
			default:
			}
		}
	})
	defer restore()

	// A generous ceiling, never asserted on: the release below is what unblocks
	// the wait, deterministically.
	restoreTO := decisionWriteTimeout
	decisionWriteTimeout = 10 * time.Second
	defer func() { decisionWriteTimeout = restoreTO }()

	type res struct {
		n   int
		err error
	}
	done := make(chan res, 1)
	go func() {
		n, err := fileDecisionDrawers(context.Background(), vault, "test-proj", "2026-06-21-abcd1234-01",
			"2026-06-21-abcd1234-01", "2026-06-21", []string{"a decision that waited"})
		done <- res{n, err}
	}()

	<-waiting          // the capture is now waiting on the held commit lock
	_ = held.Release() // release it; the capture proceeds

	r := <-done
	if r.err != nil {
		t.Fatalf("capture that waited for the lock failed: %v", r.err)
	}
	if r.n != 1 {
		t.Errorf("wrote %d chunks, want 1", r.n)
	}
	if ds := storedDecisions(t, vault, "test-proj"); len(ds) != 1 {
		t.Fatalf("stored %d decision chunks, want 1 after the wait: %+v", len(ds), ds)
	}
}

// TestDecisionWriteNotDelayedByTheRunLock is R1's other half: the decision write
// takes the commit lock only, never the index run lock, so a held run lock does
// not delay it. Driven by holding the run lock and a zero commit-lock timeout:
// if the capture waited on the run lock at all it could not succeed here.
func TestDecisionWriteNotDelayedByTheRunLock(t *testing.T) {
	vault := testVault(t)
	if _, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj", Summary: "seed",
	}); err != nil {
		t.Fatalf("seed WriteSession: %v", err)
	}

	rl, ok, err := indexstore.TryRunLock(vault, indexstore.KindRebuild, "test-proj")
	if err != nil || !ok {
		t.Fatalf("hold run lock: ok=%v err=%v", ok, err)
	}
	defer func() { _ = rl.Release() }()

	// Prove the capture never even tries the run lock.
	triedRun := false
	restore := indexstore.ObserveRunLocks(func(ev indexstore.RunLockEvent) {
		if ev == indexstore.RunTry {
			triedRun = true
		}
	})
	defer restore()

	restoreTO := decisionWriteTimeout
	decisionWriteTimeout = 0 // the commit lock is free; a run-lock wait would still fail this
	defer func() { decisionWriteTimeout = restoreTO }()

	n, err := fileDecisionDrawers(context.Background(), vault, "test-proj", "2026-06-21-abcd1234-01",
		"2026-06-21-abcd1234-01", "2026-06-21", []string{"a decision past the run lock"})
	if err != nil {
		t.Fatalf("a held run lock delayed/failed the capture: %v", err)
	}
	if n != 1 {
		t.Errorf("wrote %d chunks, want 1", n)
	}
	if triedRun {
		t.Error("the decision write tried the index run lock; it must take the commit lock only")
	}
}

// TestWriteSessionFilesDecisionsToStoreWithNilIndexer is the DEFAULT case:
// internal/hook/hook.go calls WriteSession with a nil indexer on every
// hook-driven capture, and decisions must reach the store on it.
func TestWriteSessionFilesDecisionsToStoreWithNilIndexer(t *testing.T) {
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

	ds := storedDecisions(t, vault, "test-proj")
	if len(ds) != 1 {
		t.Fatalf("stored %d decision chunks, want 1: %+v", len(ds), ds)
	}
	c := ds[0]
	if c.Content != "Use SetEscapeHTML(false)" {
		t.Errorf("content = %q, want the decision verbatim", c.Content)
	}
	if c.SourceType != storage.SourceTypeDecision {
		t.Errorf("source_type = %q, want %q", c.SourceType, storage.SourceTypeDecision)
	}
	if c.Hall != palace.HallDecisions {
		t.Errorf("hall = %q, want %q", c.Hall, palace.HallDecisions)
	}
	if c.Room != search.DecisionRoom {
		t.Errorf("room = %q, want %q", c.Room, search.DecisionRoom)
	}
	if c.AddedBy != "capture" {
		t.Errorf("added_by = %q, want capture", c.AddedBy)
	}
	// The owner is the note, keyed by its file stem (the session id).
	assertNoteOwner(t, c, result.SessionID)
	// filed_at is the note's day (no archive ingested), a bare YYYY-MM-DD.
	if want := result.SessionID[:10]; c.FiledAt != want {
		t.Errorf("filed_at = %q, want %q (the note's day)", c.FiledAt, want)
	}
	// The store id is the content-only hash, not the 32-bit DrawerID.
	assertRefShape(t, ds, result.SessionID, []int{0})
	assertNoTrackedDecisionDrawers(t, vault, "test-proj")
}

// TestWriteSessionNoDecisionsStoresNothing: a capture with no decisions stores
// no decision chunk and still writes its note.
func TestWriteSessionNoDecisionsStoresNothing(t *testing.T) {
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
	if ds := storedDecisions(t, vault, "test-proj"); len(ds) != 0 {
		t.Fatalf("stored %d decision chunks for a decision-less capture, want 0: %+v", len(ds), ds)
	}
	if _, _, rerr := vault.ReadSession("test-proj", result.SessionID[:10],
		ParseFingerprint(result.SessionID), result.Iteration); rerr != nil {
		t.Fatalf("ReadSession: %v", rerr)
	}
}

// TestDecisionSourceRefsAreUniquePerDecision: search.dedup keeps only the first
// result per exact SourceRef, so a session-level ref would strand a note's other
// decisions.
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

	ds := storedDecisions(t, vault, "test-proj")
	if len(ds) != 3 {
		t.Fatalf("stored %d decision chunks, want 3: %+v", len(ds), ds)
	}
	assertRefShape(t, ds, result.SessionID, []int{0, 1, 2})
}

// TestBlankDecisionsSkippedWithoutRenumbering: a blank entry is skipped and its
// index LEFT UNUSED, so a ref keeps pointing at the note's Nth decision.
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

	ds := storedDecisions(t, vault, "test-proj")
	if len(ds) != 2 {
		t.Fatalf("stored %d decision chunks, want 2: %+v", len(ds), ds)
	}
	assertRefShape(t, ds, result.SessionID, []int{0, 2})
}

// TestWriteSessionDecisionStoreFailureIsAccumulated: the note is capture's one
// irreplaceable output. A store that cannot be written costs a Failures entry,
// never the session.
func TestWriteSessionDecisionStoreFailureIsAccumulated(t *testing.T) {
	vault := testVault(t)
	if _, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj", Summary: "seed",
	}); err != nil {
		t.Fatalf("seed WriteSession: %v", err)
	}
	breakDecisionStore(t, vault, "test-proj")

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "Decisions cannot be filed",
		Decisions:  []string{"a decision"},
		SessionKey: "store-fail-key",
	})
	if err != nil {
		t.Fatalf("WriteSession returned an error; the note must survive a store failure: %v", err)
	}
	if result == nil || result.NotePath == "" {
		t.Fatal("note_path is empty; the note should still have landed")
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

// TestDrainStoresDecisions is the HOOK path end to end. The hook passes no
// Decisions, so its notes have decisions only once an enrichment produces them —
// and when the inline enricher misses, that happens on the drain.
func TestDrainStoresDecisions(t *testing.T) {
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

	if ds := storedDecisions(t, vault, "test-proj"); len(ds) != 0 {
		t.Fatalf("decision chunks exist before the drain: %+v", ds)
	}

	drained, err := DrainEnrichmentQueue(context.Background(), vault, cwd, workingEnricher(), 0)
	if err != nil {
		t.Fatalf("DrainEnrichmentQueue: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained = %d, want 1", drained)
	}

	ds := storedDecisions(t, vault, "test-proj")
	if len(ds) != 1 {
		t.Fatalf("stored %d decision chunks after the drain, want 1: %+v", len(ds), ds)
	}
	if ds[0].Content != "LLM decision one" {
		t.Errorf("content = %q, want the drained LLM decision", ds[0].Content)
	}
	assertRefShape(t, ds, result.SessionID, []int{0})
	// The drain's stamp is the NOTE's day, not the day the drain ran.
	if want := result.SessionID[:10]; ds[0].FiledAt != want {
		t.Errorf("filed_at = %q, want %q (the note's day)", ds[0].FiledAt, want)
	}
	assertNoTrackedDecisionDrawers(t, vault, "test-proj")
}

// TestWriteSessionStoresWhatLandedNotTheParams is the reason the ingest re-reads
// an UPDATED note instead of using the local meta: mergeCaptureMeta keeps the
// note's ENRICHED decisions when the re-capture did not itself enrich, so the
// caller's list is exactly what was NOT written.
func TestWriteSessionStoresWhatLandedNotTheParams(t *testing.T) {
	vault := testVault(t)
	const key = "retry-key-1"

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
		t.Fatalf("second capture minted a new note; the merge path was not exercised")
	}

	ds := storedDecisions(t, vault, "test-proj")
	if len(ds) != 1 {
		t.Fatalf("stored %d decision chunks, want 1 (only what landed): %+v", len(ds), ds)
	}
	if ds[0].Content != "LLM decision one" {
		t.Errorf("content = %q, want the note's own decision", ds[0].Content)
	}
	_ = first
}

// TestDecisionRoomDoesNotDependOnContent pins the room as UNCONDITIONAL: never
// classified from what the decision says.
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

	ds := storedDecisions(t, vault, "test-proj")
	if len(ds) != 1 {
		t.Fatalf("stored %d decision chunks into %q, want 1", len(ds), search.DecisionRoom)
	}
	if ds[0].Room != search.DecisionRoom {
		t.Errorf("room = %q, want %q — the room is hardcoded, never classified", ds[0].Room, search.DecisionRoom)
	}
	if ds[0].Hall != palace.HallDecisions {
		t.Errorf("hall = %q, want %q", ds[0].Hall, palace.HallDecisions)
	}
	assertRefShape(t, ds, res.SessionID, []int{0})
}

// assertNoteOwner checks the chunk is owned by the note with stem wantStem.
func assertNoteOwner(t *testing.T, c indexstore.StoredChunk, wantStem string) {
	t.Helper()
	want := "sessions/" + wantStem + ".md"
	for _, o := range c.Owners {
		if o.Kind == indexstore.OwnerNote {
			if o.ID != want {
				t.Errorf("note owner id = %q, want the note path %q", o.ID, want)
			}
			return
		}
	}
	t.Errorf("chunk has no note owner: %+v", c.Owners)
}

// assertRefShape checks the chunks' source refs against the ref contract
// "session/{id}#decision/{n}/{drawerID}", one per expected index. The last
// segment is the 32-bit storage.DrawerID of (wing, content) — the search
// discriminator — NOT the store's content-only chunk id.
func assertRefShape(t *testing.T, ds []indexstore.StoredChunk, sessionID string, wantIdx []int) {
	t.Helper()
	if len(ds) != len(wantIdx) {
		t.Fatalf("got %d chunks, want %d: %+v", len(ds), len(wantIdx), ds)
	}
	byRef := make(map[string]indexstore.StoredChunk, len(ds))
	got := make([]string, 0, len(ds))
	for _, c := range ds {
		byRef[c.SourceRef] = c
		got = append(got, c.SourceRef)
	}
	sort.Strings(got)
	for i, n := range wantIdx {
		prefix := fmt.Sprintf("session/%s#decision/%d/", sessionID, n)
		if !strings.HasPrefix(got[i], prefix) {
			t.Errorf("source_ref[%d] = %q, want prefix %q", i, got[i], prefix)
			continue
		}
		c := byRef[got[i]]
		if suffix, want := strings.TrimPrefix(got[i], prefix), storage.DrawerID(c.Wing, c.Content); suffix != want {
			t.Errorf("source_ref[%d] discriminator = %q, want storage.DrawerID %q", i, suffix, want)
		}
	}
}
