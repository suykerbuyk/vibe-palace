// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"errors"
	"os"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// kgSnapshotFixture lays a store whose KG view exercises every liveness case:
// a live archive, a superseded and reaped one, a batch, a note owner, an
// unledgered archive, a malformed interior KG line and a torn final one.
func kgSnapshotFixture(t *testing.T) *storage.Vault {
	t.Helper()
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		if err := tx.CommitArchive(commitOf("S1", "A1", "2026-05-13", "X", "Y"), newRecordingVW(nil)); err != nil {
			return err
		}
		if err := tx.CommitArchive(commitOf("S2", "B1", "2026-05-14", "P", "Q"), newRecordingVW(nil)); err != nil {
			return err
		}
		return tx.CommitBatch(BatchCommit{BatchID: "batch:m", StartDay: "2026-04-02",
			KG: []KGRecord{{ID: "kg-batch", Payload: []byte(`{"s":"batch"}`)}}}, nil)
	})
	mustTx(t, v, func(tx *Tx) error { return tx.Supersede(commitOf("S2", "B2", "2026-05-15", "R"), newRecordingVW(nil)) })
	reap(t, v, noOtherLive)

	pf, err := filesFor(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(pf.kg, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{
		`{"id":"kg-note","owner":{"kind":"note","id":"n"},"payload":{"s":"note"}}` + "\n",
		`{not json` + "\n",
		`{"id":"kg-orphan","owner":{"kind":"archive","sha":"Z"},"payload":{"s":"orphan"}}` + "\n",
		`{"id":"kg-torn","owner":{"kind":"archive","sha":"A1"},"payl`,
	} {
		if _, err := f.WriteString(l); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return v
}

// ReadKG's KG view and ledger are exactly ReadStore's.
func TestReadKGMatchesReadStore(t *testing.T) {
	v := kgSnapshotFixture(t)
	full, err := ReadStore(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	kg, err := ReadKG(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, live := range []bool{true, false} {
		if got, want := kg.KG(live), full.KG(live); !reflect.DeepEqual(got, want) {
			t.Errorf("KG(%v):\n got  %+v\n want %+v", live, got, want)
		}
	}
	all := full.KG(false)
	if len(full.KG(true)) == 0 || len(full.KG(true)) >= len(all) {
		t.Fatalf("fixture: want some live and some not-live records, got %d of %d live", len(full.KG(true)), len(all))
	}
	for _, s := range []string{"S1", "S2", "nope"} {
		gs, gok := kg.Ledger().Session(s)
		ws, wok := full.Ledger().Session(s)
		if gok != wok || !reflect.DeepEqual(gs, ws) {
			t.Errorf("Session(%s) = %+v %v, want %+v %v", s, gs, gok, ws, wok)
		}
	}
	for _, rec := range all {
		for _, o := range rec.Owners {
			gs, gd, gok := kg.Ledger().LiveOwner(o)
			ws, wd, wok := full.Ledger().LiveOwner(o)
			if gs != ws || gd != wd || gok != wok {
				t.Errorf("LiveOwner(%+v) = %q %q %v, want %q %q %v", o, gs, gd, gok, ws, wd, wok)
			}
		}
	}
}

// ReadKG never reads chunks.jsonl: counted through the read seam, and with the
// file made unreadable.
func TestReadKGNeverReadsChunks(t *testing.T) {
	v := kgSnapshotFixture(t)
	pf, err := filesFor(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("read seam", func(t *testing.T) {
		var read []string
		old := readFileFn
		readFileFn = func(p string) ([]byte, error) { read = append(read, p); return old(p) }
		defer func() { readFileFn = old }()
		if _, err := ReadKG(v, "alpha"); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(read, pf.chunks) {
			t.Errorf("ReadKG read chunks.jsonl: %q", read)
		}
		for _, want := range []string{pf.ledger, pf.kg} {
			if !slices.Contains(read, want) {
				t.Errorf("ReadKG did not read %s: %q", want, read)
			}
		}
	})
	t.Run("unreadable chunks.jsonl", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("chmod 000 does not stop this user reading")
		}
		if err := os.Chmod(pf.chunks, 0); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(pf.chunks, 0o644)
		if _, err := ReadStore(v, "alpha"); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("fixture: ReadStore = %v, want a permission error", err)
		}
		if _, err := ReadKG(v, "alpha"); err != nil {
			t.Errorf("ReadKG with chunks.jsonl unreadable: %v", err)
		}
	})
}
