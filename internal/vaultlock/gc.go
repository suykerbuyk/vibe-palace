// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultlock

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// GC of the .vp-locks sidecar litter (ADR-003 amendment 2026-10-27,
// quiescence-gated). The sidecars are empty, harmless litter that accumulates one
// per distinct path ever written; this removes the provably-unheld ones.
//
// The design is quiescence-gated and deadlock-free on both platforms:
//
//   - Writers take .gc SHARED across their open→flock window (see openLockFile).
//   - GC takes .gc EXCLUSIVE via a BOUNDED NON-BLOCKING POLL (never a blocking or
//     pending exclusive). Each failed try leaves NO pending request — unix
//     LOCK_EX|LOCK_NB, Windows LOCKFILE_FAIL_IMMEDIATELY — so GC never blocks a
//     shared (incl. nested) acquirer. That dissolves the cross-platform deadlock
//     class: GC either holds .gc exclusive (⇒ at that instant zero shared holders
//     ⇒ nobody is mid open→flock) and waits on nothing, or it holds nothing and
//     blocks no one.
//   - On a successful acquire GC sweeps; ON TIMEOUT it ABORTS and unlinks NOTHING
//     ("busy, retry"). A vault that is actively being written is simply not swept.
//
// The split hazard (vaultlock.go doc, "two holders on two inodes") is closed:
// GC unlinks only while holding .gc exclusive, which proves no concurrent opener
// can O_CREATE a fresh inode at a name GC is removing.

const (
	// gcAcquireTimeout bounds the non-blocking poll for the .gc exclusive lock.
	// GC is an occasional operator action; a couple of seconds is plenty to catch
	// a quiescent instant, and aborting past it is correct (busy vault).
	gcAcquireTimeout = 2 * time.Second
	// gcAcquirePoll is the sleep between exclusive-acquire attempts.
	gcAcquirePoll = 20 * time.Millisecond
)

// SweepEntry is one sidecar's verdict from a SweepDeadLocks run.
type SweepEntry struct {
	Name    string // sidecar file name under <vaultRoot>/.vp-locks
	Verdict string // "removed" | "would-remove" | "kept-held" | "kept-error"
	Reason  string
}

// SweepReport is the outcome of a SweepDeadLocks run.
//
// Quiescent is false ONLY when the .gc exclusive lock could not be taken within
// the bounded poll (the vault was active); in that case nothing was removed and
// Entries is empty. DryRun echoes the caller's request.
type SweepReport struct {
	Quiescent bool
	DryRun    bool
	Entries   []SweepEntry
}

// Removed returns the names of sidecars this run actually unlinked (empty on a
// dry run or a busy/non-quiescent run).
func (r SweepReport) Removed() []string {
	var out []string
	for _, e := range r.Entries {
		if e.Verdict == "removed" {
			out = append(out, e.Name)
		}
	}
	return out
}

// flockTryExclusiveFile opens an EXISTING lock file and tries an exclusive flock.
//
// 🔴 It opens O_RDWR WITHOUT O_CREATE: using O_CREATE (as TryAcquireFile does)
// would recreate a name GC just unlinked and flock a fresh inode — a
// self-inflicted split. It takes NO .gc coordination: the only caller is the GC
// sweep, which already holds .gc EXCLUSIVE (taking .gc shared here would
// self-deadlock against that hold).
//
// It returns ok=true with a closeFn the caller MUST call (and, for a removal,
// BEFORE os.Remove), ok=false when another holder has the lock, or an error. A
// vanished file surfaces as an error satisfying errors.Is(err, fs.ErrNotExist).
func flockTryExclusiveFile(path string) (ok bool, closeFn func() error, err error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0) // NO O_CREATE — see doc above
	if err != nil {
		return false, nil, fmt.Errorf("vaultlock: open sidecar %s: %w", filepath.Base(path), err)
	}
	got, ferr := flockTryExclusive(f)
	if ferr != nil {
		f.Close()
		return false, nil, fmt.Errorf("vaultlock: try sidecar lock %s: %w", filepath.Base(path), ferr)
	}
	if !got {
		f.Close()
		return false, nil, nil
	}
	return true, func() error {
		_ = funlock(f)
		return f.Close()
	}, nil
}

// SweepDeadLocks removes provably-unheld sidecar litter from <vaultRoot>/.vp-locks.
//
// It acquires the .gc coordination lock EXCLUSIVE via a bounded non-blocking poll;
// on timeout it returns a report with Quiescent=false and removes nothing. On
// success it enumerates the *.lock sidecars (never .gc), flock-tries each, and —
// INSIDE the exclusive hold — removes the unheld ones (or records them when
// dryRun). The fd is always closed BEFORE os.Remove so the removal is not blocked
// on Windows, where a handle open without FILE_SHARE_DELETE would otherwise refuse
// the delete; closing first makes the default share mode moot.
//
// The vault root's own sidecar is swept like any other, and this is safe because
// .gc separates "mid-acquire" from "finished-acquire": a holder still INSIDE
// openLockFile/flockExclusive (the open→flock window) keeps .gc SHARED, so GC
// cannot take .gc exclusive and aborts; a holder that has FINISHED its acquire
// (e.g. a committer mid-git-fetch) holds ONLY its sidecar and NO .gc, so GC
// acquires .gc, finds that sidecar via flockTryExclusiveFile (ok=false → held),
// SKIPS it, and may still sweep OTHER unheld sidecars. GC never waits on a
// network-holding lock. A swept, unheld root sidecar is recreated by the next
// committer cleanly.
func SweepDeadLocks(vaultRoot string, dryRun bool) (SweepReport, error) {
	return sweepDeadLocks(vaultRoot, dryRun, gcAcquireTimeout)
}

