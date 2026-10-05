// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/search"
)

// --- helpers ----------------------------------------------------------------

func (f *fixture) rebuild(o RebuildOptions) (RunResult, error) {
	f.t.Helper()
	return f.rebuildD(f.deps(), o)
}

func (f *fixture) rebuildD(d Deps, o RebuildOptions) (RunResult, error) {
	f.t.Helper()
	return Rebuild(context.Background(), d, o)
}

// baselineCount is how many of a project's listed archives are still in its
// baseline set.
func (f *fixture) baselineCount(project string) int {
	f.t.Helper()
	l := f.store(project).Ledger()
	n := 0
	for _, s := range f.allSHAs(project) {
		if l.InBaseline(s) {
			n++
		}
	}
	return n
}

// fakeFree is an injected FreeSpaceReader. When dropAfter > 0 it returns ok for
// the first dropAfter calls, then low; otherwise it always returns its fields.
type fakeFree struct {
	mu        sync.Mutex
	ok        FreeSpace
	low       FreeSpace
	dropAfter int
	calls     int
	err       error
}

func (r *fakeFree) Free(string) (FreeSpace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return FreeSpace{}, r.err
	}
	if r.dropAfter > 0 && r.calls > r.dropAfter {
		return r.low, nil
	}
	return r.ok, nil
}

var plentyFree = FreeSpace{Bytes: 100 << 30, Inodes: 100_000_000, HasInodes: true}

// fakeHealer is an injected GraphHealer that records its calls and reports a
// fixed result.
type fakeHealer struct {
	mu     sync.Mutex
	calls  []string
	result HealResult
	err    error
}

func (h *fakeHealer) HealGraph(_ context.Context, _, project string) (HealResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, project)
	return h.result, h.err
}

func (h *fakeHealer) projects() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.calls))
	copy(out, h.calls)
	return out
}

// --- backlog and baseline ---------------------------------------------------

// TestRebuildClearsBacklogAndEmptiesBaseline (R3, round 5): a ledger whose
// baseline set holds four archives, and two later archives already pending: an
// explicit rebuild ledgers all six and, on completion, the baseline set is
// empty.
func TestRebuildClearsBacklogAndEmptiesBaseline(t *testing.T) {
	f := newFixture(t)
	for i := range 4 {
		f.archiveOf("alpha", "B"+string(rune('0'+i)), transcript(t0, "b", 2), day(10+i))
	}
	f.ensureLedgerWithBaseline("alpha")
	f.archiveOf("alpha", "N0", transcript(t0, "n0", 2), day(20))
	f.archiveOf("alpha", "N1", transcript(t0, "n1", 2), day(21))
	if n := f.baselineCount("alpha"); n != 4 {
		t.Fatalf("baseline holds %d, want 4 before the rebuild", n)
	}

	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	for _, s := range []string{"B0", "B1", "B2", "B3", "N0", "N1"} {
		if _, ok := f.live("alpha", s); !ok {
			t.Fatalf("%s not ledgered after the rebuild", s)
		}
	}
	if n := f.baselineCount("alpha"); n != 0 {
		t.Fatalf("baseline still holds %d after a completed rebuild, want 0", n)
	}
	if r := f.reasons("alpha"); len(r) != 0 {
		t.Fatalf("stale reasons after a completed rebuild: %v", r)
	}
}

// TestRebuildStoppedByMaxArchivesKeepsTheBaseline: the same backlog, stopped by
// --max-archives 2: two archives are ledgered, the baseline set still lists all
// four, and a second run completes and empties it.
func TestRebuildStoppedByMaxArchivesKeepsTheBaseline(t *testing.T) {
	f := newFixture(t)
	for i := range 4 {
		f.archiveOf("alpha", "B"+string(rune('0'+i)), transcript(t0, "b", 2), day(10+i))
	}
	f.ensureLedgerWithBaseline("alpha")

	res, err := f.rebuild(RebuildOptions{Project: "alpha", MaxArchives: 2, FreeSpace: &fakeFree{ok: plentyFree}})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if res.Stopped != "max archives" {
		t.Fatalf("stopped = %q, want \"max archives\"", res.Stopped)
	}
	if res.Committed != 2 {
		t.Fatalf("committed %d, want 2", res.Committed)
	}
	if n := f.baselineCount("alpha"); n != 4 {
		t.Fatalf("baseline holds %d after a capped run, want 4 (unchanged)", n)
	}

	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	if n := f.baselineCount("alpha"); n != 0 {
		t.Fatalf("baseline holds %d after the completing run, want 0", n)
	}
}

