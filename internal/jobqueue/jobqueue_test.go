// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package jobqueue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// writeJob writes a minimal job file <dir>/<name>.json with the given body,
// returning its path.
func writeJob(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write job %s: %v", path, err)
	}
	return path
}

func TestClaimBasic(t *testing.T) {
	dir := t.TempDir()
	jsonPath := writeJob(t, dir, "a", `{"n":1}`)

	procPath, data, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if procPath != jsonPath+".processing" {
		t.Errorf("procPath = %q, want %q", procPath, jsonPath+".processing")
	}
	if string(data) != `{"n":1}` {
		t.Errorf("data = %q", data)
	}
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Errorf("original json still present: err=%v", err)
	}
	if _, err := os.Stat(procPath); err != nil {
		t.Errorf("processing file missing: %v", err)
	}
}

func TestClaimEmptyDir(t *testing.T) {
	dir := t.TempDir()
	procPath, data, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if procPath != "" || data != nil {
		t.Errorf("Claim on empty dir = (%q, %v), want sentinel (\"\", nil)", procPath, data)
	}
}

func TestClaimMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	procPath, data, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim on missing dir returned error: %v", err)
	}
	if procPath != "" || data != nil {
		t.Errorf("Claim on missing dir = (%q, %v), want sentinel", procPath, data)
	}
}

func TestClaimDeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	writeJob(t, dir, "b", `{"n":2}`)
	writeJob(t, dir, "a", `{"n":1}`)
	writeJob(t, dir, "c", `{"n":3}`)

	procPath, _, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	want := filepath.Join(dir, "a.json.processing")
	if procPath != want {
		t.Errorf("procPath = %q, want %q (first in sorted order)", procPath, want)
	}
}

// TestClaimSkipsInFlightClaim proves an already-claimed job (renamed to
// ".processing" by someone else) does not match Claim's ".json" suffix
// filter and is left untouched.
func TestClaimSkipsInFlightClaim(t *testing.T) {
	dir := t.TempDir()
	jsonPath := writeJob(t, dir, "a", `{"n":1}`)
	procPath := jsonPath + ".processing"
	if err := os.Rename(jsonPath, procPath); err != nil {
		t.Fatalf("simulate claim: %v", err)
	}

	gotProc, data, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if gotProc != "" || data != nil {
		t.Errorf("Claim = (%q, %v), want sentinel (in-flight claim must not be re-claimed)", gotProc, data)
	}
	if _, err := os.Stat(procPath); err != nil {
		t.Errorf("in-flight claim disturbed: %v", err)
	}
}

// TestClaimDoesNotClobberOutstandingClaimOnReenqueue pins a real fix, distinct
// from TestClaimSkipsInFlightClaim above: that test has ONLY a ".processing"
// file (no fresh ".json" of the same name exists at all, so Claim's own
// ".json"-suffix listing never even considers it). This test covers the case the earlier fix
// missed: a job is claimed (renamed to ".processing"), and — because
// internal/summarize's queue filenames are deterministic — the SAME logical
// job gets re-enqueued while that claim is still outstanding, landing a
// FRESH ".json" file back at the original name. Before the fix, Claim's
// os.Rename(jsonPath, candidate) would silently REPLACE the existing
// ".processing" file (POSIX rename semantics), destroying the first
// claimant's still-in-flight claim. Claim must instead skip this candidate
// and leave both files exactly as they were.
func TestClaimDoesNotClobberOutstandingClaimOnReenqueue(t *testing.T) {
	dir := t.TempDir()
	jsonPath := writeJob(t, dir, "a", `{"n":1}`)
	procPath := jsonPath + ".processing"
	if err := os.Rename(jsonPath, procPath); err != nil {
		t.Fatalf("simulate outstanding claim: %v", err)
	}
	origData, err := os.ReadFile(procPath)
	if err != nil {
		t.Fatalf("read outstanding claim: %v", err)
	}

	// Simulate a re-enqueue of the same job identity landing back at the
	// original name while procPath is still claimed.
	freshData := `{"n":1,"attempts":1}`
	if err := os.WriteFile(jsonPath, []byte(freshData), 0o644); err != nil {
		t.Fatalf("simulate re-enqueue: %v", err)
	}

	gotProc, data, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if gotProc != "" || data != nil {
		t.Fatalf("Claim = (%q, %v), want sentinel — the fresh re-enqueue must not be claimable while the outstanding claim holds the candidate name", gotProc, data)
	}

	// Both files must be exactly as they were: the outstanding claim
	// untouched, the fresh re-enqueue untouched.
	gotOrig, err := os.ReadFile(procPath)
	if err != nil {
		t.Fatalf("outstanding claim disturbed: %v", err)
	}
	if string(gotOrig) != string(origData) {
		t.Errorf("outstanding claim content changed: got %q, want %q", gotOrig, origData)
	}
	gotFresh, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("fresh re-enqueue disturbed: %v", err)
	}
	if string(gotFresh) != freshData {
		t.Errorf("fresh re-enqueue content changed: got %q, want %q", gotFresh, freshData)
	}
}

