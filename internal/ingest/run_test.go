// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
)

// ---- helpers ---------------------------------------------------------------

// logRecord is one captured log line.
type logRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

type capture struct {
	mu   sync.Mutex
	recs []logRecord
}

func (c *capture) Enabled(context.Context, slog.Level) bool { return true }
func (c *capture) Handle(_ context.Context, r slog.Record) error {
	lr := logRecord{level: r.Level, msg: r.Message, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool { lr.attrs[a.Key] = a.Value.String(); return true })
	c.mu.Lock()
	c.recs = append(c.recs, lr)
	c.mu.Unlock()
	return nil
}
func (c *capture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *capture) WithGroup(string) slog.Handler      { return c }

// warns returns the captured Warn lines whose message contains sub.
func (c *capture) warns(sub string) []logRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []logRecord
	for _, r := range c.recs {
		if r.level == slog.LevelWarn && strings.Contains(r.msg, sub) {
			out = append(out, r)
		}
	}
	return out
}

func captureLogs(t *testing.T) *capture {
	t.Helper()
	c := &capture{}
	old := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(old) })
	return c
}

// countOpens counts archive reads (readVerifiedFn) by archive path.
func countOpens(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var opened []string
	old := readVerifiedFn
	readVerifiedFn = func(e *archive.Entry) (archive.VerifiedArchive, error) {
		mu.Lock()
		opened = append(opened, e.ArchivePath)
		mu.Unlock()
		return old(e)
	}
	t.Cleanup(func() { readVerifiedFn = old })
	return func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(opened) }
}

func (f *fixture) run(o RunOptions) RunResult {
	f.t.Helper()
	if o.VaultRoot == "" {
		o.VaultRoot = f.v.Root
	}
	if o.Project == "" {
		o.Project = "alpha"
	}
	res, err := Run(context.Background(), f.deps(), o)
	if err != nil {
		f.t.Fatalf("Run: %v", err)
	}
	return res
}

func (f *fixture) live(project, session string) (indexstore.SessionRecord, bool) {
	f.t.Helper()
	s, ok := f.store(project).Ledger().Session(session)
	return s, ok && s.State == indexstore.StateLive
}