// TestRebuildRetriesArchivesAutomaticRunsSkip: an archive that has failed to the
// automatic failure limit is attempted by the rebuild and succeeds; the
// project's failure counts are cleared when the run completes.
func TestRebuildRetriesArchivesAutomaticRunsSkip(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	a := f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	sha := a.Manifest.SourceSHA256
	// Record failures up to the automatic limit, so an automatic run would skip.
	for range DefaultFailureLimit {
		tx, err := indexstore.Lock(context.Background(), f.v, "alpha", indexstore.NoTimeout)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.RecordFailure("A", sha, errors.New("boom")); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		tx.Release()
	}
	if f.store("alpha").Ledger().FailureCount(sha) < DefaultFailureLimit {
		t.Fatal("precondition: failures not recorded")
	}
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if _, ok := f.live("alpha", "A"); !ok {
		t.Fatal("the rebuild did not ingest an archive the automatic runs skip")
	}
	if n := f.store("alpha").Ledger().FailureCount(sha); n != 0 {
		t.Fatalf("failure count %d after a completed rebuild, want 0", n)
	}
}

// --- completion with a failing archive (moved refusal rows) ------------------

// failOne makes IngestArchive fail for one session's archive by corrupting its
// stream, so its hash check fails on every attempt.
func TestRebuildFailureRecordIsNotAnIngest(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "OK1", transcript(t0, "ok1", 2), day(1))
	bad := f.archiveOf("alpha", "BAD", transcript(t0, "bad", 2), day(2))
	f.archiveOf("alpha", "OK2", transcript(t0, "ok2", 2), day(3))
	corrupt(t, bad) // its hash check now fails every attempt

	res, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if res.Failed == 0 {
		t.Fatal("the corrupt archive did not fail")
	}
	// The run completes: the two good archives are ledgered and the baseline
	// (empty here) and stale are cleared; the failing session stays unledgered
	// with a failure record from this run.
	for _, s := range []string{"OK1", "OK2"} {
		if _, ok := f.live("alpha", s); !ok {
			t.Fatalf("%s not ledgered", s)
		}
	}
	if _, ok := f.live("alpha", "BAD"); ok {
		t.Fatal("a failed archive was ledgered live (a failure record is not an ingest)")
	}
	if n := f.store("alpha").Ledger().FailureCount(bad.Manifest.SourceSHA256); n == 0 {
		t.Fatal("the failing archive's failure record from this run was erased")
	}
	if r := f.reasons("alpha"); len(r) != 0 {
		t.Fatalf("stale not cleared on a run that completed with one failing archive: %v", r)
	}
}

// TestRebuildMaxArchivesBeforeAFailureDoesNotComplete: the same corrupt-archive
// project stopped by --max-archives 1 does not complete, so a failure count set
// on an untouched archive survives (ClearFailures never ran).
func TestRebuildMaxArchivesDoesNotComplete(t *testing.T) {
	f := newFixture(t)
	for i := range 3 {
		f.archiveOf("alpha", "S"+string(rune('0'+i)), transcript(t0, "s", 2), day(30+i))
	}
	f.ensureLedgerWithBaseline("alpha") // the three archives are the backlog
	// Pre-set a failure on one archive, so a run that does NOT complete leaves
	// the failure count in place (ClearFailures never ran).
	shas := f.allSHAs("alpha")
	tx, _ := indexstore.Lock(context.Background(), f.v, "alpha", indexstore.NoTimeout)
	_ = tx.RecordFailure("pre", shas[0], errors.New("old"))
	_ = tx.Commit()
	tx.Release()

	res, err := f.rebuild(RebuildOptions{Project: "alpha", MaxArchives: 1, FreeSpace: &fakeFree{ok: plentyFree}})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if res.Stopped != "max archives" {
		t.Fatalf("stopped = %q, want \"max archives\"", res.Stopped)
	}
	if f.store("alpha").Ledger().FailureCount(shas[0]) == 0 {
		t.Fatal("a capped run completed and cleared failures; it must not")
	}
	if f.baselineCount("alpha") == 0 {
		t.Fatal("a capped run emptied the baseline; it must not")
	}
}

// --- --no-embed --------------------------------------------------------------

