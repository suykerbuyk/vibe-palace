// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/knights-analytics/hugot"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// withStalledDownload swaps the downloadModel seam for a fake that blocks
// until unblock is closed, then returns simErr. It signals startedCh the
// moment the fake begins running, so a caller can tell the download actually
// started before racing anything against it. The real seam is restored by
// t.Cleanup.
func withStalledDownload(t *testing.T, unblock <-chan struct{}, startedCh chan<- struct{}, simErr error) {
	t.Helper()
	old := downloadModel
	downloadModel = func(modelName, destination string, options hugot.DownloadOptions) (string, error) {
		close(startedCh)
		<-unblock
		return "", simErr
	}
	t.Cleanup(func() { downloadModel = old })
}

// TestNewONNXDownloadTimeoutReturnsPromptlyInsteadOfHanging is verification
// point 2 of the task's Plan (2026-09-12): with modelDownloadTimeout shrunk
// for the test and downloadModel replaced by a fake that never returns on its
// own, NewONNX must still return a clean timeout error promptly instead of
// blocking for the real 10-minute default -- proving the goroutine+select
// wrapping around hugot.DownloadModel actually bounds the caller's wait.
func TestNewONNXDownloadTimeoutReturnsPromptlyInsteadOfHanging(t *testing.T) {
	dir := t.TempDir()
	modelName := "sentence-transformers/all-MiniLM-L6-v2"

	unblock := make(chan struct{})
	started := make(chan struct{})
	withStalledDownload(t, unblock, started, errors.New("simulated stalled download"))
	// Let the leaked goroutine's fake download finish once the test is done,
	// so it doesn't outlive the test and trip a leak detector.
	t.Cleanup(func() { close(unblock) })

	oldTimeout := modelDownloadTimeout
	modelDownloadTimeout = 50 * time.Millisecond
	t.Cleanup(func() { modelDownloadTimeout = oldTimeout })

	start := time.Now()
	_, err := NewONNX(modelName, dir, 0, 1)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("NewONNX error = %v, want a timeout error", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("NewONNX took %s, want ~50ms (proves it does not hang behind a stalled download)", elapsed)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("fake downloadModel never started -- test setup is broken, result above is not trustworthy")
	}
}

// TestNewONNXDownloadTimeoutKeepsLockHeldUntilLeakedGoroutineFinishes is
// verification point 3 of the task's Plan (2026-09-12) -- the one specific
// test the plan calls out as the test that would have caught both bugs the
// review process found during planning: a first draft that released the
// model-cache lock immediately on timeout (Review 2026-09-12's High finding),
// and a second draft whose sync.Once guard stopped a DOUBLE release but not a
// PREMATURE one, since the fast synchronous defer still wins the race against
// the slow leaked goroutine (Review 2026-09-12b's High finding).
//
// It proves the lock stays held across the timeout -- a second Acquire on the
// same lock target must block -- until the leaked goroutine's simulated
// download actually completes, at which point the lock must become
// available.
func TestNewONNXDownloadTimeoutKeepsLockHeldUntilLeakedGoroutineFinishes(t *testing.T) {
	dir := t.TempDir()
	modelName := "sentence-transformers/all-MiniLM-L6-v2"
	lockTarget := modelCacheLockPath(dir, modelName)

	unblock := make(chan struct{})
	started := make(chan struct{})
	withStalledDownload(t, unblock, started, errors.New("simulated stalled download"))

	oldTimeout := modelDownloadTimeout
	modelDownloadTimeout = 50 * time.Millisecond
	t.Cleanup(func() { modelDownloadTimeout = oldTimeout })

	_, err := NewONNX(modelName, dir, 0, 1)
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("fake downloadModel never started -- test setup is broken")
	}

	// The lock must still be held: a second Acquire on the same target must
	// NOT succeed while the leaked goroutine's download is still "running".
	acquired := make(chan struct{})
	acquireErr := make(chan error, 1)
	go func() {
		release, err := vaultlock.Acquire(dir, lockTarget)
		if err != nil {
			acquireErr <- err
			return
		}
		release()
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("second Acquire succeeded before the leaked download goroutine finished -- " +
			"the lock was released prematurely, reintroducing the model-cache race this fix exists to prevent")
	case err := <-acquireErr:
		t.Fatalf("second Acquire: %v", err)
	case <-time.After(300 * time.Millisecond):
		// Expected: still blocked, well past modelDownloadTimeout's 50ms.
	}

	// Now let the leaked goroutine's simulated download finish. Its release()
	// call should follow shortly, and the lock should become available.
	close(unblock)

	select {
	case <-acquired:
		// Good: the lock became available once the leaked goroutine finished.
	case err := <-acquireErr:
		t.Fatalf("second Acquire: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("lock was never released after the leaked goroutine's download finished")
	}
}

// TestNewONNXDownloadSuccessAfterTimeoutStillReleasesLock is the mirror image
// of the test above: when the leaked goroutine's download eventually
// SUCCEEDS (rather than erroring) after NewONNX has already returned a
// timeout to its caller, the lock-transfer path must still release the lock
// -- a success result must not be treated differently by the goroutine that
// owns the handoff.
func TestNewONNXDownloadSuccessAfterTimeoutStillReleasesLock(t *testing.T) {
	dir := t.TempDir()
	modelName := "sentence-transformers/all-MiniLM-L6-v2"
	lockTarget := modelCacheLockPath(dir, modelName)

	unblock := make(chan struct{})
	started := make(chan struct{})
	withStalledDownload(t, unblock, started, nil)

	oldTimeout := modelDownloadTimeout
	modelDownloadTimeout = 50 * time.Millisecond
	t.Cleanup(func() { modelDownloadTimeout = oldTimeout })

	_, err := NewONNX(modelName, dir, 0, 1)
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("fake downloadModel never started -- test setup is broken")
	}

	close(unblock) // the leaked goroutine's fake download "succeeds" now

	release, err := vaultlock.AcquireWithTimeout(dir, lockTarget, 5*time.Second)
	if err != nil {
		t.Fatalf("lock was never released after the leaked goroutine's download succeeded: %v", err)
	}
	release()
}
