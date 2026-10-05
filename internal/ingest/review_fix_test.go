// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
)

// Tests for the code review's required fixes 1-6 and its fold-ins (task
// pending-archive-ingester-and-per-archive-commit-step). Repros 1-5 are the
// reviewer's, adapted.

// lateHealer creates archive X, and runs a losing trigger for it, the first
// time alpha is healed: after alpha's pass, before the holder's last rescan.
type lateHealer struct {
	f    *fixture
	done atomic.Bool
	lost bool
}

func (h *lateHealer) HealGraph(_ context.Context, _, project string) (HealResult, error) {
	if project != "alpha" || h.done.Swap(true) {
		return HealResult{}, nil
	}
	x := h.f.archiveOf("alpha", "X", transcript(t0, "x", 2), day(4))
	r, err := Run(context.Background(), h.f.deps(), RunOptions{VaultRoot: h.f.v.Root, Project: "alpha", First: x.Manifest.SourceSHA256})
	h.lost = err == nil && r.LockHeld
	return HealResult{}, nil
}

// TestRunServesAnArchiveThatArrivesDuringTheRun (fix 1, repro 1): an archive
// created, with its trigger losing the run lock, after its project's pass and
// before the holder's last rescan is ingested by the holder's run.
func TestRunServesAnArchiveThatArrivesDuringTheRun(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "S", transcript(t0, "s", 2), day(1))
	h := &lateHealer{f: f}
	d := f.deps()
	d.GraphHealer = h
	if _, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if !h.lost {
		t.Fatal("precondition: the late trigger must lose the run lock")
	}
	if _, ok := f.live("alpha", "X"); !ok {
		t.Fatal("lost trigger: the archive that arrived during the run was not ingested by it")
	}
}

// TestRunAttemptsAFailingHashlessArchiveOnce (fix 2, repro 2): an explicit
// run reads a corrupt archive with no source_sha256 once, and ends.
func TestRunAttemptsAFailingHashlessArchiveOnce(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	e := f.archiveOf("alpha", "S", transcript(t0, "s", 2), day(1))
	f.archiveOf("alpha", "T", transcript(t0, "t", 2), day(2))
	m := *e.Manifest
	m.SourceSHA256 = ""
	if err := archive.WriteManifest(f.v.Root, e.ManifestPath, &m); err != nil {
		t.Fatal(err)
	}
	corrupt(t, e)
	var opens atomic.Int64
	old := readVerifiedFn
	readVerifiedFn = func(x *archive.Entry) (archive.VerifiedArchive, error) {
		if x.ArchivePath == e.ArchivePath && opens.Add(1) > 20 {
			return archive.VerifiedArchive{}, context.Canceled // the loop guard
		}
		return old(x)
	}
	t.Cleanup(func() { readVerifiedFn = old })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := Run(ctx, f.deps(), RunOptions{VaultRoot: f.v.Root, Project: "alpha", Explicit: true}); err != nil {
		t.Fatal(err)
	}
	if n := opens.Load(); n != 1 {
		t.Fatalf("the failing hashless archive was read %d times in one run, want once", n)
	}
	if _, ok := f.live("alpha", "T"); !ok {
		t.Fatal("the other archive was not ingested")
	}
}

