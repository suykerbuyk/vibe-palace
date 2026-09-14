// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package jobqueue

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- Bug 1: ReadFile-failure job loss in Claim -----------------------------

// TestClaimRestoresJobOnReadFileFailure proves that when Claim's claim-rename
// succeeds but the subsequent os.ReadFile of the freshly-claimed candidate
// fails (e.g. a transient permission or I/O error), the job is restored to
// its original claimable name rather than deleted. Before the fix, Claim
// called os.Remove(candidate) on this path, permanently destroying the job.
func TestClaimRestoresJobOnReadFileFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits are meaningless as root")
	}

	dir := t.TempDir()
	jsonPath := writeJob(t, dir, "a", `{"n":1}`)
	if err := os.Chmod(jsonPath, 0o000); err != nil {
		t.Fatalf("chmod unreadable: %v", err)
	}

	procPath, data, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if procPath != "" || data != nil {
		t.Errorf("Claim = (%q, %v), want sentinel (unreadable candidate must be skipped, not returned)", procPath, data)
	}

	candidate := jsonPath + ProcessingSuffix
	if _, statErr := os.Stat(candidate); !os.IsNotExist(statErr) {
		t.Errorf("candidate %s should not remain after a failed read: err=%v", candidate, statErr)
	}
	if _, statErr := os.Stat(jsonPath); statErr != nil {
		t.Fatalf("job should have been restored to %s, but it is gone: %v", jsonPath, statErr)
	}

	// Chain a follow-up: fix the permission problem and confirm the
	// restored job is genuinely re-claimable, proving forward progress
	// rather than a one-shot restore that leaves the job stuck.
	if err := os.Chmod(jsonPath, 0o644); err != nil {
		t.Fatalf("chmod restore: %v", err)
	}
	procPath2, data2, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("second Claim: %v", err)
	}
	if procPath2 != candidate {
		t.Errorf("second Claim procPath = %q, want %q", procPath2, candidate)
	}
	if string(data2) != `{"n":1}` {
		t.Errorf("second Claim data = %q, want %q", data2, `{"n":1}`)
	}
}

// --- Bug 2: stale-claim TOCTOU in reclaimStale ------------------------------

// TestReclaimStaleLinkFailsWhenTargetExists is the deterministic, non-racy
// core-correctness test for the hard-link fix: os.Link must fail with
// os.IsExist when the target name is already occupied, and the occupant's
// content must be left completely untouched.
func TestReclaimStaleLinkFailsWhenTargetExists(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.json")
	if err := os.WriteFile(target, []byte(`{"fresh":true}`), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	p := target + ProcessingSuffix
	if err := os.WriteFile(p, []byte(`{"stale":true}`), 0o644); err != nil {
		t.Fatalf("write stale claim: %v", err)
	}

	linkErr := os.Link(p, target)
	if !os.IsExist(linkErr) {
		t.Fatalf("os.Link(p, target) err = %v, want an os.IsExist error", linkErr)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != `{"fresh":true}` {
		t.Errorf("target content changed: got %q, want %q", data, `{"fresh":true}`)
	}
}

// TestReclaimStaleNeverClobbersFreshJobConcurrently is a genuinely concurrent
// stress test for the same fix: one goroutine repeatedly manufactures a
// stale ".processing" claim (sentinel content "A-*") and reclaims it using a
// near-zero staleAge (production uses 15 minutes; a tiny value here is
// required so the stale branch is actually entered on every iteration),
// while a second goroutine repeatedly removes-and-recreates the same target
// path with fresh sentinel content ("B-*") — the remove+recreate is what
// opens the TOCTOU window the old Stat-then-Rename code could land in. Run
// with -race. Against the old code this reliably observes a fresh "B-*" write
// clobbered by a stale "A" claim; against the os.Link fix it must never
// reproduce.
func TestReclaimStaleNeverClobbersFreshJobConcurrently(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.json")
	procPath := target + ProcessingSuffix
	const staleAge = 1 * time.Millisecond
	const iterations = 400

	done := make(chan struct{})
	var clobbered atomic.Bool
	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine A: continuously manufactures a stale claim and reclaims it.
	go func() {
		defer wg.Done()
		old := time.Now().Add(-time.Hour)
		for i := 0; i < iterations; i++ {
			_ = os.WriteFile(procPath, []byte("A"), 0o644)
			_ = os.Chtimes(procPath, old, old)
			reclaimStale(dir, staleAge)
		}
		close(done)
	}()

	// Goroutine B: continuously (re)creates target with fresh, uniquely
	// tagged content and checks it wasn't immediately overwritten by a
	// stale claim.
	go func() {
		defer wg.Done()
		var seq uint64
		for {
			select {
			case <-done:
				return
			default:
			}
			seq++
			content := fmt.Sprintf("B-%d", seq)
			_ = os.Remove(target)
			if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
				continue
			}
			got, err := os.ReadFile(target)
			if err != nil {
				continue
			}
			if strings.HasPrefix(string(got), "A") {
				clobbered.Store(true)
			}
		}
	}()

	wg.Wait()
	if clobbered.Load() {
		t.Error("a fresh job at target was clobbered by a reclaimed stale claim (TOCTOU) — hard-link fix did not hold under concurrency")
	}
}

// --- Bug 3: filepath.Glob metacharacter mishandling -------------------------

// TestClaimFindsJobInBracketedDirName proves Claim finds a real job even
// when dir's own name contains glob metacharacters ('[' and ']'), which
// filepath.Glob would otherwise interpret as a character class and silently
// fail to match.
func TestClaimFindsJobInBracketedDirName(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "proj[1]")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir bracketed dir: %v", err)
	}
	jsonPath := writeJob(t, dir, "a", `{"n":1}`)

	procPath, data, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if procPath == "" {
		t.Fatalf("Claim found no job in bracketed dir %q (procPath=%q) — filepath.Glob metacharacter bug reproduced", dir, procPath)
	}
	want := jsonPath + ProcessingSuffix
	if procPath != want {
		t.Errorf("procPath = %q, want %q", procPath, want)
	}
	if string(data) != `{"n":1}` {
		t.Errorf("data = %q, want %q", data, `{"n":1}`)
	}
}

