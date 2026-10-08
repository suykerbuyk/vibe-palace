// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// --- fixture helpers ---

// writeArchive writes a real transcript archive pair (a dummy .jsonl.zst and a
// valid .manifest.json) under Projects/<project>/transcripts, so both
// TrulyEmpty (which reads the real FS) and archive.ListEntries see it. The
// manifest file's base name is `file`; its SessionID and SourceSHA256 are set
// independently so a test can make the archive path differ from the ledgered
// path (a rename).
func writeArchive(t *testing.T, v *storage.Vault, project, file, session, sha, capturedAt string) {
	t.Helper()
	testutil.InitProject(t, v.Root, project)
	dir := filepath.Join(v.Root, "Projects", project, "transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	zst := filepath.Join(dir, file+".jsonl.zst")
	if err := os.WriteFile(zst, []byte("dummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	mp := filepath.Join(dir, file+".manifest.json")
	m := &archive.Manifest{
		SchemaVersion: 1,
		Adapter:       archive.ClaudeCodeAdapterName,
		SessionID:     session,
		SourceSHA256:  sha,
		ProjectSlug:   project,
		CapturedAt:    capturedAt,
	}
	if err := archive.WriteManifest(v.Root, mp, m); err != nil {
		t.Fatal(err)
	}
}

// writeNote writes a real session note so TrulyEmpty is false without any
// archive (the notes/unbuilt fixtures).
func writeNote(t *testing.T, v *storage.Vault, project string) {
	t.Helper()
	testutil.InitProject(t, v.Root, project)
	dir, err := v.SessionDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-05-13-aaaa0000-01.md"), []byte("# note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ensureLedger creates the project's ledger (empty baseline: no archive is on
// disk at call time unless the test wrote one first).
func ensureLedger(t *testing.T, v *storage.Vault, project string) {
	t.Helper()
	tx := lockStore(t, v, project)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// ledgerLive records a session as live-ingested for sha (via the real per-
// archive commit step).
func ledgerLive(t *testing.T, eng *Engine, v *storage.Vault, project, session, sha string) {
	t.Helper()
	commitArchive(t, eng.cache, v, project, session, sha, []string{session + " chunk"}, false)
}

// ledgerBaseline adds shas to the baseline set (the historical backlog).
func ledgerBaseline(t *testing.T, v *storage.Vault, project string, shas ...string) {
	t.Helper()
	tx := lockStore(t, v, project)
	defer tx.Release()
	if err := tx.AddToBaseline(shas); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// ledgerFailures records n failures of (session, sha) with no live record.
func ledgerFailures(t *testing.T, v *storage.Vault, project, session, sha string, n int) {
	t.Helper()
	tx := lockStore(t, v, project)
	defer tx.Release()
	for i := 0; i < n; i++ {
		if err := tx.RecordFailure(session, sha, os.ErrClosed); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// setBuiltTier records a built notes tier in completeness (what a search would
// record), so the project passes the unbuilt check.
func setBuiltTier(t *testing.T, v *storage.Vault, project string) {
	t.Helper()
	tx := lockStore(t, v, project)
	defer tx.Release()
	rec, err := tx.Completeness()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Tiers == nil {
		rec.Tiers = map[string]indexstore.TierRecord{}
	}
	rec.Tiers[tierNotes] = indexstore.TierRecord{Sources: 1}
	if _, err := tx.WriteCompleteness(rec); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// setStaleFlag persists a stale reason, as a failed build or the repair scan
// would.
func setStaleFlag(t *testing.T, v *storage.Vault, project string, r indexstore.StaleReason, localMisses int) {
	t.Helper()
	tx := lockStore(t, v, project)
	defer tx.Release()
	rec, err := tx.Completeness()
	if err != nil {
		t.Fatal(err)
	}
	rec.Stale = append(rec.Stale, r)
	rec.LocalMisses = localMisses
	if _, err := tx.WriteCompleteness(rec); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func mustCoverage(t *testing.T, eng *Engine, project string) Coverage {
	t.Helper()
	cov, err := eng.CoverageState(project)
	if err != nil {
		t.Fatal(err)
	}
	return cov
}

// --- state rows ---

func TestCoverageAbsentOnTrulyEmpty(t *testing.T) {
	eng, v := testEngine(t)
	ensureProjectDir(t, v, "proj")
	cov := mustCoverage(t, eng, "proj")
	if cov.State != CoverageAbsent {
		t.Fatalf("state = %q, want absent", cov.State)
	}
	if cov.Reason == "" {
		t.Fatal("absent carries no reason")
	}
}

// Order, pairwise: truly empty and stale-flagged reads absent (absent is tested
// before stale).
func TestCoverageAbsentOutranksStale(t *testing.T) {
	eng, v := testEngine(t)
	ensureLedger(t, v, "proj") // creates a store but no corpus
	setStaleFlag(t, v, "proj", indexstore.StaleReason{Kind: indexstore.StaleFingerprint, Fingerprint: indexstore.FingerprintChunks}, 0)
	// No notes, no archives, no chunks -> TrulyEmpty, which outranks the stale flag.
	if cov := mustCoverage(t, eng, "proj"); cov.State != CoverageAbsent {
		t.Fatalf("state = %q, want absent (absent precedes stale)", cov.State)
	}
}

func TestCoverageStaleFingerprintNoSearch(t *testing.T) {
	eng, v := testEngine(t)
	writeNote(t, v, "proj")
	setStaleFlag(t, v, "proj", indexstore.StaleReason{Kind: indexstore.StaleFingerprint, Fingerprint: indexstore.FingerprintEmbed}, 0)
	cov := mustCoverage(t, eng, "proj")
	if cov.State != CoverageStale {
		t.Fatalf("state = %q, want stale", cov.State)
	}
	if !containsText(cov.Reason, "embedder fingerprint mismatch") || !containsText(cov.Reason, "vp index rebuild") {
		t.Fatalf("stale reason = %q, want the embedder fingerprint reason naming rebuild", cov.Reason)
	}
}

func TestCoverageStaleMissingVectorsCount(t *testing.T) {
	eng, v := testEngine(t)
	writeNote(t, v, "proj")
	setStaleFlag(t, v, "proj", indexstore.StaleReason{Kind: indexstore.StaleMissingVectors}, 4)
	cov := mustCoverage(t, eng, "proj")
	if cov.State != CoverageStale {
		t.Fatalf("state = %q, want stale", cov.State)
	}
	if !containsText(cov.Reason, "4 missing vector") {
		t.Fatalf("stale reason = %q, want the missing-vector count 4", cov.Reason)
	}
}

// legacy is a state, not a flag: no `legacy` field in the payload. And legacy
// outranks unbuilt (a fresh clone of an unmigrated vault with drawers reads
// legacy, with the progress in its reason).
func TestCoverageLegacyOutranksUnbuilt(t *testing.T) {
	eng, v := testEngine(t)
	// Unmigrated vault (default) + tracked drawers + three archives, nothing built.
	addDrawer(t, v, "proj", "w", "r", "a drawer", "general")
	writeArchive(t, v, "proj", "s1", "s1", "sha1", "2026-05-13T00:00:00Z")
	writeArchive(t, v, "proj", "s2", "s2", "sha2", "2026-05-13T00:00:01Z")
	writeArchive(t, v, "proj", "s3", "s3", "sha3", "2026-05-13T00:00:02Z")
	cov := mustCoverage(t, eng, "proj")
	if cov.State != CoverageLegacy {
		t.Fatalf("state = %q, want legacy", cov.State)
	}
	if cov.M != 3 || cov.N != 0 {
		t.Fatalf("n=%d m=%d, want n=0 m=3", cov.N, cov.M)
	}
	if !containsText(cov.Reason, "not built yet on this host") || !containsText(cov.Reason, "0 of 3 sessions") {
		t.Fatalf("legacy reason = %q, want 'not built yet' and the progress", cov.Reason)
	}
}

// An ingest-only host reads unbuilt: archives ledgered live, but no search has
// built a tier, so completeness.json records none.
func TestCoverageIngestOnlyReadsUnbuilt(t *testing.T) {
	eng, v := testEngine(t)
	markMigrated(t, v)
	writeArchive(t, v, "proj", "s1", "s1", "sha1", "2026-05-13T00:00:00Z")
	ledgerLive(t, eng, v, "proj", "s1", "sha1")
	cov := mustCoverage(t, eng, "proj")
	if cov.State != CoverageUnbuilt {
		t.Fatalf("state = %q, want unbuilt (no built tier despite ledgered)", cov.State)
	}
}

// Missing fingerprint is unbuilt, never stale.
func TestCoverageMissingFingerprintIsUnbuilt(t *testing.T) {
	eng, v := testEngine(t)
	markMigrated(t, v)
	writeNote(t, v, "proj")
	cov := mustCoverage(t, eng, "proj")
	if cov.State != CoverageUnbuilt {
		t.Fatalf("state = %q, want unbuilt", cov.State)
	}
}

// notes means no live session (m = 0) with a tier built.
func TestCoverageNotes(t *testing.T) {
	eng, v := testEngine(t)
	markMigrated(t, v)
	writeNote(t, v, "proj")
	setBuiltTier(t, v, "proj")
	cov := mustCoverage(t, eng, "proj")
	if cov.State != CoverageNotes {
		t.Fatalf("state = %q, want notes", cov.State)
	}
}

// partial includes n = 0: notes built, three live sessions, none ledgered.
func TestCoveragePartialIncludesZero(t *testing.T) {
	eng, v := testEngine(t)
	markMigrated(t, v)
	setBuiltTier(t, v, "proj")
	writeArchive(t, v, "proj", "s1", "s1", "sha1", "2026-05-13T00:00:00Z")
	writeArchive(t, v, "proj", "s2", "s2", "sha2", "2026-05-13T00:00:01Z")
	writeArchive(t, v, "proj", "s3", "s3", "sha3", "2026-05-13T00:00:02Z")
	cov := mustCoverage(t, eng, "proj")
	if cov.State != CoveragePartial {
		t.Fatalf("state = %q, want partial", cov.State)
	}
	if cov.N != 0 || cov.M != 3 {
		t.Fatalf("n=%d m=%d, want n=0 m=3", cov.N, cov.M)
	}
}

// m counts sessions: one session archived on two days, the newer one ledgered.
func TestCoverageMCountsSessions(t *testing.T) {
	eng, v := testEngine(t)
	markMigrated(t, v)
	setBuiltTier(t, v, "proj")
	writeArchive(t, v, "proj", "day1", "sess", "shaOld", "2026-05-13T00:00:00Z")
	writeArchive(t, v, "proj", "day2", "sess", "shaNew", "2026-05-14T00:00:00Z")
	ledgerLive(t, eng, v, "proj", "sess", "shaNew")
	cov := mustCoverage(t, eng, "proj")
	if cov.M != 1 || cov.N != 1 || cov.State != CoverageCurrent {
		t.Fatalf("n=%d m=%d state=%q, want n=1 m=1 current", cov.N, cov.M, cov.State)
	}
}

// Same-day re-archive reads not ingested: the latest record names the old sha,
// the manifest now carries a new one.
func TestCoverageSameDayReArchiveNotIngested(t *testing.T) {
	eng, v := testEngine(t)
	markMigrated(t, v)
	setBuiltTier(t, v, "proj")
	ledgerLive(t, eng, v, "proj", "sess", "shaOld")
	writeArchive(t, v, "proj", "sess", "sess", "shaNew", "2026-05-13T00:00:00Z")
	cov := mustCoverage(t, eng, "proj")
	if cov.N != 0 || cov.State != CoveragePartial {
		t.Fatalf("n=%d state=%q, want n=0 partial", cov.N, cov.State)
	}
}

// Match by hash, not path: the live record names the manifest's sha but a
// different archive path (a rename).
func TestCoverageMatchByHashNotPath(t *testing.T) {
	eng, v := testEngine(t)
	markMigrated(t, v)
	setBuiltTier(t, v, "proj")
	// commitArchive records ArchivePath "…/sess.jsonl.zst"; the manifest file is
	// named differently, so the paths differ while the sha matches.
	ledgerLive(t, eng, v, "proj", "sess", "shaX")
	writeArchive(t, v, "proj", "renamed", "sess", "shaX", "2026-05-13T00:00:00Z")
	cov := mustCoverage(t, eng, "proj")
	if cov.N != 1 || cov.State != CoverageCurrent {
		t.Fatalf("n=%d state=%q, want n=1 current (matched by hash)", cov.N, cov.State)
	}
}

// A failure record is not an ingest: a session with only failure records never
// counts in n; it is pending and, at the limit, failing.
func TestCoverageFailureRecordIsNotIngest(t *testing.T) {
	eng, v := testEngine(t)
	markMigrated(t, v)
	setBuiltTier(t, v, "proj")
	writeArchive(t, v, "proj", "s1", "s1", "shaF", "2026-05-13T00:00:00Z")
	ensureLedger(t, v, "proj")
	ledgerFailures(t, v, "proj", "s1", "shaF", 2)
	defer swapFailureLimit(2)()
	cov := mustCoverage(t, eng, "proj")
	if cov.State != CoveragePartial || cov.N != 0 || cov.Pending != 1 || cov.Failing != 1 {
		t.Fatalf("state=%q n=%d pending=%d failing=%d, want partial 0 1 1", cov.State, cov.N, cov.Pending, cov.Failing)
	}
}

// Pending and backlog separately.
func TestCoveragePendingAndBacklogSeparately(t *testing.T) {
	eng, v := testEngine(t)
	markMigrated(t, v)
	setBuiltTier(t, v, "proj")
	for i, f := range []string{"a", "b", "c", "d", "e", "g"} {
		writeArchive(t, v, "proj", f, f, "sha"+f, time.Date(2026, 5, 13, 0, 0, i, 0, time.UTC).Format(time.RFC3339))
	}
	ledgerLive(t, eng, v, "proj", "a", "shaa")
	ledgerLive(t, eng, v, "proj", "b", "shab")
	ledgerBaseline(t, v, "proj", "shac", "shad")
	cov := mustCoverage(t, eng, "proj")
	if cov.State != CoveragePartial {
		t.Fatalf("state=%q, want partial", cov.State)
	}
	if cov.N != 2 || cov.M != 6 || cov.Backlog != 2 || cov.Pending != 2 {
		t.Fatalf("n=%d m=%d backlog=%d pending=%d, want 2 6 2 2", cov.N, cov.M, cov.Backlog, cov.Pending)
	}
	if !containsText(cov.Reason, "backlog") || !containsText(cov.Reason, "vp index rebuild") || !containsText(cov.Reason, "pending") {
		t.Fatalf("reason = %q, want pending, backlog and the rebuild command", cov.Reason)
	}
}

// No ledger yet counts as backlog.
func TestCoverageNoLedgerIsBacklog(t *testing.T) {
	eng, v := testEngine(t)
	markMigrated(t, v)
	// No ledger created: the counts are reported regardless of the state the
	// project lands in (unbuilt here), so this asserts the counts only.
	writeArchive(t, v, "proj", "s1", "s1", "sha1", "2026-05-13T00:00:00Z")
	writeArchive(t, v, "proj", "s2", "s2", "sha2", "2026-05-13T00:00:01Z")
	writeArchive(t, v, "proj", "s3", "s3", "sha3", "2026-05-13T00:00:02Z")
	cov := mustCoverage(t, eng, "proj")
	if cov.Backlog != 3 || cov.Pending != 0 {
		t.Fatalf("backlog=%d pending=%d, want 3 0", cov.Backlog, cov.Pending)
	}
}

func TestCoverageCurrent(t *testing.T) {
	eng, v := testEngine(t)
	markMigrated(t, v)
	setBuiltTier(t, v, "proj")
	writeArchive(t, v, "proj", "s1", "s1", "sha1", "2026-05-13T00:00:00Z")
	ledgerLive(t, eng, v, "proj", "s1", "sha1")
	cov := mustCoverage(t, eng, "proj")
	if cov.State != CoverageCurrent || cov.N != 1 || cov.M != 1 {
		t.Fatalf("state=%q n=%d m=%d, want current 1 1", cov.State, cov.N, cov.M)
	}
}

// --- run in progress (Scope 5) ---

func TestCoverageRunInProgressFromHolder(t *testing.T) {
	eng, _ := testEngine(t)
	origAlive, origHolder := coverageRunAlive, coverageReadHolder
	defer func() { coverageRunAlive, coverageReadHolder = origAlive, origHolder }()

	holder := func(h indexstore.Holder, err error) {
		coverageReadHolder = func(*storage.Vault) (indexstore.Holder, error) { return h, err }
	}
	alive := func(f func(int) bool) { coverageRunAlive = f }

	// A record naming a live pid, kind rebuild, progress {3,10}: reported.
	alive(func(pid int) bool { return pid == 4242 })
	holder(indexstore.Holder{PID: 4242, Kind: indexstore.KindRebuild, Project: "proj",
		StartTime: time.Now().UTC(), Progress: &indexstore.Progress{Done: 3, Total: 10}}, nil)
	run, err := eng.RunInProgress()
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.PID != 4242 || run.Kind != "rebuild" || run.Progress == nil || run.Progress.Done != 3 {
		t.Fatalf("run = %+v, want pid 4242 kind rebuild progress 3/10", run)
	}

	// The same record naming a DEAD pid: no run.
	alive(func(int) bool { return false })
	if run, _ := eng.RunInProgress(); run != nil {
		t.Fatalf("a dead pid reported a run: %+v", run)
	}

	// A missing record (ErrHolderUnknown): no run.
	alive(func(int) bool { return true })
	holder(indexstore.Holder{}, indexstore.ErrHolderUnknown)
	if run, _ := eng.RunInProgress(); run != nil {
		t.Fatalf("a missing holder reported a run: %+v", run)
	}
}

// The probe never takes the run lock (ObserveRunLocks sees 0 try events).
func TestCoverageRunProbeNeverTakesLock(t *testing.T) {
	eng, v := testEngine(t)
	markMigrated(t, v)
	setBuiltTier(t, v, "proj")
	writeArchive(t, v, "proj", "s1", "s1", "sha1", "2026-05-13T00:00:00Z")
	ledgerLive(t, eng, v, "proj", "s1", "sha1")
	defer swapRunAlive(func(int) bool { return true })()
	defer swapReadHolder(indexstore.Holder{PID: os.Getpid(), Kind: indexstore.KindIngest, StartTime: time.Now().UTC()}, nil)()

	tries := 0
	restore := indexstore.ObserveRunLocks(func(ev indexstore.RunLockEvent) {
		if ev == indexstore.RunTry {
			tries++
		}
	})
	defer restore()
	if _, err := eng.IndexCoverage("proj"); err != nil {
		t.Fatal(err)
	}
	if tries != 0 {
		t.Fatalf("the coverage probe tried the run lock %d time(s), want 0", tries)
	}
}

// --- small test seams and utilities ---

func swapFailureLimit(n int) func() {
	old := coverageFailureLimit
	coverageFailureLimit = n
	return func() { coverageFailureLimit = old }
}

func swapRunAlive(f func(int) bool) func() {
	old := coverageRunAlive
	coverageRunAlive = f
	return func() { coverageRunAlive = old }
}

func swapReadHolder(h indexstore.Holder, err error) func() {
	old := coverageReadHolder
	coverageReadHolder = func(*storage.Vault) (indexstore.Holder, error) { return h, err }
	return func() { coverageReadHolder = old }
}

func containsText(s, sub string) bool { return strings.Contains(s, sub) }

// markMigrated writes the migration marker so the legacy branch is skipped.
func markMigrated(t *testing.T, v *storage.Vault) {
	t.Helper()
	dir := filepath.Join(v.Root, ".vibe-palace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vault.toml"), []byte("authored_only = 2026-10-08\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := storage.VaultMigrated(v.Root); err != nil || !ok {
		t.Fatalf("marker not recognised: ok=%v err=%v", ok, err)
	}
}
