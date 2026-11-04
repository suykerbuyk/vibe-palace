// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"context"
	"errors"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"testing"
	"time"
)

// THE INVARIANT this file exists to satisfy
//
// FIFTEEN inert constructions were found for this one guard across six
// specification attempts — twelve by review, the last three by the implementor
// AFTER the rule below was written down. They were all the same defect:
//
//	Every assertion in this guard must observe the PRODUCTION code path, the
//	PRODUCTION constant, and the WIRING between them. A test that drives a
//	production component directly, with a context it supplies itself, proves
//	that component works; it proves nothing about whether production hands
//	that component the right context.
//
// Refinement: when a split lives INSIDE one function — pl.Tx on the outer ctx,
// the write loop on the budget ctx — the two parameters ARE the wiring, so a
// direct call with two distinct ctxs is the production path for that split.
// The wiring clause binds splits that cross a call boundary.
//
// Concretely, in this file:
//   - no test writes searchBuildBudget (construction 5);
//   - no test uses a literal where the identifier is the thing under test
//     (constructions 2 and 4);
//   - the counts come from the production decisionLoopHook, never a counter the
//     test increments itself (construction 7);
//   - the ctx the production loop checks is the one the production seam
//     returned (construction 6);
//   - there is no simulated clock — it was the machinery constructions 1, 3 and
//     4 were all built on.
//
// If you are adding to this file, assume the list of inert constructions is
// still incomplete, and check the one property all seven violate: THE WRITER
// MUST OBSERVE AN EXPIRY CAUSED BY THE PRODUCTION CONSTANT'S CURRENT VALUE.

// (b) The mechanism test: two production runs through the real seam and the
// real regenerateDecisionChunks, on the SAME live parent ctx — no deadline on
// it, never cancelled — where the ONLY input that differs is the budget.
//
// A note loop that ignores its ctx fails row one. A seam that ignores its
// budget argument fails row one. Nothing here measures elapsed time.
func TestTheBudgetBoundsTheDecisionWriter(t *testing.T) {
	const project = "proj"
	stems := []string{
		"2026-05-13-bbbb0001-01",
		"2026-05-13-bbbb0002-01",
		"2026-05-13-bbbb0003-01",
		"2026-05-13-bbbb0004-01",
		"2026-05-13-bbbb0005-01",
	}
	const notes = 5

	run := func(t *testing.T, budget time.Duration) int {
		t.Helper()
		eng, v := testEngine(t)
		ensureProjectDir(t, v, project)
		for _, s := range stems {
			writeDecisionNoteWithSession(t, v.Root, project, s, "2026-05-13", "", "D "+s)
		}

		seen := 0
		old := decisionLoopHook
		decisionLoopHook = func(int) { seen++ }
		t.Cleanup(func() { decisionLoopHook = old })

		// The SAME live parent for both rows: no deadline, never cancelled. The
		// only thing that differs between the rows is the budget argument.
		parent := context.Background()
		loopCtx, cancel := searchBuildCtx(parent, budget)
		defer cancel()

		pl, err := eng.lockProject(parent, project, 5*time.Second)
		if err != nil {
			t.Fatalf("lockProject: %v", err)
		}
		defer pl.release()
		chain := genChain{}
		// ctx = the live parent (bounds only pl.Tx's lock wait);
		// loopCtx = the budget under test.
		_ = eng.regenerateDecisionChunks(parent, loopCtx, pl, 5*time.Second, &chain, project, true)
		return seen
	}

	t.Run("an expired budget stops the writer", func(t *testing.T) {
		if got := run(t, time.Nanosecond); got >= notes {
			t.Fatalf("decisionLoopHook ran %d times with a 1ns budget, want strictly fewer than %d: the writer did not observe the deadline", got, notes)
		}
	})

	t.Run("an ample budget lets the writer finish", func(t *testing.T) {
		if got := run(t, time.Hour); got != notes {
			t.Fatalf("decisionLoopHook ran %d times with a 1h budget, want exactly %d", got, notes)
		}
	})
}

// (c) Wiring: each PRODUCTION caller of the seam must pass the PRODUCTION
// constant. Asserting the seam accepts an argument proves nothing — calling it
// from the test would do that. These assertions are on what the production
// callers actually passed, compared against the identifier and never a literal.
//
// Both sites are required: if only one were asserted, the other could pass a
// literal and this test would still pass.
func TestBothCallSitesPassTheProductionBudget(t *testing.T) {
	record := func(t *testing.T, drive func(e *Engine, project string)) []time.Duration {
		t.Helper()
		var got []time.Duration
		old := searchBuildCtx
		searchBuildCtx = func(parent context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
			got = append(got, budget)
			return old(parent, budget)
		}
		t.Cleanup(func() { searchBuildCtx = old })

		eng, v := testEngine(t)
		const project = "proj"
		ensureProjectDir(t, v, project)
		drive(eng, project)
		return got
	}

	t.Run("rebuild on the search path", func(t *testing.T) {
		got := record(t, func(e *Engine, project string) {
			// refuse=true is the search path; that is the arm that budgets.
			_, _ = e.rebuild(context.Background(), project, 5*time.Second, modeSearch, true, buildStart{})
		})
		if len(got) == 0 {
			t.Fatal("rebuild(refuse=true) never called searchBuildCtx")
		}
		for i, b := range got {
			if b != searchBuildBudget {
				t.Errorf("call %d passed %v, want searchBuildBudget (%v)", i, b, searchBuildBudget)
			}
		}
	})

	t.Run("the witnessed-load site", func(t *testing.T) {
		got := record(t, func(e *Engine, project string) {
			_ = e.ensureIndex(context.Background(), project)
		})
		// ensureIndex drives BOTH sites: the witnessed-load wrap and then
		// rebuild(refuse=true). One call means the witnessed wrap is gone —
		// which would put tryWitnessedLoad's own full corpus walk back outside
		// every budget. Counting is what distinguishes the two sites here,
		// because both pass the same constant.
		if len(got) < 2 {
			t.Fatalf("ensureIndex made %d searchBuildCtx call(s), want >= 2 "+
				"(the witnessed-load site plus rebuild): the witnessed wrap is unwired, "+
				"so tryWitnessedLoad's corpus walk is unbounded", len(got))
		}
		for i, b := range got {
			if b != searchBuildBudget {
				t.Errorf("call %d passed %v, want searchBuildBudget (%v)", i, b, searchBuildBudget)
			}
		}
	})

	t.Run("an explicit rebuild does not budget", func(t *testing.T) {
		got := record(t, func(e *Engine, project string) {
			_, _ = e.rebuild(context.Background(), project, 5*time.Second, modeExplicit, false, buildStart{})
		})
		if len(got) != 0 {
			t.Fatalf("rebuild(refuse=false) consulted the budget seam %d times, want 0: `vp index rebuild` must stay unbounded", len(got))
		}
	})
}