// TestReclaimStaleFindsClaimInBracketedDirName exercises the same
// metacharacter hazard against reclaimStale's own matching.
func TestReclaimStaleFindsClaimInBracketedDirName(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "proj[1]")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir bracketed dir: %v", err)
	}
	jsonPath := writeJob(t, dir, "a", `{"n":1}`)
	procPath := jsonPath + ProcessingSuffix
	if err := os.Rename(jsonPath, procPath); err != nil {
		t.Fatalf("orphan claim: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(procPath, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	reclaimStale(dir, 15*time.Minute)

	if _, statErr := os.Stat(jsonPath); statErr != nil {
		t.Fatalf("stale claim not reclaimed in bracketed dir %q — filepath.Glob metacharacter bug reproduced: %v", dir, statErr)
	}
	if _, statErr := os.Stat(procPath); !os.IsNotExist(statErr) {
		t.Errorf("processing file still present after reclaim: err=%v", statErr)
	}
}

// --- Bug 4: non-atomic requeue write ----------------------------------------

// TestRequeueAtomicWriteNoTornReads proves Requeue's rewrite of the job file
// is atomic from a reader's point of view. One goroutine runs Requeue with a
// large reencode payload; a second goroutine, concurrently, repeatedly opens
// the job path FRESH, reads it fully, and closes it, asserting every full
// read is either the complete old content or the complete new content, never
// a short/torn read. A single pre-opened fd would keep referencing the old
// inode forever regardless of any concurrent rename and would prove nothing,
// so this deliberately opens fresh every iteration.
func TestRequeueAtomicWriteNoTornReads(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "a.json")
	oldContent := bytes.Repeat([]byte("O"), 1<<20)  // 1MB
	newContent := bytes.Repeat([]byte("N"), 40<<20) // 40MB
	if err := os.WriteFile(jsonPath, oldContent, 0o644); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	procPath, _, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	stop := make(chan struct{})
	ready := make(chan struct{})
	var readyCount atomic.Int64
	var torn atomic.Bool
	var wg sync.WaitGroup
	// Run many concurrent readers, not just one, and hold them all at a
	// start barrier until every one is spun up: under -race the per-access
	// instrumentation cost of reading a large buffer can make a lone
	// reader (or one still paying goroutine-startup latency) too slow to
	// land inside the write's transitional window even against the pre-fix
	// in-place write, so parallelism synchronized to start together is
	// what makes the race reliably observable here. 12 readers / 40MB was
	// tuned empirically on a 16-core box to reliably reproduce the pre-fix
	// torn read within a few seconds under -race without excessive runtime.
	const nReaders = 12
	for i := 0; i < nReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if readyCount.Add(1) == int64(nReaders) {
				close(ready)
			}
			<-ready
			for {
				select {
				case <-stop:
					return
				default:
				}
				f, openErr := os.Open(jsonPath)
				if openErr != nil {
					// Momentarily absent between Claim's rename-away and
					// Requeue's rewrite; not what this test is checking.
					continue
				}
				got, readErr := io.ReadAll(f)
				_ = f.Close()
				if readErr != nil {
					continue
				}
				if bytes.Equal(got, oldContent) || bytes.Equal(got, newContent) {
					continue
				}
				torn.Store(true)
			}
		}()
	}
	<-ready

	reencode := func(int) ([]byte, error) { return newContent, nil }
	if _, err := Requeue(procPath, 1, 5, reencode); err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	close(stop)
	wg.Wait()

	if torn.Load() {
		t.Error("observed a torn/short read of the job file concurrent with Requeue's write — non-atomic write reproduced")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("stray AtomicWrite temp file left behind: %s", e.Name())
		}
	}
}

