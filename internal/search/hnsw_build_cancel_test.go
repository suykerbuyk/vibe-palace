// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
)

// TestCloseAbandonsMidBuildAndFreesRunLock is U5's acceptance test: a converter
// parked inside hnswIndex.Build must not pin Engine.Close for the whole build.
// Close cancels convertCtx; Build observes it between node-insert batches,
// returns context.Canceled (installing nothing), and runConvert's deferred
// run-lock Release fires — so Close returns within one batch and the run lock
// is free again.
//
// The corpus spans MORE THAN rebuildBatch (256) nodes on purpose. Build checks
// ctx at the top of each batch, so a cancel injected while batch 0 is parked is
// caught by batch 1's check. A sub-256 corpus would be a single batch whose
// check has already passed when the cancel lands, so the build would finish and
// swallow the cancel — exactly the hole assertion (d) exists to close.
func TestCloseAbandonsMidBuildAndFreesRunLock(t *testing.T) {
	withThresholds(t, 260, 250) // the converter fires at >= 260 live ids
	withConvertBounds(t, 64, 8) // standard converter catch-up bounds
	clearConvertHooks(t)        // nils every convert step-hook on cleanup
	v := testVault(t)
	const project = "u5close"
	e := NewEngine(embedder.NewMock(384), v, selCfg(), WithBackgroundConverter())
	seedBruteProject(t, v, e, project, 300) // 300 ids -> 2 build batches (256 + 44)

	// A test-only guard on the converter's publish path: Step-5's critical
	// section runs ONLY after Build returns nil, so if the build is genuinely
	// abandoned this never fires. It is how "(c) no partial swap" is proven —
	// Engine.Close empties e.indexes, so a post-Close index-type read cannot
	// tell "never swapped" from "swapped then wiped"; this hook can.
	var swapEntered atomic.Bool
	convertBeforeSwap = func() { swapEntered.Store(true) }

	// buildBatchHook parks the build mid-flight: it signals that batch 0 passed
	// its ctx check, then blocks until convertCtx is cancelled (by Close).
	parked := make(chan struct{})
	var once sync.Once
	buildBatchHook = func(ctx context.Context) {
		once.Do(func() { close(parked) })
		<-ctx.Done()
	}
	t.Cleanup(func() { buildBatchHook = nil })

	// Run the converter on its own goroutine; it takes the run lock, builds, and
	// parks inside Build. convertSync increments convertWG, so Close joins it.
	convDone := make(chan struct{})
	go func() {
		defer close(convDone)
		convertSync(e, project)
	}()

	select {
	case <-parked:
	case <-time.After(15 * time.Second):
		t.Fatal("the converter never reached Build's batch hook")
	}

	// (a) Close must return within a generous budget — it cancels the parked
	// build and joins the converter, rather than waiting out the whole build.
	closed := make(chan error, 1)
	go func() { closed <- e.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Engine.Close blocked on a mid-build converter (U5 regression)")
	}
	<-convDone // the converter goroutine has returned (Close joined it)

	// (c) no partial swap: the converter never entered its publish section.
	if swapEntered.Load() {
		t.Fatal("the converter swapped in a partial graph after a cancelled build")
	}

	// (b) the run lock is released, not leaked: a fresh TryRunLock succeeds.
	held, ok, err := indexstore.TryRunLock(v, indexstore.KindRebuild, "")
	if err != nil {
		t.Fatalf("TryRunLock after Close: %v", err)
	}
	if !ok {
		t.Fatal("the run lock is still held after Close: a cancelled build leaked it")
	}
	_ = held.Release()

	// (d) Build itself returns context.Canceled on a cancelled ctx. runConvert
	// discards its build error, so this proves the contract directly: a future
	// refactor that drops the ctx check (and lets a tiny corpus finish, nil) is
	// caught here even though the parked path above could not observe it.
	buildBatchHook = nil // the direct build must not re-enter the parking hook
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := newHNSWIndex(384, provisionalHNSWParams)
	berr := h.Build(cctx, [][]float32{oneHotVec(384, 1)}, []string{"x"})
	if !errors.Is(berr, context.Canceled) {
		t.Fatalf("Build on a cancelled ctx: got %v, want context.Canceled", berr)
	}
}
