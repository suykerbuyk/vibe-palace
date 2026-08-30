// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"path/filepath"
	"strings"
)

// LinkSessionNote updates an existing manifest's VaultRelSessionNote
// field in place. Used by vp_capture_session after writing a session
// note so the archive -> note link is bidirectional (ADR-001 Phase 4).
//
// The write is crash-safe at the manifest level — a failed write leaves the
// prior manifest intact — because WriteManifest goes through the shared
// temp-plus-rename primitive. There is still no fsync contract.
//
// vaultRoot reaches the primitive's surface stamp; pass "" to skip stamping
// (e.g. manifests outside any vault). "" also means there is no lock namespace,
// so the read-modify-write below runs unserialized — see lockManifest.
//
// # LOCKING POSTURE: BLOCKING
//
// This form takes the manifest's per-path vaultlock with a BLOCKING LOCK_EX
// that has NO TIMEOUT (ADR-003). Under contention it waits, forever if it must.
// That is correct for every caller it has — internal/capture, and
// vaultaudit.ApplyBackfill behind the mutates()-wrapped `vp archive link` /
// `vp_archive_link` — because all of them are already fail-stop.
//
// 🔴 IT IS NOT CORRECT EVERYWHERE. A caller with no timeout and no
// cancellation does not degrade when this blocks; it HANGS, with no error and
// no log. internal/hook is exactly that shape, which is why it calls
// TryLinkSessionNote instead. Before you reach for this function from a new
// path, ask whether that path can afford to wait forever. If it cannot, use
// TryLinkSessionNote and route ErrManifestLocked into a non-fatal branch.
//
// NESTING: the caller must NOT already hold this manifest's vaultlock — flock
// does not recurse, so a second acquire self-deadlocks permanently.
// ApplyBackfill's "sessions-directory lock, then release, THEN LinkSessionNote"
// sequence exists for this reason and must stay sequential.
func LinkSessionNote(vaultRoot, manifestPath, vaultRelSessionNote string) error {
	return linkSessionNote(vaultRoot, manifestPath, vaultRelSessionNote, LockBlocking)
}

// TryLinkSessionNote is the NON-BLOCKING sibling of LinkSessionNote: same
// read-modify-write, same result, but it REFUSES rather than waits when another
// writer already holds the manifest's per-path lock, returning an error that
// wraps ErrManifestLocked and writing nothing.
//
// It exists for internal/hook. `cmd/vp/commands.go` registers cmdHook UNWRAPPED
// and `cmd/vp/cmd_hook.go` passes context.Background() into hook.Run, so there
// is no timeout anywhere on that path: a blocking acquire there would wedge
// SessionEnd silently. Losing a back-link is already a non-fatal, logged outcome
// in that code; hanging a SessionEnd is not an outcome at all.
//
// Callers must treat ErrManifestLocked as a routine contention result, not a
// fault: check it with errors.Is and log rather than fail.
func TryLinkSessionNote(vaultRoot, manifestPath, vaultRelSessionNote string) error {
	return linkSessionNote(vaultRoot, manifestPath, vaultRelSessionNote, LockNonBlocking)
}

// linkSessionNote is the one implementation behind both exported forms. The
// lock spans the whole ReadManifest -> mutate -> WriteManifest sequence, because
// the lost-update window opens at the READ: without it, a linker that read
// before a concurrent Create's rename could write its stale body back and roll
// that Create's record out from under the archive that is actually on disk.
//
// The lock is taken HERE, never inside WriteManifest — that primitive is shared
// with Create and sits under vaultaudit's documented note-then-manifest
// sequence, and a non-reentrant, timeout-free LOCK_EX inside it would make that
// sequence a lock-order constraint (ADR-003).
func linkSessionNote(vaultRoot, manifestPath, vaultRelSessionNote string, posture LockPosture) error {
	release, err := lockManifest(vaultRoot, manifestPath, posture)
	if err != nil {
		return err
	}
	defer release()

	m, err := ReadManifest(manifestPath)
	if err != nil {
		return err
	}
	if m.VaultRelSessionNote == vaultRelSessionNote {
		return nil // idempotent no-op
	}
	m.VaultRelSessionNote = vaultRelSessionNote
	// Was atomicWriteManifest: a hand-rolled WriteManifest-to-".tmp" plus
	// os.Rename. WriteManifest now owns the temp file and the rename, so the
	// wrapper had nothing left to do, and it stamps, so the stampVault call
	// that used to follow this line is gone with it.
	return WriteManifest(vaultRoot, manifestPath, m)
}

// VaultRelPath returns the vault-relative form of an absolute path,
// with forward slashes regardless of OS. Returns the input unchanged
// if it is not inside vaultRoot.
func VaultRelPath(vaultRoot, abs string) string {
	rel, err := filepath.Rel(vaultRoot, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return filepath.ToSlash(rel)
}