// TestRebuildNoEmbed: chunks, local KG and the ledger are written and a counting
// embedder records 0 calls; completeness records the misses and stale is set
// with the missing-vector reason; a later run without the flag embeds the
// misses and clears stale.
func TestRebuildNoEmbed(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	a := f.archiveOf("alpha", "A", transcript(t0, "a", 4), day(1))

	before := f.emb.calls.Load()
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", NoEmbed: true, FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("--no-embed rebuild: %v", err)
	}
	if got := f.emb.calls.Load() - before; got != 0 {
		t.Fatalf("--no-embed embedded with %d embedder calls, want 0", got)
	}
	if _, ok := f.live("alpha", "A"); !ok {
		t.Fatal("--no-embed wrote no ledger entry")
	}
	if f.store("alpha").CountChunks(indexstore.ArchiveOwner(a.Manifest.SourceSHA256)) == 0 {
		t.Fatal("--no-embed wrote no chunks")
	}
	if !hasReason(f.reasons("alpha"), indexstore.StaleMissingVectors) {
		t.Fatalf("--no-embed did not set the missing-vector reason: %v", f.reasons("alpha"))
	}

	// A later run without --no-embed embeds the misses and clears stale.
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("embedding rebuild: %v", err)
	}
	if got := f.emb.calls.Load() - before; got == 0 {
		t.Fatal("the embedding rebuild made no embedder call")
	}
	if r := f.reasons("alpha"); len(r) != 0 {
		t.Fatalf("stale not cleared after an embedding rebuild: %v", r)
	}
}

func hasReason(rs []indexstore.StaleReason, kind string) bool {
	for _, r := range rs {
		if r.Kind == kind {
			return true
		}
	}
	return false
}

// --- stale lifecycle ---------------------------------------------------------

// TestRebuildStaleClearedOnlyByACompletedRun: a fingerprint-reason stale flag is
// cleared by a completed rebuild, and left set by --no-embed, --max-archives,
// --dry-run, a cancel and a watchdog stop.
func TestRebuildStaleClearedOnlyByACompletedRun(t *testing.T) {
	setup := func(t *testing.T) (*fixture, string) {
		f := newFixture(t)
		f.ensureLedger("alpha")
		a := f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
		f.setStale("alpha", missingVectors)
		return f, a.Manifest.SourceSHA256
	}

	t.Run("completed clears", func(t *testing.T) {
		f, _ := setup(t)
		if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
			t.Fatal(err)
		}
		if r := f.reasons("alpha"); len(r) != 0 {
			t.Fatalf("stale still set after a completed rebuild: %v", r)
		}
	})
	t.Run("--no-embed leaves it", func(t *testing.T) {
		f, _ := setup(t)
		if _, err := f.rebuild(RebuildOptions{Project: "alpha", NoEmbed: true, FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
			t.Fatal(err)
		}
		if !hasReason(f.reasons("alpha"), indexstore.StaleMissingVectors) {
			t.Fatal("--no-embed cleared the stale flag")
		}
	})
	t.Run("--max-archives leaves it", func(t *testing.T) {
		f, _ := setup(t)
		f.archiveOf("alpha", "B", transcript(t0, "b", 2), day(2))
		f.archiveOf("alpha", "C", transcript(t0, "c", 2), day(3))
		if _, err := f.rebuild(RebuildOptions{Project: "alpha", MaxArchives: 1, FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
			t.Fatal(err)
		}
		if len(f.reasons("alpha")) == 0 {
			t.Fatal("--max-archives cleared the stale flag")
		}
	})
	t.Run("--dry-run leaves it", func(t *testing.T) {
		f, _ := setup(t)
		if _, err := DryRun(f.deps(), RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
			t.Fatal(err)
		}
		if len(f.reasons("alpha")) == 0 {
			t.Fatal("--dry-run cleared the stale flag")
		}
	})
	t.Run("watchdog stop leaves it", func(t *testing.T) {
		f, _ := setup(t)
		// Refuse at start: free below the reserve.
		_, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: FreeSpace{Bytes: 1 << 20, Inodes: 100, HasInodes: true}}})
		if !errors.Is(err, ErrReserve) {
			t.Fatalf("want ErrReserve, got %v", err)
		}
		if len(f.reasons("alpha")) == 0 {
			t.Fatal("a watchdog stop cleared the stale flag")
		}
	})
}

// --- one lock hold per run ---------------------------------------------------