// TestClaimRaceNoDoubleClaim launches many concurrent Claim calls against a
// directory holding N jobs and asserts every claimed procPath is unique and
// exactly N successful claims occur in total (never more, never a repeat).
func TestClaimRaceNoDoubleClaim(t *testing.T) {
	dir := t.TempDir()
	const n = 25
	for i := range n {
		writeJob(t, dir, fmt.Sprintf("job-%02d", i), fmt.Sprintf(`{"n":%d}`, i))
	}

	var (
		mu      sync.Mutex
		claimed = map[string]int{}
		wg      sync.WaitGroup
	)

	// Over-subscribe goroutines relative to jobs so contention is likely.
	for range n * 3 {
		wg.Go(func() {
			procPath, _, err := Claim(dir, time.Hour)
			if err != nil {
				t.Errorf("Claim: %v", err)
				return
			}
			if procPath == "" {
				return
			}
			mu.Lock()
			claimed[procPath]++
			mu.Unlock()
		})
	}
	wg.Wait()

	if len(claimed) != n {
		t.Fatalf("claimed %d distinct jobs, want %d", len(claimed), n)
	}
	for p, count := range claimed {
		if count != 1 {
			t.Errorf("job %s claimed %d times, want 1", p, count)
		}
	}
}

// TestClaimReclaimsStale proves a ".processing" file older than staleAge
// with no fresh job occupying its target name is reclaimed (renamed back to
// its original ".json" name) and then immediately claimable.
func TestClaimReclaimsStale(t *testing.T) {
	dir := t.TempDir()
	jsonPath := writeJob(t, dir, "a", `{"n":1}`)
	procPath := jsonPath + ".processing"
	if err := os.Rename(jsonPath, procPath); err != nil {
		t.Fatalf("orphan claim: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(procPath, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	gotProc, data, err := Claim(dir, 15*time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if gotProc != procPath {
		t.Errorf("procPath = %q, want %q (reclaimed then re-claimed)", gotProc, procPath)
	}
	if string(data) != `{"n":1}` {
		t.Errorf("data = %q", data)
	}
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Errorf("reclaimed json should have been re-claimed to processing: err=%v", err)
	}
}