// corrupt appends junk to an archive's compressed file.
func corrupt(t *testing.T, e *archive.Entry) {
	t.Helper()
	fh, err := os.OpenFile(e.ArchivePath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fh.WriteString("junk")
	fh.Close()
}

// day is t0 plus n days, so archives land on distinct filename days.
func day(n int) time.Time { return t0.Add(time.Duration(n) * 24 * time.Hour) }

// ---- scope -----------------------------------------------------------------

// TestRunIngestsOnlyArchivesOutsideTheBaseline: a ledger created with four
// archives as backlog, then two later archives: an automatic run ingests
// exactly the two, one of them captured before every backlog archive; an
// explicit run ingests all six.
func TestRunIngestsOnlyArchivesOutsideTheBaseline(t *testing.T) {
	f := newFixture(t)
	for i := range 4 {
		f.archiveOf("alpha", "B"+string(rune('0'+i)), transcript(t0, "b", 2), day(10+i))
	}
	f.ensureLedgerWithBaseline("alpha")
	f.archiveOf("alpha", "N0", transcript(t0, "n0", 2), day(20))
	f.archiveOf("alpha", "N1", transcript(t0, "n1", 2), day(1)) // older than the backlog
	res := f.run(RunOptions{Budget: RunBudget{Archives: 10}})
	if res.Committed != 2 {
		t.Fatalf("committed %d, want the two outside the baseline", res.Committed)
	}
	for _, s := range []string{"N0", "N1"} {
		if _, ok := f.live("alpha", s); !ok {
			t.Fatalf("%s not ingested", s)
		}
	}
	if _, ok := f.live("alpha", "B0"); ok {
		t.Fatal("an automatic run ingested backlog")
	}
	res = f.run(RunOptions{Explicit: true})
	if res.Committed != 4 {
		t.Fatalf("explicit run committed %d, want the 4 backlog archives", res.Committed)
	}
}

// TestRunLeavesTheTriggersArchiveOutOfTheBaseline (4-S3): the first run on a
// host with no ledger, triggered by archive X: the new ledger's baseline set
// holds the five others, and X is ingested in that run.
func TestRunLeavesTheTriggersArchiveOutOfTheBaseline(t *testing.T) {
	f := newFixture(t)
	for i := range 5 {
		f.archiveOf("alpha", "O"+string(rune('0'+i)), transcript(t0, "o", 2), day(i))
	}
	x := f.archiveOf("alpha", "X", transcript(t0, "x", 2), day(9))
	f.run(RunOptions{First: x.Manifest.SourceSHA256})
	l := f.store("alpha").Ledger()
	if l.InBaseline(x.Manifest.SourceSHA256) {
		t.Fatal("the trigger's archive is in the baseline set")
	}
	for i := range 5 {
		if _, ok := f.live("alpha", "O"+string(rune('0'+i))); ok {
			t.Fatal("a backlog archive was ingested")
		}
	}
	if _, ok := f.live("alpha", "X"); !ok {
		t.Fatal("the trigger's archive was not ingested")
	}
}

// TestRunOrdersNewestFirstTriggeringProjectFirst: projects alpha and beta
// with pending archives, triggered from beta: beta's archives newest first,
// then alpha's newest first.
func TestRunOrdersNewestFirstTriggeringProjectFirst(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.ensureLedger("beta")
	f.archiveOf("alpha", "A1", transcript(t0, "a1", 2), day(1))
	f.archiveOf("alpha", "A2", transcript(t0, "a2", 2), day(2))
	f.archiveOf("beta", "B1", transcript(t0, "b1", 2), day(3))
	f.archiveOf("beta", "B2", transcript(t0, "b2", 2), day(4))
	opened := countOpens(t)
	f.run(RunOptions{Project: "beta", Budget: RunBudget{Archives: 10}})
	var got []string
	for _, p := range opened() {
		got = append(got, strings.SplitN(filepath.Base(p), "-", 4)[3])
	}
	if want := []string{"B2.jsonl.zst", "B1.jsonl.zst", "A2.jsonl.zst", "A1.jsonl.zst"}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

// ---- budget, failures, run end ---------------------------------------------

// TestRunBudgetCountsArchives: two of five pending archives per run; the next
// run takes the next two.
func TestRunBudgetCountsArchives(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	for i := range 5 {
		f.archiveOf("alpha", "S"+string(rune('0'+i)), transcript(t0, "s", 2), day(i))
	}
	if res := f.run(RunOptions{Budget: RunBudget{Archives: 2}}); res.Committed != 2 || res.Stopped != "budget" {
		t.Fatalf("first run %+v", res)
	}
	if res := f.run(RunOptions{Budget: RunBudget{Archives: 2}}); res.Committed != 2 {
		t.Fatalf("second run %+v", res)
	}
	if _, ok := f.live("alpha", "S4"); !ok {
		t.Fatal("newest first: S4 should be in the first run")
	}
	if _, ok := f.live("alpha", "S0"); ok {
		t.Fatal("S0, the oldest, should still be pending")
	}
}

// TestRunWallClockCap: the injected clock passes the cap after the first
// archive: that archive is committed and no other starts.
func TestRunWallClockCap(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	for i := range 3 {
		f.archiveOf("alpha", "S"+string(rune('0'+i)), transcript(t0, "s", 2), day(i))
	}
	now := t0
	d := f.deps()
	d.Now = func() time.Time { return now }
	afterPrepareFn = func(string, *archive.Entry) { now = now.Add(time.Hour) }
	t.Cleanup(func() { afterPrepareFn = nil })
	res, err := Run(context.Background(), d, RunOptions{VaultRoot: f.v.Root, Project: "alpha", Budget: RunBudget{Archives: 10, WallClock: 30 * time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Committed != 1 || res.Stopped != "wall clock" {
		t.Fatalf("result %+v, want one archive then the wall-clock stop", res)
	}
}

// TestRunDefaultsAreSmallAndStated (R5): the UNMEASURED defaults stay small.
func TestRunDefaultsAreSmallAndStated(t *testing.T) {
	b := RunOptions{}.budget()
	if b.Archives > 3 || b.Archives < 1 || b.WallClock > 10*time.Minute || (RunOptions{}).failureLimit() != 3 {
		t.Fatalf("defaults %+v, N %d", b, RunOptions{}.failureLimit())
	}
	if DefaultMaxSourceBytes <= 0 || DefaultMaxSourceBytes > 64<<20 {
		t.Fatalf("size cap %d", DefaultMaxSourceBytes)
	}
}

// TestRunSkipsAnArchiveOverTheSizeCap (R5): an automatic run leaves an
// archive over the size cap to the rebuild, with one Warn and no failure
// record; an explicit run takes it.
func TestRunSkipsAnArchiveOverTheSizeCap(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	e := f.archiveOf("alpha", "Big", transcript(t0, "big", 4), day(1))
	logs := captureLogs(t)
	opened := countOpens(t)
	f.run(RunOptions{MaxSourceBytes: e.Manifest.SourceBytes - 1})
	if len(opened()) != 0 || len(logs.warns("larger than an automatic run takes")) != 1 {
		t.Fatalf("opened %v, warns %v", opened(), logs.warns(""))
	}
	if n := f.store("alpha").Ledger().FailureCount(e.Manifest.SourceSHA256); n != 0 {
		t.Fatalf("failure count %d", n)
	}
	f.run(RunOptions{Explicit: true, MaxSourceBytes: e.Manifest.SourceBytes - 1})
	if _, ok := f.live("alpha", "Big"); !ok {
		t.Fatal("the explicit run did not take the large archive")
	}
}

// TestRunFailureLimit: an archive that always fails gains one failure per
// run, never two in one run; after N failures automatic runs skip it; an
// explicit run tries it again. A failure record is never an ingest: the
// session has no record and its chunks never load. Rewritten with new bytes,
// its count is 0 and the next run ingests it.
func TestRunFailureLimit(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	e := f.archiveOf("alpha", "Bad", transcript(t0, "bad", 2), day(1))
	corrupt(t, e)
	sha := e.Manifest.SourceSHA256
	for i := 1; i <= 3; i++ {
		f.run(RunOptions{Budget: RunBudget{Archives: 10}})
		if n := f.store("alpha").Ledger().FailureCount(sha); n != i {
			t.Fatalf("after run %d: failure count %d", i, n)
		}
	}
	opened := countOpens(t)
	f.run(RunOptions{})
	if len(opened()) != 0 {
		t.Fatal("an archive at the failure limit was retried by an automatic run")
	}
	f.run(RunOptions{Explicit: true})
	if len(opened()) != 1 {
		t.Fatalf("the explicit run opened %d archives, want the failing one", len(opened()))
	}
	st := f.store("alpha")
	if _, ok := st.Ledger().Session("Bad"); ok || len(st.Chunks(true)) != 0 {
		t.Fatal("a failure made the session ledgered or its chunks loadable")
	}
	good := f.archiveOf("alpha", "Bad", transcript(t0, "bad-fixed", 2), day(1)) // same day: rewrites the pair
	if n := f.store("alpha").Ledger().FailureCount(good.Manifest.SourceSHA256); n != 0 {
		t.Fatalf("the rewritten archive starts at %d failures", n)
	}
	f.run(RunOptions{})
	if _, ok := f.live("alpha", "Bad"); !ok {
		t.Fatal("the rewritten archive was not ingested")
	}
}

// TestRunEndsWithoutRetryingAFailure: one archive fails and a new one arrives
// during the run: the new one is ingested, the failing one is not retried in
// the same run.
func TestRunEndsWithoutRetryingAFailure(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	bad := f.archiveOf("alpha", "Bad", transcript(t0, "bad", 2), day(2))
	corrupt(t, bad)
	opened := countOpens(t)
	afterPrepareFn = nil
	added := false
	old := readVerifiedFn
	readVerifiedFn = func(e *archive.Entry) (archive.VerifiedArchive, error) {
		if !added {
			added = true
			f.archiveOf("alpha", "New", transcript(t0, "new", 2), day(1))
		}
		return old(e)
	}
	res := f.run(RunOptions{Budget: RunBudget{Archives: 10}})
	if _, ok := f.live("alpha", "New"); !ok {
		t.Fatal("the archive that arrived during the run was not ingested")
	}
	n := 0
	for _, p := range opened() {
		if p == bad.ArchivePath {
			n++
		}
	}
	if n != 1 || res.Failed != 1 {
		t.Fatalf("the failing archive was opened %d times (failed %d), want once", n, res.Failed)
	}
}

// TestRunFailureLimitSparesASupersedingSession (R2): a session mid-supersede
// whose target has failed N times is still attempted by an automatic run.
func TestRunFailureLimitSparesASupersedingSession(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory")
	}
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "S", transcript(t0, "a", 2), day(1))
	f.run(RunOptions{})
	b := f.archiveOf("alpha", "S", transcript(t0, "b", 3), day(3))
	// The supersede towards b dies after its mark (its vector write fails),
	// three runs in a row: b reaches the failure limit mid-supersede.
	cache := filepath.Join(f.v.Root, "palace", ".local", "embed-cache", "alpha")
	if err := os.Chmod(cache, 0o555); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		f.run(RunOptions{})
	}
	os.Chmod(cache, 0o755)
	l := f.store("alpha").Ledger()
	if s, _ := l.Session("S"); s.State != indexstore.StateSuperseding || l.FailureCount(b.Manifest.SourceSHA256) < 3 {
		t.Fatalf("precondition: %+v, %d failures", s, l.FailureCount(b.Manifest.SourceSHA256))
	}
	opened := countOpens(t)
	f.run(RunOptions{})
	if len(opened()) != 1 {
		t.Fatalf("opened %d archives, want the superseding session's target despite its failures", len(opened()))
	}
	if s, ok := f.live("alpha", "S"); !ok || s.SHA != b.Manifest.SourceSHA256 {
		t.Fatalf("session %+v, want b live", s)
	}
}

// TestRunNonFatalFailure: one corrupt archive among three: two ingested, one
// failure record, one Warn, and Run returns no error.
func TestRunNonFatalFailure(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "G1", transcript(t0, "g1", 2), day(1))
	bad := f.archiveOf("alpha", "Bad", transcript(t0, "bad", 2), day(2))
	f.archiveOf("alpha", "G2", transcript(t0, "g2", 2), day(3))
	corrupt(t, bad)
	logs := captureLogs(t)
	res := f.run(RunOptions{Budget: RunBudget{Archives: 10}})
	if res.Committed != 2 || res.Failed != 1 || len(logs.warns("archive failed")) != 1 {
		t.Fatalf("result %+v, warns %d", res, len(logs.warns("archive failed")))
	}
	if f.store("alpha").Ledger().FailureCount(bad.Manifest.SourceSHA256) != 1 {
		t.Fatal("no failure record")
	}
}

// TestRunCheckpointStopsTheRun: a Checkpoint that fails at once: no archive
// ledgered, no failure record, the run stops.
func TestRunCheckpointStopsTheRun(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	e := f.archiveOf("alpha", "S", transcript(t0, "s", 2), day(1))
	f.archiveOf("alpha", "T", transcript(t0, "t", 2), day(2))
	res := f.run(RunOptions{Checkpoint: func(int) error { return os.ErrDeadlineExceeded }})
	if res.Stopped != "checkpoint" || res.Committed != 0 {
		t.Fatalf("result %+v", res)
	}
	l := f.store("alpha").Ledger()
	if _, ok := l.Session("S"); ok || l.FailureCount(e.Manifest.SourceSHA256) != 0 {
		t.Fatal("a checkpoint stop ledgered the archive or recorded a failure")
	}
}

// ---- skips and Warns -------------------------------------------------------

// TestRunSkipsAStaleProjectWithAWarn (Chair ruling 2): a project whose
// embedding regime is another's is skipped with one Warn naming `vp index
// rebuild`, no archive opened; another project in the same run is ingested.
func TestRunSkipsAStaleProjectWithAWarn(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.ensureLedger("beta")
	f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	f.archiveOf("beta", "B", transcript(t0, "b", 2), day(2))
	dir := filepath.Join(f.v.Root, "palace", ".local", "embed-cache", "alpha")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "x.vec"), []byte{0, 0, 0, 0}, 0o644) // vectors with no sidecar
	logs := captureLogs(t)
	opened := countOpens(t)
	f.run(RunOptions{Budget: RunBudget{Archives: 10}})
	w := logs.warns("vp index rebuild")
	if len(w) != 1 || w[0].attrs["project"] != "alpha" {
		t.Fatalf("warns %+v, want one for alpha naming vp index rebuild", w)
	}
	for _, p := range opened() {
		if strings.Contains(p, "/alpha/") {
			t.Fatal("an archive of the stale project was opened")
		}
	}
	if _, ok := f.live("beta", "B"); !ok {
		t.Fatal("the other project was not ingested")
	}
}

