// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// The append marker (task append-only-tx-kill-leaves-append-unannounced): an
// append-only Tx sets the counter's third field to "a" on its FIRST write,
// without moving gen or epoch. A reader sees the pre-Tx value while the commit
// lock is held (a live appender) and a Stale value when the marker is present
// and the lock is free (a dead one). The next Lock repairs it with gen+1.

// appendKinds are the write kinds the helper can die after. "append-seeded" is
// "append" with chunks.fingerprint and a chunk already on disk, so the append's
// own seat (appendLines) is the FIRST write; without the seed ensureChunkFingerprint
// writes first and its seat would shadow a missing one in appendLines.
var appendKinds = []string{"append", "append-seeded", "putvectors", "commit-archive", "graph", "completeness"}

// seedOneChunk makes the first append of a later Tx the Tx's first write: the
// fingerprint and one chunk exist.
func seedOneChunk(t *testing.T, v *storage.Vault) {
	t.Helper()
	mustTx(t, v, func(tx *Tx) error {
		tx.UseRecipe(testRecipe)
		return tx.Append(NoteOwner("n"), withDay([]OwnedChunk{ownedChunk("seed", "alpha", "d")}, "2026-05-13"))
	})
}

// killAppendHelper forks the append-kill helper for write and SIGKILLs it once
// its write is durable. Commit and Release never ran. It returns the counter
// the store held before the helper's Tx.
func killAppendHelper(t *testing.T, v *storage.Vault, write string) Gen {
	t.Helper()
	before := genOf(t, v)
	h := startHelper(t, v, "append-kill", "VP_INDEXSTORE_WRITE="+write)
	h.ready(t)
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = h.cmd.Wait()
	return before
}

// A1: the done-definition. After a real SIGKILL of a writer inside an
// append-only Tx, the counter must say so: raw file third field "a", gen and
// epoch as before, and a lock-free reader gets a Stale value that equals the
// value it remembered before the Tx.
//
// Red before the change: the counter is clean and equal to the remembered one.
// Mutation: delete beginAppend from the seat of the write kind under test.
func TestAKilledAppendOnlyTxIsStaleToLockFreeReaders(t *testing.T) {
	for _, kind := range appendKinds {
		t.Run(kind, func(t *testing.T) {
			v := warmWriter(t)
			write := kind
			if kind == "append-seeded" {
				seedOneChunk(t, v)
				write = "append"
			}
			remembered := readGen(t, v, "alpha")
			killAppendHelper(t, v, write)

			raw := rawCounter(t, v, "alpha")
			fields := strings.Fields(raw)
			if len(fields) != 3 || fields[2] != "a" {
				t.Fatalf("raw counter after SIGKILL = %q, want a third field %q", raw, "a")
			}
			g, err := ReadGeneration(v, "alpha")
			if err != nil {
				t.Fatalf("ReadGeneration over a dead appender's marker: %v", err)
			}
			if !g.Stale {
				t.Fatalf("ReadGeneration = %+v, want Stale", g)
			}
			if g.Gen != remembered.Gen || g.Epoch != remembered.Epoch {
				t.Fatalf("a marker write moved the counter: %+v -> %+v (gen and epoch must not change)", remembered, g)
			}
			if g == remembered {
				t.Fatal("the Stale value equals the remembered one: a reader keyed on equality would trust it")
			}
			if after := rawCounter(t, v, "alpha"); after != raw {
				t.Fatalf("ReadGeneration changed the counter file: %q -> %q (a reader never writes)", raw, after)
			}
		})
	}
}

