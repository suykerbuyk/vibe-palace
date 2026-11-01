// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// countChunkReads installs the readFileFn seam for one project's chunks.jsonl
// and returns a function giving the count so far. It mirrors
// TestAppendDoesNotRescan's wrapper (store_test.go), which is the established
// way this package measures "how many times was the store re-read".
//
// Note: truncateTornTail also reads through readFileFn, so a fixture with a
// torn tail would inflate the count. Every fixture here is built through the
// normal write path, which always terminates with "\n".
func countChunkReads(t *testing.T, chunksPath string) func() int {
	t.Helper()
	var mu sync.Mutex
	reads := 0
	old := readFileFn
	readFileFn = func(p string) ([]byte, error) {
		if p == chunksPath {
			mu.Lock()
			reads++
			mu.Unlock()
		}
		return old(p)
	}
	t.Cleanup(func() { readFileFn = old })
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return reads
	}
}

// seedStore writes one chunk through a real Tx. Its purpose is to WARM THE
// PROCESS STATE CACHE (state.go's stateCache): tx.state() reads chunks.jsonl
// through readFileFn too, so without this the first ReplaceOwned in the
// measured Tx would charge the counter twice — once for loadState and once for
// the fold — and "one read" would be untestable. TestAppendDoesNotRescan relies
// on the same warm cache for the same reason.
func seedStore(t *testing.T, v *storage.Vault) {
	t.Helper()
	seed := withDay([]OwnedChunk{ownedChunk("seed", "alpha", "decisions")}, "2026-05-13")
	mustTx(t, v, func(tx *Tx) error { return tx.Append(NoteOwner("notes/seed.md"), seed) })
}

// rawChunks returns chunks.jsonl byte for byte. Assertions in this file are on
// RAW BYTES and RAW LINE COUNTS, never on the folded view: foldChunkLines keeps
// only the first chunk line per id, so a duplicated chunk line — the exact
// corruption a broken write-through produces — is invisible to Store.Chunks.
func rawChunks(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return data
}

func rawLineCount(t *testing.T, path string) int {
	t.Helper()
	data := rawChunks(t, path)
	if len(data) == 0 {
		return 0
	}
	return bytes.Count(data, []byte("\n"))
}

// foldOwners is the eight-owner fixture the equivalence test replays: a mix of
// pure additions and of calls that drop an owner's chunk, so one Tx exercises
// BOTH the append and the rewrite branch.
type foldCall struct {
	owner Owner
	recs  []OwnedChunk
}

func eightOwnerCalls() []foldCall {
	var out []foldCall
	for i := range 8 {
		o := NoteOwner(fmt.Sprintf("notes/%d.md", i))
		a := withDay([]OwnedChunk{ownedChunk(fmt.Sprintf("A%d", i), "alpha", "decisions")}, "2026-05-13")
		b := withDay([]OwnedChunk{ownedChunk(fmt.Sprintf("B%d", i), "alpha", "decisions")}, "2026-05-13")
		// Seed both, then (for the odd owners) drop back to one: the second
		// call takes the rewrite branch.
		out = append(out, foldCall{o, append(slices.Clone(a), b...)})
		if i%2 == 1 {
			out = append(out, foldCall{o, a})
		}
	}
	return out
}