// (d) Sizing. The budget must exceed the MEASURED regeneration floor, asserted
// against the named constant. Deliberately NOT asserted against
// notes*perNoteCost or any factor a test picks: that product can be made 1ns,
// after which every budget passes and the guard is inert. This is the only
// place regenerationFloor appears.
func TestTheBudgetExceedsTheMeasuredRegenerationFloor(t *testing.T) {
	if searchBuildBudget <= regenerationFloor {
		t.Fatalf("searchBuildBudget (%v) must exceed the measured regeneration floor (%v): "+
			"at or below it the budget expires inside the decision WRITER, which is the "+
			"failure mode this bound exists to prevent", searchBuildBudget, regenerationFloor)
	}
}

// (e) The zero-budget contract, on the real production seam. A non-positive
// budget must hand back the PARENT, not an already-expired child: returning
// context.WithTimeout(parent, 0) would turn every search-path build into an
// instant refusal.
func TestSearchBuildCtxZeroBudgetReturnsTheParent(t *testing.T) {
	for _, budget := range []time.Duration{0, -time.Second} {
		parent := context.Background()
		ctx, cancel := searchBuildCtx(parent, budget)
		if ctx != parent {
			t.Errorf("budget %v: returned a derived ctx, want the parent itself", budget)
		}
		if _, ok := ctx.Deadline(); ok {
			t.Errorf("budget %v: returned a ctx carrying a deadline", budget)
		}
		if err := ctx.Err(); err != nil {
			t.Errorf("budget %v: returned an already-done ctx: %v", budget, err)
		}
		cancel() // must be a no-op, not a cancel of the parent
		if err := ctx.Err(); err != nil {
			t.Errorf("budget %v: cancel() was not a no-op: %v", budget, err)
		}
	}

	parent := context.Background()
	ctx, cancel := searchBuildCtx(parent, time.Hour)
	defer cancel()
	if ctx == parent {
		t.Fatal("a positive budget returned the parent, want a deadline-carrying child")
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("a positive budget returned a ctx with no deadline")
	}
}

// The collectors must return their ctx error so errors.Is survives to the top
// of walkCorpus, where the launcher predicate and buildErrHook key on it. A
// %v-formatted or re-created error silently turns the soft deadline hard.
func TestCollectorCtxErrorsSurviveErrorsIs(t *testing.T) {
	_, v := testEngine(t)
	const project = "proj"
	ensureProjectDir(t, v, project)
	writeDecisionNoteWithSession(t, v.Root, project, "2026-05-13-cccc0001-01", "2026-05-13", "", "D1")

	expired, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("iteration", func(t *testing.T) {
		_, _, _, err := collectIterationCorpus(expired, v, project)
		mustBeCtxErr(t, err)
	})
	t.Run("note", func(t *testing.T) {
		_, _, _, err := collectNoteCorpus(expired, v, project)
		mustBeCtxErr(t, err)
	})
	t.Run("commitlog", func(t *testing.T) {
		_, _, _, err := collectCommitLogCorpus(expired, v, project)
		mustBeCtxErr(t, err)
	})
	t.Run("decision sets", func(t *testing.T) {
		_, err := collectNoteDecisionSets(expired, v, project)
		mustBeCtxErr(t, err)
	})
}

func mustBeCtxErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("no error on an expired ctx")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is lost the ctx error: %v (a %%v-format or re-created error breaks the launcher predicate)", err)
	}
}

// The end-to-end arm: drive the PRODUCTION search path (ensureIndex -> rebuild
// with refuse=true) through the real seam at a budget that is already spent,
// and assert the raw prelude error reaches buildErrHook as a
// context.DeadlineExceeded that errors.Is can still see.
//
// This is the arm that catches two regressions the direct-call arm above
// cannot:
//   - rebuild passing the outer ctx instead of workCtx (the preludes stop being
//     bounded at all);
//   - the regeneration error being wrapped with %v instead of %w, which makes
//     the deadline silently hard: the launcher predicate in ensureIndex and
//     mapBuildErr both key on errors.Is.
//
// The budget is injected by replacing the seam and calling the REAL one, never
// by writing searchBuildBudget. That the production call sites pass the real
// constant is asserted separately by TestBothCallSitesPassTheProductionBudget.
func TestAnExpiredPreludeBudgetReachesBuildErrHookAsDeadlineExceeded(t *testing.T) {
	const project = "proj"

	realSeam := searchBuildCtx
	searchBuildCtx = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		return realSeam(parent, time.Nanosecond)
	}
	t.Cleanup(func() { searchBuildCtx = realSeam })

	var sawRaw error
	seen := false
	oldHook := buildErrHook
	buildErrHook = func(_ string, err error) { sawRaw, seen = err, true }
	t.Cleanup(func() { buildErrHook = oldHook })

	eng, v := testEngine(t)
	ensureProjectDir(t, v, project)
	for _, s := range []string{
		"2026-05-13-dddd0001-01",
		"2026-05-13-dddd0002-01",
		"2026-05-13-dddd0003-01",
	} {
		writeDecisionNoteWithSession(t, v.Root, project, s, "2026-05-13", "", "D "+s)
	}

	_ = eng.ensureIndex(context.Background(), project)

	if !seen {
		t.Fatal("buildErrHook never fired: the search-path build did not reach it")
	}
	if sawRaw == nil {
		t.Fatal("the search-path build returned nil with an already-spent prelude budget: the preludes are not bounded")
	}
	if !errors.Is(sawRaw, context.DeadlineExceeded) {
		t.Fatalf("raw build error does not satisfy errors.Is(err, context.DeadlineExceeded): %v\n"+
			"a %%v-formatted wrap breaks the launcher predicate and mapBuildErr, "+
			"turning the soft deadline hard", sawRaw)
	}
}

