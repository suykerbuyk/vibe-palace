// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package vaultlock provides per-path exclusive advisory locks so callers can
// serialize a full read→modify→write of a vault file across processes.
//
// Lock files live under <vaultRoot>/.vp-locks/ and are named by the sha256 of
// the canonical absolute target path. The flock is held on this sidecar file,
// not on the target itself, because vibe-palace's whole-file writers (see
// internal/atomicfile) rename a temp over the target — that swaps the inode out
// from under any lock held on the target, silently breaking mutual exclusion. A
// stable sidecar keeps the lock anchored to a fixed inode for the life of the
// read-modify-write.
//
// The advisory lock is released automatically by the OS on process exit, so a
// lingering .lock marker file left behind by a crash is harmless: the next
// Acquire reopens it and locks cleanly.
//
// The canonical key is the path with its deepest EXISTING ancestor resolved
// through EvalSymlinks and the missing remainder rejoined, so a path supplied by
// vaultfs (rooted in the EvalSymlinks-resolved vault) and the same file supplied
// by storage (a lexical filepath.Join of a possibly symlinked root) hash to the
// same lock key and contend on the same lock file — including for a file whose
// directories do not exist yet, and before and after they are created.
//
// vaultlock is a near-leaf package: it imports only the standard library,
// except on the windows build, whose flock_windows.go pulls in
// golang.org/x/sys/windows for the LockFileEx/UnlockFileEx byte-range lock.
package vaultlock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrLockWaitTimeout is returned by AcquireWithTimeout when the lock is not
// obtained before the deadline elapses.
var ErrLockWaitTimeout = errors.New("vaultlock: timed out waiting for lock")

// Acquire takes an exclusive advisory lock guarding targetAbsPath. vaultRoot is
// the absolute vault root; targetAbsPath is the absolute path of the file the
// caller is about to read-modify-write. It returns a release function that
// unlocks and closes the lock handle; release is idempotent and safe to defer.
func Acquire(vaultRoot, targetAbsPath string) (release func() error, err error) {
	return acquireByKey(vaultRoot, canonicalKey(targetAbsPath))
}

// acquireByKey is Acquire for a key already computed by canonicalKey.
func acquireByKey(vaultRoot, key string) (release func() error, err error) {
	f, err := openLockFile(vaultRoot, key)
	if err != nil {
		return nil, err
	}
	if err := flockExclusive(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("vaultlock: acquire lock: %w", err)
	}
	return releaser(f), nil
}

// AcquirePair takes the exclusive locks guarding two paths, in ascending
// canonical-key order, and returns one release for both.
//
// 🔴 THIS IS THE ONE SANCTIONED NESTED ACQUISITION (ADR-003, amendment
// 2026-09-19), and its only caller is storage.MoveTaskToProject, which must
// serialise against every writer of its source AND its destination. Every other
// caller holds at most one vault lock at a time. The deadlock argument rests on
// two facts: no holder of a task-file lock waits on another lock, and this
// function waits only while holding its LOWER key, for its HIGHER key — so every
// wait edge between task-file locks is strictly key-increasing and none can
// close a cycle. The order is owned here: callers pass two paths and cannot
// choose it.
//
// Each key is computed ONCE, and that same value both orders the pair and names
// the sidecar, so the order and the lock identity cannot disagree. Two paths
// with one key take one lock (a self-deadlock is impossible, not merely
// unreachable). If the second acquire fails, the first is released before the
// error returns. The returned release frees the higher key, then the lower; it
// is idempotent and returns the first error.
func AcquirePair(vaultRoot, pathA, pathB string) (release func() error, err error) {
	lo, hi := canonicalKey(pathA), canonicalKey(pathB)
	if lo == hi {
		return acquireByKey(vaultRoot, lo)
	}
	if hi < lo {
		lo, hi = hi, lo
	}
	releaseLo, err := acquireByKey(vaultRoot, lo)
	if err != nil {
		return nil, err
	}
	releaseHi, err := acquireByKey(vaultRoot, hi)
	if err != nil {
		_ = releaseLo()
		return nil, err
	}
	var once sync.Once
	return func() error {
		var rerr error
		once.Do(func() {
			herr := releaseHi()
			lerr := releaseLo()
			if herr != nil {
				rerr = herr
			} else {
				rerr = lerr
			}
		})
		return rerr
	}, nil
}

// TryAcquire is the NON-BLOCKING form of Acquire. It attempts an exclusive
// advisory lock without blocking: if another holder already has the lock it
// returns ok=false (and a nil release), leaving the caller to refuse rather than
// wait. err is non-nil only on a genuine failure (bad root, open failure, a
// syscall error other than "would block").
//
// This is the primitive the KG filename migration uses to demand EXCLUSIVE
// access before it starts a bulk rename/remove/prune: it refuses to run rather
// than block on, or silently race, a concurrent writer.
//
// PLATFORM: this excludes on BOTH platforms. Windows is no longer a no-op stub
// (ADR-003 amendment 2026-08-18): flock_windows.go takes a real LockFileEx
// byte-range lock, and contention surfaces as ERROR_LOCK_VIOLATION, which maps
// to ok=false exactly as unix EWOULDBLOCK does. A caller that refuses on
// ok=false refuses for the same reason everywhere.
func TryAcquire(vaultRoot, targetAbsPath string) (release func() error, ok bool, err error) {
	f, err := openLockFile(vaultRoot, canonicalKey(targetAbsPath))
	if err != nil {
		return nil, false, err
	}
	got, err := flockTryExclusive(f)
	if err != nil {
		f.Close()
		return nil, false, fmt.Errorf("vaultlock: try acquire lock: %w", err)
	}
	if !got {
		f.Close()
		return nil, false, nil
	}
	return releaser(f), true, nil
}