// A2: the next Lock repairs an append marker with gen+1 and the SAME epoch
// (an append extends the store as a prefix), and clears it.
//
// Mutation: stop repairing "a" in ensureGenFile; or repair it with a new epoch.
func TestAKilledAppendIsRepairedByTheNextLockWithoutAnEpochChange(t *testing.T) {
	v := warmWriter(t)
	remembered := readGen(t, v, "alpha")
	killAppendHelper(t, v, "putvectors")

	mustTx(t, v, func(tx *Tx) error {
		got := tx.Generation()
		if got.Gen != remembered.Gen+1 || got.Epoch != remembered.Epoch {
			t.Errorf("Lock over a dead appender's marker returned %+v from %+v, want gen+1 and the same epoch", got, remembered)
		}
		return nil
	})
	if raw := rawCounter(t, v, "alpha"); len(strings.Fields(raw)) != 2 {
		t.Fatalf("raw counter after the repairing Lock = %q, want the marker cleared", raw)
	}
	clean, err := ReadGeneration(v, "alpha")
	if err != nil || clean.Stale {
		t.Fatalf("ReadGeneration after the repair = %+v, %v; want clean", clean, err)
	}
	if clean == remembered {
		t.Fatal("the repaired counter equals the remembered one: the append is still unannounced")
	}
}

// A3: while the appender is ALIVE (commit lock held), a reader gets exactly the
// pre-Tx value: not an error, not Stale. That is what keeps a search's fast
// path during a live append. Commit clears the marker with gen+1.
//
// Mutation: make the probe always report "free" (every live append is Stale).
func TestALiveAppendTxIsInvisibleToLockFreeReaders(t *testing.T) {
	v := warmWriter(t)
	before := readGen(t, v, "alpha")
	h := startHelper(t, v, "append-hold", "VP_INDEXSTORE_WRITE=putvectors")
	h.ready(t)

	raw := rawCounter(t, v, "alpha")
	if f := strings.Fields(raw); len(f) != 3 || f[2] != "a" {
		t.Fatalf("raw counter during a live append = %q, want the marker %q", raw, "a")
	}
	g, err := ReadGeneration(v, "alpha")
	if err != nil {
		t.Fatalf("ReadGeneration during a live append: %v (a live append must not fail a reader)", err)
	}
	if g != before {
		t.Fatalf("ReadGeneration during a live append = %+v, want exactly the pre-Tx %+v", g, before)
	}
	h.finish(t)
	if raw := rawCounter(t, v, "alpha"); len(strings.Fields(raw)) != 2 {
		t.Fatalf("raw counter after Commit = %q, want the marker cleared", raw)
	}
	after := readGen(t, v, "alpha")
	if after.Gen != before.Gen+1 || after.Epoch != before.Epoch {
		t.Fatalf("an append Tx moved the counter %+v -> %+v, want gen+1 and the same epoch", before, after)
	}
}

// A4: a destructive step after an append supersedes "a" with "w": the reader
// then fails closed, exactly as for any destructive Tx, and the totals match
// today's (gen+2, new epoch).
//
// Mutation: beginDestructive skips the bump when the Tx already holds "a".
func TestADestructiveStepSupersedesTheAppendMarker(t *testing.T) {
	v := warmWriter(t)
	before := readGen(t, v, "alpha")
	mustTx(t, v, func(tx *Tx) error {
		tx.UseRecipe(testRecipe)
		if err := tx.PutVectors(noVectors{}, map[string][]float32{"v1": {1}}); err != nil {
			return err
		}
		if f := strings.Fields(rawCounter(t, v, "alpha")); len(f) != 3 || f[2] != "a" {
			t.Errorf("after the append the raw counter = %v, want marker a", f)
		}
		if err := tx.Rewrite(map[string]Labels{"nothing": {Room: "r"}}); err != nil {
			return err
		}
		if _, err := ReadGeneration(v, "alpha"); !errors.Is(err, ErrWriteInFlight) {
			t.Errorf("after a destructive step ReadGeneration err = %v, want ErrWriteInFlight", err)
		}
		return nil
	})
	after := readGen(t, v, "alpha")
	if after.Gen != before.Gen+2 || after.Epoch == before.Epoch {
		t.Fatalf("append then destructive moved %+v -> %+v, want gen+2 and a new epoch", before, after)
	}
}