// TestRunWarnsOnAFingerprintWrittenMidRun (R7): another process writes
// another recipe's chunks.fingerprint after the stale pre-check: nothing is
// written and one Warn names `vp index rebuild`.
func TestRunWarnsOnAFingerprintWrittenMidRun(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "S", transcript(t0, "s", 2), day(1))
	beforeLockFn = func(string, *archive.Entry) {
		beforeLockFn = nil
		os.WriteFile(filepath.Join(f.v.Root, "palace", ".local", "index", "alpha", "chunks.fingerprint"), []byte("other"), 0o644)
	}
	t.Cleanup(func() { beforeLockFn = nil })
	logs := captureLogs(t)
	res := f.run(RunOptions{})
	if res.Committed != 0 || len(logs.warns("vp index rebuild")) != 1 {
		t.Fatalf("result %+v, warns %d", res, len(logs.warns("vp index rebuild")))
	}
	if _, ok := f.store("alpha").Ledger().Session("S"); ok || f.store("alpha").Ledger().FailureCount(f.allSHAs("alpha")[0]) != 0 {
		t.Fatal("a stale project got a session or a failure record")
	}
}

// TestRunSkipsAnArchiveWithNoHash (R4): an automatic run skips an archive
// whose manifest has no source_sha256 (one Warn, not opened, no failure); an
// explicit run ingests it under the sha256 of its bytes, and a rerun skips it.
func TestRunSkipsAnArchiveWithNoHash(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	e := f.archiveOf("alpha", "S", transcript(t0, "s", 2), day(1))
	m := *e.Manifest
	m.SourceSHA256 = ""
	if err := archive.WriteManifest(f.v.Root, e.ManifestPath, &m); err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(t)
	opened := countOpens(t)
	f.run(RunOptions{})
	if len(opened()) != 0 || len(logs.warns("no source_sha256")) != 1 {
		t.Fatalf("opened %v, warns %d", opened(), len(logs.warns("no source_sha256")))
	}
	f.run(RunOptions{Explicit: true})
	s, ok := f.live("alpha", "S")
	if !ok || s.SHA != e.Manifest.SourceSHA256 {
		t.Fatalf("session %+v, want it keyed by its bytes' sha256", s)
	}
	if res := f.run(RunOptions{Explicit: true}); res.Committed != 0 {
		t.Fatalf("a rerun committed %d", res.Committed)
	}
}

