// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/departedpath"
)

// U3 of the mcp-vault-routing-per-project epic: the write-funnel backstop. It
// enforces the cross-vault refusal at the layer BOTH the CLI and MCP call, so a
// CLI subcommand cannot bypass U2's MCP-only dispatch gate.
//
// Two behaviors, by sink:
//   - CONTENT writes (atomicfile.writeAtomic via the injected guard, and
//     appendUnderLock directly): a write for a project bound to another vault is
//     REFUSED, fail-closed (ErrCrossVaultWrite).
//   - the pull/sync merge-heal DELETE of regenerable derived data
//     (dropDerivedPaths): a cross-vault derived delete is WARNED and then
//     PROCEEDS — it is corrective cleanup of regenerable data from the wrong
//     vault, never a mislocating write (operator ruling).
//
// The classify is against a process-global startup snapshot built EAGERLY (in
// cmd/vp preRun and bootstrap), never inside the write funnel: the guard fires
// under the per-path write flock (vaultlock.Acquire is a no-timeout LOCK_EX), so
// building a snapshot there — which runs git — would be deadlock-adjacent.

// ErrCrossVaultWrite is the sentinel every cross-vault content-write refusal
// wraps, so callers detect it with errors.Is. It is the WRITE path only; the
// derived-delete path warns and proceeds, it does not return this error.
var ErrCrossVaultWrite = errors.New("cross-vault write refused")

// CrossVaultWriteError is the typed refusal of a content write for a project
// bound to a vault other than the one being written. It names the project, its
// bound vault (V), and the write target (W); it wraps ErrCrossVaultWrite.
//
// It is NOT internal/mcp's crossVaultRefusal (that lives above this layer): same
// intent, a separate object, because atomicfile/storage cannot import mcp.
type CrossVaultWriteError struct {
	Slug    string // the project the write path names
	Bound   string // V: the vault this project is bound to on this host
	Target  string // W: the vault the write targets
	unbuilt bool   // the routing snapshot could not be built (VaultVerdictInvalid)
}

func (e *CrossVaultWriteError) Error() string {
	if e.unbuilt {
		return fmt.Sprintf("refusing write to project %q: the [project_vaults] routing snapshot is "+
			"not built on this host (fix the config and retry, or reload the MCP server)", e.Slug)
	}
	return fmt.Sprintf("refusing to write project %q into vault %s: it is bound to vault %s on this host; "+
		"write under the right vault, or fix the [project_vaults] binding", e.Slug, e.Target, e.Bound)
}

func (e *CrossVaultWriteError) Unwrap() error { return ErrCrossVaultWrite }

// activeSnap is the process-global startup snapshot of this host's
// [project_vaults] table. The zero value is UNBUILT (built==false); when the
// guard is armed, the content check then fail-closes (VaultVerdictInvalid), so a
// write-capable command that failed to install a snapshot refuses rather than
// misrouting. Populated eagerly by SetActiveProjectVaultSnapshot before any write
// lock (cmd/vp preRun for every write-capable command, and bootstrap for MCP).
var (
	activeSnapMu sync.RWMutex
	activeSnap   ProjectVaultSnapshot
)

// SetActiveProjectVaultSnapshot installs the process-global routing snapshot the
// write guard and derived-delete warning classify against, and returns a
// function that restores the previous one (for hermetic tests).
func SetActiveProjectVaultSnapshot(snap ProjectVaultSnapshot) (restore func()) {
	activeSnapMu.Lock()
	prev := activeSnap
	activeSnap = snap
	activeSnapMu.Unlock()
	return func() {
		activeSnapMu.Lock()
		activeSnap = prev
		activeSnapMu.Unlock()
	}
}

func activeProjectVaultSnapshot() ProjectVaultSnapshot {
	activeSnapMu.RLock()
	defer activeSnapMu.RUnlock()
	return activeSnap
}

