// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package detachlaunch

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Child-process env for TestFDSweepHelper, set by the parent test below.
const (
	fdCheckEnv       = "VP_DETACHLAUNCH_FDCHECK"
	fdCheckOutEnv    = "VP_DETACHLAUNCH_FDCHECK_OUT"
	fdCheckSpecEnv   = "VP_DETACHLAUNCH_FDCHECK_SPEC" // "fd:marker,fd:marker"
	fdCheckCwdPrefix = "cwd="
)

// TestFDSweepHelper is the re-exec child for TestLaunchDetachesCwdAndClosesInheritedFDs.
// Guarded by an env var so a plain `go test` run skips it. It reports its own
// working directory and, for each (fd, marker) pair the parent planted, whether
// that specific inherited descriptor is still open in this child — proven by
// CONTENT, not by the raw fd number: a descriptor whose number the child's own
// runtime happens to have reused points at a different file and reads back a
// different marker, so only a genuinely inherited descriptor is reported leaked.
func TestFDSweepHelper(t *testing.T) {
	if os.Getenv(fdCheckEnv) != "1" {
		t.Skip("not invoked as the fd-sweep test helper process")
	}
	out := os.Getenv(fdCheckOutEnv)
	spec := os.Getenv(fdCheckSpecEnv)

	var b strings.Builder
	cwd, _ := os.Getwd()
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	fmt.Fprintf(&b, "%s%s\n", fdCheckCwdPrefix, cwd)

	for _, pair := range strings.Split(spec, ",") {
		if pair == "" {
			continue
		}
		fdStr, marker, _ := strings.Cut(pair, ":")
		fd, err := strconv.Atoi(fdStr)
		if err != nil {
			continue
		}
		buf := make([]byte, len(marker))
		n, rerr := syscall.Pread(fd, buf, 0)
		leaked := rerr == nil && string(buf[:n]) == marker
		fmt.Fprintf(&b, "fd %d leaked=%v\n", fd, leaked)
	}

	_ = os.WriteFile(out, []byte(b.String()), 0o644)
	os.Exit(0)
}

// TestLaunchDetachesCwdAndClosesInheritedFDs is the 4-S6 test: a detached child
// starts in os.TempDir() (never the caller's checkout) and inherits none of the
// non-close-on-exec descriptors the parent held — specifically a planted
// duplicate and a stand-in vault-lock descriptor. It asserts only those two
// planted descriptors and the working directory, as the plan requires, so
// descriptors the Go test runtime opens for itself are never asserted.
func TestLaunchDetachesCwdAndClosesInheritedFDs(t *testing.T) {
	// A non-".test" copy of this binary: Launch's fork-bomb guard refuses to
	// detach a `.test` binary, so the real os.Executable() cannot be used here.
	helper := testHelperBinary(t)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "result.txt")
	logPath := filepath.Join(dir, "child.log")

	// Plant two descriptors the Go runtime did NOT open close-on-exec: back
	// each with a file carrying a unique marker, then place a duplicate at a
	// fixed high fd number. syscall.Dup2 clears FD_CLOEXEC on the new fd, so
	// without the sweep these would be inherited by the child. High numbers
	// keep them clear of the fds the runtime uses for itself.
	plant := func(num int, marker string) {
		f, err := os.CreateTemp(dir, "fd-*")
		if err != nil {
			t.Fatalf("CreateTemp: %v", err)
		}
		if _, err := f.WriteString(marker); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		if err := syscall.Dup2(int(f.Fd()), num); err != nil {
			t.Fatalf("dup2 onto %d: %v", num, err)
		}
		_ = f.Close() // the duplicate at num stays open and non-close-on-exec
		t.Cleanup(func() { _ = syscall.Close(num) })
	}
	const (
		plantedFD  = 231 // the "planted non-close-on-exec duplicate"
		lockFD     = 232 // the stand-in vault-lock descriptor
		plantedMrk = "vp-detachlaunch-planted-marker"
		lockMrk    = "vp-detachlaunch-lock-marker"
	)
	plant(plantedFD, plantedMrk)
	plant(lockFD, lockMrk)

	t.Setenv(fdCheckEnv, "1")
	t.Setenv(fdCheckOutEnv, outPath)
	t.Setenv(fdCheckSpecEnv, fmt.Sprintf("%d:%s,%d:%s", plantedFD, plantedMrk, lockFD, lockMrk))

	if _, err := Launch(helper, []string{"-test.run=^TestFDSweepHelper$"}, logPath); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// The child writes outPath then exits. Poll briefly for it.
	var data []byte
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(outPath); err == nil && len(b) > 0 {
			data = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(data) == 0 {
		t.Fatalf("child never wrote %s (see %s)", outPath, logPath)
	}
	report := string(data)

	wantCwd := os.TempDir()
	if resolved, err := filepath.EvalSymlinks(wantCwd); err == nil {
		wantCwd = resolved
	}
	if !strings.Contains(report, fdCheckCwdPrefix+wantCwd+"\n") {
		t.Errorf("child cwd is not os.TempDir() (%q); report:\n%s", wantCwd, report)
	}
	if !strings.Contains(report, fmt.Sprintf("fd %d leaked=false", plantedFD)) {
		t.Errorf("planted descriptor %d leaked into the child; report:\n%s", plantedFD, report)
	}
	if !strings.Contains(report, fmt.Sprintf("fd %d leaked=false", lockFD)) {
		t.Errorf("vault-lock descriptor %d leaked into the child; report:\n%s", lockFD, report)
	}
}
