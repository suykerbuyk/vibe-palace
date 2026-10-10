// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package detachlaunch starts a detached, session-leader child process that
// survives the parent's own exit or session teardown — POSIX via setsid,
// Windows via a new process group plus explicit job-object breakaway — and
// reaps it in the background so a long-lived parent (such as `vp mcp`,
// which blocks on its server's Listen loop for the whole session) never
// accumulates zombie/defunct children.
//
// This backs fire-and-forget CLI subcommand launches (e.g. relaunching `vp
// drain summaries ...` right after something enqueues background work): the
// caller must return immediately, and the child's own stdout/stderr must
// land somewhere discoverable without ever blocking the parent on a read of
// them.
package detachlaunch

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LaunchFunc is the signature of Launch, named so callers can accept an
// injectable launcher (e.g. to stub out the real subprocess spawn in a
// test).
type LaunchFunc func(binary string, args []string, logPath string) (pid int, err error)

// ErrRefusedTestBinary is returned by Launch when it would otherwise detach a
// Go test binary — either by self-relaunching under `go test` (where
// os.Executable() is the package's `.test` binary) or when handed an explicit
// `*.test` path. Detaching a test binary is the ADR-014 fork-bomb regression:
// an unfiltered re-exec of a `.test` binary (notably cmd/vp's, whose TestMain
// re-runs the whole suite) re-triggers the very detach that spawned it, an
// unbounded recursive self-spawn that reboots the host. Callers already treat
// a Launch error as non-fatal and log it, so a sentinel error is more honest
// than a silent pid-0 no-op.
var ErrRefusedTestBinary = errors.New("detachlaunch: refusing to self-relaunch a Go test binary")

// runningUnderGoTest reports whether the resolved self-executable is a Go test
// binary. It replicates the two-pronged idiom of
// internal/surface/guard.go runningUnderGoTest() — the name ends in ".test",
// or the testing framework's flags are registered — rather than importing
// internal/surface (which would be a new dependency). The flag.Lookup half
// catches `go test -c -o <custom>` and `-exec` wrappers whose filename does
// not end in ".test". The suffix is checked on the RESOLVED self, not
// os.Args[0].
func runningUnderGoTest(self string) bool {
	if strings.HasSuffix(self, ".test") {
		return true
	}
	return flag.Lookup("test.v") != nil
}

// Launch starts binary (with args) as a detached child process and returns
// as soon as the child has started — it never blocks waiting for the child
// to run to completion.
//
// binary == "" resolves os.Executable() to self-relaunch the currently
// running binary (e.g. relaunching `vp` as `vp drain summaries ...`).
//
// The child's stdout and stderr are both redirected to logPath (opened for
// create/append), deliberately NOT an os.Pipe(): a pipe's buffer can fill
// and block the child if nothing drains it, and Launch keeps no long-lived
// reader around to do that. logPath is a plain file instead, so the child's
// own output — including any of its own errors — remains durably
// discoverable afterward, without the parent ever blocking on a read of it.
// The child's stdin is left nil, so it can never block waiting on
// interactive input either.
//
// Immediately after a successful Start(), a non-blocking goroutine calls
// cmd.Wait() to reap the child once it exits. Without this, a long-lived
// caller that calls Launch repeatedly over its lifetime (e.g. `vp mcp`,
// which blocks on its server's Listen loop until the session ends) would
// accumulate zombie/defunct child processes. Launch itself still returns the
// instant Start() does — it never waits on that goroutine.
//
// If the first Start() attempt (using setDetached's attribute set) fails,
// Launch retries exactly once with setDetachedFallback's more conservative
// set before giving up. This exists for Windows: CREATE_BREAKAWAY_FROM_JOB
// (part of setDetached there) makes CreateProcess fail outright with
// access-denied when the parent's job object does not grant
// JOB_OBJECT_LIMIT_BREAKAWAY_OK — a real, documented Windows behavior this
// package cannot verify from a non-Windows host, so failing open (retry
// without the flag, still detached, just not breakaway-protected) is the
// defensive choice over asserting the flag is always safe. On POSIX,
// setDetachedFallback is identical to setDetached, so a retry after a
// genuine failure (e.g. a missing binary) simply fails the same way again.
func Launch(binary string, args []string, logPath string) (pid int, err error) {
	if binary == "" {
		self, err := os.Executable()
		if err != nil {
			return 0, fmt.Errorf("detachlaunch: resolve self: %w", err)
		}
		// Fork-bomb guard: refuse to self-relaunch a Go test binary. Return
		// BEFORE startDetached so no process starts and no logPath file is
		// created. The production `vp` binary is never a `.test` and registers
		// no test flags, so this is a no-op there.
		if runningUnderGoTest(self) {
			return 0, ErrRefusedTestBinary
		}
		binary = self
	} else if strings.HasSuffix(filepath.Base(binary), ".test") {
		// Defense in depth: refuse an explicitly-named `*.test` binary too,
		// again before any process or log file.
		return 0, ErrRefusedTestBinary
	}

	pid, startErr := startDetached(binary, args, logPath, setDetached)
	if startErr == nil {
		return pid, nil
	}

	pid, fallbackErr := startDetached(binary, args, logPath, setDetachedFallback)
	if fallbackErr == nil {
		return pid, nil
	}
	return 0, fmt.Errorf("detachlaunch: start %s: %w (fallback attempt also failed: %v)", binary, startErr, fallbackErr)
}

// startDetached builds a fresh *exec.Cmd (a Cmd can only be Start()'d once,
// so a retry needs its own instance), applies attrs, and starts it. On
// success it hands the reaper goroutine responsibility for the returned pid
// to the caller's process table, exactly as Launch's own doc comment
// describes.
func startDetached(binary string, args []string, logPath string, attrs func(*exec.Cmd)) (pid int, err error) {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return 0, fmt.Errorf("open log %s: %w", logPath, err)
	}

	cmd := exec.Command(binary, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// A detached child must not pin a working directory: left unset it would
	// inherit the caller's cwd, which for the hook is the project checkout, so
	// the long-lived ingester would hold that directory open for its whole run.
	// The detached commands take the vault root by flag and need no cwd, so
	// point it at a directory nothing cares about keeping.
	cmd.Dir = os.TempDir()
	attrs(cmd)

	// Before the fork, mark every descriptor >= 3 the parent holds
	// close-on-exec, so the child inherits only stdin/stdout/stderr. Without
	// this a descriptor the caller inherited from its own host (an agent, an
	// IDE), or a vault-lock descriptor the parent is holding, would pass to the
	// child and stay open for the whole detached run. Go opens its own files
	// close-on-exec already; this covers the ones it did not open. No-op on
	// Windows, where the handle list is explicit (see the package comment).
	markInheritedFDsCloseOnExec()

	if startErr := cmd.Start(); startErr != nil {
		_ = logFile.Close()
		return 0, startErr
	}

	// The child inherited its own copy of logFile's descriptor at Start; the
	// parent's handle is no longer needed and must not be held open for the
	// child's (potentially long) lifetime.
	_ = logFile.Close()

	pid = cmd.Process.Pid

	// Non-blocking reaper: the caller must return the instant Start() does,
	// but something still has to eventually call Wait(), or the child
	// becomes a zombie once it exits. Run that call in the background
	// rather than inline.
	go func() {
		_ = cmd.Wait()
	}()

	return pid, nil
}
