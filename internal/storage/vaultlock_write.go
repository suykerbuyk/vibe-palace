// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// This file holds the shared vault write primitives, deliberately side by
// side, because THE ONE THING A READER MUST NOT GET WRONG IS WHICH OF THEM
// TAKES THE LOCK:
//
//   - lockedWrite  ACQUIRES the per-path lock, then does a whole-file replace.
//   - LockedUpdate ACQUIRES the per-path lock, then does read → transform →
//     replace, all inside the one critical section.
//   - appendUnderLock does NOT acquire anything. The caller must already hold it.
//
// Getting that backwards does not produce an error. vaultlock.Acquire is a
// blocking LOCK_EX with no timeout, so a same-path second acquire in the same
// process HANGS FOREVER (ADR-003, "RMW callers must not double-acquire").
// Naming them apart is the only guard the compiler cannot give us.

// lockedWrite serializes a blind whole-file write of absPath against any other
// vault writer of the same path (CLI vs MCP, or concurrent goroutines) by
// holding the per-path advisory lock across the write. RMW callers that already
// hold the lock must call atomicfile.Write directly instead.
func (v *Vault) lockedWrite(absPath string, data []byte, opts ...atomicfile.Option) error {
	release, err := vaultlock.Acquire(v.Root, absPath)
	if err != nil {
		return fmt.Errorf("storage: lock %s: %w", absPath, err)
	}
	defer release()
	return atomicfile.Write(v.Root, absPath, data, opts...)
}

// LockedUpdate is the read-modify-write counterpart to lockedWrite: it holds the
// per-path advisory lock across the WHOLE read → transform → write sequence, so
// no other vault writer of absPath (CLI vs MCP, or a concurrent goroutine) can
// land an edit in the window between the read and the write. Locking only the
// final write is too late — the lost-update window opens at the read (ADR-003).
//
// It is a package function rather than a *Vault method because its callers
// outside this package hold a bare vault root string, and taking vaultRoot
// explicitly makes it read like the two primitives it is composed of:
// vaultlock.Acquire(vaultRoot, absPath) and atomicfile.Write(vaultRoot, absPath).
//
// 🔴 It calls atomicfile.Write DIRECTLY, and must keep doing so. Routing the
// write through v.lockedWrite would re-acquire the SAME per-path lock while this
// function still holds it, and vaultlock.Acquire is a blocking LOCK_EX with no
// timeout — the result is a permanent self-deadlock, not an error anyone can see
// or recover from. See the file header above and ADR-003, "RMW callers must not
// double-acquire".
//
// transform receives the current file bytes and returns the bytes to write.
// Returning a NIL slice with a nil error means "nothing to do": the file is left
// untouched and LockedUpdate returns nil, which is what lets a caller decide
// whether a write is needed from content it could only read under the lock.
// Returning an EMPTY, non-nil slice writes an empty file. A transform error is
// returned unwrapped, so callers keep their own error vocabulary.
//
// atomicfile.Write owns the temp file, the rename and the surface stamp, so
// callers must not hand-roll a sidecar .tmp and must not stamp again afterwards.
func LockedUpdate(vaultRoot, absPath string, transform func(current []byte) ([]byte, error), opts ...atomicfile.Option) error {
	release, err := vaultlock.Acquire(vaultRoot, absPath)
	if err != nil {
		return fmt.Errorf("storage: lock %s: %w", absPath, err)
	}
	defer release()

	current, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("storage: read %s: %w", absPath, err)
	}
	next, err := transform(current)
	if err != nil {
		return err
	}
	if next == nil {
		return nil
	}
	return atomicfile.Write(vaultRoot, absPath, next, opts...)
}

// appendUnderLock appends data to the vault file at absPath and records the
// surface stamp for it.
//
// 🔴 THE CALLER MUST ALREADY HOLD vaultlock.Acquire(v.Root, absPath). This
// primitive deliberately does NOT acquire it, and that is not an oversight to
// tidy up later: both callers hold the lock across a read→compute→append
// sequence that is the whole point of their critical section — the commit log
// re-reads itself to dedup, and the iteration writer derives the next number
// from the file. Moving the acquire in here would either narrow those critical
// sections to the append alone (reintroducing the check-then-act races they
// were written to close) or deadlock on the second acquire. See ADR-003.
//
// # Why this exists at all
//
// It is family F4 of the Option E funnel work: the append counterpart to
// atomicfile.Write. Before it, the two append writers each hand-rolled
// OpenFile + write + close + v.stamp, and each stamped by hand — which meant
// "did this writer remember to stamp?" was a question you answered by reading
// the call site. Now the primitive owns it and there is nothing to remember.
//
// # What it deliberately does NOT do
//
// It does not fail-stop, and it does not consult surface.CheckCompatible. The
// stamp stays exactly what StampForPath documents it to be: a BEST-EFFORT
// POST-WRITE side effect whose failure is logged and never propagated. This
// primitive is about ROUTING, not about the compatibility gate — gate placement
// is a later phase and is not settled. Do not smuggle a check in here.
//
// # Durability
//
// No fsync, matching both call sites as they stand: the rename-based
// atomicfile.Write offers WithFsync and neither append writer asked for it. That
// is a deliberate parity choice, not an oversight — adding an fsync here would
// silently change the durability profile of the commit log and iterations.md on
// every wrap. If one of them ever needs it, add it as an explicit option with a
// caller that uses it, rather than as a default nobody asked for.
func (v *Vault) appendUnderLock(absPath string, data []byte) error {
	// O_APPEND rather than O_RDWR + Seek(0, io.SeekEnd): both land at EOF under
	// a held lock, but O_APPEND positions atomically at write time, so it is the
	// one that is still correct if a future caller's lock discipline slips.
	f, err := os.OpenFile(absPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write: %w", err)
	}
	// Checked, not deferred. A deferred Close drops its error, and on a delayed
	// -allocation filesystem that is where a short write is reported — the one
	// place an append can lose data silently.
	if err := f.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	v.stamp(absPath)
	return nil
}