// Guards the WRITE LOOP's own loopCtx check, which the end-to-end arm cannot
// reach: with an already-spent budget regenerateDecisionChunks short-circuits
// in collectNoteDecisionSets and never enters the loop, so the loop's check is
// invisible to that arm. (Found by mutant 7 staying green — construction #8.)
//
// Here the budget ctx is live when the loop is entered and is retired from the
// PRODUCTION hook partway through, so the discriminator is exactly "does the
// production write loop consult its loopCtx". It does not assert anything about
// searchBuildBudget; (b), (c) and (d) do that.
func TestTheDecisionWriteLoopConsultsItsLoopCtx(t *testing.T) {
	const project = "proj"
	stems := []string{
		"2026-05-13-eeee0001-01", "2026-05-13-eeee0002-01", "2026-05-13-eeee0003-01",
		"2026-05-13-eeee0004-01", "2026-05-13-eeee0005-01",
	}
	eng, v := testEngine(t)
	ensureProjectDir(t, v, project)
	for _, s := range stems {
		writeDecisionNoteWithSession(t, v.Root, project, s, "2026-05-13", "", "D "+s)
	}

	loopCtx, retire := context.WithCancel(context.Background())
	defer retire()

	seen := 0
	old := decisionLoopHook
	decisionLoopHook = func(n int) {
		seen++
		if n == 2 {
			retire() // live for notes 0..2, done from note 3 on
		}
	}
	t.Cleanup(func() { decisionLoopHook = old })

	pl, err := eng.lockProject(context.Background(), project, 5*time.Second)
	if err != nil {
		t.Fatalf("lockProject: %v", err)
	}
	defer pl.release()
	chain := genChain{}
	_ = eng.regenerateDecisionChunks(context.Background(), loopCtx, pl, 5*time.Second, &chain, project, true)

	if seen != 3 {
		t.Fatalf("decisionLoopHook ran %d times, want 3: the write loop did not consult loopCtx "+
			"(it entered notes 0..2, and the ctx was done from note 3 on)", seen)
	}
}

// Guards that the CORPUS WALK runs on the prelude budget's ctx rather than the
// outer ctx — the half of mutant 1 that the end-to-end arm cannot see, because
// rebuild's entry guard returns first when the budget is already spent
// (construction #9).
//
// The outer ctx stays live throughout. The budget ctx is retired from the
// production walk hook, so the discriminator is "does walkCorpus observe the
// ctx rebuild handed it".
func TestTheCorpusWalkRunsOnThePreludeBudgetCtx(t *testing.T) {
	const project = "proj"

	var budgetCtx context.Context
	var retire context.CancelFunc
	realSeam := searchBuildCtx
	searchBuildCtx = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		budgetCtx, retire = context.WithCancel(parent)
		return budgetCtx, retire
	}
	t.Cleanup(func() { searchBuildCtx = realSeam })

	walked := 0
	oldWalk := corpusWalkItemHook
	corpusWalkItemHook = func(string, int) {
		walked++
		if retire != nil {
			retire() // the budget is done from the next item on
		}
	}
	t.Cleanup(func() { corpusWalkItemHook = oldWalk })

	var sawRaw error
	oldHook := buildErrHook
	buildErrHook = func(_ string, err error) { sawRaw = err }
	t.Cleanup(func() { buildErrHook = oldHook })

	eng, v := testEngine(t)
	ensureProjectDir(t, v, project)
	for i, s := range []string{
		"2026-05-13-ffff0001-01", "2026-05-13-ffff0002-01", "2026-05-13-ffff0003-01",
	} {
		_ = i
		writeDecisionNoteWithSession(t, v.Root, project, s, "2026-05-13", "", "D "+s)
	}

	// Outer ctx stays live for the whole call.
	_ = eng.ensureIndex(context.Background(), project)

	if walked == 0 {
		t.Fatal("the corpus walk never reached an item; the test proved nothing")
	}
	if !errors.Is(sawRaw, context.Canceled) && !errors.Is(sawRaw, context.DeadlineExceeded) {
		t.Fatalf("the corpus walk did not stop on the prelude budget's ctx — raw build error was %v "+
			"(nil means the walk ran to completion on a live ctx); rebuild is passing the outer "+
			"ctx to walkCorpus instead of workCtx", sawRaw)
	}
}

