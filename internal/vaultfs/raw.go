// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"os"
)

// This file is family F2 of the Option E funnel: THE removal-and-rename sink
// for the whole tree.
//
// # The layering, and why it is two layers rather than one
//
//	RemoveNoLock / RenameNoLock   <- the sink. One syscall each. No policy.
//	Delete / Move (write.go)      <- the policy layer. Refusals, containment,
//	                                 locking, compare-and-set — then the sink.
//
// Before this split there were TWO removal APIs: vaultfs.Delete/Move, reachable
// only from the 4 MCP tools and 4 CLI subcommands, and a bare os.Remove /
// os.Rename at every other removal site in the tree. A funnel rule then had to
// anchor on both, and the second "both" was a hand-maintained list — the exact
// shape the first-principles epic exists to delete.
//
// So the sink is deliberately underneath the policy, not beside it. Callers
// that already do their own locking and their own path resolution — the ones
// inside internal/storage — call the sink directly. Callers arriving from
// outside (MCP, CLI) go through Delete/Move and inherit the policy. One sink
// underneath, one policy API above, and a funnel rule with one anchor.
//
// # 🔴 THE SINK NEVER LOCKS, AND THAT IS LOAD-BEARING
//
// vaultlock.Acquire is a BLOCKING syscall.Flock(LOCK_EX) on a per-path sidecar,
// and every Acquire opens a fresh descriptor — so flock does not recurse. A
// second acquire of the same path in the same process HANGS FOREVER, with no
// timeout and no error (ADR-003, "RMW callers must not double-acquire").
//
// storage.(*Vault).DeleteMemory already holds the per-path lock across its
// unlink (memory.go). If the sink acquired, that call would deadlock on itself.
// Hence: the sink acquires nothing, ever, and vaultfs.Delete keeps its own
// Acquire and calls the sink from inside it.
//
// # Why the name says NoLock rather than UnderLock
//
// The sibling append primitive is storage.(*Vault).appendUnderLock, whose name
// asserts that the CALLER HOLDS the lock — true at both of its call sites.
// That assertion is NOT true here, so copying the name would have shipped a
// false one. The sink's callers, and what actually excludes them:
//
//	Delete / Move (this package)     per-path vaultlock — Move locks the
//	                                 DESTINATION only, deliberately (write.go)
//	storage.DeleteMemory             per-path vaultlock
//	storage.moveTask                 NOTHING — the source unlink is unlocked.
//	                                 Pre-existing; see the note at its call site
//	storage.applyProjectPlan         the migration-wide TryAcquire, not per-path
//	storage.pruneEmptyDirs           the migration-wide TryAcquire, not per-path
//	archive.Create                   per-path vaultlock on the MANIFEST, held
//	                                 across the read-then-.bak-rename window
//	                                 this sink serves (ADR-003; archive/lock.go)
//
// The sink itself acquires nothing, ever; exclusion is per-caller, and the table
// above is the record of it. A NAME claiming every caller holds the lock would
// be a hand-maintained assertion about behaviour, free to disagree with it —
// which is the thing being deleted, not a thing to add. So would a tally in this
// paragraph, which is why there is none: the rows carry the fact, and a row
// changes with the caller it describes. NoLock describes the primitive's own
// behaviour instead, and cannot rot.
//
// # What the sink deliberately does NOT do
//
// It does not STAMP. Removals and renames do not write content, and the surface
// stamp tracks content-write semantics — stated already at both policy call
// sites and at storage.DeleteMemory. This is the deliberate opposite of F1
// (atomicfile.Write) and F4 (appendUnderLock), which stamp because they write
// content. Do not "fix" the asymmetry; it is the design.
//
// It does not GATE. No surface.CheckCompatible, no fail-stop. This phase is
// ROUTING; gate placement is a later phase and is not settled. The value of a
// single sink is precisely that a gate, if one is ever ruled, has one place to
// go — which is a reason to keep it clean now, not a licence to pre-install it.
//
// It does not WRAP its error. Every caller already wraps with the vocabulary of
// its own layer — Delete says "vaultfs: remove <relPath>", using the caller's
// relative path rather than the absolute one, and storage.DeleteMemory tests
// the result with errors.Is(err, fs.ErrNotExist) to stay idempotent. Wrapping
// here would double every message and change what the MCP surface returns.
//
// It does not VALIDATE the path. Absolute-path and containment guarantees come
// from ResolveSafePath above, or from the caller having built the path off a
// vault root. A lexical containment check here would be actively wrong: paths
// arriving from ResolveSafePath are EvalSymlinks-resolved, so on a vault whose
// root is itself a symlink they do not lexically sit under the unresolved root
// the caller knows.

// RemoveNoLock removes a single vault entry at absPath.
//
// This is os.Remove: it succeeds on a file OR an empty directory, and returns
// the raw, unwrapped error on failure so callers can test it with
// errors.Is(err, fs.ErrNotExist).
//
// That it accepts directories is why Delete's file-only rule lives in Delete
// rather than here: internal/storage's KG prune removes empty directories
// through this sink, and the MCP surface refuses them. Both are correct,
// because one is the sink and the other is policy.
//
// 🔴 Takes no lock. See the file comment.
func RemoveNoLock(absPath string) error {
	return os.Remove(absPath)
}

// RenameNoLock renames oldAbs to newAbs.
//
// This is os.Rename: it does NOT refuse an existing destination — the rename
// replaces it. Move's refuse-existing-destination rule is therefore policy in
// Move, enforced by a stat above this call, and any other caller that needs it
// enforces it for itself.
//
// Returns the raw, unwrapped error on failure, for the same reason
// RemoveNoLock does.
//
// 🔴 Takes no lock. See the file comment.
func RenameNoLock(oldAbs, newAbs string) error {
	return os.Rename(oldAbs, newAbs)
}
