// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package detachlaunch

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// setDetached marks cmd to start detached from the parent's console and
// process group. CREATE_NEW_PROCESS_GROUP + DETACHED_PROCESS keep the child
// from receiving console control events meant for the parent (Ctrl+C,
// window close) and from inheriting the parent's console.
// CREATE_BREAKAWAY_FROM_JOB is defensive: it lets the child escape a Job
// Object with KILL_ON_JOB_CLOSE — common under Windows Terminal, VS Code, and
// ConPTY — that would otherwise kill the child the moment the parent's job
// is torn down, even though the child already sits in its own process
// group.
//
// All three flags are sourced from golang.org/x/sys/windows rather than
// stdlib syscall for consistency — this project already depends on
// golang.org/x/sys directly and already uses it this way in
// internal/vaultlock/flock_windows.go.
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS | windows.CREATE_BREAKAWAY_FROM_JOB,
	}
}

// setDetachedFallback drops CREATE_BREAKAWAY_FROM_JOB. CreateProcess refuses
// that flag outright with access-denied when the parent's job object does
// not grant JOB_OBJECT_LIMIT_BREAKAWAY_OK — a real, documented Windows
// behavior this package cannot verify from a non-Windows host, so Launch
// treats ANY first-attempt Start failure as a reason to retry once with this
// more conservative set rather than asserting the defensive flag is safe
// sight-unseen. The child still gets its own process group / console
// detachment either way; only the job-breakaway protection is lost on this
// fallback path, leaving it vulnerable to KILL_ON_JOB_CLOSE if the job
// actually enforces it — strictly better than failing to start at all.
func setDetachedFallback(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}