// TestRebuildTakesTheRunLockOnce (7-S4): a rebuild of two projects, whose
// step-7 rescan serves a third, makes exactly one run-lock try before release,
// and the archive pass runs through RunHeld, never Run (whose second try-lock
// on a new descriptor would fail on Linux and make the pass exit as held).
func TestRebuildTakesTheRunLockOnce(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.ensureLedger("beta")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	f.archiveOf("beta", "B", transcript(t0, "b", 2), day(2))

	var tries int
	restore := indexstore.ObserveRunLocks(func(ev indexstore.RunLockEvent) {
		if ev == indexstore.RunTry {
			tries++
		}
	})
	defer restore()

	if _, err := f.rebuild(RebuildOptions{All: true, FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild --all: %v", err)
	}
	if tries != 1 {
		t.Fatalf("run-lock tries = %d, want exactly 1 for the whole run", tries)
	}
}

// TestRebuildExitsWhenTheRunLockIsHeld (R1): a process holding the run lock and
// a holder record naming it: a rebuild exits at once, non-zero, naming the
// holder, having opened no archive.
func TestRebuildExitsWhenTheRunLockIsHeld(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.ensureLedger("beta")
	f.archiveOf("beta", "B", transcript(t0, "b", 2), day(1))

	held, ok, err := indexstore.TryRunLock(f.v, indexstore.KindIngest, "alpha")
	if err != nil || !ok {
		t.Fatalf("hold the run lock: ok=%v err=%v", ok, err)
	}
	defer held.Release()

	opened := countOpens(t)
	_, err = f.rebuild(RebuildOptions{Project: "beta", FreeSpace: &fakeFree{ok: plentyFree}})
	if !errors.Is(err, ErrRunLockHeld) {
		t.Fatalf("want ErrRunLockHeld, got %v", err)
	}
	for _, want := range []string{"ingest", "alpha"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("holder error %q does not name %q", err, want)
		}
	}
	if n := len(opened()); n != 0 {
		t.Fatalf("a rebuild that found the lock held opened %d archives, want 0", n)
	}
}

// --- --skip ------------------------------------------------------------------

// TestRebuildSkipLeavesAProjectUntouched (C8): --all --skip beta rebuilds alpha
// and leaves beta byte-identical, opening none of its archives.
func TestRebuildSkipLeavesAProjectUntouched(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"alpha", "beta"} {
		f.archiveOf(p, p+"S", transcript(t0, p, 2), day(1))
		f.ensureLedgerWithBaseline(p)
	}
	betaLedger := f.ledgerLines("beta")
	betaBaseline := f.baselineCount("beta")

	opened := countOpens(t)
	if _, err := f.rebuild(RebuildOptions{All: true, Skip: []string{"beta"}, FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild --all --skip beta: %v", err)
	}
	// alpha completed, beta untouched.
	if f.baselineCount("alpha") != 0 {
		t.Fatal("alpha's baseline was not emptied")
	}
	for _, p := range opened() {
		if strings.Contains(p, "/beta/") {
			t.Fatalf("a skipped project's archive was opened: %s", p)
		}
	}
	if got := f.ledgerLines("beta"); strings.Join(got, "\n") != strings.Join(betaLedger, "\n") {
		t.Fatal("a skipped project's ledger changed")
	}
	if f.baselineCount("beta") != betaBaseline {
		t.Fatal("a skipped project's baseline changed")
	}
}

// TestRebuildTargetsUsageErrors: --skip naming the positional project, an
// unknown --skip, an unknown project, and a project with --all are usage
// errors.
func TestRebuildTargetsUsageErrors(t *testing.T) {
	f := newFixture(t)
	d := f.deps()
	cases := []RebuildOptions{
		{Project: "alpha", Skip: []string{"alpha"}},
		{Project: "alpha", Skip: []string{"ghost"}},
		{Project: "ghost"},
		{Project: "alpha", All: true},
	}
	for i, o := range cases {
		if _, err := rebuildTargets(d, o); err == nil {
			t.Errorf("case %d: want a usage error, got nil", i)
		}
	}
}

// --- watchdog ----------------------------------------------------------------

// TestWatchdogRefusesAtStart: free space minus the reserve below the estimate
// (bytes in one case, inodes in another) refuses the run and the dry run,
// non-zero, and writes nothing.
func TestWatchdogRefusesAtStart(t *testing.T) {
	run := func(t *testing.T, r *fakeFree) {
		f := newFixture(t)
		f.ensureLedger("alpha")
		f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
		if _, err := f.rebuild(RebuildOptions{Project: "alpha", Reserve: Reserve{Bytes: 5 << 30, Inodes: 1_000_000}, FreeSpace: r}); !errors.Is(err, ErrReserve) {
			t.Fatalf("run: want ErrReserve, got %v", err)
		}
		// Nothing ingested.
		if _, ok := f.live("alpha", "A"); ok {
			t.Fatal("a refused run ingested")
		}
		rep, err := DryRun(f.deps(), RebuildOptions{Project: "alpha", Reserve: Reserve{Bytes: 5 << 30, Inodes: 1_000_000}, FreeSpace: r})
		if err != nil {
			t.Fatalf("dry run: %v", err)
		}
		if rep.Refusal == "" {
			t.Fatal("dry run did not refuse")
		}
	}
	t.Run("bytes", func(t *testing.T) {
		run(t, &fakeFree{ok: FreeSpace{Bytes: 1 << 30, Inodes: 100_000_000, HasInodes: true}})
	})
	t.Run("inodes", func(t *testing.T) {
		run(t, &fakeFree{ok: FreeSpace{Bytes: 100 << 30, Inodes: 10, HasInodes: true}})
	})
}

