// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Store support the pending-archive ingester needs
// (pending-archive-ingester-and-per-archive-commit-step, plan revisions D9 and
// R1): the batch and superseded readers, captured_at on session records, and
// the re-target of a supersede whose target vanished.

// TestLedgerBatchAndSupersededReaders: Batch and BatchIDs read ledgered
// import batches (the repair pass's batch-shortfall check); Superseded reads
// both a replaced archive and one recorded superseded on arrival.
func TestLedgerBatchAndSupersededReaders(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		for _, id := range []string{"mempalace:b:1", "mempalace:b:4", "mempalace:b:0", "mempalace:b:5", "mempalace:b:3", "mempalace:b:2"} {
			c := BatchCommit{BatchID: id, StartDay: "2026-04-02", Chunks: []OwnedChunk{ownedChunk("x"+id, "alpha", "general"), ownedChunk("y"+id, "alpha", "general")}}
			if err := tx.CommitBatch(c, nil); err != nil {
				return err
			}
		}
		if err := tx.CommitArchive(commitOf("S", "A", "2026-05-13", "x"), newRecordingVW(nil)); err != nil {
			return err
		}
		if err := tx.Supersede(commitOf("S", "B", "2026-05-14", "y"), newRecordingVW(nil)); err != nil {
			return err
		}
		return tx.RecordSuperseded("T", "old", "")
	})
	l := ledgered(t, v).Ledger()
	if got := l.BatchIDs(); !slices.Equal(got, []string{"mempalace:b:0", "mempalace:b:1", "mempalace:b:2", "mempalace:b:3", "mempalace:b:4", "mempalace:b:5"}) {
		t.Fatalf("BatchIDs = %v, want all six, sorted", got)
	}
	if b, ok := l.Batch("mempalace:b:1"); !ok || b.ChunkCount != 2 || b.StartDay != "2026-04-02" || b.ID != "mempalace:b:1" {
		t.Fatalf("Batch = %+v, %v", b, ok)
	}
	if _, ok := l.Batch("nope"); ok {
		t.Fatal("Batch of an unledgered id reported ok")
	}
	for sha, want := range map[string]bool{"A": true, "old": true, "B": false, "never": false} {
		if got := l.Superseded(sha); got != want {
			t.Errorf("Superseded(%s) = %v, want %v", sha, got, want)
		}
	}
}

// TestSessionRecordCarriesCapturedAt: the archive's captured_at is recorded on
// the live record by CommitArchive and by Supersede; a record written without
// one gets it when the archive is committed again; and committing the same
// archive again with the same captured_at appends nothing.
func TestSessionRecordCarriesCapturedAt(t *testing.T) {
	v := newVault(t)
	a := commitOf("S", "A", "2026-05-13", "x")
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(a, newRecordingVW(nil))
	})
	a.CapturedAt = "2026-05-13T10:00:00Z"
	mustTx(t, v, func(tx *Tx) error { return tx.CommitArchive(a, newRecordingVW(nil)) })
	if s, _ := ledgered(t, v).Ledger().Session("S"); s.CapturedAt != a.CapturedAt {
		t.Fatalf("CapturedAt = %q after CommitArchive", s.CapturedAt)
	}
	pf, _ := filesFor(v, "alpha")
	before := len(fileLines(t, pf.ledger))
	mustTx(t, v, func(tx *Tx) error { return tx.CommitArchive(a, newRecordingVW(nil)) })
	if after := len(fileLines(t, pf.ledger)); after != before {
		t.Fatalf("re-committing the same archive appended %d ledger lines", after-before)
	}
	b := commitOf("S", "B", "2026-05-14", "y")
	b.CapturedAt = "2026-05-14T10:00:00Z"
	mustTx(t, v, func(tx *Tx) error { return tx.Supersede(b, newRecordingVW(nil)) })
	if s, _ := ledgered(t, v).Ledger().Session("S"); s.CapturedAt != b.CapturedAt || s.SHA != "B" {
		t.Fatalf("after the supersede: %+v", s)
	}
}