// TestRunRevertsAStuckSupersede (fix 3, repro 3): a supersede from A towards
// B crashed, and B vanished with nothing newer: the next run makes A live
// again, with its own chunks and KG records, and nothing is owned by B.
func TestRunRevertsAStuckSupersede(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory")
	}
	f := newFixture(t)
	f.ensureLedger("alpha")
	a := f.archiveOf("alpha", "S", transcript(t0, "a", 2), day(1))
	f.run(RunOptions{})
	aKG := len(f.store("alpha").KG(true))
	b := f.archiveOf("alpha", "S", transcript(t0, "b", 3), day(3))
	cache := f.cacheDir("alpha")
	if err := os.Chmod(cache, 0o555); err != nil {
		t.Fatal(err)
	}
	f.run(RunOptions{})
	os.Chmod(cache, 0o755)
	if s, _ := f.store("alpha").Ledger().Session("S"); s.State != indexstore.StateSuperseding {
		t.Fatalf("precondition: %+v", s)
	}
	os.Remove(b.ArchivePath)
	os.Remove(b.ManifestPath)
	f.run(RunOptions{})
	st := f.store("alpha")
	s, _ := st.Ledger().Session("S")
	if s.State != indexstore.StateLive || s.SHA != a.Manifest.SourceSHA256 {
		t.Fatalf("session %+v, want A live again", s)
	}
	owner := indexstore.ArchiveOwner(a.Manifest.SourceSHA256)
	n := 0
	for _, k := range st.KG(true) {
		if slices.Contains(k.Owners, owner) {
			n++
		}
	}
	if n == 0 || n != aKG {
		t.Fatalf("A owns %d live KG records, want its %d", n, aKG)
	}
	if st.CountChunks(owner) == 0 || st.CountChunks(indexstore.ArchiveOwner(b.Manifest.SourceSHA256)) != 0 {
		t.Fatal("A must own its chunks again and B nothing")
	}
}

// poisonEmb fails every embed of alpha's archive text while on.
type poisonEmb struct {
	embedder.Embedder
	on atomic.Bool
}

func (p *poisonEmb) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if p.on.Load() {
		for _, s := range texts {
			if strings.Contains(s, " of aA") {
				return nil, errors.New("poison")
			}
		}
	}
	return p.Embedder.EmbedBatch(ctx, texts)
}

// TestRunRepairDoesNotStarveOtherProjects (fix 4, repro 4): alpha's repair
// always fails; beta's pulled archive is still ingested on the next run.
func TestRunRepairDoesNotStarveOtherProjects(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.ensureLedger("beta")
	var es []*archive.Entry
	for i, s := range []string{"A1", "A2", "A3"} {
		es = append(es, f.archiveOf("alpha", s, transcript(t0, "a"+s, 2), day(i)))
	}
	pe := &poisonEmb{Embedder: f.emb}
	d := f.deps()
	d.Embedder = pe
	if _, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha", Budget: RunBudget{Archives: 10}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		f.dropVectors("alpha", indexstore.ArchiveOwner(e.Manifest.SourceSHA256), 1)
	}
	pe.on.Store(true)
	f.archiveOf("beta", "B", transcript(t0, "b", 2), day(5))
	if _, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.live("beta", "B"); !ok {
		t.Fatal("starved: alpha's failing repair spent the budget before beta's pending archive")
	}
}

// TestRunWarnsOncePerArchivePerRun (fix 5, repro 5): a run whose passes
// repeat (another project still had work) writes the no-hash and the size-cap
// Warns once each, not once per pass.
func TestRunWarnsOncePerArchivePerRun(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.ensureLedger("beta")
	e := f.archiveOf("alpha", "S", transcript(t0, "s", 2), day(1))
	big := f.archiveOf("alpha", "Big", transcript(t0, "big", 6), day(2))
	f.archiveOf("alpha", "T", transcript(t0, "t", 2), day(3))
	f.archiveOf("beta", "U", transcript(t0, "u", 2), day(4))
	m := *e.Manifest
	m.SourceSHA256 = ""
	archive.WriteManifest(f.v.Root, e.ManifestPath, &m)
	logs := captureLogs(t)
	res := f.run(RunOptions{Budget: RunBudget{Archives: 10}, MaxSourceBytes: big.Manifest.SourceBytes - 1})
	if res.Committed < 2 {
		t.Fatalf("precondition: %+v", res)
	}
	if n := len(logs.warns("no source_sha256")); n != 1 {
		t.Fatalf("%d no-hash Warns in one run, want 1", n)
	}
	if n := len(logs.warns("larger than an automatic run takes")); n != 1 {
		t.Fatalf("%d size-cap Warns in one run, want 1", n)
	}
}

