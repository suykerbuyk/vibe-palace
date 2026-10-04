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

// fileVW writes each vector as a .vec file in the project's embed cache, the
// layout the reaper reads.
type fileVW struct{ v *storage.Vault }

func (w fileVW) Put(project, id string, vec []float32) error {
	dir, err := w.v.EmbedCacheDir(project)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, id+".vec"), []byte{1}, 0o644)
}

func vecExists(t *testing.T, v *storage.Vault, id string) bool {
	t.Helper()
	dir, _ := v.EmbedCacheDir("alpha")
	_, err := os.Stat(filepath.Join(dir, id+".vec"))
	return err == nil
}

func noOtherLive() (map[string]bool, error) { return map[string]bool{}, nil }

func reap(t *testing.T, v *storage.Vault, other func() (map[string]bool, error)) []string {
	t.Helper()
	var got []string
	mustTx(t, v, func(tx *Tx) error {
		var err error
		got, err = tx.Reap(other)
		return err
	})
	return got
}

// A CommitArchive killed after its chunks, whose archive A is then recorded
// superseded, leaves owner lines, records and vectors nothing collects. Reap
// drops A's ownership, deletes what A alone owned (Y), keeps what another live
// archive also owns (X), and unlinks the vectors of the deleted chunks.
func TestReapCollectsAKilledArchiveLaterSuperseded(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		return tx.CommitArchive(commitOf("B", "Bsha", "2026-05-14", "X"), fileVW{v})
	})
	killAt(t, "kg")
	_ = withTx(t, v, func(tx *Tx) error { return tx.CommitArchive(commitOf("S", "A", "2026-05-13", "X", "Y"), fileVW{v}) })
	commitStep = func(string) error { return nil }
	mustTx(t, v, func(tx *Tx) error {
		if err := tx.CommitArchive(commitOf("S", "A2", "2026-05-15", "Z"), fileVW{v}); err != nil {
			return err
		}
		return tx.RecordSuperseded("S", "A", "")
	})

	got := reap(t, v, noOtherLive)
	if !slices.Equal(got, []string{idOf("Y")}) {
		t.Fatalf("reaped %v, want only Y's vector", got)
	}
	s := ledgered(t, v)
	if ids := ids(s.Chunks(false)); !slices.Equal(ids, sorted(idOf("X"), idOf("Z"))) {
		t.Fatalf("stored %v, want X and Z", ids)
	}
	for _, c := range s.Chunks(false) {
		if slices.Contains(c.Owners, ArchiveOwner("A")) {
			t.Fatalf("chunk %q is still owned by the superseded archive A", c.Content)
		}
	}
	for _, k := range s.KG(false) {
		if slices.Contains(k.Owners, ArchiveOwner("A")) {
			t.Fatalf("KG record %s is still owned by A", k.ID)
		}
	}
	if !vecExists(t, v, idOf("X")) || !vecExists(t, v, idOf("Z")) || vecExists(t, v, idOf("Y")) {
		t.Fatal("vectors: want X and Z kept, Y gone")
	}
}

// After a Supersede pruned chunk Y, Reap unlinks Y's vector and keeps the
// vectors of the chunks still stored, and of the ids another source holds.
func TestReapCollectsVectorsASupersedePruned(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		if err := tx.CommitArchive(commitOf("S", "A", "2026-05-13", "X", "Y"), fileVW{v}); err != nil {
			return err
		}
		return tx.Supersede(commitOf("S", "A2", "2026-05-14", "X", "Z"), fileVW{v})
	})
	if err := (fileVW{v}).Put("alpha", "drawer-1", nil); err != nil {
		t.Fatal(err)
	}
	got := reap(t, v, func() (map[string]bool, error) { return map[string]bool{"drawer-1": true}, nil })
	if !slices.Equal(got, []string{idOf("Y")}) {
		t.Fatalf("reaped %v, want only Y", got)
	}
	for _, id := range []string{idOf("X"), idOf("Z"), "drawer-1"} {
		if !vecExists(t, v, id) {
			t.Fatalf("live vector %s was reaped", id)
		}
	}
}