// supersedeCrashedTowards leaves session S superseding from A (chunks x, y)
// to B (chunks y, z), killed after B's KG rewrite: B's chunks and KG records
// are in the store, owned by B, and the session is not done.
func supersedeCrashedTowards(t *testing.T) *storage.Vault {
	t.Helper()
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(commitOf("S", "A", "2026-05-13", "x", "y"), newRecordingVW(nil))
	})
	killAt(t, "kg")
	err := withTx(t, v, func(tx *Tx) error {
		return tx.Supersede(commitOf("S", "B", "2026-05-14", "y", "z"), newRecordingVW(nil))
	})
	if !errors.Is(err, errKill) {
		t.Fatalf("Supersede = %v, want the kill", err)
	}
	commitStep = func(string) error { return nil }
	if s, _ := ledgered(t, v).Ledger().Session("S"); s.State != StateSuperseding || s.SHA != "B" || s.SupersedingFrom != "A" {
		t.Fatalf("precondition: %+v", s)
	}
	if n := ledgered(t, v).CountChunks(ArchiveOwner("B")); n == 0 {
		t.Fatal("precondition: B's chunk rewrite must have landed")
	}
	bKG := 0
	for _, k := range ledgered(t, v).KG(false) {
		if slices.Contains(k.Owners, ArchiveOwner("B")) {
			bKG++
		}
	}
	if bKG == 0 {
		t.Fatal("precondition: B's KG rewrite must have landed")
	}
	return v
}

// noArchiveOwns asserts that no chunk or KG record names any of shas as owner.
func noArchiveOwns(t *testing.T, v *storage.Vault, shas ...string) {
	t.Helper()
	s := ledgered(t, v)
	for _, c := range s.Chunks(false) {
		for _, o := range c.Owners {
			if slices.Contains(shas, o.SHA) {
				t.Errorf("chunk %s still owned by %s", c.ID, o.SHA)
			}
		}
	}
	for _, k := range s.KG(false) {
		for _, o := range k.Owners {
			if slices.Contains(shas, o.SHA) {
				t.Errorf("KG record %s still owned by %s", k.ID, o.SHA)
			}
		}
	}
}

// TestSupersedeRetargetsAVanishedTarget (R1): a supersede towards B crashed,
// then B was rewritten to C before any resume. Supersede(C, Retarget B)
// finishes the session on C: C live with generation 2, A and B superseded,
// and no chunk or KG record owned by A or B.
func TestSupersedeRetargetsAVanishedTarget(t *testing.T) {
	v := supersedeCrashedTowards(t)
	c := commitOf("S", "C", "2026-05-15", "z", "w")
	c.Retarget = "B"
	mustTx(t, v, func(tx *Tx) error { return tx.Supersede(c, newRecordingVW(nil)) })
	s := ledgered(t, v)
	sr, _ := s.Ledger().Session("S")
	if sr.State != StateLive || sr.SHA != "C" || sr.Generation != 2 || sr.StartDay != "2026-05-15" {
		t.Fatalf("session = %+v, want C live at generation 2", sr)
	}
	if !s.Ledger().Superseded("A") || !s.Ledger().Superseded("B") {
		t.Fatal("A and B must both read superseded")
	}
	noArchiveOwns(t, v, "A", "B")
	if got, want := ids(s.Chunks(true)), sorted(idOf("z"), idOf("w")); !slices.Equal(got, want) {
		t.Fatalf("live chunks %v, want C's z and w", got)
	}
}

// TestSupersedeRetargetResumesAfterACrash: a crash right after the re-target
// mark leaves the session superseding to C; the plain resume (no Retarget)
// finishes it, and B's partial records are still removed.
func TestSupersedeRetargetResumesAfterACrash(t *testing.T) {
	v := supersedeCrashedTowards(t)
	c := commitOf("S", "C", "2026-05-15", "z", "w")
	c.Retarget = "B"
	killAt(t, "superseding")
	if err := withTx(t, v, func(tx *Tx) error { return tx.Supersede(c, newRecordingVW(nil)) }); !errors.Is(err, errKill) {
		t.Fatalf("Supersede = %v, want the kill", err)
	}
	commitStep = func(string) error { return nil }
	c.Retarget = ""
	mustTx(t, v, func(tx *Tx) error { return tx.Supersede(c, newRecordingVW(nil)) })
	if sr, _ := ledgered(t, v).Ledger().Session("S"); sr.State != StateLive || sr.SHA != "C" {
		t.Fatalf("session = %+v, want C live", sr)
	}
	noArchiveOwns(t, v, "A", "B")
}