// TestReplaceOwnedFoldsChunksOncePerTx is the measurement this unit exists for:
// N ReplaceOwned calls in ONE Tx read and fold chunks.jsonl once, not N times.
//
// It also asserts the result is byte-identical to the same calls made one Tx
// per call. The count alone is not the proof — a cache that never writes
// through still reports one read while corrupting the file — so the raw-bytes
// equivalence is load-bearing, not decoration.
func TestReplaceOwnedFoldsChunksOncePerTx(t *testing.T) {
	calls := eightOwnerCalls()

	// All the calls in ONE Tx.
	vOne := newVault(t)
	pfOne, err := filesFor(vOne, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	seedStore(t, vOne)
	readsOne := countChunkReads(t, pfOne.chunks)
	mustTx(t, vOne, func(tx *Tx) error {
		for _, c := range calls {
			if err := tx.ReplaceOwned(c.owner, c.recs); err != nil {
				return err
			}
		}
		return nil
	})
	if got := readsOne(); got != 1 {
		t.Fatalf("chunks.jsonl was read %d times for %d ReplaceOwned calls in one Tx; want exactly 1", got, len(calls))
	}

	// The same calls, one Tx each.
	vMany := newVault(t)
	pfMany, err := filesFor(vMany, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	seedStore(t, vMany)
	for _, c := range calls {
		mustTx(t, vMany, func(tx *Tx) error { return tx.ReplaceOwned(c.owner, c.recs) })
	}

	gotBytes, wantBytes := rawChunks(t, pfOne.chunks), rawChunks(t, pfMany.chunks)
	if !bytes.Equal(gotBytes, wantBytes) {
		t.Fatalf("one-Tx and Tx-per-call stores differ.\none-Tx (%d lines):\n%s\nper-call (%d lines):\n%s",
			rawLineCount(t, pfOne.chunks), gotBytes, rawLineCount(t, pfMany.chunks), wantBytes)
	}

	// No surviving id may appear on more than one chunk line: a duplicate is
	// what a missing write-through produces, and the fold would hide it. The
	// ids the odd owners dropped must be gone outright — a chunk left with no
	// owner is pruned.
	live, dropped := map[string]bool{}, map[string]bool{}
	for _, c := range calls {
		for _, r := range c.recs {
			dropped[r.ID] = true
		}
	}
	lastOf := map[string][]OwnedChunk{}
	for _, c := range calls {
		lastOf[c.owner.key()] = c.recs
	}
	for _, recs := range lastOf {
		for _, r := range recs {
			live[r.ID] = true
			delete(dropped, r.ID)
		}
	}
	for id := range live {
		line := []byte(`{"kind":"chunk","id":"` + id + `"`)
		if n := bytes.Count(gotBytes, line); n != 1 {
			t.Fatalf("chunk line for live id %s appears %d times in the raw file, want 1", id, n)
		}
	}
	for id := range dropped {
		if bytes.Contains(gotBytes, []byte(`"id":"`+id+`"`)) {
			t.Fatalf("id %s was dropped by its owner's last call but is still in the raw file", id)
		}
	}
	if len(dropped) == 0 {
		t.Fatal("fixture: no id was dropped, so the rewrite branch was never exercised")
	}
}

// A ReplaceOwned that appends must be visible to the NEXT ReplaceOwned in the
// same Tx. A cache that is never written through fails this while still
// reporting one read, which is why the count test above is not sufficient on
// its own.
func TestReplaceOwnedAppendThenObserveInOneTx(t *testing.T) {
	v := newVault(t)
	pf, err := filesFor(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	n1, n2 := NoteOwner("notes/1.md"), NoteOwner("notes/2.md")
	A := withDay([]OwnedChunk{ownedChunk("A", "alpha", "decisions")}, "2026-05-13")

	mustTx(t, v, func(tx *Tx) error {
		if err := tx.ReplaceOwned(n1, A); err != nil {
			return err
		}
		// n2 now claims the SAME content. A's chunk line already exists on
		// disk; only n2's owner line is new.
		return tx.ReplaceOwned(n2, A)
	})

	data := rawChunks(t, pf.chunks)
	chunkLineA := []byte(`{"kind":"chunk","id":"` + idOf("A") + `"`)
	if n := bytes.Count(data, chunkLineA); n != 1 {
		t.Fatalf("A's chunk line appears %d times in the raw file, want 1:\n%s", n, data)
	}
	if n := rawLineCount(t, pf.chunks); n != 3 {
		t.Fatalf("chunks.jsonl has %d raw lines, want 3 (one chunk, two owners):\n%s", n, data)
	}
	cs := ledgered(t, v).Chunks(false)
	if len(cs) != 1 || len(cs[0].Owners) != 2 {
		t.Fatalf("stored %+v, want one chunk owned by both notes", cs)
	}
}

// Rewrite after ReplaceOwned in one Tx: both edits must reach the file. This is
// the fold-aliasing case — rewriteChunks and ReplaceOwned now share one
// Tx-owned fold object — and the eight-owner equivalence test never reaches
// rewriteChunks.
func TestRewriteAfterReplaceOwnedInOneTx(t *testing.T) {
	v := newVault(t)
	n1 := NoteOwner("notes/1.md")
	A := withDay([]OwnedChunk{ownedChunk("A", "alpha", "decisions")}, "2026-05-13")

	mustTx(t, v, func(tx *Tx) error {
		if err := tx.ReplaceOwned(n1, A); err != nil {
			return err
		}
		return tx.Rewrite(map[string]Labels{idOf("A"): {Wing: "beta"}})
	})

	cs := ledgered(t, v).Chunks(false)
	if len(cs) != 1 {
		t.Fatalf("stored %+v, want exactly one chunk", cs)
	}
	if cs[0].Wing != "beta" {
		t.Fatalf("wing %q, want beta: the Rewrite was lost", cs[0].Wing)
	}
	if len(cs[0].Owners) != 1 || cs[0].Owners[0].ID != "notes/1.md" {
		t.Fatalf("owners %+v, want notes/1.md: the ReplaceOwned was lost", cs[0].Owners)
	}
}

// A write that LANDS and then returns an error leaves the file ahead of the
// cached fold, so the next fold-using call in the SAME Tx must re-read from
// disk. The injection shape is store_test.go's TestCacheIsDroppedAfterAFailedWrite:
// call through to the real write first, then return the error — a wrapper that
// returns BEFORE writing would not exercise this at all.
func TestReplaceOwnedReReadsAfterAFailedWriteInTheSameTx(t *testing.T) {
	v := newVault(t)
	pf, err := filesFor(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	n1, n2 := NoteOwner("notes/1.md"), NoteOwner("notes/2.md")
	A := withDay([]OwnedChunk{ownedChunk("A", "alpha", "decisions")}, "2026-05-13")
	B := withDay([]OwnedChunk{ownedChunk("B", "alpha", "decisions")}, "2026-05-13")
	C := withDay([]OwnedChunk{ownedChunk("C", "alpha", "decisions")}, "2026-05-13")

	// Seed: n1 owns A and B, so dropping back to A takes the rewrite branch.
	mustTx(t, v, func(tx *Tx) error { return tx.Append(n1, append(slices.Clone(A), B...)) })

	reads := countChunkReads(t, pf.chunks)
	oldWrite := writeFileFn
	writeFileFn = func(p string, d []byte) error {
		if err := oldWrite(p, d); err != nil {
			return err
		}
		if strings.HasSuffix(p, chunksFile) {
			return errors.New("sync dir: input/output error")
		}
		return nil
	}

	var afterFirst, afterSecond int
	err = withTx(t, v, func(tx *Tx) error {
		if err := tx.ReplaceOwned(n1, A); err == nil {
			t.Fatal("the injected error was not returned")
		}
		afterFirst = reads()
		writeFileFn = oldWrite // let the second call actually succeed
		if err := tx.ReplaceOwned(n2, C); err != nil {
			return err
		}
		afterSecond = reads()
		return nil
	})
	writeFileFn = oldWrite
	if err != nil {
		t.Fatalf("the second ReplaceOwned failed: %v", err)
	}
	if afterSecond <= afterFirst {
		t.Fatalf("chunks.jsonl was read %d times before and %d after the failed write; the fold was not dropped", afterFirst, afterSecond)
	}

	// The failed write LANDED, so B is gone from the file; the second call must
	// have been decided against that file, and must not reintroduce B or
	// duplicate A.
	data := rawChunks(t, pf.chunks)
	if n := bytes.Count(data, []byte(`"id":"`+idOf("A")+`"`)); n != 2 {
		t.Fatalf("A appears on %d raw lines, want 2 (its chunk line and n1's owner line):\n%s", n, data)
	}
	if bytes.Contains(data, []byte(`"id":"`+idOf("B")+`"`)) {
		t.Fatalf("B came back after the write that removed it landed:\n%s", data)
	}
	if n := bytes.Count(data, []byte(`{"kind":"chunk","id":"`+idOf("C")+`"`)); n != 1 {
		t.Fatalf("C's chunk line appears %d times, want 1:\n%s", n, data)
	}
}

// A chunk that leaves the fold must take its owner's byOwner entry with it. A
// ghost id there is NOT a crash: ReplaceOwned sees an id outside keep, takes
// the rewrite branch, and rewrites the whole file on every later call —
// silently restoring the O(notes x whole-file-write) cost this unit removes.
//
// Two arms, because the two removal paths are not equally reachable:
//
//   - "orphan" is the REACHABLE one. foldChunkLines drops an owner line whose
//     chunk line is missing, AFTER setOwner has already indexed it.
//   - "pruned" documents chunkFold.prune. prune's guard is len(owners) == 0, so
//     by the time it deletes an entry dropOwner has already unindexed every
//     line; its unindex loop is defensive and cannot be made to fail today.
//     Kept as a regression fixture for anyone who widens that guard.
func TestPrunedChunkLeavesNoGhostInTheOwnerIndex(t *testing.T) {
	t.Run("orphan", func(t *testing.T) {
		v := newVault(t)
		pf, err := filesFor(v, "alpha")
		if err != nil {
			t.Fatal(err)
		}
		n1 := NoteOwner("notes/1.md")
		G := withDay([]OwnedChunk{ownedChunk("G", "alpha", "decisions")}, "2026-05-13")
		A := withDay([]OwnedChunk{ownedChunk("A", "alpha", "decisions")}, "2026-05-13")
		mustTx(t, v, func(tx *Tx) error { return tx.Append(n1, G) })

		// Hand-edit: drop G's CHUNK line, keep n1's owner line for it. That is
		// the orphan foldChunkLines discards — and whose byOwner entry must go
		// with it.
		var kept []string
		for _, l := range fileLines(t, pf.chunks) {
			if strings.HasPrefix(l, `{"kind":"chunk","id":"`+idOf("G")+`"`) {
				continue
			}
			kept = append(kept, l)
		}
		if len(kept) != 1 {
			t.Fatalf("fixture: kept %d lines, want 1 (n1's orphan owner line)", len(kept))
		}
		if err := os.WriteFile(pf.chunks, []byte(strings.Join(kept, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := rawChunks(t, pf.chunks)

		// n1 now owns nothing real, so adding A must take the APPEND branch:
		// the existing bytes stay as a prefix and the new lines follow.
		mustTx(t, v, func(tx *Tx) error { return tx.ReplaceOwned(n1, A) })

		after := rawChunks(t, pf.chunks)
		if !bytes.HasPrefix(after, before) {
			t.Fatalf("the file was rewritten, not appended to — a ghost id for n1 forced the rewrite branch.\nbefore:\n%s\nafter:\n%s", before, after)
		}
		if n := bytes.Count(after, []byte(`{"kind":"chunk","id":"`+idOf("A")+`"`)); n != 1 {
			t.Fatalf("A's chunk line appears %d times, want 1:\n%s", n, after)
		}
	})

	t.Run("pruned", func(t *testing.T) {
		v := newVault(t)
		pf, err := filesFor(v, "alpha")
		if err != nil {
			t.Fatal(err)
		}
		n1, n2 := NoteOwner("notes/1.md"), NoteOwner("notes/2.md")
		P := withDay([]OwnedChunk{ownedChunk("P", "alpha", "decisions")}, "2026-05-13")
		Q := withDay([]OwnedChunk{ownedChunk("Q", "alpha", "decisions")}, "2026-05-13")
		mustTx(t, v, func(tx *Tx) error { return tx.Append(n1, P) })

		mustTx(t, v, func(tx *Tx) error {
			// Drops n1 from P, leaving P ownerless: prune deletes it.
			if err := tx.ReplaceOwned(n1, nil); err != nil {
				return err
			}
			// n1 owns nothing now, so this must APPEND.
			return tx.ReplaceOwned(n1, Q)
		})

		after := rawChunks(t, pf.chunks)
		if bytes.Contains(after, []byte(`"id":"`+idOf("P")+`"`)) {
			t.Fatalf("P survived the prune:\n%s", after)
		}
		if n := bytes.Count(after, []byte(`{"kind":"chunk","id":"`+idOf("Q")+`"`)); n != 1 {
			t.Fatalf("Q's chunk line appears %d times, want 1:\n%s", n, after)
		}
		// And an unrelated owner still appends cleanly afterwards.
		before := rawChunks(t, pf.chunks)
		mustTx(t, v, func(tx *Tx) error { return tx.ReplaceOwned(n2, Q) })
		if !bytes.HasPrefix(rawChunks(t, pf.chunks), before) {
			t.Fatalf("adding a second owner for Q rewrote the file instead of appending")
		}
	})
}

// An appended line must survive a LATER rewrite in the same Tx. This is the
// precise hazard the write-through closes, and the one the append-then-observe
// test above does NOT catch: on the append branch `s` carries the truth, so a
// stale fold is masked there. The rewrite branch rebuilds the whole file FROM
// THE FOLD, so a fold that never learned about an earlier append silently drops
// those lines on the way out.
func TestAppendedLineSurvivesALaterRewriteInTheSameTx(t *testing.T) {
	v := newVault(t)
	pf, err := filesFor(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	seedStore(t, v)
	n1, n2 := NoteOwner("notes/1.md"), NoteOwner("notes/2.md")
	A := withDay([]OwnedChunk{ownedChunk("A", "alpha", "decisions")}, "2026-05-13")
	B := withDay([]OwnedChunk{ownedChunk("B", "alpha", "decisions")}, "2026-05-13")
	C := withDay([]OwnedChunk{ownedChunk("C", "alpha", "decisions")}, "2026-05-13")

	mustTx(t, v, func(tx *Tx) error {
		if err := tx.ReplaceOwned(n1, A); err != nil { // append
			return err
		}
		if err := tx.ReplaceOwned(n2, append(slices.Clone(B), C...)); err != nil { // append
			return err
		}
		return tx.ReplaceOwned(n2, B) // rewrite: drops C, rebuilds the file from the fold
	})

	data := rawChunks(t, pf.chunks)
	for _, want := range []string{"seed", "A", "B"} {
		if !bytes.Contains(data, []byte(`"id":"`+idOf(want)+`"`)) {
			t.Fatalf("%s was lost by the rewrite — the fold never learned about the append that added it:\n%s", want, data)
		}
	}
	if bytes.Contains(data, []byte(`"id":"`+idOf("C")+`"`)) {
		t.Fatalf("C survived the rewrite that dropped it:\n%s", data)
	}
	for _, want := range []string{"seed", "A", "B"} {
		if n := bytes.Count(data, []byte(`{"kind":"chunk","id":"`+idOf(want)+`"`)); n != 1 {
			t.Fatalf("%s's chunk line appears %d times, want 1:\n%s", want, n, data)
		}
	}
}