// A pending archive's records survive Reap: a CommitArchive killed before its
// ledger entry is an ingest in progress, and its chunks and vectors stay so a
// rerun completes it without re-embedding. A batch killed the same way stays
// too.
func TestReapKeepsAPendingArchive(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error { _, err := tx.EnsureLedger(nil); return err })
	killAt(t, "kg")
	_ = withTx(t, v, func(tx *Tx) error { return tx.CommitArchive(commitOf("S", "A", "2026-05-13", "X"), fileVW{v}) })
	_ = withTx(t, v, func(tx *Tx) error {
		return tx.CommitBatch(BatchCommit{BatchID: "batch:m", StartDay: "2026-04-02",
			Chunks: []OwnedChunk{ownedChunk("B1", "alpha", "general")}, Vectors: map[string][]float32{idOf("B1"): {1}}}, fileVW{v})
	})
	commitStep = func(string) error { return nil }
	if got := reap(t, v, noOtherLive); len(got) != 0 {
		t.Fatalf("reaped %v from a pending archive and a pending batch", got)
	}
	if got := ids(ledgered(t, v).Chunks(false)); !slices.Equal(got, sorted(idOf("X"), idOf("B1"))) {
		t.Fatalf("stored %v after the reap, want X and B1", got)
	}
	if !vecExists(t, v, idOf("X")) || !vecExists(t, v, idOf("B1")) {
		t.Fatal("a pending record's vector was reaped")
	}
}

// A Reap with nothing to collect writes nothing: the counter does not move,
// so running engines are not made to reload on every rebuild.
func TestReapWithNothingToCollectWritesNothing(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		if err := tx.CommitArchive(commitOf("S", "A", "2026-05-13", "X"), fileVW{v}); err != nil {
			return err
		}
		return tx.Supersede(commitOf("S", "A2", "2026-05-14", "X"), fileVW{v})
	})
	reap(t, v, noOtherLive)
	before := readGen(t, v, "alpha")
	reap(t, v, noOtherLive)
	if after := readGen(t, v, "alpha"); after != before {
		t.Fatalf("a reap with nothing to collect moved the counter %+v -> %+v", before, after)
	}
}

// A failing live-set read aborts the vector step: nothing is unlinked on the
// strength of a live set that could not be read.
func TestReapAbortsOnALiveSetError(t *testing.T) {
	v := newVault(t)
	if err := (fileVW{v}).Put("alpha", "drawer-1", nil); err != nil {
		t.Fatal(err)
	}
	err := withTx(t, v, func(tx *Tx) error {
		_, err := tx.Reap(func() (map[string]bool, error) { return nil, errors.New("unreadable room") })
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "unreadable room") {
		t.Fatalf("err=%v, want the live-set error", err)
	}
	if !vecExists(t, v, "drawer-1") {
		t.Fatal("a vector was reaped although the live set could not be read")
	}
}

// The reaper is safe across processes: a helper ingests archives in a loop,
// writing each archive's vectors and chunks in one commit step, while this
// process reaps in a loop. Afterwards every chunk in the store has its vector.
func TestReaperIsSafeAcrossProcesses(t *testing.T) {
	v := newVault(t)
	h := startHelper(t, v, "ingest-loop")
	h.ready(t)
	touch(t, h.file("go"))
	for !fileExists(h.file("done")) {
		reap(t, v, noOtherLive)
	}
	h.wait(t)
	reap(t, v, noOtherLive)
	cs := ledgered(t, v).Chunks(false)
	if len(cs) != 200 {
		t.Fatalf("%d chunks stored, want 200", len(cs))
	}
	for _, c := range cs {
		if !vecExists(t, v, c.ID) {
			t.Fatalf("chunk %q has no vector: the reaper unlinked a vector of a committed chunk", c.Content)
		}
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// Reap holds the index commit lock: a reap in another goroutine started
// between a commit's vector step and its chunk step waits for the commit, and
// then keeps the vector that commit made durable.
func TestReapWaitsForAnInFlightCommit(t *testing.T) {
	v := newVault(t)
	mustTx(t, v, func(tx *Tx) error { _, err := tx.EnsureLedger(nil); return err })
	done := make(chan []string, 1)
	old := commitStep
	commitStep = func(step string) error {
		if step == "vectors" {
			go func() {
				tx, err := Lock(context.Background(), v, "alpha", NoTimeout)
				if err != nil {
					t.Error(err)
					done <- nil
					return
				}
				tx.UseRecipe(testRecipe)
				got, err := tx.Reap(noOtherLive)
				if err != nil {
					t.Error(err)
				}
				_ = tx.Commit()
				done <- got
			}()
		}
		return nil
	}
	mustTx(t, v, func(tx *Tx) error { return tx.CommitArchive(commitOf("S", "A", "2026-05-13", "X"), fileVW{v}) })
	commitStep = old
	var got []string
	within(t, "the reap after the commit", func() { got = <-done })
	if len(got) != 0 || !vecExists(t, v, idOf("X")) {
		t.Fatalf("the reap unlinked %v; X's vector, made durable by the commit it waited for, must stay", got)
	}
}
