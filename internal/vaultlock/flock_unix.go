// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build unix

package vaultlock

import (
	"errors"
	"os"
	"syscall"
)

// flockExclusive acquires an exclusive advisory lock on f, blocking until it is
// available.
func flockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// flockTryExclusive attempts a NON-BLOCKING exclusive advisory lock on f. It
// returns ok=true when the lock was taken, ok=false when another holder already
// has it (EWOULDBLOCK), and a non-nil error only on any other syscall failure.
func flockTryExclusive(f *os.File) (ok bool, err error) {
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return false, err
}

// flockShared acquires a SHARED advisory lock on f, blocking until it is
// available. Shared locks are mutually compatible; they block, and are blocked
// by, an exclusive lock on the same inode. Writers take this on the .vp-locks/.gc
// coordination file across their open→flock window so a GC sweep (which takes .gc
// exclusive) cannot unlink a sidecar out from under an in-flight acquire
// (ADR-003 amendment 2026-10-27).
func flockShared(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_SH)
}

// flockTryShared attempts a NON-BLOCKING shared advisory lock on f. It returns
// ok=true when the lock was taken, ok=false when an exclusive holder already has
// it (EWOULDBLOCK), and a non-nil error only on any other syscall failure. The
// error mapping mirrors flockTryExclusive exactly.
func flockTryShared(f *os.File) (ok bool, err error) {
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return false, err
}

// funlock releases the advisory lock on f.
func funlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
