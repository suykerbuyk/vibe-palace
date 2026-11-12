// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/index"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// flagCounter rewrites project's counter file with the in-flight flag set, as
// a destructive Tx leaves it. It writes the counter's text directly, the
// format indexstore documents on Gen: "gen epoch" plus a third field "w".
func flagCounter(t *testing.T, v *storage.Vault, project string) {
	t.Helper()
	p, err := v.IndexGenerationPath(project)
	if err != nil {
		t.Fatal(err)
	}
	g, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(fmt.Sprintf("%d %d w\n", g.Gen+1, g.Epoch+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := indexstore.ReadGeneration(v, project); !errors.Is(err, indexstore.ErrWriteInFlight) {
		t.Fatalf("fixture: the flagged counter reads err = %v, want ErrWriteInFlight", err)
	}
}

// TestASaveAfterTheTxsOwnDestructiveWriteIsNotTrippedByItsOwnFlag: a Tx that
// has begun a destructive write has set the counter's in-flight flag itself,
// so saveGraph must read the counter through the Tx. With ReadGeneration it
// would see its own flag and fail with ErrWriteInFlight.
//
// Mutation: put ReadGeneration back in saveGraph. saveGraph then returns
// ErrWriteInFlight and this test fails at the saveGraph call.
func TestASaveAfterTheTxsOwnDestructiveWriteIsNotTrippedByItsOwnFlag(t *testing.T) {
	v, project := newGraphVault(t)
	const dims = 32
	h := buildHNSW(t, dims, 20, smallParams(16))
	data, w := h.snapshot()

	tx, err := indexstore.Lock(context.Background(), v, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	tx.UseRecipe(index.ChunkRecipe{IndexerVersion: index.IndexerVersion})
	if err := tx.Discard(indexstore.DiscardChunks); err != nil {
		_ = tx.Release()
		t.Fatal(err)
	}
	if _, rerr := indexstore.ReadGeneration(v, project); !errors.Is(rerr, indexstore.ErrWriteInFlight) {
		_ = tx.Release()
		t.Fatalf("PRESENCE: after a public destructive method the counter reads err = %v, want ErrWriteInFlight; "+
			"without the flag this test would pass on the mutation", rerr)
	}
	out, err := saveGraph(tx, v, project, h, data, w, tx.Generation())
	if err != nil {
		_ = tx.Release()
		t.Fatalf("saveGraph inside a Tx that began a destructive write: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if out != written {
		t.Fatalf("outcome = %v, want written", out)
	}
	if _, err := indexstore.ReadGeneration(v, project); err != nil {
		t.Fatalf("after Commit the counter reads err = %v, want clean", err)
	}
}

// TestEnsureIndexNeverRemembersAFlaggedCounter: while the counter is flagged,
// ensureIndex must not leave a remembered generation for the project, so the
// next search does not take the loaded fast path on a counter that vouched
// for nothing. It adds no early return: the build's ordinary remember rule
// (an unreadable start counter is not remembered) is what is pinned.
//
// The counter is flagged by hand and no writer holds the lock, so the build's
// own final commit may repair it; whatever it does, `loaded` must not hold the
// flagged or any pre-repair value.
//
// Mutation: make remember store start.gen even when it was not readable. The
// engine then remembers the zero Gen of the failed read and this test fails.
func TestEnsureIndexNeverRemembersAFlaggedCounter(t *testing.T) {
	v := testVault(t)
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10, IndexTranscripts: true})
	t.Cleanup(func() { eng.Close() })

	const project = "proj"
	commitDrawerGraph(t, v, project, 5)
	flagCounter(t, v, project)

	_ = eng.ensureIndex(context.Background(), project) // its error, if any, is not the subject

	eng.buildMu.Lock()
	got, ok := eng.loaded[project]
	eng.buildMu.Unlock()
	if ok {
		t.Fatalf("ensureIndex over a flagged counter left loaded[%s] = %v", project, got)
	}
}

// TestAKilledDestructiveWriterStrandsNoReader: a real SIGKILL of a writer in
// Tx.Discard(DiscardChunks), at the step after the ledger and chunks are
// removed and before the graph is. A STALE hnsw.idx is therefore on disk beside
// a store the destructive write already changed, and the counter is still
// flagged. Readers that key on the counter must then install, persist and
// delete nothing:
//
//   - tryWitnessedLoad, with a counter value read before the kill, installs
//     nothing;
//   - HealGraph over a fingerprint-mismatched graph skips, leaves hnsw.idx's
//     bytes alone, and does not delete it;
//   - neither read repaired the counter: a reader never writes.
//
// Mutation: drop the in-flight flag write in beginDestructive. The counter
// then reads clean after the kill; the PRESENCE check fails first, and with
// that check removed the heal rewrites hnsw.idx.
func TestAKilledDestructiveWriterStrandsNoReader(t *testing.T) {
	ctx := context.Background()
	eng, _, v, project := healBase(t, []string{"k one", "k two", "k three", "k four"}, true, 2, 1)
	stale := writeValidGraph(t, v, project)
	g0, err := indexstore.ReadGeneration(v, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) == 0 {
		t.Fatal("PRESENCE: no graph on disk before the kill")
	}
	bumpEfSearch(t) // the graph on disk now mismatches the configured fingerprint

	killDiscardHelper(t, v, project)

	// PRESENCE (D1): the kill landed with the graph still on disk and the
	// counter flagged. Without both, nothing below proves a reader refused.
	if got := readGraphBytes(t, v, project); string(got) != string(stale) {
		t.Fatalf("PRESENCE: the stale graph is not on disk after the kill (%d bytes, want %d)", len(got), len(stale))
	}
	flagged, err := indexstore.ReadGeneration(v, project)
	if !errors.Is(err, indexstore.ErrWriteInFlight) {
		t.Fatalf("PRESENCE: counter after the kill = %v, err %v, want ErrWriteInFlight", flagged, err)
	}

	// Reader 1: the witnessed load.
	if eng.tryWitnessedLoad(ctx, project, g0) {
		t.Fatal("tryWitnessedLoad installed a graph over a counter flagged in-flight")
	}
	eng.mu.RLock()
	_, inMem := eng.indexes[project]
	eng.mu.RUnlock()
	if inMem {
		t.Fatal("a refused witnessed load must install nothing")
	}

	// Reader 2: the heal (persists a rebuilt graph, or deletes a below-L one).
	out, err := eng.HealGraph(ctx, v.Root, project)
	if err != nil {
		t.Fatalf("HealGraph over an in-flight counter: %v (a live window must skip, not fail)", err)
	}
	if out.Rebuilt || out.Deleted || !strings.Contains(out.Reason, "in flight") {
		t.Fatalf("HealGraph outcome = %+v, want a skip naming the in-flight write", out)
	}
	if got := readGraphBytes(t, v, project); string(got) != string(stale) {
		t.Fatal("the heal persisted or deleted hnsw.idx over a counter flagged in-flight")
	}

	if _, err := indexstore.ReadGeneration(v, project); !errors.Is(err, indexstore.ErrWriteInFlight) {
		t.Fatalf("a reader changed the counter: ReadGeneration now err = %v, want it still ErrWriteInFlight", err)
	}
}

// TestConvertOverAFlaggedCounterSwapsAndSavesNothing: the converter reads the
// counter at the start of its critical section (runConvert step 5.1) as the
// witness for the swap and the save. If that read reports ErrWriteInFlight the
// converter must return before the swap: no hnsw.idx is written, and the
// installed index is still the brute index it started from.
//
// The counter is flagged at convertBeforeFinal, which runs just before step
// 5.1, and keeps its Gen and epoch, so a reader that ignored the error and
// used the zero Gen it comes with would be the only thing that differs.
//
// Mutation: make step 5.1's error test `gerr != nil && false`. The converter
// then goes on and reaches the swap step, and this test fails on the first
// assertion.
func TestConvertOverAFlaggedCounterSwapsAndSavesNothing(t *testing.T) {
	withThresholds(t, 4, 2)
	clearConvertHooks(t)
	v := testVault(t)
	const project = "flagconv"
	e := newSelEngine(t, v)
	seedBruteProject(t, v, e, project, 5)
	b0 := installedIndex(t, e, project)

	convertBeforeFinal = func() {
		g, err := indexstore.ReadGeneration(v, project)
		if err != nil {
			t.Errorf("fixture: ReadGeneration: %v", err)
			return
		}
		p, err := v.IndexGenerationPath(project)
		if err != nil {
			t.Errorf("fixture: %v", err)
			return
		}
		if err := os.WriteFile(p, []byte(fmt.Sprintf("%d %d w\n", g.Gen, g.Epoch)), 0o644); err != nil {
			t.Errorf("fixture: %v", err)
		}
	}
	var reached bool
	convertBeforeSwap = func() { reached = true }

	convertSync(e, project)

	if reached {
		t.Fatal("the converter reached the swap step over a flagged counter")
	}
	if _, err := os.Stat(graphFilePath(t, v, project)); !os.IsNotExist(err) {
		t.Fatalf("(i) hnsw.idx was written over a flagged counter (stat err %v)", err)
	}
	if got := installedIndex(t, e, project); got != b0 {
		t.Fatalf("(ii) installed index is %T, want the brute index the converter started from", got)
	}
	// PRESENCE, last because a converter that went on would have cleared the
	// flag with its own finishing bump: the fixture flagged the counter, and a
	// converter that stopped at step 5.1 leaves it so.
	if _, err := indexstore.ReadGeneration(v, project); !errors.Is(err, indexstore.ErrWriteInFlight) {
		t.Fatalf("PRESENCE: the counter is not flagged after the conversion (err %v)", err)
	}
}