// ---- locks -----------------------------------------------------------------

// TestRunExitsWhenTheRunLockIsHeld: while the run lock is held as ingest,
// rebuild or lifecycle, a run exits at once, opens no archive and writes no
// holder record; its First is left in the inbox (R3).
func TestRunExitsWhenTheRunLockIsHeld(t *testing.T) {
	for _, kind := range []indexstore.RunKind{indexstore.KindIngest, indexstore.KindRebuild, indexstore.KindLifecycle} {
		t.Run(string(kind), func(t *testing.T) {
			f := newFixture(t)
			x := f.archiveOf("alpha", "X", transcript(t0, "x", 2), day(1))
			held, ok, err := indexstore.TryRunLock(f.v, kind, "")
			if err != nil || !ok {
				t.Fatalf("TryRunLock: %v %v", ok, err)
			}
			defer held.Release()
			before, _ := indexstore.ReadHolder(f.v)
			opened := countOpens(t)
			res := f.run(RunOptions{First: x.Manifest.SourceSHA256})
			if !res.LockHeld || len(opened()) != 0 {
				t.Fatalf("result %+v, opened %v", res, opened())
			}
			if after, _ := indexstore.ReadHolder(f.v); after != before {
				t.Fatalf("holder record changed: %+v -> %+v", before, after)
			}
			if got, _ := indexstore.ReadFirsts(f.v, "alpha"); !slices.Equal(got, []string{x.Manifest.SourceSHA256}) {
				t.Fatalf("inbox %v, want the trigger's First", got)
			}
		})
	}
}