// sweepDeadLocks is SweepDeadLocks with a caller-chosen bound on the .gc
// exclusive poll. Split out so tests can drive the abort path with a short
// timeout instead of waiting the production two seconds.
func sweepDeadLocks(vaultRoot string, dryRun bool, acquireTimeout time.Duration) (SweepReport, error) {
	if vaultRoot == "" {
		return SweepReport{}, fmt.Errorf("vaultlock: vaultRoot must not be empty")
	}
	if !filepath.IsAbs(vaultRoot) {
		return SweepReport{}, fmt.Errorf("vaultlock: vaultRoot must be absolute, got %q", vaultRoot)
	}
	lockDir := filepath.Join(vaultRoot, ".vp-locks")
	if _, err := os.Stat(lockDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No lock dir yet ⇒ nothing to sweep; vacuously quiescent.
			return SweepReport{Quiescent: true, DryRun: dryRun}, nil
		}
		return SweepReport{}, fmt.Errorf("vaultlock: stat lock dir: %w", err)
	}

	gcRelease, got, err := acquireGCExclusiveBounded(vaultRoot, acquireTimeout)
	if err != nil {
		return SweepReport{}, err
	}
	if !got {
		// The vault is active (a .gc shared holder exists at every poll). Abort,
		// remove nothing. No-op under any doubt.
		return SweepReport{Quiescent: false, DryRun: dryRun}, nil
	}
	defer gcRelease()

	entries, err := os.ReadDir(lockDir)
	if err != nil {
		return SweepReport{}, fmt.Errorf("vaultlock: read lock dir: %w", err)
	}

	rep := SweepReport{Quiescent: true, DryRun: dryRun}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == gcCoordName {
			continue // never sweep the coordination lock itself
		}
		if filepath.Ext(name) != ".lock" {
			continue // only hashed sidecars are swept
		}
		path := filepath.Join(lockDir, name)

		ok, closeFn, tryErr := flockTryExclusiveFile(path)
		if tryErr != nil {
			if errors.Is(tryErr, fs.ErrNotExist) {
				continue // vanished between ReadDir and open — nothing to do
			}
			rep.Entries = append(rep.Entries, SweepEntry{Name: name, Verdict: "kept-error", Reason: tryErr.Error()})
			continue
		}
		if !ok {
			rep.Entries = append(rep.Entries, SweepEntry{Name: name, Verdict: "kept-held", Reason: "held by a live writer"})
			continue
		}

		if dryRun {
			_ = closeFn()
			rep.Entries = append(rep.Entries, SweepEntry{Name: name, Verdict: "would-remove", Reason: "unheld"})
			continue
		}

		// Close the fd BEFORE unlinking (Windows share-mode safety), then remove.
		_ = closeFn()
		if rmErr := os.Remove(path); rmErr != nil {
			if errors.Is(rmErr, fs.ErrNotExist) {
				continue
			}
			rep.Entries = append(rep.Entries, SweepEntry{Name: name, Verdict: "kept-error", Reason: rmErr.Error()})
			continue
		}
		rep.Entries = append(rep.Entries, SweepEntry{Name: name, Verdict: "removed", Reason: "unheld"})
	}

	sort.Slice(rep.Entries, func(i, j int) bool { return rep.Entries[i].Name < rep.Entries[j].Name })
	return rep, nil
}

// acquireGCExclusiveBounded takes the .vp-locks/.gc coordination lock EXCLUSIVE
// via a BOUNDED NON-BLOCKING POLL: repeated flockTryExclusive + a small sleep, up
// to timeout. It NEVER issues a blocking/pending exclusive request, so it never
// blocks a shared acquirer (the keystone of the quiescence design). On success it
// returns a release and got=true; on timeout, got=false and nothing held.
func acquireGCExclusiveBounded(vaultRoot string, timeout time.Duration) (release func() error, got bool, err error) {
	lockDir := filepath.Join(vaultRoot, ".vp-locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return nil, false, fmt.Errorf("vaultlock: create lock dir: %w", err)
	}
	f, err := os.OpenFile(gcLockPath(vaultRoot), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, fmt.Errorf("vaultlock: open gc coordination lock: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		ok, ferr := flockTryExclusive(f)
		if ferr != nil {
			f.Close()
			return nil, false, fmt.Errorf("vaultlock: gc coordination (exclusive): %w", ferr)
		}
		if ok {
			return releaser(f), true, nil
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, false, nil
		}
		time.Sleep(gcAcquirePoll)
	}
}