// Guards the %w on rebuild's regeneration wrap. The already-spent-budget arm
// cannot reach it: rebuild's entry guard returns the ctx error directly, so the
// wrap is never exercised (construction #9). Here the budget is live on entry
// and is retired from inside the write loop, so the error genuinely originates
// in regenerateDecisionChunks and travels out through that wrap.
func TestTheRegenerationWrapPreservesErrorsIs(t *testing.T) {
	const project = "proj"

	var retire context.CancelFunc
	realSeam := searchBuildCtx
	searchBuildCtx = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		c, cancel := context.WithCancel(parent)
		retire = cancel
		return c, cancel
	}
	t.Cleanup(func() { searchBuildCtx = realSeam })

	entered := 0
	oldLoop := decisionLoopHook
	decisionLoopHook = func(int) {
		entered++
		if retire != nil {
			retire()
		}
	}
	t.Cleanup(func() { decisionLoopHook = oldLoop })

	var sawRaw error
	oldHook := buildErrHook
	buildErrHook = func(_ string, err error) { sawRaw = err }
	t.Cleanup(func() { buildErrHook = oldHook })

	eng, v := testEngine(t)
	ensureProjectDir(t, v, project)
	for _, s := range []string{
		"2026-05-13-99990001-01", "2026-05-13-99990002-01", "2026-05-13-99990003-01",
	} {
		writeDecisionNoteWithSession(t, v.Root, project, s, "2026-05-13", "", "D "+s)
	}

	_ = eng.ensureIndex(context.Background(), project)

	if entered == 0 {
		t.Fatal("the decision write loop was never entered; the test proved nothing")
	}
	if !errors.Is(sawRaw, context.Canceled) && !errors.Is(sawRaw, context.DeadlineExceeded) {
		t.Fatalf("errors.Is lost the ctx error through rebuild's regeneration wrap: %v\n"+
			"a %%v there turns the soft deadline hard: ensureIndex's launcher predicate "+
			"and mapBuildErr both key on errors.Is", sawRaw)
	}
}

// ---------------------------------------------------------------------------
// WIRING TESTS
//
// The invariant's missing clause, learned the hard way: observing the
// production component and the production constant is NOT enough. A test that
// drives a production component DIRECTLY, with a context it supplies itself,
// proves that component works. It proves nothing about whether production hands
// that component the right context.
//
// Constructions 10 and 11 were both exactly that: keeping the seam call but
// passing the outer ctx on to tryWitnessedLoad, and calling
// regenerateDecisionChunks(ctx, ctx, ...) from rebuild, each left every named
// guard green. The two tests below close them by retiring the ctx the SEAM
// returned and asserting the PRODUCTION consumer stopped.
// ---------------------------------------------------------------------------

// B2: rebuild must hand the prelude budget to the decision writer as its loop
// ctx. Driven through rebuild — not by calling regenerateDecisionChunks — with
// a budget that is LIVE on entry and retired from the production
// decisionLoopHook mid-loop.
func TestRebuildBoundsTheDecisionWriterWithThePreludeBudget(t *testing.T) {
	const project = "proj"
	stems := []string{
		"2026-05-13-77770001-01", "2026-05-13-77770002-01", "2026-05-13-77770003-01",
		"2026-05-13-77770004-01", "2026-05-13-77770005-01", "2026-05-13-77770006-01",
	}

	var retire context.CancelFunc
	realSeam := searchBuildCtx
	searchBuildCtx = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		c, cancel := context.WithCancel(parent)
		retire = cancel
		return c, cancel
	}
	t.Cleanup(func() { searchBuildCtx = realSeam })

	entered := 0
	oldLoop := decisionLoopHook
	decisionLoopHook = func(n int) {
		entered++
		if n == 2 && retire != nil {
			retire()
		}
	}
	t.Cleanup(func() { decisionLoopHook = oldLoop })

	eng, v := testEngine(t)
	ensureProjectDir(t, v, project)
	for _, s := range stems {
		writeDecisionNoteWithSession(t, v.Root, project, s, "2026-05-13", "", "D "+s)
	}

	// The OUTER ctx stays live for the whole call: only the budget is retired.
	_, _ = eng.rebuild(context.Background(), project, 5*time.Second, modeSearch, true, buildStart{})

	if entered == 0 {
		t.Fatal("the decision write loop was never entered; the test proved nothing")
	}
	if entered != 3 {
		t.Fatalf("the write loop entered %d notes of %d, want exactly 3: rebuild is not handing "+
			"the prelude budget to regenerateDecisionChunks as its loop ctx "+
			"(a regenerateDecisionChunks(ctx, ctx, ...) regression restores unbounded "+
			"decision regeneration with every other guard green)", entered, len(stems))
	}
}

// B1: the witnessed-load site must hand the ctx the seam returned to
// tryWitnessedLoad, whose own full corpus walk is otherwise unbounded.
//
// Construction: 40 drawers against a 32 embed batch, so a search-path rebuild
// REFUSES and the only way the project can end up built is the witnessed
// consume. The budget ctx is retired from the production walk hook on the first
// item, so with correct wiring the witnessed walk aborts and nothing is
// consumed; a wrap that discards wctx walks on the live outer ctx, completes,
// and consumes.
func TestTheWitnessedLoadRunsOnTheCtxTheSeamReturned(t *testing.T) {
	v := testVault(t)
	emb := newCountingEmbedder(384)
	eng := NewEngine(emb, v, storage.Config{SearchDefaultLimit: 10, EmbedderBatchSize: 32})
	t.Cleanup(func() { eng.Close() })
	// Session notes so the witnessed walk actually iterates items: a
	// drawers-only corpus walks zero per-item loops and the hook never fires,
	// which would make this test prove nothing.
	ensureProjectDir(t, v, "proj")
	for _, st := range []string{
		"2026-05-13-55550001-01", "2026-05-13-55550002-01", "2026-05-13-55550003-01",
	} {
		writeDecisionNoteWithSession(t, v.Root, "proj", st, "2026-05-13", "", "D "+st)
	}
	commitDrawerGraph(t, v, "proj", 40)

	var retire context.CancelFunc
	realSeam := searchBuildCtx
	searchBuildCtx = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		c, cancel := context.WithCancel(parent)
		retire = cancel
		return c, cancel
	}
	t.Cleanup(func() { searchBuildCtx = realSeam })

	walked := 0
	oldWalk := corpusWalkItemHook
	corpusWalkItemHook = func(string, int) {
		walked++
		if retire != nil {
			retire()
		}
	}
	t.Cleanup(func() { corpusWalkItemHook = oldWalk })

	_, _ = eng.Search(context.Background(), "distinct chunk number 7", SearchFilters{Project: "proj"})

	if walked == 0 {
		t.Fatal("no corpus walk item was reached; the test proved nothing")
	}
	if eng.HasIndex("proj") {
		t.Fatal("the witnessed load consumed the graph although its budget ctx was retired on the " +
			"first walk item: tryWitnessedLoad is running on the outer ctx, not the ctx " +
			"searchBuildCtx returned, so its corpus walk is unbounded")
	}
}

