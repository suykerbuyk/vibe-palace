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
	"fmt"
	"os"
	"os/exec"
)

// LaunchFunc is the signature of Launch, named so callers can accept an
// injectable launcher (e.g. to stub out the real subprocess spawn in a
// test).
type LaunchFunc func(binary string, args []string, logPath string) (pid int, err error)

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
		binary = self
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
	attrs(cmd)

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