// TestRetargetIsRefusedUnlessItNamesTheTarget: a Retarget that is not the
// ledger's superseding target, or one given to CommitArchive, is refused and
// writes nothing.
func TestRetargetIsRefusedUnlessItNamesTheTarget(t *testing.T) {
	v := supersedeCrashedTowards(t)
	pf, _ := filesFor(v, "alpha")
	before := fileLines(t, pf.ledger)
	c := commitOf("S", "C", "2026-05-15", "z")
	c.Retarget = "not-B"
	if err := withTx(t, v, func(tx *Tx) error { return tx.Supersede(c, newRecordingVW(nil)) }); !errors.Is(err, ErrOtherArchive) {
		t.Fatalf("a wrong Retarget: %v, want ErrOtherArchive", err)
	}
	c.Retarget = "B"
	c.SessionID = "T"
	if err := withTx(t, v, func(tx *Tx) error { return tx.CommitArchive(c, newRecordingVW(nil)) }); err == nil {
		t.Fatal("CommitArchive accepted a Retarget")
	}
	if after := fileLines(t, pf.ledger); !slices.Equal(after, before) {
		t.Fatal("a refused re-target wrote to the ledger")
	}
}

// TestInboxRecordsAndDropsFirsts (R3): NoteFirst records a trigger's First
// once, under the project's commit lock; a holder reads it with Firsts and
// drops served entries; an emptied inbox is removed; a non-sha is refused and
// a gone project is ErrProjectGone.
func TestInboxRecordsAndDropsFirsts(t *testing.T) {
	v := newVault(t)
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, sha := range []string{b, a, b} {
		if err := NoteFirst(context.Background(), v, "alpha", sha, NoTimeout); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	mustTx(t, v, func(tx *Tx) error {
		var err error
		got, err = tx.Firsts()
		return err
	})
	if !slices.Equal(got, []string{a, b}) {
		t.Fatalf("Firsts = %v, want a and b once each", got)
	}
	mustTx(t, v, func(tx *Tx) error { return tx.DropFirsts([]string{a}) })
	mustTx(t, v, func(tx *Tx) error {
		var err error
		got, err = tx.Firsts()
		return err
	})
	if !slices.Equal(got, []string{b}) {
		t.Fatalf("after dropping a: %v", got)
	}
	mustTx(t, v, func(tx *Tx) error { return tx.DropFirsts([]string{b}) })
	pf, _ := filesFor(v, "alpha")
	if _, err := os.Stat(filepath.Join(pf.dir, inboxFile)); !os.IsNotExist(err) {
		t.Fatalf("the emptied inbox is still there (stat err %v)", err)
	}
	if err := NoteFirst(context.Background(), v, "alpha", "Projects/alpha/transcripts/x.jsonl.zst", NoTimeout); err == nil {
		t.Fatal("NoteFirst accepted a path")
	}
	if err := NoteFirst(context.Background(), v, "gone", a, NoTimeout); !errors.Is(err, ErrProjectGone) {
		t.Fatalf("NoteFirst on a gone project: %v, want ErrProjectGone", err)
	}
}

// TestSupersedeRevertsToItsSourceWhenTheTargetVanished (R1): the supersede
// towards B crashed and B is gone with nothing newer: Supersede(A, Retarget B)
// makes A live again (generation 2), records B superseded, and keeps A's own
// chunks and KG records, owned by A, while nothing is owned by B.
func TestSupersedeRevertsToItsSourceWhenTheTargetVanished(t *testing.T) {
	v := supersedeCrashedTowards(t)
	a := commitOf("S", "A", "2026-05-13", "x", "y")
	a.Retarget = "B"
	mustTx(t, v, func(tx *Tx) error { return tx.Supersede(a, newRecordingVW(nil)) })
	st := ledgered(t, v)
	l := st.Ledger()
	if s, _ := l.Session("S"); s.State != StateLive || s.SHA != "A" || s.Generation != 2 {
		t.Fatalf("session %+v, want A live at generation 2", s)
	}
	if l.Superseded("A") || !l.Superseded("B") {
		t.Fatalf("superseded: A %v, B %v; want A current, B superseded", l.Superseded("A"), l.Superseded("B"))
	}
	if got, want := ids(st.Chunks(true)), sorted(idOf("x"), idOf("y")); !slices.Equal(got, want) {
		t.Fatalf("live chunks %v, want A's x and y", got)
	}
	aKG := 0
	for _, k := range st.KG(true) {
		if slices.Contains(k.Owners, ArchiveOwner("A")) {
			aKG++
		}
	}
	if aKG != 2 {
		t.Fatalf("A owns %d live KG records, want its 2: the revert dropped its own records", aKG)
	}
	noArchiveOwns(t, v, "B")
}
