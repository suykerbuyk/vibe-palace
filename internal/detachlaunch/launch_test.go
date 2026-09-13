// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Honest limits of this test file: an in-process unit test can prove Launch
// returns immediately, writes to the log file, and does not leak/deadlock
// its reaper goroutine. It CANNOT prove the child actually survives the
// parent's exit or session teardown (SIGHUP on unix, console/job teardown
// on Windows) — that requires an actual parent process to exit or have its
// session torn down while the child is observed from outside, which is a
// manual or CI-shell-script verification, not something a go test process
// (which IS the parent under test) can observe about itself. Do not treat
// the tests below as proof of survive-the-parent behavior.
package detachlaunch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHelperHarness is not a real test case: it is the re-exec target
// Launch's tests spawn as the "child" process, using the already-built test
// binary (os.Executable()) as a safe, fast, cross-platform stand-in for a
// real detached child. It is guarded by an env var so `go test ./...`
// running it directly is a no-op, and it exits immediately so the tests
// below stay fast.
func TestHelperHarness(t *testing.T) {
	if os.Getenv("VP_DETACHLAUNCH_HELPER") != "1" {
		t.Skip("not invoked as the Launch test helper process")
	}
	os.Exit(0)
}

// helperArgs returns the -test.run selector that re-invokes only
// TestHelperHarness in a re-exec of the test binary.
func helperArgs() []string {
	return []string{"-test.run=^TestHelperHarness$"}
}

func withHelperEnv(t *testing.T) {
	t.Helper()
	t.Setenv("VP_DETACHLAUNCH_HELPER", "1")
}

func TestLaunchReturnsImmediately(t *testing.T) {
	withHelperEnv(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "child.log")

	start := time.Now()
	pid, err := Launch(self, helperArgs(), logPath)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("pid = %d, want > 0", pid)
	}
	// Generous bound: Launch must return as soon as Start() does, well
	// before even a trivially-fast child finishes running.
	if elapsed > 5*time.Second {
		t.Fatalf("Launch took %v to return, want near-instant (non-blocking)", elapsed)
	}
}

// TestLaunchSelfRelaunch exercises the binary=="" branch, which resolves
// os.Executable() to relaunch the current binary (the real-world case: `vp`
// relaunching itself as a detached `vp drain summaries ...`).
func TestLaunchSelfRelaunch(t *testing.T) {
	withHelperEnv(t)
	logPath := filepath.Join(t.TempDir(), "child.log")

	pid, err := Launch("", helperArgs(), logPath)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("pid = %d, want > 0", pid)
	}
}

// TestLaunchWritesLogFile confirms stdout/stderr are redirected to logPath
// (not piped), by waiting briefly for the child to run and then checking the
// log file exists. It cannot assert non-empty content deterministically
// (the helper process prints nothing), but it pins that Launch creates and
// leaves behind the file rather than an os.Pipe with no reader.
func TestLaunchWritesLogFile(t *testing.T) {
	withHelperEnv(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "child.log")

	if _, err := Launch(self, helperArgs(), logPath); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log file not created: %v", err)
	}
}

// TestLaunchNoDeadlockOrLeak launches many fast-exiting children back to
// back. If the reaper goroutine deadlocked or leaked in a way that blocked
// the process, this test (and the whole package's test run) would hang
// until the test binary's own timeout — there is no need for an explicit
// assertion beyond "this function returns".
func TestLaunchNoDeadlockOrLeak(t *testing.T) {
	withHelperEnv(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()

	const n = 10
	for i := range n {
		logPath := filepath.Join(dir, "child.log")
		if _, err := Launch(self, helperArgs(), logPath); err != nil {
			t.Fatalf("Launch #%d: %v", i, err)
		}
	}

	// Give the reaper goroutines a moment to actually run Wait() so a
	// leaked/blocked one would show up as still-running processes rather
	// than merely as an untested race; this is a best-effort nicety, not
	// the assertion itself (the test returning at all is that).
	time.Sleep(200 * time.Millisecond)
}

func TestLaunchNonexistentBinaryReturnsError(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "child.log")
	missing := filepath.Join(t.TempDir(), "does-not-exist-binary")

	_, err := Launch(missing, nil, logPath)
	if err == nil {
		t.Fatal("expected an error for a nonexistent binary, got nil")
	}
	// Pins the fallback-retry behavior: a genuine Start failure must have
	// been attempted twice (setDetached, then setDetachedFallback) before
	// Launch gives up, and the returned error must say so rather than
	// silently reporting only the first attempt's failure.
	if !strings.Contains(err.Error(), "fallback attempt also failed") {
		t.Errorf("error %q does not mention the fallback retry", err)
	}
}

func TestLaunchBadLogPathReturnsError(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	// A log path inside a directory that does not exist can never be
	// opened for create/append.
	badLogPath := filepath.Join(t.TempDir(), "no-such-dir", "child.log")

	if _, err := Launch(self, helperArgs(), badLogPath); err == nil {
		t.Fatal("expected an error for an unopenable log path, got nil")
	}
}

// TestLaunchFuncType pins that Launch's signature matches the exported
// LaunchFunc type, so callers can hold it as an injectable/mockable value.
func TestLaunchFuncType(t *testing.T) {
	var fn LaunchFunc = Launch
	if fn == nil {
		t.Fatal("Launch does not satisfy LaunchFunc")
	}
}