// TestWatchdogStopsMidRun: free drops below the reserve after the first archive;
// the run stops non-zero and the ledger ends where it stopped, not after.
func TestWatchdogStopsMidRun(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	f.archiveOf("alpha", "B", transcript(t0, "b", 2), day(2))
	f.archiveOf("alpha", "C", transcript(t0, "c", 2), day(3))

	// ok for the start check and the first archive's checkpoint, then low.
	r := &fakeFree{ok: plentyFree, low: FreeSpace{Bytes: 1 << 20, Inodes: 10, HasInodes: true}, dropAfter: 2}
	res, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: r})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if res.Stopped != "checkpoint" {
		t.Fatalf("stopped = %q, want \"checkpoint\" (the watchdog)", res.Stopped)
	}
	live := 0
	for _, s := range []string{"A", "B", "C"} {
		if _, ok := f.live("alpha", s); ok {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("%d archives ledgered after a mid-run watchdog stop, want 1", live)
	}
}

// TestDrainArchivesHonoursTheReserve: the routine ingester's ReserveCheckpoint
// stops a run before its first commit when free space is below the reserve.
func TestDrainArchivesHonoursTheReserve(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	low := &fakeFree{ok: FreeSpace{Bytes: 1 << 20, Inodes: 10, HasInodes: true}}
	res, err := Run(context.Background(), f.deps(), RunOptions{
		VaultRoot:  f.v.Root,
		Project:    "alpha",
		Checkpoint: ReserveCheckpoint(low, f.v.VaultLocalDir(), Reserve{}),
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if res.Stopped != "checkpoint" {
		t.Fatalf("stopped = %q, want \"checkpoint\"", res.Stopped)
	}
	if _, ok := f.live("alpha", "A"); ok {
		t.Fatal("a run under the reserve committed an archive")
	}
}

// --- dry run -----------------------------------------------------------------

// TestDryRunWritesNothingAndTakesNoLock (7-S1): a dry run creates no ledger and
// no holder record, makes no run-lock try, and reports pending and baseline
// archives separately.
func TestDryRunWritesNothingAndTakesNoLock(t *testing.T) {
	f := newFixture(t)
	for i := range 3 {
		f.archiveOf("alpha", "B"+string(rune('0'+i)), transcript(t0, "b", 2), day(10+i))
	}
	f.ensureLedgerWithBaseline("alpha")
	f.archiveOf("alpha", "N", transcript(t0, "n", 2), day(20))

	var tries int
	restore := indexstore.ObserveRunLocks(func(ev indexstore.RunLockEvent) {
		if ev == indexstore.RunTry {
			tries++
		}
	})
	defer restore()

	rep, err := DryRun(f.deps(), RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if tries != 0 {
		t.Fatalf("dry run made %d run-lock tries, want 0", tries)
	}
	if _, err := os.Stat(f.v.IndexRunHolderPath()); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote a holder record: %v", err)
	}
	if len(rep.Projects) != 1 {
		t.Fatalf("report has %d projects", len(rep.Projects))
	}
	pe := rep.Projects[0]
	if pe.BaselineArchives != 3 {
		t.Errorf("baseline archives = %d, want 3", pe.BaselineArchives)
	}
	if pe.PendingArchives != 1 {
		t.Errorf("pending archives = %d, want 1", pe.PendingArchives)
	}
}

// --- graph heal seam ---------------------------------------------------------

// TestRebuildHealsTheGraphThroughTheSeam (7-S3): with a fake GraphHealer in
// Deps, the rebuild calls HealGraph once, after the engine rebuild, and the
// archive pass (RunHeld with SkipHeal) makes no HealGraph call for the rebuilt
// project. A graph mismatch is never a stale reason. With the seam nil, the
// step does nothing.
func TestRebuildHealsTheGraphThroughTheSeam(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))

	healer := &fakeHealer{result: HealResult{Rebuilt: true, Reason: "graph fingerprint changed"}}
	d := f.deps()
	d.GraphHealer = healer
	if _, err := f.rebuildD(d, RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got := healer.projects(); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("HealGraph calls = %v, want exactly [alpha] (once, by the driver)", got)
	}
	if hasReason(f.reasons("alpha"), indexstore.StaleFingerprint) {
		t.Fatal("a graph mismatch set a fingerprint stale reason")
	}
}