// TestClaimDoesNotReclaimFresh proves a recent ".processing" claim (within
// staleAge) is presumed live and left untouched.
func TestClaimDoesNotReclaimFresh(t *testing.T) {
	dir := t.TempDir()
	jsonPath := writeJob(t, dir, "a", `{"n":1}`)
	procPath := jsonPath + ".processing"
	if err := os.Rename(jsonPath, procPath); err != nil {
		t.Fatalf("simulate live claim: %v", err)
	}

	gotProc, data, err := Claim(dir, 15*time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if gotProc != "" || data != nil {
		t.Errorf("Claim = (%q, %v), want sentinel (fresh claim must not be reclaimed)", gotProc, data)
	}
	if _, err := os.Stat(procPath); err != nil {
		t.Errorf("fresh claim disturbed: %v", err)
	}
}

// TestClaimStaleReclaimDropsWhenFreshJobPresent proves that when a stale
// claim's target name is already occupied by a fresh job (unusual, but
// possible), the stale claim is dropped rather than clobbering the fresh
// job.
func TestClaimStaleReclaimDropsWhenFreshJobPresent(t *testing.T) {
	dir := t.TempDir()
	jsonPath := writeJob(t, dir, "a", `{"n":1}`)
	procPath := jsonPath + ".processing"
	// Manufacture a stale processing file at procPath without consuming the
	// fresh job that already exists at jsonPath.
	if err := os.WriteFile(procPath, []byte(`{"n":0,"stale":true}`), 0o644); err != nil {
		t.Fatalf("write stale claim: %v", err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(procPath, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	gotProc, data, err := Claim(dir, 15*time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if gotProc != procPath {
		t.Errorf("procPath = %q, want %q (fresh job claimed)", gotProc, procPath)
	}
	if string(data) != `{"n":1}` {
		t.Errorf("data = %q, want fresh job bytes", data)
	}
	if _, err := os.Stat(procPath + ".processing"); !os.IsNotExist(err) {
		t.Errorf("unexpected double-claim artifact")
	}
}

func TestRequeueBelowCapRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeJob(t, dir, "a", `{"n":1,"attempts":0}`)

	procPath, data, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	var reencodeCalledWith int
	reencode := func(attempts int) ([]byte, error) {
		reencodeCalledWith = attempts
		var v map[string]any
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		v["attempts"] = attempts
		return json.Marshal(v)
	}
	if _, err := Requeue(procPath, 1, 5, reencode); err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if reencodeCalledWith != 1 {
		t.Errorf("reencode called with attempts=%d, want 1", reencodeCalledWith)
	}

	jsonPath := filepath.Join(dir, "a.json")
	if _, err := os.Stat(jsonPath); err != nil {
		t.Errorf("requeued job not present: %v", err)
	}
	if _, err := os.Stat(procPath); !os.IsNotExist(err) {
		t.Errorf(".processing not cleaned up after requeue")
	}

	// Reclaimable again.
	procPath2, data2, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("second Claim: %v", err)
	}
	if procPath2 != procPath {
		t.Errorf("second claim procPath = %q, want %q", procPath2, procPath)
	}
	var v map[string]any
	if err := json.Unmarshal(data2, &v); err != nil {
		t.Fatalf("unmarshal requeued data: %v", err)
	}
	if v["attempts"].(float64) != 1 {
		t.Errorf("requeued attempts = %v, want 1", v["attempts"])
	}
}

func TestRequeueDeadLetterAtCap(t *testing.T) {
	dir := t.TempDir()
	writeJob(t, dir, "a", `{"n":1}`)

	procPath, _, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	reencodeCalled := false
	reencode := func(attempts int) ([]byte, error) {
		reencodeCalled = true
		return nil, nil
	}
	deadLettered, err := Requeue(procPath, 5, 5, reencode)
	if err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if !deadLettered {
		t.Error("deadLettered = false, want true when attempts >= maxAttempts")
	}
	if reencodeCalled {
		t.Error("reencode must not be called once attempts >= maxAttempts")
	}

	jsonPath := filepath.Join(dir, "a.json")
	failedPath := jsonPath + ".failed"
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Errorf("active job still present after dead-letter: err=%v", err)
	}
	if _, err := os.Stat(procPath); !os.IsNotExist(err) {
		t.Errorf(".processing not cleaned up after dead-letter")
	}
	data, err := os.ReadFile(failedPath)
	if err != nil {
		t.Fatalf("dead-letter file missing: %v", err)
	}
	if string(data) != `{"n":1}` {
		t.Errorf("dead-letter bytes = %q, want original claimed bytes", data)
	}

	// Dead-lettered job is never claimable again.
	gotProc, gotData, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim after dead-letter: %v", err)
	}
	if gotProc != "" || gotData != nil {
		t.Errorf("Claim after dead-letter = (%q, %v), want sentinel", gotProc, gotData)
	}
}

func TestRequeueAttemptsAboveCapAlsoDeadLetters(t *testing.T) {
	dir := t.TempDir()
	writeJob(t, dir, "a", `{"n":1}`)
	procPath, _, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	deadLettered, err := Requeue(procPath, 9, 5, func(int) ([]byte, error) { return nil, nil })
	if err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if !deadLettered {
		t.Error("deadLettered = false, want true when attempts > maxAttempts")
	}
	if _, err := os.Stat(filepath.Join(dir, "a.json.failed")); err != nil {
		t.Errorf("dead-letter file missing: %v", err)
	}
}

func TestRequeueReencodeFailureRestoresClaim(t *testing.T) {
	dir := t.TempDir()
	writeJob(t, dir, "a", `{"n":1}`)
	procPath, _, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	boom := fmt.Errorf("boom")
	_, err = Requeue(procPath, 1, 5, func(int) ([]byte, error) { return nil, boom })
	if err == nil {
		t.Fatal("Requeue: want error on reencode failure")
	}

	jsonPath := filepath.Join(dir, "a.json")
	data, statErr := os.ReadFile(jsonPath)
	if statErr != nil {
		t.Fatalf("claim not restored after reencode failure: %v", statErr)
	}
	if string(data) != `{"n":1}` {
		t.Errorf("restored data = %q, want original bytes", data)
	}
	if _, err := os.Stat(procPath); !os.IsNotExist(err) {
		t.Errorf(".processing should have been renamed back: err=%v", err)
	}
}

// TestRequeueRestoreDoesNotClobberFreshReenqueue pins a real fix:
// restoreClaim's best-effort restore, on a reencode/write failure, must NOT
// blindly rename the claim back over its original name if a FRESH job of the
// same identity was re-enqueued there while this claim was outstanding — that
// would silently overwrite the fresh copy with this claim's stale bytes
// (including a stale Attempts count). The claim is left as an orphaned
// ".processing" file instead; reclaimStale's own existing clobber-avoidance
// safely drops an orphan like this once it goes stale, rather than
// overwriting whatever fresh job now occupies the target name.
func TestRequeueRestoreDoesNotClobberFreshReenqueue(t *testing.T) {
	dir := t.TempDir()
	writeJob(t, dir, "a", `{"n":1}`)
	procPath, _, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	// Simulate a fresh re-enqueue of the same identity landing at the
	// original name while procPath is still an outstanding claim.
	jsonPath := filepath.Join(dir, "a.json")
	freshData := `{"n":1,"attempts":0}`
	if err := os.WriteFile(jsonPath, []byte(freshData), 0o644); err != nil {
		t.Fatalf("simulate fresh re-enqueue: %v", err)
	}

	boom := fmt.Errorf("boom")
	if _, err := Requeue(procPath, 1, 5, func(int) ([]byte, error) { return nil, boom }); err == nil {
		t.Fatal("Requeue: want error on reencode failure")
	}

	// The fresh re-enqueue must be untouched.
	gotFresh, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("fresh re-enqueue disturbed: %v", err)
	}
	if string(gotFresh) != freshData {
		t.Errorf("fresh re-enqueue content changed: got %q, want %q", gotFresh, freshData)
	}
	// The old claim must be left in place (not restored over the fresh
	// file, not deleted) — an orphan for reclaimStale to handle later.
	if _, err := os.Stat(procPath); err != nil {
		t.Errorf("old claim missing, want it left as an orphan: %v", err)
	}
}