// A5: a Tx whose writes are all no-ops sets nothing: the counter file is
// byte-identical and no marker appears at any point inside the Tx.
//
// Mutation: seat beginAppend ahead of a no-op guard (putVectors' empty return,
// appendLines' empty return, WriteCompleteness' identical-bytes return,
// DeleteGraph on a missing graph).
func TestATxWithNoEffectiveWriteSetsNothing(t *testing.T) {
	v := warmWriter(t)
	// One real append first, so chunks.fingerprint and the chunk exist: the
	// no-op calls below then have nothing left to write.
	mustTx(t, v, func(tx *Tx) error {
		tx.UseRecipe(testRecipe)
		return tx.Append(NoteOwner("n"), withDay([]OwnedChunk{ownedChunk("seed", "alpha", "d")}, "2026-05-13"))
	})
	start := rawCounter(t, v, "alpha")
	same := func(step string) {
		t.Helper()
		if got := rawCounter(t, v, "alpha"); got != start {
			t.Fatalf("after %s the raw counter = %q, want %q unchanged", step, got, start)
		}
	}
	mustTx(t, v, func(tx *Tx) error {
		tx.UseRecipe(testRecipe)
		same("Lock")
		if err := tx.PutVectors(noVectors{}, nil); err != nil {
			return err
		}
		same("an empty PutVectors")
		if err := tx.Append(NoteOwner("n"), withDay([]OwnedChunk{ownedChunk("seed", "alpha", "d")}, "2026-05-13")); err != nil {
			return err
		}
		same("an Append of records already present")
		if err := tx.DeleteGraph(); err != nil {
			return err
		}
		same("DeleteGraph with no graph")
		if _, err := tx.WriteCompleteness(Completeness{}); err != nil {
			return err
		}
		same("a zero WriteCompleteness over no file")
		return nil
	})
	same("Release")
}

// A6: a FAILED append moves the counter: the marker was set before the write,
// so the Release bump announces it (gen+1, same epoch, marker cleared).
//
// Mutation: drop tx.wrote = true in beginAppend.
func TestAFailedAppendMovesTheCounterAndClearsTheMarker(t *testing.T) {
	v := warmWriter(t)
	seedOneChunk(t, v) // so the failing append is the Tx's first write: nothing else marks or counts it
	before := readGen(t, v, "alpha")
	pf, err := filesFor(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	old := fsyncFile
	fsyncFile = func(f *os.File) error {
		if f.Name() == pf.chunks {
			return errors.New("disk full")
		}
		return old(f)
	}
	err = withTx(t, v, func(tx *Tx) error {
		tx.UseRecipe(testRecipe)
		return tx.Append(NoteOwner("n"), withDay([]OwnedChunk{ownedChunk("n3", "alpha", "d")}, "2026-05-13"))
	})
	fsyncFile = old
	if err == nil {
		t.Fatal("the injected append error was not returned")
	}
	after := readGen(t, v, "alpha")
	if after.Gen != before.Gen+1 || after.Epoch != before.Epoch {
		t.Fatalf("a failed append moved the counter %+v -> %+v, want gen+1 and the same epoch (it was announced before the write)", before, after)
	}
	if raw := rawCounter(t, v, "alpha"); len(strings.Fields(raw)) != 2 {
		t.Fatalf("raw counter after the failed append = %q, want the marker cleared", raw)
	}
}

// A7: the probe does not call a FINISHED writer's marker stale. The marker is
// present and the lock free, but the writer's finishing bump lands between the
// probe and the re-read; the reader must return the fresh clean value.
//
// Mutation: drop the re-read after a free probe.
func TestTheStaleCheckDoesNotCallAFinishedWriterStale(t *testing.T) {
	v := warmWriter(t)
	g0 := readGen(t, v, "alpha")
	gp, err := v.IndexGenerationPath("alpha")
	if err != nil {
		t.Fatal(err)
	}
	counter := func(g Gen, mark string) []byte {
		return []byte(fmt.Sprintf("%d %d%s\n", g.Gen, g.Epoch, mark))
	}
	if err := os.WriteFile(gp, counter(g0, " a"), 0o644); err != nil {
		t.Fatal(err)
	}
	next := Gen{Gen: g0.Gen + 1, Epoch: g0.Epoch}
	defer SetProbeHeldHook(func() {
		if err := os.WriteFile(gp, counter(next, ""), 0o644); err != nil {
			t.Error(err)
		}
	})()
	g, err := ReadGeneration(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if g != next {
		t.Fatalf("ReadGeneration = %+v, want the clean post-finish %+v, not Stale", g, next)
	}
}

// A8: the seat ratchet. Every function that calls tx.noteWrite must call
// beginAppend or beginDestructive earlier in the SAME function, so a new write
// method cannot forget the marker. (noteWrite itself is exempt.)
//
// Mutation: delete a beginAppend seat, or move it after its effect.
func TestEveryNoteWriteIsSeatedFirst(t *testing.T) {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files[n] = f
	}
	for name, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "noteWrite" {
				continue
			}
			var notePos, seatPos token.Pos
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "noteWrite":
					if notePos == 0 {
						notePos = call.Pos()
					}
				case "beginAppend", "beginDestructive":
					if seatPos == 0 {
						seatPos = call.Pos()
					}
				}
				return true
			})
			if notePos != 0 && (seatPos == 0 || seatPos > notePos) {
				t.Errorf("%s: %s calls tx.noteWrite with no beginAppend/beginDestructive before it", name, fn.Name.Name)
			}
		}
	}
}