// TestRunServesALosingTriggersFirst (R3 race): run A holds the run lock with
// no ledger yet; trigger B with First X exits on the held lock; A then
// creates the ledger: X is not in the baseline set and A ingests it. The
// same when B's note lands after A created the ledger.
func TestRunServesALosingTriggersFirst(t *testing.T) {
	for _, late := range []bool{false, true} {
		f := newFixture(t)
		f.archiveOf("alpha", "Old", transcript(t0, "old", 2), day(1))
		x := f.archiveOf("alpha", "X", transcript(t0, "x", 2), day(2))
		held, ok, err := indexstore.TryRunLock(f.v, indexstore.KindIngest, "alpha")
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		if late {
			f.ensureLedgerWithBaseline("alpha") // X becomes backlog before B's note lands
		}
		if res := f.run(RunOptions{First: x.Manifest.SourceSHA256}); !res.LockHeld {
			t.Fatal("B got the lock")
		}
		if _, err := RunHeld(context.Background(), f.deps(), held, RunOptions{VaultRoot: f.v.Root, Project: "alpha"}); err != nil {
			t.Fatal(err)
		}
		held.Release()
		if _, ok := f.live("alpha", "X"); !ok {
			t.Fatalf("late=%v: the losing trigger's archive was not ingested", late)
		}
		if _, ok := f.live("alpha", "Old"); ok {
			t.Fatalf("late=%v: backlog was ingested", late)
		}
		if got, _ := indexstore.ReadFirsts(f.v, "alpha"); len(got) != 0 {
			t.Fatalf("late=%v: the served inbox entry was not dropped: %v", late, got)
		}
	}
}

// TestRunHolderRecordAndProgress: during a run the holder record names an
// ingest run and its project, and its progress rises by one per archive.
func TestRunHolderRecordAndProgress(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	for i := range 3 {
		f.archiveOf("alpha", "S"+string(rune('0'+i)), transcript(t0, "s", 2), day(i))
	}
	var seen []int
	afterPrepareFn = func(string, *archive.Entry) {
		h, err := indexstore.ReadHolder(f.v)
		if err != nil || h.Kind != indexstore.KindIngest || h.Project != "alpha" || h.PID != os.Getpid() {
			t.Errorf("holder %+v, %v", h, err)
		}
		done := 0
		if h.Progress != nil {
			done = h.Progress.Done
		}
		seen = append(seen, done)
	}
	t.Cleanup(func() { afterPrepareFn = nil })
	f.run(RunOptions{Budget: RunBudget{Archives: 10}})
	if !slices.Equal(seen, []int{0, 1, 2}) {
		t.Fatalf("progress seen %v, want 0, 1, 2", seen)
	}
	if _, err := indexstore.ReadHolder(f.v); err == nil {
		t.Fatal("the holder record outlived the run")
	}
}

