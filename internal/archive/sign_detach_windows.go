// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package archive

import (
	"os/exec"
	"syscall"
)

// Process creation flags (Windows process-creation docs).
const (
	// detachedProcess: the child has no console at all, so it cannot inherit the
	// parent's console and open CONIN$ to read a passphrase; the attempt fails.
	detachedProcess = 0x00000008
	// createNoWindow is carried with it as belt and braces; the OS ignores it
	// when detachedProcess is also set.
	createNoWindow = 0x08000000
)

// detachFromTerminal starts the child without a console. COMPILE-CHECKED ONLY:
// Windows CI does not run the signing tests, so this behavior has not been
// exercised against a real ssh-keygen.
func detachFromTerminal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | createNoWindow}
}