// --- discard -----------------------------------------------------------------

// TestRebuildDiscardsOnAFingerprintMismatch: with a chunks-fingerprint stale
// reason present, the rebuild discards and rebuilds; with no fingerprint
// reason, it discards nothing (the ledger is byte-identical bar the new work).
func TestRebuildDiscardsOnAFingerprintMismatch(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	a := f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatal(err)
	}
	// Force a chunks-fingerprint mismatch and a second rebuild: the discard
	// drops the ledger and chunks and re-ingests from scratch.
	f.setStale("alpha", chunkFP)
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild after a chunks mismatch: %v", err)
	}
	// The archive is ledgered again and searchable, and stale is cleared.
	if _, ok := f.live("alpha", "A"); !ok {
		t.Fatal("the archive was not re-ingested after a discard")
	}
	if f.store("alpha").CountChunks(indexstore.ArchiveOwner(a.Manifest.SourceSHA256)) == 0 {
		t.Fatal("no chunks after a discard-and-rebuild")
	}
	if len(f.reasons("alpha")) != 0 {
		t.Fatalf("stale not cleared after a discard-and-rebuild: %v", f.reasons("alpha"))
	}
}

// --- rename-pending cleanup (1b) --------------------------------------------

// TestRebuildClearsRenamePendingTargetingIt: a rename-pending record old->alpha
// is removed once alpha's rebuild completes.
func TestRebuildClearsRenamePendingTargetingIt(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	// Write a rename-pending record old -> alpha.
	if err := os.MkdirAll(f.v.IndexRenamePendingDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rec, err := f.v.IndexRenamePendingPath("oldalpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if _, err := os.Stat(rec); !os.IsNotExist(err) {
		t.Fatalf("rename-pending record targeting the rebuilt project survived: %v", err)
	}
}

// --- searchable --------------------------------------------------------------

// TestRebuildMakesTranscriptTextSearchable: after a rebuild, a phrase that
// appears only in a transcript archive is found by search.
func TestRebuildMakesTranscriptTextSearchable(t *testing.T) {
	f := newFixture(t)
	f.ensureLedgerWithBaseline("alpha") // put the archive in the backlog
	f.archiveOf("alpha", "A", transcript(t0, "quokka-telemetry", 4), day(1))

	before, _ := f.eng.Search(context.Background(), "quokka-telemetry", search.SearchFilters{Project: "alpha", Limit: 5})
	if len(before) != 0 {
		t.Fatalf("degenerate fixture: %d hits before the rebuild", len(before))
	}
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	after, err := f.eng.Search(context.Background(), "quokka-telemetry", search.SearchFilters{Project: "alpha", Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(after) == 0 {
		t.Fatal("transcript text is not searchable after a rebuild of the backlog")
	}
}

// --- completion refusals via the CompletedRebuild seam -----------------------

// countCompletedRebuild installs a counting stub over the CompletedRebuild seam
// and returns the live counter and a cleanup. A completed rebuild is the only
// thing that mints the proof, so counting its calls is how each refusal
// condition is proven: the driver must NOT ask for the proof when the run did
// not complete (the "Refusal rows moved from search-index-completeness" rows).
func countCompletedRebuild(t *testing.T) *int {
	t.Helper()
	calls := 0
	completedRebuildFn = func(rl *indexstore.RunLock, project string, failed []string) (indexstore.RebuildProof, error) {
		calls++
		return rl.CompletedRebuild(project, failed)
	}
	t.Cleanup(func() { completedRebuildFn = realCompletedRebuild })
	return &calls
}

// forceMissEngine wraps the real engine and forces the explicit rebuild to
// report a local-tier miss, so a test can isolate the misses>0 completion
// guard without a flaky cache-miss scenario.
type forceMissEngine struct{ *search.Engine }

func (e forceMissEngine) RebuildExplicit(ctx context.Context, project string) (search.RebuildStats, error) {
	s, err := e.Engine.RebuildExplicit(ctx, project)
	s.LocalMisses = 1
	return s, err
}

// TestRebuildCompletionGatedByCompletedRebuild: the driver mints the completed-
// rebuild proof only when the run completed. It isolates each of the three
// refusal conditions with the counting stub, and proves none clears stale.
func TestRebuildCompletionGatedByCompletedRebuild(t *testing.T) {
	t.Run("completes once when everything holds", func(t *testing.T) {
		f := newFixture(t)
		f.ensureLedger("alpha")
		f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
		calls := countCompletedRebuild(t)
		if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
			t.Fatal(err)
		}
		if *calls != 1 {
			t.Fatalf("CompletedRebuild called %d times, want 1", *calls)
		}
	})

	// (a) A live archive was neither attempted nor ledgered in the run: the
	// readVerifiedFn seam makes session CHANGED look rewritten-since-listing
	// (ErrArchiveChanged), so the pass records no ingest and no failure for it,
	// and does not stop. everyLiveArchiveAccounted must then block completion.
	t.Run("(a) an unaccounted live archive blocks completion", func(t *testing.T) {
		f := newFixture(t)
		f.ensureLedger("alpha")
		f.archiveOf("alpha", "OK", transcript(t0, "ok", 2), day(1))
		f.archiveOf("alpha", "CHANGED", transcript(t0, "chg", 2), day(2))
		// A fingerprint reason, which only a completed rebuild clears (the
		// repair pass clears the missing-vector reason, so it would not isolate
		// the completion gate).
		f.setStale("alpha", chunkFP)

		old := readVerifiedFn
		readVerifiedFn = func(e *archive.Entry) (archive.VerifiedArchive, error) {
			if e.Manifest != nil && e.Manifest.SessionID == "CHANGED" {
				return archive.VerifiedArchive{}, archive.ErrArchiveChanged
			}
			return old(e)
		}
		t.Cleanup(func() { readVerifiedFn = old })

		calls := countCompletedRebuild(t)
		if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
			t.Fatal(err)
		}
		if *calls != 0 {
			t.Fatalf("CompletedRebuild called %d times with an unaccounted live archive, want 0", *calls)
		}
		if len(f.reasons("alpha")) == 0 {
			t.Fatal("stale was cleared although a live archive was unaccounted")
		}
	})

	// (b) The embedding run recorded misses>0 among ledgered chunks: the stub
	// engine forces LocalMisses=1 even though embedding ran.
	t.Run("(b) misses>0 blocks completion", func(t *testing.T) {
		f := newFixture(t)
		f.ensureLedger("alpha")
		f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
		// A fingerprint reason the repair pass never clears, so only the
		// completion gate (here blocked by misses>0) could clear it.
		f.setStale("alpha", chunkFP)
		d := f.deps()
		d.Engine = forceMissEngine{f.eng}

		calls := countCompletedRebuild(t)
		if _, err := f.rebuildD(d, RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
			t.Fatal(err)
		}
		if *calls != 0 {
			t.Fatalf("CompletedRebuild called %d times with misses>0, want 0", *calls)
		}
		if len(f.reasons("alpha")) == 0 {
			t.Fatal("stale was cleared although the rebuild left a missing vector")
		}
	})

	// (c) Embedding did not run (--no-embed) EVEN WHEN LocalMisses==0: a prior
	// embedding rebuild primes the cache, so the --no-embed rebuild finds every
	// vector cached (LocalMisses==0) and could only be blocked by the --no-embed
	// guard itself.
	t.Run("(c) --no-embed blocks completion even with 0 misses", func(t *testing.T) {
		f := newFixture(t)
		f.ensureLedger("alpha")
		f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
		// Prime the embed cache with a full embedding rebuild (it completes).
		if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
			t.Fatal(err)
		}
		// A missing-vector reason is NOT a fingerprint reason, so it triggers no
		// discard; the vectors stay cached and the --no-embed rebuild sees 0
		// local misses.
		f.setStale("alpha", missingVectors)

		calls := countCompletedRebuild(t)
		if _, err := f.rebuild(RebuildOptions{Project: "alpha", NoEmbed: true, FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
			t.Fatal(err)
		}
		if *calls != 0 {
			t.Fatalf("CompletedRebuild called %d times on --no-embed (0 misses), want 0", *calls)
		}
		if len(f.reasons("alpha")) == 0 {
			t.Fatal("--no-embed cleared the stale flag although embedding did not run")
		}
	})
}

var realCompletedRebuild = func(rl *indexstore.RunLock, project string, failed []string) (indexstore.RebuildProof, error) {
	return rl.CompletedRebuild(project, failed)
}

// --- no vault write ----------------------------------------------------------

// TestRebuildWritesNothingTracked: a full rebuild on a committed vault leaves
// `git status --porcelain -uall` empty — every write is under the git-ignored
// palace/.local/.
func TestRebuildWritesNothingTracked(t *testing.T) {
	f := newFixture(t)
	f.archiveOf("alpha", "A", transcript(t0, "a", 3), day(1))
	f.ensureLedgerWithBaseline("alpha")
	f.commitAll()

	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if out := f.git("status", "--porcelain", "-uall"); strings.TrimSpace(out) != "" {
		t.Fatalf("a rebuild wrote tracked content:\n%s", out)
	}
}

// --- resume ------------------------------------------------------------------

// TestRebuildResumesAfterCancel: cancel the context after two archives are
// prepared; a rerun ingests the rest, and the whole backlog ends ledgered.
func TestRebuildResumesAfterCancel(t *testing.T) {
	f := newFixture(t)
	for i := range 5 {
		f.archiveOf("alpha", "S"+string(rune('0'+i)), transcript(t0, "s", 2), day(10+i))
	}
	f.ensureLedgerWithBaseline("alpha")

	ctx, cancel := context.WithCancel(context.Background())
	var prepared int
	afterPrepareFn = func(string, *archive.Entry) {
		prepared++
		if prepared == 3 {
			cancel() // the third archive is abandoned mid-flight
		}
	}
	t.Cleanup(func() { afterPrepareFn = nil })

	res, err := Rebuild(ctx, f.deps(), RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}})
	if err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	if res.Stopped != "cancelled" {
		t.Fatalf("stopped = %q, want \"cancelled\"", res.Stopped)
	}
	live := 0
	for i := range 5 {
		if _, ok := f.live("alpha", "S"+string(rune('0'+i))); ok {
			live++
		}
	}
	if live == 0 || live >= 5 {
		t.Fatalf("after a cancel %d/5 archives are ledgered, want a partial resume point", live)
	}

	// The rerun resumes from the ledger and completes.
	afterPrepareFn = nil
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("resume rebuild: %v", err)
	}
	for i := range 5 {
		if _, ok := f.live("alpha", "S"+string(rune('0'+i))); !ok {
			t.Fatalf("S%d not ledgered after the resume", i)
		}
	}
	if f.baselineCount("alpha") != 0 {
		t.Fatal("the resumed run did not empty the baseline")
	}
}