// TestTidyLeavesANewerArchiveItDidNotIngestPending (fix 6, M1): a newer
// archive of a ledgered session that this run left alone (here: over the
// automatic size cap) is not recorded superseded by the run's tidy, so the
// explicit rebuild still ingests it.
func TestTidyLeavesANewerArchiveItDidNotIngestPending(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "S", transcript(t0, "a", 2), day(1))
	f.run(RunOptions{})
	b := f.archiveOf("alpha", "S", transcript(t0, "b", 6), day(5))
	f.run(RunOptions{MaxSourceBytes: b.Manifest.SourceBytes - 1})
	if f.store("alpha").Ledger().Superseded(b.Manifest.SourceSHA256) {
		t.Fatal("tidy recorded a newer archive it did not ingest superseded")
	}
	f.run(RunOptions{Explicit: true})
	if s, _ := f.live("alpha", "S"); s.SHA != b.Manifest.SourceSHA256 {
		t.Fatalf("session %+v, want the newer archive live", s)
	}
}

// TestRunDoesNotChargeAnArchiveCommittedUnderTheLock (fix 6, R6 at the run
// level): another process commits an archive between this run's embed and its
// lock. This run's commit writes nothing (its vectors are not written: the
// other process embedded none), and it is charged no budget unit and no
// progress, so a budget of one still commits the next archive.
func TestRunDoesNotChargeAnArchiveCommittedUnderTheLock(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	first := f.archiveOf("alpha", "S0", transcript(t0, "s0", 2), day(2))
	f.archiveOf("alpha", "S1", transcript(t0, "s1", 2), day(1))
	var progress []int
	beforeLockFn = func(p string, e *archive.Entry) {
		if e.ArchivePath == first.ArchivePath && progress == nil {
			progress = []int{}
			if _, err := IngestArchive(context.Background(), f.deps(), p, e, IngestOptions{Embed: false}); err != nil {
				t.Fatal(err)
			}
			return
		}
		h, _ := indexstore.ReadHolder(f.v)
		done := 0
		if h.Progress != nil {
			done = h.Progress.Done
		}
		progress = append(progress, done)
	}
	t.Cleanup(func() { beforeLockFn = nil })
	res := f.run(RunOptions{Budget: RunBudget{Archives: 1}})
	if res.Committed != 1 {
		t.Fatalf("committed %d, want the second archive within a budget of one", res.Committed)
	}
	if _, ok := f.live("alpha", "S1"); !ok {
		t.Fatal("S1 not live")
	}
	if len(progress) == 0 || progress[len(progress)-1] != 0 {
		t.Fatalf("progress before S1's commit %v, want 0", progress)
	}
	for _, c := range f.store("alpha").Chunks(true) {
		if c.SourceRef == "S0" {
			if _, hit, _ := f.eng.CachedVector("alpha", c.ID); hit {
				t.Fatal("the under-lock path wrote S0's vectors after finding it ledgered")
			}
		}
	}
}

// TestRunCreatesEveryLedgerUpFront (fold-in): a budget stop in the first
// project does not leave the second without a ledger: beta's baseline is
// fixed at the run's start, so an archive beta gains afterwards is new, not
// backlog.
func TestRunCreatesEveryLedgerUpFront(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "A1", transcript(t0, "a1", 2), day(1))
	f.archiveOf("alpha", "A2", transcript(t0, "a2", 2), day(2))
	f.archiveOf("alpha", "A3", transcript(t0, "a3", 2), day(3))
	f.archiveOf("beta", "Old", transcript(t0, "old", 2), day(1))
	if res := f.run(RunOptions{Budget: RunBudget{Archives: 1}}); res.Stopped != "budget" {
		t.Fatalf("precondition: the budget must stop the run in alpha: %+v", res)
	}
	if !f.store("beta").Ledger().Exists() {
		t.Fatal("beta's ledger was not created up front")
	}
	n := f.archiveOf("beta", "New", transcript(t0, "new", 2), day(4))
	if f.store("beta").Ledger().InBaseline(n.Manifest.SourceSHA256) {
		t.Fatal("an archive beta gained after the run started is backlog")
	}
	f.run(RunOptions{Project: "beta"})
	if _, ok := f.live("beta", "New"); !ok {
		t.Fatal("beta's new archive was not ingested")
	}
}

