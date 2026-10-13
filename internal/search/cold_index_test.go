// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// newColdModel returns a LazyEmbedder whose construction blocks on gate (nil =
// immediate) and then either fails with failWith or returns a call-counting
// mock. Ready() is false until construction completes, so it models the cold
// ONNX model the search path must never embed against blindly.
func newColdModel(gate <-chan struct{}, failWith error) (*embedder.LazyEmbedder, *countingEmbedder) {
	counter := newCountingEmbedder(384)
	lazy := embedder.NewLazy(func() (embedder.Embedder, error) {
		if gate != nil {
			<-gate
		}
		if failWith != nil {
			return nil, failWith
		}
		return counter, nil
	})
	return lazy, counter
}

// ctxBlockEmbedder blocks EmbedBatch until the ctx is cancelled (or a long
// fallback), so a search-path embed budget can be shown to fire.
type ctxBlockEmbedder struct{ embedder.Embedder }

func (c ctxBlockEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		return c.Embedder.EmbedBatch(ctx, texts)
	}
}

// B1.1: a search against a project whose embedding model is still constructing
// waits a bounded time, then refuses with the typed error — and NEVER calls
// Embed/EmbedBatch on the caller's goroutine (that would block uninterruptibly
// inside the LazyEmbedder's once). Mutant: a blind on-caller get in
// waitEmbedderReady blocks the search for ever (test times out).
func TestSearchColdModelBoundedWaitNoBlindEmbed(t *testing.T) {
	defer swapDuration(&readyWait, 150*time.Millisecond)()
	gate := make(chan struct{})
	lazy, counter := newColdModel(gate, nil)
	v := testVault(t)
	eng := NewEngine(lazy, v, storage.Config{SearchDefaultLimit: 10})
	// Release the construct FIRST at cleanup so Close's warmWG.Wait() returns.
	t.Cleanup(func() { close(gate); eng.Close() })
	addDrawer(t, v, "proj", "wing", "room", "some content to index", "facts")

	start := time.Now()
	_, err := eng.Search(context.Background(), "query", SearchFilters{Project: "proj"})
	elapsed := time.Since(start)

	var nb *IndexNotBuiltError
	if !errors.As(err, &nb) {
		t.Fatalf("want *IndexNotBuiltError, got %v", err)
	}
	if ec, bc := counter.counts(); ec != 0 || bc != 0 {
		t.Fatalf("blind embed on the caller: Embed=%d EmbedBatch=%d; want 0/0", ec, bc)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("search blocked %v; the bounded readiness wait was not applied", elapsed)
	}
}

// B1.1 fast-fail: a construct that fails returns the typed error immediately,
// not after the full readyWait. Mutant: drop the <-warmDone arm and the wait
// runs the whole budget.
func TestSearchColdModelConstructFailedFastFail(t *testing.T) {
	defer swapDuration(&readyWait, 3*time.Second)()
	lazy, counter := newColdModel(nil, errors.New("construct boom"))
	v := testVault(t)
	eng := NewEngine(lazy, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })
	addDrawer(t, v, "proj", "wing", "room", "some content to index", "facts")

	start := time.Now()
	_, err := eng.Search(context.Background(), "query", SearchFilters{Project: "proj"})
	elapsed := time.Since(start)

	var nb *IndexNotBuiltError
	if !errors.As(err, &nb) {
		t.Fatalf("want *IndexNotBuiltError, got %v", err)
	}
	if ec, bc := counter.counts(); ec != 0 || bc != 0 {
		t.Fatalf("embedded on a failed-construct model: Embed=%d EmbedBatch=%d", ec, bc)
	}
	if elapsed > 1*time.Second {
		t.Fatalf("did not fast-fail: waited %v (near the %v budget)", elapsed, readyWait)
	}
}

