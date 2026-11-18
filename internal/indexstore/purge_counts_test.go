// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

// KG honesty, purge side (plan v3 §3): the purge reports what it removed in
// counts, keeps them through a partial failure, and distinguishes committed
// from attempted.
//
// The new fields are read by NAME through reflect so these tests COMPILE on
// the code before the change and fail on the absent field (red for the right
// reason), rather than failing to build.

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func purgeField(t *testing.T, res TranscriptPurge, name string) reflect.Value {
	t.Helper()
	f := reflect.ValueOf(res).FieldByName(name)
	if !f.IsValid() {
		t.Fatalf("TranscriptPurge has no field %s", name)
	}
	return f
}

func purgeInt(t *testing.T, res TranscriptPurge, name string) int {
	t.Helper()
	return int(purgeField(t, res, name).Int())
}

// kgCountStore seeds alpha with REAL entity/triple record ids:
//
//	archive-owned only: entities e1, e2; triples t1, t2, t3   (5 records go)
//	owned by archive AND a batch: entity eShared               (survives, trimmed)
//	batch-owned only: entity eBatch                            (survives)
func kgCountStore(t *testing.T, v *storage.Vault) {
	t.Helper()
	const sha = "sha-archive-1"
	fakeArchives(t, sha)
	ent := func(n string) KGRecord {
		id, p := index.EntityRecord(n, n, "concept")
		return KGRecord{ID: id, Payload: p}
	}
	tri := func(s string) KGRecord {
		id, p := index.TripleRecord(s, "relates", "x", "")
		return KGRecord{ID: id, Payload: p}
	}
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		c := commitOf("sess-a", sha, "2026-05-13", "archive chunk one")
		c.KG = []KGRecord{ent("e1"), ent("e2"), ent("eShared"), tri("t1"), tri("t2"), tri("t3")}
		if err := tx.CommitArchive(c, noVectors{}); err != nil {
			return err
		}
		bc := BatchCommit{BatchID: "batch-m", StartDay: "2026-05-13", Vectors: map[string][]float32{}}
		bc.Chunks = append(bc.Chunks, ownedChunk("imported batch chunk", "alpha", "general"))
		bc.KG = []KGRecord{ent("eShared"), ent("eBatch")}
		return tx.CommitBatch(bc, nil)
	})
}

// A successful purge reports entities and triples removed, and a record shared
// with a batch is NOT counted as removed.
//
// Q3: the fixture holds archive-only, shared and batch-only records, so a
// predicate that counted every record (or every archive owner line) differs.
//
// Mutation (M7): count after dropOwnersWhere, or count shared records.
func TestThePurgeCountsTheKGRecordsItRemoved(t *testing.T) {
	v := newVault(t)
	kgCountStore(t, v)
	if got := kgOwnerKinds(t, v); got[OwnerArchive] == 0 || got[OwnerBatch] == 0 {
		t.Fatalf("PRESENCE: the fixture lacks archive and batch KG owners: %v", got)
	}
	res := purgeAlpha(t, v)
	if got := purgeInt(t, res, "KGEntitiesDropped"); got != 2 {
		t.Errorf("KGEntitiesDropped = %d, want 2 (e1, e2; eShared survives under its batch owner)", got)
	}
	if got := purgeInt(t, res, "KGTriplesDropped"); got != 3 {
		t.Errorf("KGTriplesDropped = %d, want 3", got)
	}
	if !res.KGDropped {
		t.Error("KGDropped (the existing bool) must stay true")
	}
	steps := purgeField(t, res, "Completed").Interface().([]string)
	if want := []string{"purge-ledger", "purge-chunks", "purge-kg", "purge-vectors"}; !slices.Equal(steps, want) {
		t.Errorf("Completed = %v, want %v", steps, want)
	}
}

// A purge that fails at purge-kg keeps what committed and names what it only
// attempted. The injected error is not a kill (see the block comment in
// purge_transcripts_test.go): only file content and the returned struct are
// asserted.
//
// Mutation (M11, store side): drop the attempted/committed split.
func TestAFailedPurgeKeepsCommittedAndAttemptedCounts(t *testing.T) {
	v := newVault(t)
	kgCountStore(t, v)
	boom := errors.New("injected: disk full")
	defer SetCommitStepHook(func(step string) error {
		if step == "purge-kg" {
			return boom
		}
		return nil
	})()

	var res TranscriptPurge
	var perr error
	tx, err := Lock(t.Context(), v, "alpha", NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	res, perr = tx.DropTranscriptTier(func() (map[string]bool, error) { return map[string]bool{}, nil })
	_ = tx.Release()
	if !errors.Is(perr, boom) {
		t.Fatalf("the purge returned %v, want the injected error", perr)
	}
	steps := purgeField(t, res, "Completed").Interface().([]string)
	if want := []string{"purge-ledger", "purge-chunks"}; !slices.Equal(steps, want) {
		t.Errorf("Completed = %v, want %v (purge-kg did not commit)", steps, want)
	}
	if got := purgeInt(t, res, "KGEntitiesDropped") + purgeInt(t, res, "KGTriplesDropped"); got != 0 {
		t.Errorf("committed KG counts = %d, want 0: the KG step never committed", got)
	}
	if e, tr := purgeInt(t, res, "KGEntitiesAttempted"), purgeInt(t, res, "KGTriplesAttempted"); e != 2 || tr != 3 {
		t.Errorf("attempted KG counts = %d entities, %d triples; want 2 and 3", e, tr)
	}
	if got := purgeField(t, res, "Err").String(); got == "" {
		t.Error("Err is empty on a failed purge")
	}
	if res.ChunksDropped == 0 {
		t.Error("the committed chunk count was lost on the error return")
	}
}