// staleErrEngine fails Stale for one project.
type staleErrEngine struct {
	CacheEngine
	bad string
}

func (s staleErrEngine) Stale(p string) (bool, []indexstore.StaleReason, error) {
	if p == s.bad {
		return false, nil, errors.New("unreadable")
	}
	return s.CacheEngine.Stale(p)
}

// TestRunIsolatesAProjectItCannotRead (fold-in): an error reading one
// project's state is a Warn, and the other projects are still served.
func TestRunIsolatesAProjectItCannotRead(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.ensureLedger("beta")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	f.archiveOf("beta", "B", transcript(t0, "b", 2), day(2))
	d := f.deps()
	d.Engine = staleErrEngine{CacheEngine: f.eng, bad: "alpha"}
	logs := captureLogs(t)
	if _, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha"}); err != nil {
		t.Fatalf("one unreadable project stopped the run: %v", err)
	}
	if _, ok := f.live("beta", "B"); !ok {
		t.Fatal("the other project was not served")
	}
	if len(logs.warns("cannot read the project's stale state")) != 1 {
		t.Fatalf("warns %+v", logs.warns(""))
	}
}

// TestRunCancelledIsNotAFailure (fold-in): a run whose context is cancelled
// mid-archive records no failure and stops.
func TestRunCancelledIsNotAFailure(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	e := f.archiveOf("alpha", "S", transcript(t0, "s", 2), day(1))
	ctx, cancel := context.WithCancel(context.Background())
	afterPrepareFn = func(string, *archive.Entry) { cancel() }
	t.Cleanup(func() { afterPrepareFn = nil })
	res, err := Run(ctx, f.deps(), RunOptions{VaultRoot: f.v.Root, Project: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 || res.Stopped != "cancelled" || f.store("alpha").Ledger().FailureCount(e.Manifest.SourceSHA256) != 0 {
		t.Fatalf("result %+v, failures %d; want a cancelled stop and no failure", res, f.store("alpha").Ledger().FailureCount(e.Manifest.SourceSHA256))
	}
}

// TestRunLogsALedgerThatDisappeared (fold-in): a ledger removed mid-run (a
// discard) ends the project's pass with a Warn, not silently.
func TestRunLogsALedgerThatDisappeared(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "S", transcript(t0, "s", 2), day(1))
	beforeLockFn = func(string, *archive.Entry) {
		beforeLockFn = nil
		os.Remove(filepath.Join(f.v.Root, "palace", ".local", "index", "alpha", "ledger.jsonl"))
	}
	t.Cleanup(func() { beforeLockFn = nil })
	logs := captureLogs(t)
	f.run(RunOptions{})
	if len(logs.warns("ledger disappeared")) != 1 {
		t.Fatalf("warns %+v", logs.warns(""))
	}
}

// TestRunDoesNotHealAProjectFoundStaleMidRun (fold-in): a project found stale
// at its commit (another process wrote another recipe) is not healed.
func TestRunDoesNotHealAProjectFoundStaleMidRun(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "S", transcript(t0, "s", 2), day(1))
	beforeLockFn = func(string, *archive.Entry) {
		beforeLockFn = nil
		os.WriteFile(filepath.Join(f.v.Root, "palace", ".local", "index", "alpha", "chunks.fingerprint"), []byte("other"), 0o644)
	}
	t.Cleanup(func() { beforeLockFn = nil })
	h := &recordingHealer{f: f}
	d := f.deps()
	d.GraphHealer = h
	if _, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(h.calls, "alpha") {
		t.Fatalf("a project found stale mid-run was healed: %v", h.calls)
	}
}

// TestRunListsOnlyVisitedProjects (fold-in): a project with no archives and
// no ledger is not reported as visited.
func TestRunListsOnlyVisitedProjects(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "S", transcript(t0, "s", 2), day(1))
	res := f.run(RunOptions{})
	if !slices.Equal(res.Projects, []string{"alpha"}) {
		t.Fatalf("projects %v, want only alpha (beta has nothing)", res.Projects)
	}
}
