// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

// Guards for Tx.DropTranscriptTier.
//
// 🔴 EVERY GUARD IN THE G9 BLOCK ASSERTS FILE CONTENT ONLY. An injected error
// through SetCommitStepHook is NOT a kill: it unwinds through
// `defer tx.Release()`, and Release -> finishLocked performs the finishing
// generation bump exactly as Commit does. No guard here may assert anything
// about the generation counter; K1 is the real-kill guard that can.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// mixedOwnerStore seeds alpha with chunks and KG records under all three
// owner kinds, and ledger lines of every kind the purge must keep or drop.
// It returns the archive sha it ingested.
//
// This is the Q3 PRECONDITION for the whole block: without an archive owner
// there is nothing for the purge to remove, and without a batch and a note
// owner "only archive owners went" is true of a predicate that removes
// everything.
func mixedOwnerStore(t *testing.T, v *storage.Vault) string {
	t.Helper()
	const sha = "sha-archive-1"
	fakeArchives(t, sha, "sha-backlog-1")
	mustTx(t, v, func(tx *Tx) error {
		if _, err := tx.EnsureLedger(nil); err != nil {
			return err
		}
		// An ingested archive: chunks + KG + a recSession line.
		if err := tx.CommitArchive(commitOf("sess-a", sha, "2026-05-13",
			"archive chunk one", "archive chunk two"), noVectors{}); err != nil {
			return err
		}
		// A mempalace import batch: chunks AND KG under one BatchOwner, and a
		// recBatch ledger line. NOT re-derivable from the vault, which is why
		// the purge rewrites the ledger instead of removing it.
		bc := BatchCommit{BatchID: "batch-m", StartDay: "2026-05-13", Vectors: map[string][]float32{}}
		oc := ownedChunk("imported batch chunk", "alpha", "general")
		bc.Chunks = append(bc.Chunks, oc)
		bc.KG = append(bc.KG, KGRecord{ID: "kg-batch-1", Payload: []byte(`{"s":"batch"}`)})
		if err := tx.CommitBatch(bc, nil); err != nil {
			return err
		}
		// A note-owned decision chunk: re-derivable, but it must survive.
		return tx.Append(NoteOwner("sessions/2026-05-13-aaaa0000-01.md"),
			withDay([]OwnedChunk{ownedChunk("a note-owned decision chunk", "alpha", "general")}, "2026-05-13"))
	})
	return sha
}