// AcquireWithTimeout is the bounded-wait form of Acquire: it polls for the
// exclusive lock (via the same non-blocking primitive TryAcquire uses) and
// gives up with ErrLockWaitTimeout once timeout has elapsed, instead of
// blocking indefinitely like Acquire. Use it wherever a stuck lock-holder
// (e.g., a hung network download performed while holding the lock) must not
// be allowed to starve every other waiter for however long the holder itself
// takes to time out or hang.
func AcquireWithTimeout(vaultRoot, targetAbsPath string, timeout time.Duration) (release func() error, err error) {
	f, err := openLockFile(vaultRoot, canonicalKey(targetAbsPath))
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	const pollInterval = 50 * time.Millisecond
	for {
		ok, err := flockTryExclusive(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("vaultlock: try acquire lock: %w", err)
		}
		if ok {
			return releaser(f), nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("%w: %s after %s", ErrLockWaitTimeout, targetAbsPath, timeout)
		}
		time.Sleep(pollInterval)
	}
}

// openLockFile validates the root and opens (creating if needed) the sidecar
// lock file named by key, which every caller computes with canonicalKey. It
// performs no locking.
//
// It takes the KEY, not the path, because AcquirePair must compute each key
// once: the value that orders the pair has to be the value that names the
// sidecar, or the order and the lock identity could disagree.
func openLockFile(vaultRoot, key string) (*os.File, error) {
	if vaultRoot == "" {
		return nil, fmt.Errorf("vaultlock: vaultRoot must not be empty")
	}
	if !filepath.IsAbs(vaultRoot) {
		return nil, fmt.Errorf("vaultlock: vaultRoot must be absolute, got %q", vaultRoot)
	}

	lockDir := filepath.Join(vaultRoot, ".vp-locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return nil, fmt.Errorf("vaultlock: create lock dir: %w", err)
	}

	sum := sha256.Sum256([]byte(key))
	lockName := hex.EncodeToString(sum[:]) + ".lock"
	lockPath := filepath.Join(lockDir, lockName)

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("vaultlock: open lock file: %w", err)
	}
	return f, nil
}

// releaser wraps f in the idempotent unlock-and-close release function shared by
// Acquire and TryAcquire.
func releaser(f *os.File) func() error {
	var once sync.Once
	return func() error {
		var rerr error
		once.Do(func() {
			uerr := funlock(f)
			cerr := f.Close()
			if uerr != nil {
				rerr = fmt.Errorf("vaultlock: release lock: %w", uerr)
				return
			}
			if cerr != nil {
				rerr = fmt.Errorf("vaultlock: close lock file: %w", cerr)
			}
		})
		return rerr
	}
}

// canonicalKey reduces targetAbsPath to a stable identity shared by every
// spelling of the same file: EvalSymlinks the path; if that fails, walk up to
// the deepest ancestor that EvalSymlinks can resolve and rejoin the missing
// remainder lexically. The walk continues only past ancestors that do not exist;
// any other error (EACCES, ENOTDIR) stops it at the lexical clean, as before,
// because guessing past an unreadable ancestor could name a different
// directory.
//
// So one file has one key whether or not its directories exist yet. The earlier
// rule resolved only the parent and otherwise fell back to a lexical clean; with
// the vault reached through a symlink and more than the leaf missing, that keyed
// the file by its unresolved spelling until its directory was created and by
// its resolved spelling afterwards — two sidecars for one file, so a holder of
// one did not exclude a holder of the other (task
// retire-racing-a-cross-project-move-duplicates-the-task).
//
// Keys are unchanged for every existing path and for every path whose parent
// exists. Only a path with a missing parent, reached through a symlinked vault
// root, gets a different key from the previous rule — and the previous rule
// already keyed that path inconsistently, so an old process and a new one side
// by side are no worse off than two old ones.
//
// RESIDUAL: a path component that is, or later becomes, a symlink — including
// an existing DANGLING symlink whose target is created later — can still move
// the key, because the walk passes over a component EvalSymlinks reports as
// not existing. vp creates no symlinks inside the vault; this needs a hand-made
// link.
func canonicalKey(targetAbsPath string) string {
	clean := filepath.Clean(targetAbsPath)
	if real, err := filepath.EvalSymlinks(clean); err == nil {
		return real
	}
	tail := filepath.Base(clean)
	for dir := filepath.Dir(clean); ; dir = filepath.Dir(dir) {
		real, err := filepath.EvalSymlinks(dir)
		if err == nil {
			return filepath.Join(real, tail)
		}
		if !os.IsNotExist(err) || filepath.Dir(dir) == dir {
			return clean
		}
		tail = filepath.Join(filepath.Base(dir), tail)
	}
}