// TestRunHeldRunsUnderTheCallersLock (7-S4): RunHeld ingests under a lock the
// caller holds, and leaves it held.
func TestRunHeldRunsUnderTheCallersLock(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "S", transcript(t0, "s", 2), day(1))
	held, ok, err := indexstore.TryRunLock(f.v, indexstore.KindRebuild, "alpha")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer held.Release()
	if _, err := RunHeld(context.Background(), f.deps(), held, RunOptions{VaultRoot: f.v.Root, Project: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.live("alpha", "S"); !ok {
		t.Fatal("RunHeld ingested nothing")
	}
	if _, ok, _ := indexstore.TryRunLock(f.v, indexstore.KindIngest, ""); ok {
		t.Fatal("RunHeld released the caller's lock")
	}
}

// TestRunNoLostTrigger (1-S2): (b) an archive added after the last rescan and
// before the release makes the run re-acquire and ingest it; (c) an archive
// the budget left over was seen, so the run does not re-acquire for it.
func TestRunNoLostTrigger(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "S", transcript(t0, "s", 2), day(1))
	beforeReleaseFn = func() {
		beforeReleaseFn = nil
		f.archiveOf("alpha", "X", transcript(t0, "x", 2), day(2))
	}
	t.Cleanup(func() { beforeReleaseFn = nil })
	res := f.run(RunOptions{})
	if res.Passes != 2 {
		t.Fatalf("passes %d, want a re-acquire for the late archive", res.Passes)
	}
	if _, ok := f.live("alpha", "X"); !ok {
		t.Fatal("the late archive was lost")
	}

	g := newFixture(t)
	g.ensureLedger("alpha")
	for i := range 3 {
		g.archiveOf("alpha", "Y"+string(rune('0'+i)), transcript(t0, "y", 2), day(i))
	}
	if res := g.run(RunOptions{Budget: RunBudget{Archives: 1}}); res.Passes != 1 || res.Committed != 1 {
		t.Fatalf("result %+v: budget leftovers made the run re-acquire", res)
	}
}

// TestRunMovesOnFromAProjectRemovedMidRun (R6): the project disappears
// between the prepare and the commit: no failure record, the next project is
// ingested, and the release does not re-acquire for the gone project.
func TestRunMovesOnFromAProjectRemovedMidRun(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.ensureLedger("beta")
	a := f.archiveOf("alpha", "A", transcript(t0, "a", 2), day(1))
	f.archiveOf("beta", "B", transcript(t0, "b", 2), day(2))
	beforeLockFn = func(p string, _ *archive.Entry) {
		if p == "alpha" {
			beforeLockFn = nil
			os.RemoveAll(filepath.Join(f.v.Root, "Projects", "alpha"))
		}
	}
	t.Cleanup(func() { beforeLockFn = nil })
	res := f.run(RunOptions{Budget: RunBudget{Archives: 10}})
	if res.Failed != 0 || res.Passes != 1 {
		t.Fatalf("result %+v: a failure, or a re-acquire for the gone project", res)
	}
	if _, ok := f.live("beta", "B"); !ok {
		t.Fatal("the next project was not ingested")
	}
	if st, err := indexstore.ReadStore(f.v, "alpha"); err == nil && st.Ledger().FailureCount(a.Manifest.SourceSHA256) != 0 {
		t.Fatal("a failure was recorded for the gone project")
	}
}

// ---- supersede through the run ----------------------------------------------

// TestRunSupersedesInEitherOrder: session S's earlier archive A and later
// archive B. Ingested A then B, or with B ingested first and A met later: the
// same live archive (B), A recorded superseded, and in the second order A is
// never opened.
func TestRunSupersedesInEitherOrder(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	a := f.archiveOf("alpha", "S", transcript(t0, "a", 2), day(1))
	f.run(RunOptions{})
	b := f.archiveOf("alpha", "S", transcript(t0, "b", 3), day(3))
	f.run(RunOptions{})
	s1, _ := f.live("alpha", "S")
	l1 := f.store("alpha").Ledger()

	g := newFixture(t)
	g.ensureLedger("alpha")
	b2 := g.archiveOf("alpha", "S", transcript(t0, "b", 3), day(3))
	g.run(RunOptions{})
	opened := countOpens(t)
	a2 := g.archiveOf("alpha", "S", transcript(t0, "a", 2), day(1))
	g.run(RunOptions{})
	s2, _ := g.live("alpha", "S")
	if s1.SHA != b.Manifest.SourceSHA256 || s2.SHA != b2.Manifest.SourceSHA256 || s1.SHA != s2.SHA {
		t.Fatalf("live %s / %s, want B in both orders", s1.SHA, s2.SHA)
	}
	if !l1.Superseded(a.Manifest.SourceSHA256) || !g.store("alpha").Ledger().Superseded(a2.Manifest.SourceSHA256) {
		t.Fatal("A must read superseded in both orders")
	}
	if len(opened()) != 0 {
		t.Fatalf("the older archive met late was opened: %v", opened())
	}
	if got, want := len(g.store("alpha").Chunks(true)), len(f.store("alpha").Chunks(true)); got != want {
		t.Fatalf("%d live chunks in one order, %d in the other", got, want)
	}
}