// §1 corpus gate: a cold corpus whose miss count exceeds one embed batch, with
// nothing servable in memory, refuses before embedding — no EmbedBatch, and
// e.loaded / e.indexes are left untouched. Mutant: widen/remove the gate and the
// whole corpus embeds.
func TestColdCorpusOverBudgetRefusesWithoutEmbedding(t *testing.T) {
	eng, v, emb := countingEngine(t, storage.Config{EmbedderBatchSize: 32})
	for i := 0; i < 40; i++ {
		addDrawer(t, v, "proj", "wing", "room", fmt.Sprintf("distinct chunk number %d", i), "facts")
	}

	_, err := eng.Search(context.Background(), "query", SearchFilters{Project: "proj"})

	var nb *IndexNotBuiltError
	if !errors.As(err, &nb) {
		t.Fatalf("want *IndexNotBuiltError, got %v", err)
	}
	if _, bc := emb.counts(); bc != 0 {
		t.Fatalf("EmbedBatch called %d times; the gate must refuse before embedding", bc)
	}
	eng.buildMu.Lock()
	_, loaded := eng.loaded["proj"]
	eng.buildMu.Unlock()
	eng.mu.RLock()
	_, inMem := eng.indexes["proj"]
	eng.mu.RUnlock()
	if loaded || inMem {
		t.Fatalf("a refused build must leave nothing published: loaded=%v inMemory=%v", loaded, inMem)
	}
}

// S1: the serve-as-is path on a soft error serves only a non-empty in-memory
// index (HasIndex), never an empty/cold-insert one. Mutant: bare-presence
// predicate serves the empty index as an empty success.
func TestMapBuildErrServesOnlyNonEmptyIndex(t *testing.T) {
	eng, _ := testEngine(t)

	empty := newBruteIndex(384)
	eng.mu.Lock()
	eng.indexes["p"] = empty
	eng.mu.Unlock()
	eng.buildMu.Lock()
	eng.loaded["p"] = indexstore.Gen{}
	eng.buildMu.Unlock()

	if err := eng.mapBuildErr("p", errEmbedRefused); err == nil {
		t.Fatal("an empty in-memory index was served as-is; want *IndexNotBuiltError")
	} else {
		var nb *IndexNotBuiltError
		if !errors.As(err, &nb) {
			t.Fatalf("want *IndexNotBuiltError, got %v", err)
		}
	}

	full := newBruteIndex(384)
	if err := full.Build([][]float32{unitVec(0)}, []string{"id-1"}); err != nil {
		t.Fatal(err)
	}
	eng.mu.Lock()
	eng.indexes["p"] = full
	eng.mu.Unlock()
	if err := eng.mapBuildErr("p", errEmbedRefused); err != nil {
		t.Fatalf("a non-empty in-memory index must be served as-is, got %v", err)
	}
}

// B1.2: a search joining another search's in-flight build does not wait
// unboundedly for it — it returns within joinBuildTimeout. Mutant: a bare
// <-b.done blocks the joiner until the (stalled) owner finishes.
func TestJoinBoundedWhenOwnerBuildStalls(t *testing.T) {
	defer swapDuration(&joinBuildTimeout, 150*time.Millisecond)()
	be := &blockingEmbedder{Embedder: embedder.NewMock(384), started: make(chan struct{}), release: make(chan struct{})}
	v := testVault(t)
	eng := NewEngine(be, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })
	addDrawer(t, v, "proj", "wing", "room", "one drawer the owner embeds", "facts")

	ownerDone := make(chan struct{})
	go func() {
		defer close(ownerDone)
		_, _ = eng.Search(context.Background(), "owner query", SearchFilters{Project: "proj"})
	}()
	<-be.started // the owner holds the build, mid-embed

	start := time.Now()
	_, err := eng.Search(context.Background(), "joiner query", SearchFilters{Project: "proj"})
	elapsed := time.Since(start)

	var nb *IndexNotBuiltError
	if !errors.As(err, &nb) {
		t.Fatalf("want *IndexNotBuiltError from the bounded join, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("the joiner blocked %v on the stalled owner; the join is not bounded", elapsed)
	}
	close(be.release)
	<-ownerDone
}

// stallOwnerHoldingBuild starts a Search whose rebuild stalls mid-embed on be,
// holding the project's in-flight build (buildsRun) so a second search joins it.
// It returns a func to release the owner and wait for it. The project must carry
// exactly one uncached drawer (<= one batch) so the build passes the §1 gate and
// reaches embedMisses, where be blocks.
func stallOwnerHoldingBuild(t *testing.T, eng *Engine, be *blockingEmbedder, project string) func() {
	t.Helper()
	ownerDone := make(chan struct{})
	go func() {
		defer close(ownerDone)
		_, _ = eng.Search(context.Background(), "owner query", SearchFilters{Project: project})
	}()
	<-be.started // the owner is mid-embed, holding the build
	return func() {
		close(be.release)
		<-ownerDone
	}
}

