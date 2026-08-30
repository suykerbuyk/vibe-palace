// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"errors"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// LockPosture selects how a manifest read-modify-write in this package
// serializes against a concurrent writer of the SAME manifest path.
//
// Two sites take the per-manifest lock (ADR-003): Create, across its
// ReadManifest -> idempotency compare -> .bak rename -> compress -> WriteManifest
// window, and the LinkSessionNote family, across its ReadManifest -> mutate ->
// WriteManifest. Nothing else in this package locks, and the lock is
// deliberately NOT inside WriteManifest: WriteManifest is shared by both paths
// and by vaultaudit's documented "note first, then manifest — sequential, never
// nested" backfill sequence, and a non-reentrant LOCK_EX inside the shared
// primitive would turn that sequence into a lock-order constraint.
//
// # 🔴 THE HAZARD THIS TYPE EXISTS TO MAKE VISIBLE
//
// vaultlock's exclusive lock is a bare syscall.Flock(fd, LOCK_EX): no LOCK_NB,
// NO TIMEOUT, and not reentrant. Under contention LockBlocking WAITS, forever if
// it must. That is correct for a fail-stop caller (the MCP server, the
// mutates()-wrapped `vp archive create` / `vp archive link`), which the whole
// rest of the vault already behaves like.
//
// It is WRONG for the hook. `cmd/vp/commands.go` registers cmdHook UNWRAPPED, and
// `cmd/vp/cmd_hook.go` passes context.Background() into hook.Run — there is no
// timeout anywhere on that path. A blocking acquire there does not degrade a
// capture, it hangs SessionEnd with no error and no log, which is strictly worse
// than losing the archive. The hook therefore asks for LockNonBlocking and
// routes ErrManifestLocked into the non-fatal warn branches it already has.
//
// 🔴 A FUTURE CALLER THAT COPIES THE BLOCKING FORM ONTO A NO-TIMEOUT PATH
// REINTRODUCES THAT HANG. Before you leave the zero value in place, check that
// the caller can afford to wait forever.
type LockPosture int

const (
	// LockBlocking waits for the manifest lock (vaultlock.Acquire). It is the
	// ZERO VALUE deliberately: every caller that predates this locking — the MCP
	// server, the CLI, capture, the backfill applier — is already fail-stop, so
	// the default preserves their semantics and only the hook has to opt out.
	LockBlocking LockPosture = iota

	// LockNonBlocking refuses instead of waiting (vaultlock.TryAcquire). On
	// contention the call returns an error wrapping ErrManifestLocked and writes
	// nothing. Use it, and only it, from a path with no timeout and no
	// cancellation — today that is internal/hook.
	LockNonBlocking
)

// ErrManifestLocked reports that a LockNonBlocking caller found the manifest's
// per-path vaultlock already held and refused rather than waiting. It is a
// contention outcome, never a corruption: nothing was read and nothing written.
//
// The hook path treats it exactly as it treats any other archive failure — a
// non-fatal warn — so a contended lock costs a SessionEnd at most its archive
// ledger entry, never the session itself.
var ErrManifestLocked = errors.New("archive: manifest is locked by another writer")

// lockManifest takes the per-path vaultlock guarding manifestPath and returns
// the release func. It is the ONLY place this package acquires a vault lock.
//
// vaultRoot == "" means "this manifest is not inside a vault" — the same escape
// hatch LinkSessionNote already documents for the surface stamp. There is no
// lock namespace to key against, so the call proceeds unserialized and the
// returned release is a no-op. Every non-test caller in the tree passes a real
// root.
//
// 🔴 DO NOT CALL THIS TWICE FOR ONE PATH ON ONE GOROUTINE. flock does not
// recurse — every Acquire opens a fresh descriptor — so a second acquire of the
// same manifest is a permanent self-deadlock with no error (ADR-003, "RMW
// callers must not double-acquire"). That is why Create and the LinkSessionNote
// family each acquire exactly once, at the top, and call raw ReadManifest /
// WriteManifest underneath.
func lockManifest(vaultRoot, manifestPath string, posture LockPosture) (release func() error, err error) {
	if vaultRoot == "" {
		return func() error { return nil }, nil
	}
	switch posture {
	case LockNonBlocking:
		rel, ok, terr := vaultlock.TryAcquire(vaultRoot, manifestPath)
		if terr != nil {
			return nil, fmt.Errorf("lock manifest: %w", terr)
		}
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrManifestLocked, manifestPath)
		}
		return rel, nil
	default:
		rel, aerr := vaultlock.Acquire(vaultRoot, manifestPath)
		if aerr != nil {
			return nil, fmt.Errorf("lock manifest: %w", aerr)
		}
		return rel, nil
	}
}