// TestRunSameDayRewriteSupersedesByHash (4-B2): the session's archive is
// rewritten in place with new bytes: the new hash supersedes the old with the
// old bytes gone from disk, and no chunk names the old hash.
func TestRunSameDayRewriteSupersedesByHash(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	a := f.archiveOf("alpha", "S", transcript(t0, "a", 2), day(1))
	f.run(RunOptions{})
	b := f.archiveOf("alpha", "S", transcript(t0, "b", 3), day(1).Add(time.Hour))
	if a.ArchivePath != b.ArchivePath {
		t.Fatal("precondition: a same-day re-archive rewrites one path")
	}
	f.run(RunOptions{})
	s, _ := f.live("alpha", "S")
	if s.SHA != b.Manifest.SourceSHA256 || s.Generation != 2 {
		t.Fatalf("session %+v", s)
	}
	if n := f.store("alpha").CountChunks(indexstore.ArchiveOwner(a.Manifest.SourceSHA256)); n != 0 {
		t.Fatalf("%d chunks still owned by the old hash", n)
	}
}

// TestRunRetargetsAfterACrashAndARewrite (R1): a supersede towards B dies
// after its mark (B's vector write fails); then B is rewritten to C before
// any resume. The next run re-targets: C live, A and B superseded, no chunk
// owned by A or B.
func TestRunRetargetsAfterACrashAndARewrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory")
	}
	f := newFixture(t)
	f.ensureLedger("alpha")
	a := f.archiveOf("alpha", "S", transcript(t0, "a", 2), day(1))
	f.run(RunOptions{})
	b := f.archiveOf("alpha", "S", transcript(t0, "b", 3), day(3))
	cache := filepath.Join(f.v.Root, "palace", ".local", "embed-cache", "alpha")
	if err := os.Chmod(cache, 0o555); err != nil {
		t.Fatal(err)
	}
	f.run(RunOptions{})
	os.Chmod(cache, 0o755)
	if s, _ := f.store("alpha").Ledger().Session("S"); s.State != indexstore.StateSuperseding || s.SHA != b.Manifest.SourceSHA256 {
		t.Fatalf("precondition: %+v, want superseding towards b", s)
	}
	c := f.archiveOf("alpha", "S", transcript(t0, "c", 4), day(3).Add(time.Hour)) // rewrites b's pair
	if c.ArchivePath != b.ArchivePath {
		t.Fatal("precondition: c rewrites b's path")
	}
	f.run(RunOptions{})
	st := f.store("alpha")
	s, _ := st.Ledger().Session("S")
	if s.State != indexstore.StateLive || s.SHA != c.Manifest.SourceSHA256 {
		t.Fatalf("session %+v, want c live", s)
	}
	for _, sha := range []string{a.Manifest.SourceSHA256, b.Manifest.SourceSHA256} {
		if !st.Ledger().Superseded(sha) || st.CountChunks(indexstore.ArchiveOwner(sha)) != 0 {
			t.Fatalf("%s must be superseded and own nothing", sha[:8])
		}
	}
}

// TestRunNeverRollsBack: the ledgered live archive is gone and only an
// older archive of the session remains: the run does not ingest it over the
// live record, and Warns.
func TestRunNeverRollsBack(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	old := f.archiveOf("alpha", "S", transcript(t0, "old", 2), day(1))
	newer := f.archiveOf("alpha", "S", transcript(t0, "new", 2), day(5))
	// Ingest only the newer one, with the older hidden from the listing (a
	// run would otherwise record it superseded), then bring the older back
	// and remove the newer from disk.
	tmp := old.ManifestPath + ".hidden"
	os.Rename(old.ManifestPath, tmp)
	f.run(RunOptions{})
	os.Rename(tmp, old.ManifestPath)
	os.Remove(newer.ManifestPath)
	os.Remove(newer.ArchivePath)
	logs := captureLogs(t)
	opened := countOpens(t)
	f.run(RunOptions{})
	if len(opened()) != 0 {
		t.Fatalf("an older archive was ingested over the live one: %v", opened())
	}
	if s, _ := f.live("alpha", "S"); s.SHA != newer.Manifest.SourceSHA256 {
		t.Fatalf("the session was rolled back to %s", s.SHA)
	}
	if len(logs.warns("no longer on disk")) != 1 {
		t.Fatalf("warns %+v", logs.warns(""))
	}
}