// installInMemoryIndex white-box-installs a non-empty in-memory index (+ its
// metadata) for project WITHOUT marking it current (e.loaded stays clear): the
// decision-7 busy-warm shape a serve-as-is arm must still answer from.
func installInMemoryIndex(t *testing.T, eng *Engine, project, id, content string) {
	t.Helper()
	idx := newBruteIndex(384)
	if err := idx.Build([][]float32{unitVec(0)}, []string{id}); err != nil {
		t.Fatal(err)
	}
	eng.mu.Lock()
	eng.indexes[project] = idx
	eng.metadata[id] = drawerMeta{Project: project, Content: content, Date: "2026-04-07"}
	eng.mu.Unlock()
}

// S-CR1 (serve-as-is, timeout arm): a search joining a stalled owner build, with
// a non-empty in-memory index and e.loaded clear, returns within joinBuildTimeout
// with hits from that index — not *IndexNotBuiltError. Mutant: drop
// servableInMemory on the timeout arm (always refuse) → red.
func TestJoinServesNonEmptyInMemoryIndexOnTimeout(t *testing.T) {
	defer swapDuration(&joinBuildTimeout, 150*time.Millisecond)()
	be := &blockingEmbedder{Embedder: embedder.NewMock(384), started: make(chan struct{}), release: make(chan struct{})}
	v := testVault(t)
	eng := NewEngine(be, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })

	// The in-memory index to be served (loaded stays clear).
	installInMemoryIndex(t, eng, "proj", "served-id", "the loaded answer")
	// One uncached drawer so the owner build reaches embedMisses and stalls.
	addDrawer(t, v, "proj", "wing", "room", "a fresh drawer the owner embeds", "facts")

	release := stallOwnerHoldingBuild(t, eng, be, "proj")
	defer release()

	start := time.Now()
	res, err := eng.Search(context.Background(), "the loaded answer", SearchFilters{Project: "proj", Limit: 5})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a bounded join with a servable in-memory index must serve it, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("join did not return within the bound: %v", elapsed)
	}
	if !hasContent(res, "the loaded answer") {
		t.Fatalf("the in-memory index was not served: %+v", res)
	}
}

// S-CR1 (serve-as-is, ctx.Done arm): a cancelled-ctx join with an empty
// (Len()==0) in-memory index refuses with *IndexNotBuiltError — never a
// nil/empty success. Mutant: drop servableInMemory on the ctx.Done arm → the
// empty index is served as-is and the search returns an empty result, no error.
func TestJoinCancelledCtxEmptyIndexRefuses(t *testing.T) {
	be := &blockingEmbedder{Embedder: embedder.NewMock(384), started: make(chan struct{}), release: make(chan struct{})}
	v := testVault(t)
	eng := NewEngine(be, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })

	// An EMPTY in-memory index (Len()==0) — must not be served as an empty success.
	empty := newBruteIndex(384)
	eng.mu.Lock()
	eng.indexes["proj"] = empty
	eng.mu.Unlock()
	addDrawer(t, v, "proj", "wing", "room", "a fresh drawer the owner embeds", "facts")

	release := stallOwnerHoldingBuild(t, eng, be, "proj")
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the join's <-ctx.Done() arm fires immediately
	_, err := eng.Search(ctx, "query", SearchFilters{Project: "proj", Limit: 5})

	var nb *IndexNotBuiltError
	if !errors.As(err, &nb) {
		t.Fatalf("a cancelled join with an empty in-memory index must refuse, got %v", err)
	}
}

