// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !windows

package archive

import (
	"os/exec"
	"syscall"
)

// detachFromTerminal starts the child in a new session, so it has no controlling
// terminal and cannot open the terminal device to ask for a passphrase.
func detachFromTerminal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
