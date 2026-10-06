// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// storeDecisions reads a project's decision chunks from the host-local store.
func storeDecisions(t *testing.T, v *storage.Vault, project string) []indexstore.StoredChunk {
	t.Helper()
	st, err := indexstore.ReadStore(v, project)
	if err != nil {
		t.Fatalf("ReadStore(%s): %v", project, err)
	}
	var out []indexstore.StoredChunk
	for _, c := range st.Chunks(false) {
		if c.SourceType == storage.SourceTypeDecision {
			out = append(out, c)
		}
	}
	return out
}

// writeDecisionNoteWithSession is writeDecisionNote plus an archive_session_id,
// so a test can date a decision from the ledger.
func writeDecisionNoteWithSession(t *testing.T, vaultRoot, project, stem, date, archiveSessionID string, decisions ...string) {
	t.Helper()
	dir := filepath.Join(vaultRoot, "Projects", project, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{"---", "session_id: " + stem, "project: " + project, "date: " + date}
	if archiveSessionID != "" {
		lines = append(lines, "archive_session_id: "+archiveSessionID)
	}
	lines = append(lines, "decisions:")
	for _, d := range decisions {
		lines = append(lines, "  - "+strconv.Quote(d))
	}
	lines = append(lines, "---", "")
	if err := os.WriteFile(filepath.Join(dir, stem+".md"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDecisionFiledAtRejectsAMalformedDay pins the refusal rather than a
// fallback to wall-clock, on the one input nobody is watching.
func TestDecisionFiledAtRejectsAMalformedDay(t *testing.T) {
	for _, bad := range []string{"", "2026-6-1", "2026-06-21T00:00:00Z", "not-a-day"} {
		if got, err := DecisionFiledAt(bad); err == nil {
			t.Errorf("DecisionFiledAt(%q) = %q, want an error", bad, got)
		}
	}
	if got, err := DecisionFiledAt("2026-06-21"); err != nil || got != "2026-06-21T00:00:00Z" {
		t.Errorf("DecisionFiledAt(valid) = %q, %v", got, err)
	}
}

// TestDecisionStoreIdIsTheContentHash: the store id is index.ChunkID(content),
// the content-only wide hash — never the 32-bit storage.DrawerID, and the same
// decision text in another project (another wing) has the same id, so neither a
// reclassification nor a project rename changes it.
func TestDecisionStoreIdIsTheContentHash(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	const decision = "chose the content-only chunk id"

	writeDecisionNote(t, v.Root, "proj-a", "2026-05-13-aaaa0000-01", "2026-05-13", decision)
	writeDecisionNote(t, v.Root, "proj-b", "2026-05-13-bbbb0000-01", "2026-05-13", decision)
	if _, err := eng.Rebuild(ctx, "proj-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Rebuild(ctx, "proj-b"); err != nil {
		t.Fatal(err)
	}

	da := storeDecisions(t, v, "proj-a")
	db := storeDecisions(t, v, "proj-b")
	if len(da) != 1 || len(db) != 1 {
		t.Fatalf("stored a=%d b=%d decision chunks, want 1 each", len(da), len(db))
	}
	want := index.ChunkID(decision)
	if da[0].ID != want {
		t.Errorf("store id = %q, want index.ChunkID = %q", da[0].ID, want)
	}
	if da[0].ID != db[0].ID {
		t.Errorf("the same decision in another wing got a different id: %q vs %q", da[0].ID, db[0].ID)
	}
	// The 32-bit DrawerID is never the store id, and never appears as one.
	for _, c := range da {
		if c.ID == storage.DrawerID(c.Wing, c.Content) {
			t.Errorf("store id equals the 32-bit DrawerID %q; it must be the content hash", c.ID)
		}
	}
}

// TestDecisionDatedByLedgerStartDay: a note dated one day whose archive_session_id
// names a session the ledger records live with a DIFFERENT start day takes the
// ledger's day — and no archive is opened (the archive file never exists here).
func TestDecisionDatedByLedgerStartDay(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	// Ledger session "sess-x" live with start day 2026-05-13 (commitArchive's).
	commitArchive(t, eng.cache, v, "proj", "sess-x", "sha-x", []string{"a transcript chunk"}, true)

	// A note dated the day BEFORE, linking that session.
	writeDecisionNoteWithSession(t, v.Root, "proj", "2026-05-12-aaaa0000-01", "2026-05-12", "sess-x",
		"a decision dated by the ledger")

	// The archive the ledger names is NOT on disk (commitArchive records a path
	// but writes no file). The dating below therefore PROVES the path opens no
	// archive: it reads only the ledger's start day. This stands in for the
	// plan's "archive-open counter reads 0" acceptance — no counter seam is
	// needed because the code calls neither archive.ResolveEntry nor
	// index.SessionDate (confirmed in review), so an absent archive is dated
	// correctly rather than erroring.
	if _, err := os.Stat(filepath.Join(v.Root, "Projects", "proj", "transcripts", "sess-x.jsonl.zst")); !os.IsNotExist(err) {
		t.Fatalf("precondition: the ledgered archive must be absent so dating cannot read it; stat err = %v", err)
	}

	if _, err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}

	ds := storeDecisions(t, v, "proj")
	if len(ds) != 1 {
		t.Fatalf("stored %d decision chunks, want 1", len(ds))
	}
	if ds[0].FiledAt != "2026-05-13" {
		t.Errorf("filed_at = %q, want 2026-05-13 (the ledger's start day, not the note's 2026-05-12)", ds[0].FiledAt)
	}
}

// TestDecisionNoSessionKeepsTheNoteDay: a note that names no session dates its
// decisions by its own day.
func TestDecisionNoSessionKeepsTheNoteDay(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	writeDecisionNote(t, v.Root, "proj", "2026-05-12-aaaa0000-01", "2026-05-12", "an undated-by-ledger decision")
	if _, err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}
	ds := storeDecisions(t, v, "proj")
	if len(ds) != 1 || ds[0].FiledAt != "2026-05-12" {
		t.Fatalf("decision chunks = %+v, want one dated 2026-05-12", ds)
	}
}

// TestNotesTierBuildRemovesAnEditedOutDecision: a note with two decisions, then
// edited to one, loses the removed decision at the next notes-tier build; a note
// deleted entirely loses both.
func TestNotesTierBuildRemovesAnEditedOutDecision(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	const stem = "2026-05-13-aaaa0000-01"

	writeDecisionNote(t, v.Root, "proj", stem, "2026-05-13", "D1 keep")
	// Two decisions.
	writeDecisionNoteWithSession(t, v.Root, "proj", stem, "2026-05-13", "", "D1 keep", "D2 drop")
	if _, err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}
	if ds := storeDecisions(t, v, "proj"); len(ds) != 2 {
		t.Fatalf("after capture: %d decision chunks, want 2: %+v", len(ds), ds)
	}

	// Edit to keep only D1.
	writeDecisionNoteWithSession(t, v.Root, "proj", stem, "2026-05-13", "", "D1 keep")
	if _, err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}
	ds := storeDecisions(t, v, "proj")
	if len(ds) != 1 || !strings.Contains(ds[0].Content, "D1 keep") {
		t.Fatalf("after edit: %+v, want only D1", ds)
	}

	// Delete the note entirely.
	if err := os.Remove(filepath.Join(v.Root, "Projects", "proj", "sessions", stem+".md")); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}
	if ds := storeDecisions(t, v, "proj"); len(ds) != 0 {
		t.Fatalf("after delete: %d decision chunks, want 0: %+v", len(ds), ds)
	}
}