// §5: a build-budget deadline during the embed is mapped to the typed not-ready
// error (not a lock timeout), and nothing is remembered or published. Mutant:
// treating DeadlineExceeded as a lock timeout returns ErrIndexNotReady instead.
func TestEmbedBudgetDeadlineRefusesAndDoesNotRemember(t *testing.T) {
	defer swapDuration(&embedBudget, 100*time.Millisecond)()
	emb := ctxBlockEmbedder{Embedder: embedder.NewMock(384)}
	v := testVault(t)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })
	addDrawer(t, v, "proj", "wing", "room", "one drawer to embed", "facts")

	_, err := eng.Search(context.Background(), "query", SearchFilters{Project: "proj"})

	var nb *IndexNotBuiltError
	if !errors.As(err, &nb) {
		t.Fatalf("want *IndexNotBuiltError, got %v", err)
	}
	if errors.Is(err, ErrIndexNotReady) {
		t.Fatal("a deadline must NOT be mapped as a busy-lock ErrIndexNotReady")
	}
	eng.buildMu.Lock()
	_, loaded := eng.loaded["proj"]
	eng.buildMu.Unlock()
	eng.mu.RLock()
	_, inMem := eng.indexes["proj"]
	eng.mu.RUnlock()
	if loaded || inMem {
		t.Fatalf("a deadlined build must publish nothing: loaded=%v inMemory=%v", loaded, inMem)
	}
}

// §6: a cross-project search skips a cold/over-budget project and still answers
// from the warm one. Mutant: ensureAllIndexes returning on the first not-ready
// project drops the warm results too.
func TestCrossProjectSkipsNotReadyServesWarm(t *testing.T) {
	eng, v, _ := countingEngine(t, storage.Config{EmbedderBatchSize: 32})
	ctx := context.Background()

	// warm: a small project, built and cached by a first scoped search.
	addDrawer(t, v, "warm", "wing", "room", "a warm searchable drawer", "facts")
	if _, err := eng.Search(ctx, "warm", SearchFilters{Project: "warm"}); err != nil {
		t.Fatalf("priming the warm project: %v", err)
	}
	// cold: over one batch of uncached misses.
	for i := 0; i < 40; i++ {
		addDrawer(t, v, "cold", "wing", "room", fmt.Sprintf("cold distinct chunk %d", i), "facts")
	}

	results, err := eng.Search(ctx, "warm", SearchFilters{})
	if err != nil {
		t.Fatalf("cross-project search must answer from the warm project, got %v", err)
	}
	if len(results) == 0 {
		t.Fatal("cross-project search returned no results; the warm project was dropped")
	}
	for _, r := range results {
		if r.Project == "cold" {
			t.Fatalf("a result came from the refused cold project: %+v", r)
		}
	}
}

// S2: Search returns the typed not-ready error unwrapped — no `build index for`
// / `search:` prefix — so the surfaces read the `vp index rebuild` text cleanly.
func TestSearchReturnsNotBuiltErrorUnwrapped(t *testing.T) {
	eng, v, _ := countingEngine(t, storage.Config{EmbedderBatchSize: 32})
	for i := 0; i < 40; i++ {
		addDrawer(t, v, "proj", "wing", "room", fmt.Sprintf("distinct chunk %d", i), "facts")
	}

	_, err := eng.Search(context.Background(), "query", SearchFilters{Project: "proj"})
	var nb *IndexNotBuiltError
	if !errors.As(err, &nb) {
		t.Fatalf("want *IndexNotBuiltError, got %v", err)
	}
	msg := err.Error()
	for _, bad := range []string{"build index for", "search:"} {
		if strings.Contains(msg, bad) {
			t.Fatalf("error text is wrapped (%q contains %q); must be unwrapped", msg, bad)
		}
	}
	if !strings.Contains(msg, "vp index rebuild") {
		t.Fatalf("error text %q does not name `vp index rebuild`", msg)
	}
}

// WarmEmbedder constructs the embedder off the caller and makes it ready; a
// non-lazy (already-ready) embedder is a no-op that still closes the signal.
func TestWarmEmbedderConstructsDetached(t *testing.T) {
	lazy, _ := newColdModel(nil, nil)
	v := testVault(t)
	eng := NewEngine(lazy, v, storage.Config{SearchDefaultLimit: 10})
	t.Cleanup(func() { eng.Close() })

	if eng.EmbedderReady() {
		t.Fatal("a fresh lazy embedder must not be ready before warm-up")
	}
	eng.WarmEmbedder()
	deadline := time.Now().Add(2 * time.Second)
	for !eng.EmbedderReady() {
		if time.Now().After(deadline) {
			t.Fatal("WarmEmbedder did not construct the embedder")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// swapDuration sets *p to v and returns a restore func, for test-local bounds.
func swapDuration(p *time.Duration, v time.Duration) func() {
	old := *p
	*p = v
	return func() { *p = old }
}