// P1 (seam): the probe holds the commit lock while a ZERO-timeout writer
// attempts it: that attempt fails (a zero timeout tries exactly once) and
// changes nothing; once the probe is released the same call succeeds, and, as
// the repairing Lock, clears the marker. A 5 s writer in the same position
// succeeds through the poll loop (vaultlock.pollExclusive, 50 ms) and its
// write is announced.
//
// Mutation: the probe never calls probeHeldHook (the seam is dead), or keeps
// the lock after returning.
func TestAProbeHeldAcrossAWriterAttempt(t *testing.T) {
	v := warmWriter(t)
	killAppendHelper(t, v, "putvectors")

	var zeroErr error
	var slow chan error
	hookRan := false
	defer SetProbeHeldHook(func() {
		hookRan = true
		_, zeroErr = Lock(context.Background(), v, "alpha", 0)
		slow = make(chan error, 1)
		go func() {
			tx, err := Lock(context.Background(), v, "alpha", 5*time.Second)
			if err == nil {
				tx.UseRecipe(testRecipe)
				err = tx.PutVectors(noVectors{}, map[string][]float32{"w": {2}})
				if cerr := tx.Commit(); err == nil {
					err = cerr
				}
			}
			slow <- err
		}()
	})()
	g, err := ReadGeneration(v, "alpha")
	if err != nil || !g.Stale {
		t.Fatalf("ReadGeneration = %+v, %v; want Stale", g, err)
	}
	if !hookRan {
		t.Fatal("the probe never ran its held hook: no probe ran, or the seam is not wired")
	}
	if !errors.Is(zeroErr, vaultlock.ErrLockWaitTimeout) {
		t.Fatalf("a zero-timeout Lock while the probe held the commit lock = %v, want ErrLockWaitTimeout", zeroErr)
	}
	if err := <-slow; err != nil {
		t.Fatalf("a 5 s writer behind the probe failed: %v (it must retry, not lose its write)", err)
	}
	if got := readGen(t, v, "alpha"); got.Gen < 2 {
		t.Fatalf("counter after the retried write = %+v", got)
	}
	if raw := rawCounter(t, v, "alpha"); len(strings.Fields(raw)) != 2 {
		t.Fatalf("raw counter after the writers = %q, want clean", raw)
	}
}