// R2: pins the ctx SPLIT in regenerateDecisionChunks — pl.Tx takes the OUTER
// ctx so a busy commit lock is classified as a lock timeout and skipped, while
// the loop ctx carries the prelude budget. Previously the production comment
// cited a test of this name that did not exist.
//
// Construction: another holder owns the commit lock for the whole call, and the
// loop ctx carries a deadline SHORTER than pl.Tx's wait. With the outer ctx on
// pl.Tx the wait ends in vaultlock.ErrLockWaitTimeout, isLockTimeout matches,
// and the regeneration skips and returns nil. Passing the loop ctx instead ends
// the wait in context.DeadlineExceeded, which isLockTimeout does NOT match, and
// the error escapes.
func TestTheCommitLockWaitUsesTheOuterCtxNotThePreludeBudget(t *testing.T) {
	const project = "proj"
	eng, v := testEngine(t)
	ensureProjectDir(t, v, project)
	writeDecisionNoteWithSession(t, v.Root, project, "2026-05-13-88880001-01", "2026-05-13", "", "D1")

	// Hold the project's commit lock for the duration.
	holder, err := indexstore.Lock(context.Background(), v, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Release() }()

	pl, err := eng.lockProject(context.Background(), project, 5*time.Second)
	if err != nil {
		t.Fatalf("lockProject: %v", err)
	}
	defer pl.release()

	// The prelude budget expires well before pl.Tx's 2s wait gives up.
	loopCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	chain := genChain{}
	gotErr := eng.regenerateDecisionChunks(context.Background(), loopCtx, pl, 2*time.Second, &chain, project, true)

	if gotErr != nil {
		t.Fatalf("regenerateDecisionChunks returned %v, want nil (skipped on a busy lock): "+
			"pl.Tx is being given the prelude budget instead of the outer ctx, so a busy "+
			"commit lock is misclassified as a budget expiry", gotErr)
	}
	if !chain.skipped {
		t.Fatal("chain.skipped is false: the busy-lock skip arm was not taken")
	}
}

// R5: the gone-owner loop is destructive and must stop on an expired budget
// rather than deleting and committing beside a truncated write prefix.
//
// Defined answer, pinned here: an expired budget DELETES NOTHING. The write
// prefix already committed is additive and a later unbounded Rebuild completes
// it; a deletion performed beside a truncated prefix cannot afterwards be
// distinguished from one the corpus actually called for.
func TestPartialRegenerationStopsBeforeDeletingGoneOwners(t *testing.T) {
	const project = "proj"
	stems := []string{
		"2026-05-13-66660001-01", "2026-05-13-66660002-01",
		"2026-05-13-66660003-01", "2026-05-13-66660004-01",
	}
	eng, v := testEngine(t)
	ensureProjectDir(t, v, project)

	dates := map[string]string{}
	seeded := map[string][]string{}
	for _, s := range stems {
		dates[s] = "2026-05-13"
		seeded[s] = []string{"D " + s}
	}
	// Seed all four, then remove the last from disk: it becomes a "gone" owner.
	writeDecisionsPerNoteTx(t, v, project, stems, seeded, dates)
	for _, s := range stems[:3] {
		writeDecisionNoteWithSession(t, v.Root, project, s, dates[s], "", seeded[s]...)
	}
	goneStem := stems[3]

	// The budget must stay live through EVERY write and go done exactly when
	// the gone loop starts. armAfterLastWrite gives one grace Err() call after
	// the last note is reached: that note's own check passes, its write lands,
	// and the gone loop's first check is the one that sees the deadline. A
	// plain cancel at the last note would abort that note instead and the gone
	// loop would never be reached at all — which is how the first version of
	// this test passed against a gone loop that had no check.
	loopCtx := &armAfterLastWrite{Context: context.Background(), done: make(chan struct{}), grace: 1}
	oldLoop := decisionLoopHook
	decisionLoopHook = func(n int) {
		if n == len(stems)-2 { // index of the last SURVIVING note
			loopCtx.armed = true
		}
	}
	t.Cleanup(func() { decisionLoopHook = oldLoop })

	pl, err := eng.lockProject(context.Background(), project, 5*time.Second)
	if err != nil {
		t.Fatalf("lockProject: %v", err)
	}
	chain := genChain{}
	_ = eng.regenerateDecisionChunks(context.Background(), loopCtx, pl, 5*time.Second, &chain, project, true)
	pl.release()

	owners := decisionOwnerIDs(t, v, project)
	goneOwner := noteDecisionOwnerKey(goneStem)
	found := false
	for _, o := range owners {
		if o == goneOwner {
			found = true
		}
	}
	if !found {
		t.Fatalf("the gone note's decision chunks were deleted although the budget expired "+
			"before the gone loop: an expired budget must DELETE NOTHING, so a later "+
			"unbounded rebuild performs the deletion. owners=%v want to include %q",
			owners, goneOwner)
	}
	// The WRITE prefix must still have committed: the contract is "the prefix
	// may commit, no delete may". Without this, a regression that aborts before
	// the last write AND skips the delete would pass the check above.
	for _, st := range stems[:3] {
		want := noteDecisionOwnerKey(st)
		seen := false
		for _, o := range owners {
			if o == want {
				seen = true
			}
		}
		if !seen {
			t.Fatalf("surviving note %q is absent from the store: the additive write prefix "+
				"did not commit. owners=%v", st, owners)
		}
	}
}

