// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package cli

import (
	"os"

	"golang.org/x/term"
)

// IsTerminal reports whether f is a real terminal. Unlike a character-device
// check (fi.Mode()&os.ModeCharDevice != 0 — true for /dev/null too, which is
// a character device but never a terminal), this asks the OS directly via
// golang.org/x/term, so /dev/null and a shell pipe both correctly report
// false.
//
// The VP_ASSUME_TTY=1 escape hatch forces true — used by integration tests
// that drive interactive prompts through a pipe. Production code should
// never rely on this override; it has no user-facing documentation.
func IsTerminal(f *os.File) bool {
	if os.Getenv("VP_ASSUME_TTY") == "1" {
		return true
	}
	return term.IsTerminal(int(f.Fd()))
}
