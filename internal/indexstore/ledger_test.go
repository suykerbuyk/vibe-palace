// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"errors"
	"testing"
)

// LiveOwner answers for a live archive (its session and day) and a ledgered
// batch (no session, its day), and for nothing else: an unledgered or
// superseded archive, a session mid-supersede, an unledgered batch, a note.
func TestLedgerLiveOwner(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		if err := tx.CommitArchive(commitOf("S2", "aa", "2026-05-03", "x"), newRecordingVW(nil)); err != nil {
			return err
		}
		if err := tx.CommitArchive(commitOf("S3", "cc", "2026-05-20", "z"), newRecordingVW(nil)); err != nil {
			return err
		}
		return tx.CommitBatch(BatchCommit{BatchID: "batch:m", StartDay: "2026-04-02"}, nil)
	})
	l := ledgered(t, v).Ledger()
	if s, d, ok := l.LiveOwner(ArchiveOwner("aa")); !ok || s != "S2" || d != "2026-05-03" {
		t.Errorf("LiveOwner(archive aa) = %q, %q, %v; want S2, 2026-05-03, true", s, d, ok)
	}
	if s, d, ok := l.LiveOwner(BatchOwner("batch:m")); !ok || s != "" || d != "2026-04-02" {
		t.Errorf("LiveOwner(batch) = %q, %q, %v; want \"\", 2026-04-02, true", s, d, ok)
	}
	for name, o := range map[string]Owner{
		"unledgered archive": ArchiveOwner("zz"),
		"unledgered batch":   BatchOwner("batch:none"),
		"note owner":         NoteOwner("note-1"),
	} {
		if _, _, ok := l.LiveOwner(o); ok {
			t.Errorf("LiveOwner(%s) reported live", name)
		}
	}

	// A completed supersede: the old archive is no longer live, the new one is.
	mustTx(t, v, func(tx *Tx) error { return tx.Supersede(commitOf("S2", "ab", "2026-05-04", "x"), newRecordingVW(nil)) })
	l = ledgered(t, v).Ledger()
	if _, _, ok := l.LiveOwner(ArchiveOwner("aa")); ok {
		t.Error("a superseded archive is reported live")
	}
	if s, d, ok := l.LiveOwner(ArchiveOwner("ab")); !ok || s != "S2" || d != "2026-05-04" {
		t.Errorf("LiveOwner(new archive) = %q, %q, %v", s, d, ok)
	}

	// A supersede killed after its superseding mark: neither archive is live.
	old := commitStep
	commitStep = func(step string) error {
		if step == "superseding" {
			return errors.New("killed")
		}
		return nil
	}
	_ = withTx(t, v, func(tx *Tx) error { return tx.Supersede(commitOf("S3", "cd", "2026-05-21", "z"), newRecordingVW(nil)) })
	commitStep = old
	l = ledgered(t, v).Ledger()
	if _, _, ok := l.LiveOwner(ArchiveOwner("cc")); ok {
		t.Error("the archive of a session mid-supersede is reported live")
	}
	if _, _, ok := l.LiveOwner(ArchiveOwner("cd")); ok {
		t.Error("the incoming archive of a session mid-supersede is reported live")
	}
}