// --- Bug 5: reclaimStale measures content age, not claim age ---------------

// TestClaimDoesNotPrematurelyReclaimOldJobJustClaimed is the regression test
// for the second-opinion review's CRITICAL finding: os.Rename does not update
// mtime on POSIX, so a freshly-claimed job's ".processing" file inherits
// whatever mtime its CONTENT last had (its original enqueue time, or its
// last Requeue rewrite) rather than "time since this claim was made".
//
// Before the fix, a job that had simply been sitting queued for longer than
// staleAge before ever being claimed would look orphaned (crashed claimant)
// to reclaimStale the INSTANT it was claimed — even though nothing crashed
// and the claim is brand new. Concretely: claim job "a" (old content mtime),
// leave its claim outstanding (exactly what DrainEnrichmentQueue's deferred-
// fairness fix does with a failed item for the rest of one call) without
// calling Done/Requeue on it, then call Claim again. Pre-fix, that second
// Claim call's own reclaimStale step sees "a"'s old-mtime ".processing" file,
// treats it as stale, hard-link-restores it to "a.json", and that SAME Claim
// call then re-claims "a" again (it still sorts first) instead of ever
// reaching "b" — reproducing the exact starvation the deferred-fairness
// design exists to prevent, just triggered by item age instead of immediate
// re-enqueue. Post-fix (os.Chtimes stamps jsonPath's mtime to now BEFORE the
// claim-rename, so the resulting ".processing" file is born already fresh —
// see Claim's own comment for why this must happen before the rename, not
// after), "a"'s claim reads as fresh, reclaimStale leaves it alone, and the
// second Claim call correctly reaches "b". This test's own assertions only
// check end-state (which item each Claim call returns, and that "a"'s claim
// survives untouched) — it does not care which side of the rename the stamp
// happens on, so it needed no change when the stamp was moved from
// after the rename to before it; only this comment did.
func TestClaimDoesNotPrematurelyReclaimOldJobJustClaimed(t *testing.T) {
	dir := t.TempDir()

	// "a" was queued (or last rewritten) long before this drain call runs —
	// its content mtime is old, even though it has never actually been
	// claimed until the Claim call below.
	aPath := writeJob(t, dir, "a", `{"n":1}`)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(aPath, old, old); err != nil {
		t.Fatalf("Chtimes a.json: %v", err)
	}
	bPath := writeJob(t, dir, "b", `{"n":2}`)
	_ = bPath

	const staleAge = 15 * time.Minute

	// First Claim: claims "a" (lexicographically first). Its claim is left
	// outstanding afterward — no Done/Requeue is called on it — exactly the
	// state a deferred, failed item is left in for the rest of one
	// DrainEnrichmentQueue call.
	procA, dataA, err := Claim(dir, staleAge)
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	if procA == "" {
		t.Fatal("first Claim found nothing claimable")
	}
	if string(dataA) != `{"n":1}` {
		t.Fatalf("first Claim returned %q, want job a's content", dataA)
	}

	// Second Claim, immediately after: must reach "b", not re-claim "a".
	procB, dataB, err := Claim(dir, staleAge)
	if err != nil {
		t.Fatalf("second Claim: %v", err)
	}
	if procB == "" {
		t.Fatal("second Claim found nothing claimable — job b was never reached")
	}
	if procB == procA {
		t.Fatalf("second Claim re-claimed the SAME outstanding claim %s instead of reaching job b — reclaimStale prematurely reclaimed it based on stale content mtime", procA)
	}
	if string(dataB) != `{"n":2}` {
		t.Errorf("second Claim returned %q, want job b's content", dataB)
	}

	// "a"'s claim must still be exactly where the first Claim left it —
	// untouched by the second Claim call's own reclaimStale step.
	if _, statErr := os.Stat(procA); statErr != nil {
		t.Errorf("job a's outstanding claim %s should still exist, untouched: %v", procA, statErr)
	}
}