// TestSharedDecisionTakesTheEarliestOwnerDate: two notes carrying the same
// decision text share one chunk with two owners; its filed_at is the earliest
// owner's day, independent of capture order.
func TestSharedDecisionTakesTheEarliestOwnerDate(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()
	const decision = "a decision two notes recorded"

	writeDecisionNote(t, v.Root, "proj", "2026-05-20-aaaa0000-01", "2026-05-20", decision)
	writeDecisionNote(t, v.Root, "proj", "2026-05-13-bbbb0000-01", "2026-05-13", decision)
	if _, err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}

	ds := storeDecisions(t, v, "proj")
	if len(ds) != 1 {
		t.Fatalf("stored %d decision chunks, want 1 shared chunk: %+v", len(ds), ds)
	}
	owners := 0
	for _, o := range ds[0].Owners {
		if o.Kind == indexstore.OwnerNote {
			owners++
		}
	}
	if owners != 2 {
		t.Errorf("shared chunk has %d note owners, want 2", owners)
	}
	if ds[0].FiledAt != "2026-05-13" {
		t.Errorf("filed_at = %q, want the earliest owner's day 2026-05-13", ds[0].FiledAt)
	}
}

// TestRebuildReproducesDecisions: after the store is discarded, a rebuild brings
// the decision chunks back from the session notes, with no capture.
func TestRebuildReproducesDecisions(t *testing.T) {
	eng, v := testEngine(t)
	ctx := context.Background()

	writeDecisionNote(t, v.Root, "proj", "2026-05-13-aaaa0000-01", "2026-05-13", "a reproduced decision")
	if _, err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}
	before := storeDecisions(t, v, "proj")
	if len(before) != 1 {
		t.Fatalf("stored %d, want 1", len(before))
	}

	// Discard the store by removing the index directory, then rebuild.
	dir, err := v.IndexDir("proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Rebuild(ctx, "proj"); err != nil {
		t.Fatal(err)
	}
	after := storeDecisions(t, v, "proj")
	if len(after) != 1 || after[0].ID != before[0].ID || after[0].FiledAt != before[0].FiledAt {
		t.Fatalf("after rebuild: %+v, want the same chunk as %+v", after, before)
	}
}