// ---- the vault -------------------------------------------------------------

// TestRunWritesOnlyHostLocalFiles: on an unmigrated vault with a tracked
// drawer, a run ingests into palace/.local/index/<p>/ and leaves every
// tracked file, the legacy palace/<p>/ingested-archives.jsonl included,
// untouched: git status is empty.
func TestRunWritesOnlyHostLocalFiles(t *testing.T) {
	f := newFixture(t)
	drawer := filepath.Join(f.v.Root, "palace", "alpha", "drawers", "technical", "general", "d1.md")
	os.MkdirAll(filepath.Dir(drawer), 0o755)
	os.WriteFile(drawer, []byte("---\nid: d1\n---\nA tracked drawer.\n"), 0o644)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "S", transcript(t0, "s", 2), day(1))
	f.commitAll()
	f.run(RunOptions{})
	if _, ok := f.live("alpha", "S"); !ok {
		t.Fatal("not ingested on an unmigrated vault")
	}
	if out := f.git("status", "--porcelain", "-uall"); out != "" {
		t.Fatalf("tracked files changed:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.v.Root, "palace", "alpha", "ingested-archives.jsonl")); !os.IsNotExist(err) {
		t.Fatal("the legacy ledger was written")
	}
	if len(f.ledgerLines("alpha")) == 0 {
		t.Fatal("no ledger under palace/.local/index/alpha/")
	}
}

// TestRunDoesNotChargeAnArchiveAnotherRunCommitted (R6): another process
// commits an archive this run already prepared: no budget unit and no
// progress is charged for it, so with a budget of one the run still commits
// the next archive.
func TestRunDoesNotChargeAnArchiveAnotherRunCommitted(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	f.archiveOf("alpha", "S0", transcript(t0, "s0", 2), day(1))
	f.archiveOf("alpha", "S1", transcript(t0, "s1", 2), day(2))
	afterPrepareFn = func(p string, e *archive.Entry) {
		afterPrepareFn = nil
		if _, err := IngestArchive(context.Background(), f.deps(), p, e, IngestOptions{Embed: false}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { afterPrepareFn = nil })
	var progress []int
	beforeLockFn = func(string, *archive.Entry) {
		if h, err := indexstore.ReadHolder(f.v); err == nil {
			done := 0
			if h.Progress != nil {
				done = h.Progress.Done
			}
			progress = append(progress, done)
		}
	}
	t.Cleanup(func() { beforeLockFn = nil })
	res := f.run(RunOptions{Budget: RunBudget{Archives: 1}})
	if res.Committed != 1 {
		t.Fatalf("committed %d, want the second archive committed within a budget of one", res.Committed)
	}
	for _, s := range []string{"S0", "S1"} {
		if _, ok := f.live("alpha", s); !ok {
			t.Fatalf("%s not live", s)
		}
	}
	// The last probe is the run's own second archive, just before its commit
	// (the earlier one is the other process's ingest, inside the seam).
	if len(progress) == 0 || progress[len(progress)-1] != 0 {
		t.Fatalf("progress before the second commit %v, want 0: the other run's archive was charged", progress)
	}
}

// TestRunRetriesARewrittenArchiveNextRun (adopted note): an archive whose
// manifest changed between the listing and the read is not a failure; the
// next run ingests the new bytes.
func TestRunRetriesARewrittenArchiveNextRun(t *testing.T) {
	f := newFixture(t)
	f.ensureLedger("alpha")
	e := f.archiveOf("alpha", "S", transcript(t0, "s", 2), day(1))
	old := readVerifiedFn
	readVerifiedFn = func(x *archive.Entry) (archive.VerifiedArchive, error) {
		readVerifiedFn = old
		f.archiveOf("alpha", "S", transcript(t0, "s2", 3), day(1).Add(time.Hour)) // the hook rewrites the pair
		return old(x)
	}
	t.Cleanup(func() { readVerifiedFn = old })
	res := f.run(RunOptions{})
	if res.Changed != 1 || res.Failed != 0 {
		t.Fatalf("result %+v, want one change and no failure", res)
	}
	if n := f.store("alpha").Ledger().FailureCount(e.Manifest.SourceSHA256); n != 0 {
		t.Fatalf("failure count %d", n)
	}
	f.run(RunOptions{})
	if s, ok := f.live("alpha", "S"); !ok || s.SHA == e.Manifest.SourceSHA256 {
		t.Fatalf("session %+v, want the rewritten archive live", s)
	}
}
