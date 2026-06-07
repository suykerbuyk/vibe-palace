// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package vaultlock

import (
	"os"
	"syscall"
)

// flockExclusive acquires an exclusive advisory lock on f, blocking until it is
// available.
func flockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// funlock releases the advisory lock on f.
func funlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