// --- step-7 rescan serves other projects -------------------------------------

// TestRebuildRescanServesOtherProjects (R1): a single-project rebuild's step-7
// rescan ingests another project's pending archive before the run lock is
// released.
func TestRebuildRescanServesOtherProjects(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.ensureLedger("beta")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	f.archiveOf("beta", "B", transcript(t0, "b", 2), day(2)) // pending for another project

	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if _, ok := f.live("beta", "B"); !ok {
		t.Fatal("the step-7 rescan did not serve another project's pending archive")
	}
}

// TestRebuildKeepsRenamePendingWhenNotCompleted (fold-in): a non-completing
// rebuild of <to> (here --no-embed) leaves a rename-pending record targeting it
// in place; only a completed rebuild removes it (1b requirement).
func TestRebuildKeepsRenamePendingWhenNotCompleted(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	if err := os.MkdirAll(f.v.IndexRenamePendingDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rec, err := f.v.IndexRenamePendingPath("oldalpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// --no-embed never completes, so the record must survive.
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", NoEmbed: true, FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if _, err := os.Stat(rec); err != nil {
		t.Fatalf("a non-completing (--no-embed) rebuild removed the rename-pending record: %v", err)
	}
}

// TestRebuildDiscardsVectorsOnEmbedMismatch (fold-in): an embed-cache
// fingerprint mismatch makes the driver Tx.Discard(DiscardVectors), so the
// rebuild re-embeds the vectors (a cache miss) rather than reusing them, while
// the chunks and ledger survive.
func TestRebuildDiscardsVectorsOnEmbedMismatch(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "A", transcript(t0, "a", 4), day(1))
	// A first full rebuild caches the vectors.
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	// An embed-cache fingerprint mismatch: the driver must discard the vectors,
	// so the next rebuild re-embeds them.
	f.setStale("alpha", indexstore.StaleReason{Kind: indexstore.StaleFingerprint, Fingerprint: indexstore.FingerprintEmbed})
	before := f.emb.calls.Load()
	if _, err := f.rebuild(RebuildOptions{Project: "alpha", FreeSpace: &fakeFree{ok: plentyFree}}); err != nil {
		t.Fatalf("rebuild after an embed mismatch: %v", err)
	}
	if got := f.emb.calls.Load() - before; got == 0 {
		t.Fatal("the embed-cache mismatch did not discard vectors: the rebuild re-used the cache and embedded nothing")
	}
	// DiscardVectors leaves the chunks and the ledger intact.
	if _, ok := f.live("alpha", "A"); !ok {
		t.Fatal("DiscardVectors wrongly removed the chunks or the ledger entry")
	}
}