// oneShotCtx reports Done/Err only AFTER its first Err() call. It exists to
// reach a collector's per-file / per-entry ctx check, which an
// already-cancelled ctx cannot: the top-of-function check returns first, so a
// %v-formatted error on the INNER return stays invisible (R3).
type oneShotCtx struct {
	context.Context
	calls int
	done  chan struct{}
}

func newOneShotCtx() *oneShotCtx {
	return &oneShotCtx{Context: context.Background(), done: make(chan struct{})}
}

func (c *oneShotCtx) Err() error {
	c.calls++
	if c.calls <= 1 {
		return nil // the top-of-function check sees a live ctx
	}
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return context.Canceled
}

func (c *oneShotCtx) Done() <-chan struct{} { return c.done }

// R3: the collectors' INNER (per-file / per-entry) ctx returns must also
// preserve errors.Is. TestCollectorCtxErrorsSurviveErrorsIs only reaches the
// top-of-function return, so a %v there went undetected.
func TestCollectorInnerLoopCtxErrorsSurviveErrorsIs(t *testing.T) {
	_, v := testEngine(t)
	const project = "proj"
	ensureProjectDir(t, v, project)
	for _, s := range []string{"2026-05-13-44440001-01", "2026-05-13-44440002-01"} {
		writeDecisionNoteWithSession(t, v.Root, project, s, "2026-05-13", "", "D "+s)
	}
	writeIterationsMD(t, v.Root, project, "## Iteration 1 — first\n\nbody one here with enough prose to chunk.\n\n## Iteration 2 — second\n\nbody two here with enough prose to chunk.\n")
	writeCommitLog(t, v.Root, project, [][2]string{
		{"aaaaaaaaaaaa", "first commit body with enough prose to chunk"},
		{"bbbbbbbbbbbb", "second commit body with enough prose to chunk"},
	})

	t.Run("note", func(t *testing.T) {
		c := newOneShotCtx()
		_, _, _, err := collectNoteCorpus(c, v, project)
		mustBeInnerCtxErr(t, err, c.calls)
	})
	t.Run("decision sets", func(t *testing.T) {
		c := newOneShotCtx()
		_, err := collectNoteDecisionSets(c, v, project)
		mustBeInnerCtxErr(t, err, c.calls)
	})
	t.Run("iteration", func(t *testing.T) {
		c := newOneShotCtx()
		_, _, _, err := collectIterationCorpus(c, v, project)
		mustBeInnerCtxErr(t, err, c.calls)
	})
	t.Run("commitlog", func(t *testing.T) {
		c := newOneShotCtx()
		_, _, _, err := collectCommitLogCorpus(c, v, project)
		mustBeInnerCtxErr(t, err, c.calls)
	})
}

func mustBeInnerCtxErr(t *testing.T, err error, calls int) {
	t.Helper()
	if calls < 2 {
		t.Fatalf("the collector consulted ctx %d time(s): its inner loop has no ctx check, "+
			"so the per-file/per-entry bound does not exist", calls)
	}
	if err == nil {
		t.Fatal("no error although the ctx went done inside the loop")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is lost the ctx error on the INNER return: %v", err)
	}
}

// armAfterLastWrite stays live until armed, then allows `grace` further Err()
// calls before reporting Canceled. It is a context double, never a stand-in for
// a production component: the loops it bounds are the real ones.
type armAfterLastWrite struct {
	context.Context
	done  chan struct{}
	armed bool
	grace int
}

func (c *armAfterLastWrite) Err() error {
	if !c.armed {
		return nil
	}
	if c.grace > 0 {
		c.grace--
		return nil
	}
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return context.Canceled
}

func (c *armAfterLastWrite) Done() <-chan struct{} { return c.done }

