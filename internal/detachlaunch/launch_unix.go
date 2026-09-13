// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package detachlaunch

import (
	"os/exec"
	"syscall"
)

// setDetached marks cmd to start as its own session leader (setsid), so the
// child survives the parent's controlling-terminal/session teardown
// (SIGHUP) instead of dying with it.
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// setDetachedFallback is identical to setDetached on POSIX: there is no
// second, more conservative attribute set to fall back to here (unlike
// Windows' CREATE_BREAKAWAY_FROM_JOB, which can itself cause Start to fail
// under a restrictive job object). Launch still calls this if a first
// Start() fails, but on POSIX that retry will fail identically (e.g. a
// missing binary) rather than succeed differently.
func setDetachedFallback(cmd *exec.Cmd) {
	setDetached(cmd)
}