// TestRequeueWriteFailureRestoresClaim proves that when the rewrite to the
// original ".json" name fails, Requeue restores the claim (renames
// procPath back) and returns an error instead of losing the job.
func TestRequeueWriteFailureRestoresClaim(t *testing.T) {
	dir := t.TempDir()
	writeJob(t, dir, "a", `{"n":1}`)
	procPath, _, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	// Occupy the target ".json" name with a directory so os.WriteFile fails.
	jsonPath := filepath.Join(dir, "a.json")
	if err := os.Mkdir(jsonPath, 0o755); err != nil {
		t.Fatalf("mkdir blocker: %v", err)
	}

	_, err = Requeue(procPath, 1, 5, func(int) ([]byte, error) { return []byte(`{"n":1,"attempts":1}`), nil })
	if err == nil {
		t.Fatal("Requeue: want error when rewrite target is unwritable")
	}
	if _, err := os.Stat(procPath); err != nil {
		t.Errorf("claim should have been restored: %v", err)
	}
}

func TestDoneRemovesClaim(t *testing.T) {
	dir := t.TempDir()
	writeJob(t, dir, "a", `{"n":1}`)
	procPath, _, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := Done(procPath); err != nil {
		t.Fatalf("Done: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(leftovers) != 0 {
		t.Errorf("leftovers after Done: %v", leftovers)
	}
}

func TestDoneMissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	if err := Done(filepath.Join(dir, "nope.json.processing")); err == nil {
		t.Error("Done on missing file: want error, got nil")
	}
}

// TestClaimRequeueDoneFullRoundTrip exercises the whole lifecycle: claim,
// requeue below cap (reclaimable), claim again, then Done.
func TestClaimRequeueDoneFullRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeJob(t, dir, "a", `{"n":1,"attempts":0}`)

	procPath, data, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	reencode := func(attempts int) ([]byte, error) {
		var v map[string]any
		_ = json.Unmarshal(data, &v)
		v["attempts"] = attempts
		return json.Marshal(v)
	}
	if _, err := Requeue(procPath, 1, 5, reencode); err != nil {
		t.Fatalf("Requeue: %v", err)
	}

	procPath2, _, err := Claim(dir, time.Hour)
	if err != nil {
		t.Fatalf("re-Claim: %v", err)
	}
	if procPath2 == "" {
		t.Fatal("re-Claim: want claimable job after below-cap requeue")
	}
	if err := Done(procPath2); err != nil {
		t.Fatalf("Done: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(leftovers) != 0 {
		t.Errorf("leftovers after full round trip: %v", leftovers)
	}
}