// guardArmed gates EVERY cross-vault check — the atomicfile hook, the direct
// appendUnderLock call, and the derived-delete warning. It is set only by
// ArmProjectVaultGuard (cmd/vp main), so in library builds and tests the guard is
// entirely inert, exactly like SetIncomingIngestLauncher. Without it the direct
// callers (appendUnderLock, dropDerivedPaths) would fail-close every project-tree
// write in tests, where no snapshot is installed. The real binary always arms it,
// so production enforces (fail-closed on an unset/unbuilt snapshot). The LIFECYCLE
// exemption is NOT here — it is per-write-site, via atomicfile.ForMisboundExempt.
var guardArmed atomic.Bool

// ArmProjectVaultGuard activates the cross-vault guard: it sets the armed flag AND
// installs the content-write check into atomicfile (the funnel every full-file
// write goes through). It returns a restore func. cmd/vp main arms it once; tests
// arm it explicitly and restore on cleanup.
func ArmProjectVaultGuard() (restore func()) {
	prev := guardArmed.Swap(true)
	restoreHook := atomicfile.SetMisboundRefuse(refuseCrossVaultContentWrite)
	return func() {
		restoreHook()
		guardArmed.Store(prev)
	}
}

// refuseCrossVaultContentWrite is the guard atomicfile.writeAtomic runs under the
// write flock. It does ONLY cheap, lock-safe work — a slug parse plus an
// in-memory classify (at most a filepath.EvalSymlinks inside sameVaultRoot, the
// same class of fs read the departedpath check already does here). No git, no
// config read, no build.
//
// 🔴 It derives the slug FIRST and returns nil for a NON-project path BEFORE
// reading the snapshot holder, so a non-project write (e.g. vault init's
// .vibe-palace/remotes.toml) is never bricked by an unset holder.
func refuseCrossVaultContentWrite(vaultRoot, absPath string) error {
	if !guardArmed.Load() {
		return nil // inert in library builds and tests (see guardArmed)
	}
	if vaultRoot == "" {
		return nil
	}
	rel, ok := departedpath.RelOfAbs(vaultRoot, absPath)
	if !ok {
		return nil
	}
	slug, ok := departedpath.TreeSlug(rel)
	if !ok {
		return nil // non-project path: not this guard's concern (B2)
	}
	verdict, info, _ := ClassifyProjectVault(slug, vaultRoot, activeProjectVaultSnapshot())
	switch verdict {
	case VaultBoundHere, VaultUnbound:
		return nil
	case VaultVerdictInvalid:
		return &CrossVaultWriteError{Slug: slug, Target: vaultRoot, unbuilt: true}
	default: // VaultBoundElsewhere, VaultBindingBroken
		return &CrossVaultWriteError{Slug: slug, Bound: info, Target: vaultRoot}
	}
}

// warnCrossVaultDerivedDelete logs a warning when a derived path about to be
// deleted belongs to a project bound to another vault, then returns so the
// caller PROCEEDS. The delete is corrective cleanup of regenerable data from the
// wrong vault (it never touches the project's real vault), so it is allowed —
// operator ruling (ALLOW + WARN, never refuse).
//
// Silent on same-vault / unbound. Silent on an unbuilt snapshot
// (VaultVerdictInvalid): preRun already logged any build failure once, and the
// content funnel independently fail-closes writes on it, so re-warning per
// derived path would be noise.
func warnCrossVaultDerivedDelete(vaultRoot, relPath string) {
	if !guardArmed.Load() {
		return // inert in library builds and tests (see guardArmed)
	}
	slug, ok := departedpath.TreeSlug(relPath)
	if !ok {
		return
	}
	verdict, info, _ := ClassifyProjectVault(slug, vaultRoot, activeProjectVaultSnapshot())
	switch verdict {
	case VaultBoundElsewhere, VaultBindingBroken:
		slog.Warn("vault: deleting a derived path for a project bound to another vault on this host "+
			"(regenerable cleanup; proceeding)",
			"op", "storage.dropDerived",
			"fault", "operational",
			"project", slug,
			"bound", info, // V (the bound vault root) for Elsewhere; the reason for Broken
			"target", vaultRoot, // W
			"path", relPath,
		)
	default: // VaultBoundHere, VaultUnbound, VaultVerdictInvalid → no warning
	}
}
