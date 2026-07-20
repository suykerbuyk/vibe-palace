// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build windows

package vaultlock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// We lock a single fixed byte at offset 0 rather than the whole file. The lock
// sidecar is empty (0 bytes), and LockFileEx over a zero-length range is a
// no-op on Windows; locking byte 0 of an empty file is legal and the standard
// idiom. offset 0 / length 1 => low=1, high=0 for both lock and unlock.
const (
	lockRangeLow  uint32 = 1
	lockRangeHigh uint32 = 0
)

// flockExclusive acquires an exclusive lock on f, blocking until it is
// available. LOCKFILE_EXCLUSIVE_LOCK without LOCKFILE_FAIL_IMMEDIATELY makes
// LockFileEx block, matching the blocking LOCK_EX semantics of the unix path.
//
// Re-acquire semantics: Windows byte-range locks are per-HANDLE, and each
// vaultlock.Acquire opens a fresh handle on the sidecar. A second same-path
// Acquire in one process therefore contends against the first handle and
// blocks forever — the ADR-003 "double-acquire" trap holds on Windows too, but
// via per-handle contention rather than unix's per-open-file-description
// mechanism. The trap has the same shape (blocking contention) on both
// platforms, so ADR-003's "RMW callers must not double-acquire" guidance stays
// true everywhere.
func flockExclusive(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		lockRangeLow,
		lockRangeHigh,
		&overlapped,
	)
}

// flockTryExclusive is the non-blocking companion to flockExclusive. Adding
// LOCKFILE_FAIL_IMMEDIATELY makes LockFileEx return at once instead of blocking:
// on contention it fails with ERROR_LOCK_VIOLATION, which maps to ok=false; any
// other failure is a real error.
func flockTryExclusive(f *os.File) (ok bool, err error) {
	var overlapped windows.Overlapped
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		lockRangeLow,
		lockRangeHigh,
		&overlapped,
	)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return false, err
}

// funlock releases the exclusive lock on f over the same offset 0 / length 1
// range taken by flockExclusive. It must run before the handle closes; the OS
// also drops the lock implicitly when the handle closes or the process exits.
func funlock(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0,
		lockRangeLow,
		lockRangeHigh,
		&overlapped,
	)
}
