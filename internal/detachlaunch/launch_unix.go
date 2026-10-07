// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package detachlaunch

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
)

// setDetached marks cmd to start as its own session leader (setsid), so the
// child survives the parent's controlling-terminal/session teardown
// (SIGHUP) instead of dying with it.
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// markInheritedFDsCloseOnExec sets FD_CLOEXEC on every open descriptor >= 3,
// so a child started right after it inherits only stdin/stdout/stderr. It
// enumerates the process's own descriptors through the kernel's fd directory
// (/proc/self/fd on Linux, /dev/fd on darwin) and skips the directory handle
// it opened to read them. Setting FD_CLOEXEC only affects exec, never ordinary
// use, so a descriptor the parent still needs keeps working in the parent; it
// is simply not passed across the fork+exec. Any failure is ignored: the sweep
// is a best-effort tightening on top of Go's own close-on-exec defaults, not a
// correctness gate, and a parent with no readable fd directory just falls back
// to Go's defaults.
func markInheritedFDsCloseOnExec() {
	dir := "/proc/self/fd"
	if runtime.GOOS == "darwin" {
		dir = "/dev/fd"
	}
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return
	}
	self := int(d.Fd())
	for _, name := range names {
		fd, err := strconv.Atoi(name)
		if err != nil || fd < 3 || fd == self {
			continue
		}
		syscall.CloseOnExec(fd)
	}
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