// R5, the mid-loop case: a deadline landing between gone-owner deletions must
// not split the set. pl.finishTx() commits before regenerateDecisionChunks
// inspects writeErr, so a delete already applied to the Tx is durable even on
// an error return — which is why a per-iteration ctx check in the gone loop
// produces a PARTIAL delete rather than a safe one.
//
// THREE gone owners and grace 3. A single-owner fixture cannot see this class
// at all, and two owners cannot see it ROBUSTLY: one grace count has to serve
// two plausible mutant shapes — the entry check REPLACED by a per-iteration
// one, and the entry check PLUS a per-iteration one — and tuning for either
// un-tunes the other, so the second shape deletes nothing and reds on the
// wrong assertion. With three owners and grace 3 both shapes produce a genuine
// one-of-N split while correct code still deletes all three.
func TestGoneOwnerDeletesAreAllOrNothing(t *testing.T) {
	const project = "proj"
	// THREE gone owners, not two. With two, a single grace count cannot catch
	// both plausible mutant shapes: grace tuned for "entry check replaced by a
	// per-iteration one" lets "entry check PLUS a per-iteration one" exhaust the
	// grace at the entry check and delete nothing, which is R5a's failure, not
	// this one's. Three owners with grace 3 makes BOTH shapes produce a genuine
	// one-of-N split while the correct code still deletes all three.
	stems := []string{
		"2026-05-13-33330001-01", "2026-05-13-33330002-01", // survive
		"2026-05-13-33330003-01", "2026-05-13-33330004-01", "2026-05-13-33330005-01", // gone
	}
	eng, v := testEngine(t)
	ensureProjectDir(t, v, project)

	dates, seeded := map[string]string{}, map[string][]string{}
	for _, s := range stems {
		dates[s], seeded[s] = "2026-05-13", []string{"D " + s}
	}
	writeDecisionsPerNoteTx(t, v, project, stems, seeded, dates)
	survivors := stems[:2]
	for _, s := range survivors {
		writeDecisionNoteWithSession(t, v.Root, project, s, dates[s], "", seeded[s]...)
	}
	goneKeys := []string{
		noteDecisionOwnerKey(stems[2]), noteDecisionOwnerKey(stems[3]), noteDecisionOwnerKey(stems[4]),
	}

	// grace 3: the last surviving note's own check passes (its write lands),
	// the gone loop's entry check passes, one per-iteration check would pass,
	// and the next reports done — so a mutant that checks per iteration splits
	// the set whether or not it also kept the entry check.
	loopCtx := &armAfterLastWrite{Context: context.Background(), done: make(chan struct{}), grace: 3}
	oldLoop := decisionLoopHook
	decisionLoopHook = func(n int) {
		if n == len(survivors)-1 {
			loopCtx.armed = true
		}
	}
	t.Cleanup(func() { decisionLoopHook = oldLoop })

	pl, err := eng.lockProject(context.Background(), project, 5*time.Second)
	if err != nil {
		t.Fatalf("lockProject: %v", err)
	}
	chain := genChain{}
	_ = eng.regenerateDecisionChunks(context.Background(), loopCtx, pl, 5*time.Second, &chain, project, true)
	pl.release()

	owners := decisionOwnerIDs(t, v, project)
	stillThere := map[string]bool{}
	for _, o := range owners {
		stillThere[o] = true
	}
	present, absent := 0, 0
	for _, k := range goneKeys {
		if stillThere[k] {
			present++
		} else {
			absent++
		}
	}
	if present > 0 && absent > 0 {
		t.Fatalf("the gone set was SPLIT by a deadline between deletions — %d of %d deleted, "+
			"%d left behind. finishTx makes the deletes already applied durable, so this is a "+
			"PARTIAL delete committed to disk. gone=%v owners=%v",
			absent, len(goneKeys), present, goneKeys, owners)
	}
	if present == len(goneKeys) {
		t.Fatalf("no gone owner was deleted although the budget was live at the gone loop's "+
			"single entry check; owners=%v", owners)
	}
}

// F2, §2.2's written ruling: a slow corpus walk is a REFUSAL even on a warm
// corpus when nothing is servable in memory, and serve-as-is when something
// is. The plan's non-regression table warns explicitly that
// TestSearchPathWarmBuildNotThrottled covers the COOLDOWN path and NOT this
// one, so this is the only test of the ruling.
//
// "Warm" here means the embed cache is warm — a full Rebuild has run, so a
// rebuild would embed nothing and stats.Embedded would be 0. The budget is
// retired from the production walk hook, so the engine never learns that: the
// deadline denies it exactly the information the warm exception would need,
// which is the reasoning §2.2 gives for not implementing one.
func TestWarmCorpusWithASlowWalkIsRefused(t *testing.T) {
	newWarmVault := func(t *testing.T) (*storage.Vault, *launchSpy) {
		t.Helper()
		v := testVault(t)
		for i := 0; i < 8; i++ {
			addDrawer(t, v, "proj", "wing", "room", fmt.Sprintf("warm chunk number %d", i), "facts")
		}
		// Warm the embed cache with one full, unbounded rebuild.
		warm := NewEngine(embedder.NewMock(384), v, storage.Config{SearchDefaultLimit: 10, EmbedderBatchSize: 32})
		if _, err := warm.Rebuild(context.Background(), "proj"); err != nil {
			t.Fatalf("warming rebuild: %v", err)
		}
		warm.Close()
		return v, &launchSpy{}
	}

	retireOnWalk := func(t *testing.T) *int {
		t.Helper()
		walked := 0
		var retire context.CancelFunc
		realSeam := searchBuildCtx
		searchBuildCtx = func(parent context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
			c, cancel := context.WithCancel(parent)
			retire = cancel
			return c, cancel
		}
		t.Cleanup(func() { searchBuildCtx = realSeam })
		oldWalk := corpusWalkItemHook
		corpusWalkItemHook = func(string, int) {
			walked++
			if retire != nil {
				retire()
			}
		}
		t.Cleanup(func() { corpusWalkItemHook = oldWalk })
		return &walked
	}

	t.Run("nothing servable in memory: refuse, and the launcher fires", func(t *testing.T) {
		v, spy := newWarmVault(t)
		// A FRESH engine: the embed cache is warm but no index is in memory.
		eng := NewEngine(embedder.NewMock(384), v, storage.Config{SearchDefaultLimit: 10, EmbedderBatchSize: 32},
			WithRebuildLauncher(spy.launcher()))
		t.Cleanup(func() { eng.Close() })
		walked := retireOnWalk(t)

		_, err := eng.Search(context.Background(), "warm chunk number 3", SearchFilters{Project: "proj"})
		if *walked == 0 {
			t.Fatal("no corpus walk ran: the deadline was never exercised and this arm proves nothing")
		}
		var nb *IndexNotBuiltError
		if !errors.As(err, &nb) {
			t.Fatalf("want *IndexNotBuiltError when the walk is cut short and nothing is in "+
				"memory, got %v (the F2 ruling: a slow walk refuses even on a warm corpus)", err)
		}
		if len(spy.projects()) == 0 {
			t.Fatal("the detached rebuild launcher did not fire on the degraded search")
		}
	})

	t.Run("a non-empty in-memory index: serve as-is, and the launcher still fires", func(t *testing.T) {
		v, spy := newWarmVault(t)
		eng := NewEngine(embedder.NewMock(384), v, storage.Config{SearchDefaultLimit: 10, EmbedderBatchSize: 32},
			WithRebuildLauncher(spy.launcher()))
		t.Cleanup(func() { eng.Close() })

		// Install an in-memory index first, with no budget interference.
		if _, err := eng.Search(context.Background(), "warm chunk number 3", SearchFilters{Project: "proj"}); err != nil {
			t.Fatalf("priming search: %v", err)
		}
		if !eng.servableInMemory("proj") {
			t.Fatal("the priming search left no servable in-memory index; the arm proves nothing")
		}
		// Move the STORE generation so the next search rebuilds. A new drawer
		// alone does not: the generation moves on a store commit, and without
		// this the search short-circuits as current and the arm is vacuous.
		bump, berr := indexstore.Lock(context.Background(), v, "proj", indexstore.NoTimeout)
		if berr != nil {
			t.Fatal(berr)
		}
		if werr := bump.WriteGraph([]byte("x")); werr != nil {
			_ = bump.Release()
			t.Fatal(werr)
		}
		if cerr := bump.Commit(); cerr != nil {
			t.Fatal(cerr)
		}
		walked := retireOnWalk(t)

		res, err := eng.Search(context.Background(), "warm chunk number 3", SearchFilters{Project: "proj"})
		if *walked == 0 {
			t.Fatal("no corpus walk ran: the deadline was never exercised and this arm proves nothing")
		}
		if err != nil {
			t.Fatalf("want serve-as-is from the in-memory index, got error %v", err)
		}
		if len(res) == 0 {
			t.Fatal("serve-as-is returned no results although a non-empty index was in memory")
		}
		if len(spy.projects()) == 0 {
			t.Fatal("the detached rebuild launcher did not fire on the degraded serve-as-is")
		}
	})
}