// alphaFiles is the project's on-disk index paths, from production's own
// resolver rather than a hand-written path a layout change would rot.
func alphaFiles(t *testing.T, v *storage.Vault) projectFiles {
	t.Helper()
	pf, err := filesFor(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	return pf
}

func purgeAlpha(t *testing.T, v *storage.Vault) TranscriptPurge {
	t.Helper()
	var res TranscriptPurge
	mustTx(t, v, func(tx *Tx) error {
		var err error
		res, err = tx.DropTranscriptTier(func() (map[string]bool, error) { return map[string]bool{}, nil })
		if err != nil {
			return err
		}
		return tx.Commit()
	})
	return res
}

// ownerKinds is the set of owner kinds the chunk fold still carries.
func ownerKinds(t *testing.T, v *storage.Vault) map[string]int {
	t.Helper()
	s := ledgered(t, v)
	out := map[string]int{}
	for _, k := range []string{OwnerArchive, OwnerBatch, OwnerNote} {
		if n := s.CountOwnersOfKind(k); n > 0 {
			out[k] = n
		}
	}
	return out
}

// kgOwnerKinds is the same for the KG fold, read from the file so the chunk
// side cannot stand in for it.
func kgOwnerKinds(t *testing.T, v *storage.Vault) map[string]int {
	t.Helper()
	out := map[string]int{}
	data, err := os.ReadFile(alphaFiles(t, v).kg)
	if os.IsNotExist(err) {
		return out
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec struct {
			Owners []Owner `json:"owners"`
			Owner  *Owner  `json:"owner"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		owners := rec.Owners
		if rec.Owner != nil {
			owners = append(owners, *rec.Owner)
		}
		for _, o := range owners {
			out[o.Kind]++
		}
	}
	return out
}

// ledgerKinds counts the ledger's lines by record kind.
func ledgerKinds(t *testing.T, v *storage.Vault) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, l := range fileLines(t, alphaFiles(t, v).ledger) {
		if l == "" {
			continue
		}
		var rec struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal([]byte(l), &rec) != nil {
			continue
		}
		out[rec.Kind]++
	}
	return out
}

// G9 — TestThePurgeDropsOnlyArchiveOwnedCHUNKS.
//
// Q1 PATH: component (it drives the Tx directly), and it is the right level:
// the thing under test IS the Tx method.
// Q2 VALUE: all three owner kinds are present before, so "only archive went"
// is a discriminating outcome.
// Q3 PRESENCE (G9b): > 0 chunks of EACH of the archive, note and batch owner
// kinds before the purge, asserted.
//
// Mutations, one predicate arm at a time:
//
//	(a) chunk predicate -> `return true`: the batch and note arms red.
//	(b) chunk predicate -> `o.Kind == OwnerBatch`: the archive arm reds.
func TestThePurgeDropsOnlyArchiveOwnedCHUNKS(t *testing.T) {
	v := newVault(t)
	mixedOwnerStore(t, v)

	before := ownerKinds(t, v)
	for _, k := range []string{OwnerArchive, OwnerBatch, OwnerNote} {
		if before[k] == 0 {
			t.Fatalf("PRESENCE (G9b): no %s-owned chunk before the purge: %v", k, before)
		}
	}

	res := purgeAlpha(t, v)
	if res.ChunksDropped == 0 {
		t.Fatalf("the purge reports dropping no chunk: %+v", res)
	}

	after := ownerKinds(t, v)
	if after[OwnerArchive] != 0 {
		t.Fatalf("%d archive-owned chunk(s) survived the purge: %v", after[OwnerArchive], after)
	}
	if after[OwnerBatch] != before[OwnerBatch] {
		t.Fatalf("the purge destroyed BATCH-owned chunks (%d -> %d). Imported mempalace batches are "+
			"NOT re-derivable from the vault; preserving them is the whole reason this is not "+
			"Tx.Discard(DiscardChunks). (The guarantee is scoped to the PURGE: a chunks-recipe "+
			"mismatch earlier in the same rebuild still discards them through that coarse path, "+
			"which is a separately filed defect.)", before[OwnerBatch], after[OwnerBatch])
	}
	if after[OwnerNote] != before[OwnerNote] {
		t.Fatalf("the purge destroyed NOTE-owned chunks (%d -> %d)", before[OwnerNote], after[OwnerNote])
	}
}

// G9c — TestThePurgeDropsOnlyArchiveOwnedKG. The KG side, which G9 cannot see:
// chunks and KG share one BatchOwner, and kgFold.dropOwnersWhere is a SEPARATE
// function with a SEPARATE signature (it takes no keep map), so a KG-side
// mistake would destroy imported batch KG with every chunk-side assertion
// green.
//
// Q3 PRESENCE (G9d): > 0 KG records under a BatchOwner AND > 0 under an
// ArchiveOwner before, asserted.
//
// Mutations, neither of which touches the chunk-side predicate:
//
//	(a) KG predicate -> `return true`: the batch-KG assertion reds.
//	(b) KG predicate -> `o.Kind == OwnerBatch`: the archive-KG assertion reds.
func TestThePurgeDropsOnlyArchiveOwnedKG(t *testing.T) {
	v := newVault(t)
	mixedOwnerStore(t, v)

	before := kgOwnerKinds(t, v)
	if before[OwnerArchive] == 0 {
		t.Fatalf("PRESENCE (G9d): no archive-owned KG record before the purge: %v", before)
	}
	if before[OwnerBatch] == 0 {
		t.Fatalf("PRESENCE (G9d): no batch-owned KG record before the purge, so 'batch KG survived' "+
			"is true of a predicate that removes everything: %v", before)
	}

	purgeAlpha(t, v)

	after := kgOwnerKinds(t, v)
	if after[OwnerArchive] != 0 {
		t.Fatalf("%d archive-owned KG record(s) survived the purge: %v", after[OwnerArchive], after)
	}
	if after[OwnerBatch] != before[OwnerBatch] {
		t.Fatalf("the purge destroyed BATCH-owned KG (%d -> %d). The KG predicate is a second "+
			"function on a second fold; a chunk-side assertion cannot see this", before[OwnerBatch], after[OwnerBatch])
	}
}

// G9e — TestThePurgeKeepsTheBatchAndBaselineLedgerLines.
//
// Q2 VALUE: recBatch and baseline lines present before and after; session
// lines present before and gone after.
// Q3 PRESENCE: recBatch AND baseline lines present before, AND ≥ 1 recSession
// line to drop, all asserted.
//
// Mutation: replace the in-place rewrite with
// removeIfExists(tx.files.ledger) + freshBaseline(nil), i.e. revision 1's
// shape. The recBatch lines then vanish.
func TestThePurgeKeepsTheBatchAndBaselineLedgerLines(t *testing.T) {
	v := newVault(t)
	mixedOwnerStore(t, v)

	before := ledgerKinds(t, v)
	if before[recBatch] == 0 {
		t.Fatalf("PRESENCE: no recBatch line before the purge: %v", before)
	}
	if before[recBaseline] == 0 {
		t.Fatalf("PRESENCE: no baseline line before the purge: %v", before)
	}
	if before[recSession] == 0 {
		t.Fatalf("PRESENCE: no recSession line to drop: %v", before)
	}

	purgeAlpha(t, v)

	after := ledgerKinds(t, v)
	if after[recSession] != 0 {
		t.Fatalf("%d recSession line(s) survived the purge: %v", after[recSession], after)
	}
	if after[recBatch] != before[recBatch] {
		t.Fatalf("the purge dropped recBatch lines (%d -> %d). internal/ingest's repair pass checks "+
			"imported batches against them, and the batch data they name is not re-derivable. "+
			"(Scoped to the PURGE: discardOnMismatch earlier in the same rebuild still removes "+
			"the whole ledger, recBatch lines included.)",
			before[recBatch], after[recBatch])
	}
	if after[recBaseline] != before[recBaseline] {
		t.Fatalf("the purge dropped the baseline record (%d -> %d). It is the automatic ingester's "+
			"skip-list for a backlog that is still un-ingested", before[recBaseline], after[recBaseline])
	}
	// The dropped archives were ADDED to the skip-list, additively.
	s := ledgered(t, v)
	if !s.Ledger().InBaseline("sha-archive-1") {
		t.Fatal("the purged archive is not in the baseline set: on a later opt-in the automatic " +
			"ingester would take it 3-at-a-time, which is the drowning the baseline set prevents")
	}
}

// G9g — TestEveryPurgeIntermediateLeavesNoLoadableTranscript.
//
// 🔴 FILE CONTENT ONLY. See the block comment at the top of this file.
//
// Q3 PRESENCE: ≥ 1 live-owned archive chunk before, asserted — otherwise "no
// archive-owned chunk" is trivially true at every step.
//
// Mutation: reorder the purge to drop chunks BEFORE the ledger rewrite. The
// kill-after-chunks case then leaves a StateLive session record over a store
// with no chunks for it — the state discard.go's comment forbids: "a kill
// could leave a ledger that says an archive is ingested over a store that no
// longer holds its chunks, and search would answer from nothing without a
// word."
func TestEveryPurgeIntermediateLeavesNoLoadableTranscript(t *testing.T) {
	for _, step := range []string{"purge-ledger", "purge-chunks", "purge-kg", "purge-vectors"} {
		t.Run(step, func(t *testing.T) {
			v := newVault(t)
			mixedOwnerStore(t, v)
			if loadableArchiveChunks(t, v) == 0 {
				t.Fatal("PRESENCE: no live-owned archive chunk before the purge, so 'none loadable' " +
					"is trivially true at every step")
			}

			killAt(t, step)
			_ = withTx(t, v, func(tx *Tx) error {
				_, err := tx.DropTranscriptTier(nil)
				return err
			})

			// After the ledger step and every later one, nothing archive-owned
			// is LOADABLE: Chunks(true) skips a chunk with no live owner.
			if n := loadableArchiveChunks(t, v); n != 0 {
				t.Fatalf("after a kill at %q, %d archive-owned chunk(s) are still loadable. "+
					"The ledger must go FIRST: any other order can leave a ledger saying an archive "+
					"is ingested over a store that no longer holds its chunks", step, n)
			}
			// 🔴 And no LIVE session record is left over a store that no
			// longer holds its chunks. This is the state discard.go:52-54
			// names: "a kill could leave a ledger that says an archive is
			// ingested over a store that no longer holds its chunks, and
			// search would answer from nothing without a word." Dropping the
			// chunks first produces exactly it, and the "none loadable"
			// assertion above cannot see it — with the chunks already gone,
			// Chunks(true) yields none either way.
			st := ledgered(t, v)
			for _, rec := range liveSessions(st.Ledger()) {
				if got := st.CountChunks(ArchiveOwner(rec.SHA)); got < rec.ChunkCount {
					t.Fatalf("after a kill at %q the ledger still says session %q is LIVE with %d "+
						"chunks, and the store holds %d. The ledger must be rewritten FIRST, so an "+
						"interrupted purge never leaves a ledger claiming an archive the store no "+
						"longer has", step, rec.SessionID, rec.ChunkCount, got)
				}
			}
			// And the batch data is intact at every intermediate.
			if ownerKinds(t, v)[OwnerBatch] == 0 {
				t.Fatalf("a kill at %q destroyed the batch-owned chunks", step)
			}
		})
	}
}

// loadableArchiveChunks counts the archive-owned chunks Store.Chunks(true)
// yields — exactly what search would serve.
func loadableArchiveChunks(t *testing.T, v *storage.Vault) int {
	t.Helper()
	s := ledgered(t, v)
	n := 0
	for _, c := range s.Chunks(true) {
		if c.Selected != nil && c.Selected.Kind == OwnerArchive {
			n++
		}
	}
	return n
}

// G9h — TestThePurgeIsIdempotent. Run twice; the FOLDED state converges.
//
// 🔴 IT MUST NOT ASSERT FILE BYTES. AddToBaseline APPENDS a recBaselineAdd
// line, so a second purge leaves different bytes while the folded baseline set
// is equal. Asserting bytes would redden on a CORRECT implementation.
//
// Q3 PRESENCE: run 1 must actually drop something, asserted.
//
// Mutation: make the ledger rewrite non-convergent, or make AddToBaseline
// replace rather than union.
func TestThePurgeIsIdempotent(t *testing.T) {
	v := newVault(t)
	sha := mixedOwnerStore(t, v)

	first := purgeAlpha(t, v)
	if first.LedgerLinesDropped == 0 && first.ChunksDropped == 0 {
		t.Fatalf("PRESENCE: run 1 dropped nothing, so convergence proves nothing: %+v", first)
	}
	oneChunks, oneKG, oneLedger := ownerKinds(t, v), kgOwnerKinds(t, v), ledgerKinds(t, v)
	oneBaseline := baselineOf(ledgered(t, v).Ledger())

	// 🔴 The state after run 1 must itself be RIGHT, not merely stable.
	// Comparing run 1 with run 2 alone cannot see a purge that damages the
	// store identically every time — and the specific damage this pins is
	// AddToBaseline being made to REPLACE rather than UNION, which drops the
	// pre-existing backlog from the automatic ingester's skip-list on run 1
	// and then has nothing left to change on run 2.
	l1 := ledgered(t, v).Ledger()
	if !l1.InBaseline(sha) {
		t.Fatalf("after run 1 the purged archive %q is not in the baseline set: %v", sha, oneBaseline)
	}
	if !l1.InBaseline("sha-backlog-1") {
		t.Fatalf("after run 1 the PRE-EXISTING backlog archive left the baseline set: %v. "+
			"AddToBaseline must UNION, not replace: the set is the automatic ingester's skip-list, "+
			"and shrinking it hands those archives to the automatic path on a later opt-in",
			oneBaseline)
	}
	if oneLedger[recBatch] == 0 {
		t.Fatalf("after run 1 the recBatch lines are gone: %v", oneLedger)
	}
	if oneLedger[recSession] != 0 {
		t.Fatalf("after run 1 recSession lines survive: %v", oneLedger)
	}

	purgeAlpha(t, v)

	if got := ownerKinds(t, v); !mapsEqual(got, oneChunks) {
		t.Fatalf("the chunk owner set moved on the second purge: %v -> %v", oneChunks, got)
	}
	if got := kgOwnerKinds(t, v); !mapsEqual(got, oneKG) {
		t.Fatalf("the KG owner set moved on the second purge: %v -> %v", oneKG, got)
	}
	if got := ledgerKinds(t, v); got[recSession] != 0 || got[recBatch] != oneLedger[recBatch] {
		t.Fatalf("the ledger's kept/dropped kinds moved on the second purge: %v -> %v", oneLedger, got)
	}
	got := baselineOf(ledgered(t, v).Ledger())
	if !slices.Equal(got, oneBaseline) {
		t.Fatalf("the FOLDED baseline set moved on the second purge: %v -> %v", oneBaseline, got)
	}
	if !ledgered(t, v).Ledger().InBaseline(sha) {
		t.Fatalf("the dropped archive %q left the baseline set on the second purge", sha)
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// G9i — TestThePurgeDoesNotWriteTheFingerprint.
//
// 🔴 OBSERVE THE WRITE, NOT THE VALUE. A re-added fingerprint write stores
// recipe.Sum(), which on an already-built host is the value already in the
// file — so a byte comparison stays green on its own mutation. This records
// every path the writeFileFn seam saw.
//
// Q3 PRESENCE: chunks.fingerprint EXISTS and the Tx has a recipe set, both
// asserted — a re-added write would not run either if they were absent.
//
// Mutation: re-introduce a `writeFile(tx.files.fingerprint, []byte(r.Sum()))`
// step in the purge; the path then appears in the recorded set.
func TestThePurgeDoesNotWriteTheFingerprint(t *testing.T) {
	v := newVault(t)
	mixedOwnerStore(t, v)

	// Give the store a fingerprint, as an already-built host has.
	fp := alphaFiles(t, v).fingerprint
	if err := os.WriteFile(fp, []byte("a-recipe-sum"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fp); err != nil {
		t.Fatalf("PRESENCE: chunks.fingerprint does not exist, so a re-added write would be the "+
			"only write and this guard would be about nothing: %v", err)
	}

	var paths []string
	old := writeFileFn
	writeFileFn = func(path string, data []byte) error {
		paths = append(paths, path)
		return old(path, data)
	}
	t.Cleanup(func() { writeFileFn = old })

	mustTx(t, v, func(tx *Tx) error {
		// PRESENCE: the Tx carries a recipe, so a re-added write has one to
		// take a Sum() from.
		tx.UseRecipe(testRecipe)
		if _, err := tx.DropTranscriptTier(nil); err != nil {
			return err
		}
		return tx.Commit()
	})

	if len(paths) == 0 {
		t.Fatal("PRESENCE: the purge wrote NO file at all, so 'it did not write the fingerprint' " +
			"is true of a purge that did nothing")
	}
	for _, p := range paths {
		if filepath.Base(p) == "chunks.fingerprint" {
			t.Fatalf("the purge wrote %s. It does not change the chunk recipe, so rewriting the "+
				"fingerprint could never make discardOnMismatch fire — and if it somehow did, that "+
				"recovery is Tx.Discard(DiscardChunks), which destroys the batch data this ordering "+
				"exists to preserve. Paths written: %v", p, paths)
		}
	}
}

// G9j — TestAPartialPurgeIsFinishedByRerunning.
//
// Q3 PRESENCE: ≥ 1 live-owned archive chunk before, asserted.
//
// Mutation: make any purge step non-idempotent.
func TestAPartialPurgeIsFinishedByRerunning(t *testing.T) {
	v := newVault(t)
	mixedOwnerStore(t, v)
	if loadableArchiveChunks(t, v) == 0 {
		t.Fatal("PRESENCE: no live-owned archive chunk to purge")
	}

	// Stop at the ledger step, as a kill would: the ledger write has landed
	// and the chunk rewrite has not, so there IS a partial state to finish.
	killAt(t, "purge-ledger")
	if err := withTx(t, v, func(tx *Tx) error {
		_, err := tx.DropTranscriptTier(nil)
		return err
	}); !errors.Is(err, errKill) {
		t.Fatalf("the injected kill at purge-ledger did not stop the purge: %v", err)
	}
	restoreCommitStep(t)
	if ownerKinds(t, v)[OwnerArchive] == 0 {
		t.Fatal("PRESENCE: the kill at purge-ledger already reclaimed the chunks, so there is no " +
			"partial state for the re-run to finish")
	}
	if loadableArchiveChunks(t, v) != 0 {
		t.Fatal("the ledger step landed but archive chunks are still loadable")
	}

	// Re-running finishes it.
	purgeAlpha(t, v)
	if n := ownerKinds(t, v)[OwnerArchive]; n != 0 {
		t.Fatalf("re-running the purge left %d archive-owned chunk(s); it is not resumable", n)
	}
	if ownerKinds(t, v)[OwnerBatch] == 0 {
		t.Fatal("finishing the purge destroyed the batch-owned chunks")
	}
}

// G9k — TestThePurgeIsANoOpAfterACoarseDiscard (E3).
//
// `rebuildProject` calls discardOnMismatch at step 3, BEFORE the purge, and on
// a chunks-recipe mismatch that reaches Tx.Discard(DiscardChunks) — whose
// coarse path removes ledger, chunks, KG and graph wholesale, batch data
// included. §H's preservation protects the PURGE path and gives NO protection
// there. This guard pins only that the purge composes cleanly afterwards; the
// batch-data loss on that path is a filed defect, not something this unit
// solves.
//
// Q3 PRESENCE: archive owners present BEFORE the Discard, asserted, so the
// discard really had something to remove.
//
// Mutation: make the purge error when it finds no archive owner.
func TestThePurgeIsANoOpAfterACoarseDiscard(t *testing.T) {
	v := newVault(t)
	mixedOwnerStore(t, v)
	if ownerKinds(t, v)[OwnerArchive] == 0 {
		t.Fatal("PRESENCE: no archive owner before the discard, so its removal proves nothing")
	}

	mustTx(t, v, func(tx *Tx) error {
		tx.UseRecipe(testRecipe)
		if err := tx.Discard(DiscardChunks); err != nil {
			return err
		}
		return tx.Commit()
	})

	res := purgeAlpha(t, v)
	if res.ChunksDropped != 0 || res.LedgerLinesDropped != 0 {
		t.Fatalf("the purge after a coarse discard was not a no-op: %+v", res)
	}
}

// K1 — WIRING. TestAPurgeKilledAfterTheLedgerStepLeavesTheCounterMoved.
//
// It pins that the purge's explicit mid-Tx bump happened: the counter after
// a real SIGKILL past the ledger step is two Gen past the pre-purge value,
// read raw because the in-flight flag it also carries makes ReadGeneration
// report ErrWriteInFlight. That the flag keeps lock-free readers out is pinned
// by TestAKilledDestructiveTxLeavesTheCounterInFlightUntilTheNextLock.
//
// Q1 PATH: wiring — a real forked process, a real SIGKILL, a durable counter.
// Q2 VALUE: the counter after the kill must EXCEED the pre-bump value.
// Q3 PRESENCE: ≥ 1 recSession line to drop, asserted. With none the rewrite
// changes nothing, writeFile is not called, and the step has nothing to
// announce — the guard would then redden on CORRECT code.
//
// Mutation: remove the explicit bump from the purge-ledger step and leave it
// to Commit. After the kill the counter sits at the pre-bump value.
func TestAPurgeKilledAfterTheLedgerStepLeavesTheCounterMoved(t *testing.T) {
	v := newVault(t)
	mixedOwnerStore(t, v)
	if ledgerKinds(t, v)[recSession] == 0 {
		t.Fatal("PRESENCE: no recSession line to drop, so the ledger rewrite would write nothing " +
			"and the step would have nothing to announce")
	}
	// A graph on disk, so K2's precondition holds on the same store.
	graph := alphaFiles(t, v).graph
	if err := os.WriteFile(graph, []byte("a graph built over transcripts"), 0o644); err != nil {
		t.Fatal(err)
	}

	before, err := ReadGeneration(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}

	h := startHelper(t, v, "purge-kill")
	h.ready(t)
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = h.cmd.Wait()

	// The raw counter: after the kill ReadGeneration reports ErrWriteInFlight
	// (TestAKilledDestructiveTxLeavesTheCounterInFlightUntilTheNextLock), so
	// the Gen is read past the flag.
	genPath, perr := v.IndexGenerationPath("alpha")
	if perr != nil {
		t.Fatal(perr)
	}
	after, _, err := readGenRaw(genPath)
	if err != nil {
		t.Fatal(err)
	}
	// 🔴 TWO, not one. beginDestructive contributes EXACTLY ONE increment
	// (bumpGen advances Gen by one, once per Tx), and it fires before the
	// first destructive write — so `after > before` holds even with no
	// post-ledger bump at all, and asserting only that is an INERT guard.
	// Commit and Release never ran under a SIGKILL, so a SECOND increment can
	// only be the explicit bump this step performs right after the ledger
	// write. That second increment is what makes the write ANNOUNCED.
	if after.Gen < before.Gen+2 {
		t.Fatalf("after a real SIGKILL past the purge-ledger step the counter is %v, only %d past "+
			"the pre-purge %v. beginDestructive accounts for one; Commit and Release never ran, so "+
			"without the explicit post-ledger bump the ledger write is never announced and every "+
			"lock-free reader keying on generation EQUALITY concludes nothing changed",
			after, after.Gen-before.Gen, before)
	}

	// K2 — TestAfterAKilledPurgeNoGraphSurvives. The graph is removed inside
	// the same step, so a reader that has not yet loaded one finds none.
	//
	// Q3 PRESENCE: an hnsw.idx EXISTED before the purge, asserted above by
	// writing it; otherwise "no graph after" is trivially true.
	//
	// Mutation: stop removing hnsw.idx in the purge-ledger step.
	if _, err := os.Stat(graph); !os.IsNotExist(err) {
		t.Fatalf("hnsw.idx survived a killed purge (stat err %v). It is removed inside the ledger "+
			"step so a reader that has not yet loaded the graph finds none, rather than installing "+
			"one built over the transcripts this purge removed", err)
	}
}

// restoreCommitStep puts the real (nil-ish) commit-step hook back after a
// killAt, so the follow-up purge in the same test runs to completion.
func restoreCommitStep(t *testing.T) {
	t.Helper()
	commitStep = func(string) error { return nil }
}

// liveSessions is every session the ledger records as StateLive.
func liveSessions(l *Ledger) []SessionRecord {
	var out []SessionRecord
	if l == nil {
		return out
	}
	// l.sessions is package-private; this guard lives in the package, which
	// is what lets it read the fold directly rather than through a reader
	// that might itself be what broke.
	for _, rec := range l.sessions {
		if rec.State == StateLive {
			out = append(out, rec)
		}
	}
	return out
}