// The budgeted search path must not run the DESTRUCTIVE delete phase at all.
//
// Tx.ReplaceOwned with recs == nil always takes the rewrite branch, and that
// branch ends in writeChunkFold — encodeLines plus a full writeFile of
// chunks.jsonl. So the phase is O(G x L) in deletions x store lines, measured
// at ~3.1 s per whole-store pass on a 537k-line store. Five deleted notes would
// spend the whole 15 s budget AFTER the only budget check. Computing the gone
// set costs a further whole-store READ.
//
// Both arms are asserted: the budgeted path neither reads the store for this
// nor deletes, and the unbounded path still does both.
func TestTheBudgetedPathDoesNotRunTheDestructiveDeletePhase(t *testing.T) {
	const project = "proj"
	stems := []string{"2026-05-13-22220001-01", "2026-05-13-22220002-01"}

	build := func(t *testing.T, refuse bool) ([]string, int) {
		t.Helper()
		eng, v := testEngine(t)
		ensureProjectDir(t, v, project)
		dates, seeded := map[string]string{}, map[string][]string{}
		for _, s := range stems {
			dates[s], seeded[s] = "2026-05-13", []string{"D " + s}
		}
		writeDecisionsPerNoteTx(t, v, project, stems, seeded, dates)
		// Only the first note survives on disk; the second becomes "gone".
		writeDecisionNoteWithSession(t, v.Root, project, stems[0], dates[stems[0]], "", seeded[stems[0]]...)

		// Count only the reads made BEFORE the corpus walk starts: the walk does
		// its own legitimate store read as its first corpus source, so a raw
		// total cannot tell the gone-set read from it. The first walk item is
		// the boundary.
		reads, readsBeforeWalk, walkStarted := 0, 0, false
		realRead := readStoreFn
		readStoreFn = func(vv *storage.Vault, p string) (*indexstore.Store, error) {
			reads++
			return realRead(vv, p)
		}
		t.Cleanup(func() { readStoreFn = realRead })
		oldWalk := corpusWalkItemHook
		corpusWalkItemHook = func(string, int) {
			if !walkStarted {
				walkStarted, readsBeforeWalk = true, reads-1 // minus the walk's own read
			}
		}
		t.Cleanup(func() { corpusWalkItemHook = oldWalk })

		// Driven through rebuild, NOT by calling regenerateDecisionChunks with
		// an applyGone the test computes itself: that would pin the writer and
		// leave rebuild free to pass the wrong value. The wiring is the thing
		// under test.
		if refuse {
			_, _ = eng.rebuild(context.Background(), project, 5*time.Second, modeSearch, true, buildStart{})
		} else if _, rerr := eng.Rebuild(context.Background(), project); rerr != nil {
			t.Fatalf("Rebuild: %v", rerr)
		}
		return decisionOwnerIDs(t, v, project), readsBeforeWalk
	}

	goneOwner := noteDecisionOwnerKey(stems[1])

	t.Run("budgeted path: no store read, no delete", func(t *testing.T) {
		owners, reads := build(t, true)
		found := false
		for _, o := range owners {
			if o == goneOwner {
				found = true
			}
		}
		if !found {
			t.Fatalf("the budgeted path deleted a gone owner: that is an O(G x L) whole-store "+
				"rewrite per deletion, running after the only budget check. owners=%v", owners)
		}
		if reads != 0 {
			t.Fatalf("the budgeted path read the whole store %d time(s) before the corpus walk, "+
				"to compute a gone set it does not apply", reads)
		}
	})

	t.Run("unbounded path: still reads and still deletes", func(t *testing.T) {
		owners, reads := build(t, false)
		for _, o := range owners {
			if o == goneOwner {
				t.Fatalf("the unbounded path did not delete the gone owner; deletion must still "+
					"happen there. owners=%v", owners)
			}
		}
		if reads == 0 {
			t.Fatal("the unbounded path never read the store, so it cannot have computed a gone set")
		}
	})
}
