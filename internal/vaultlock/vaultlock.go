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
// The canonicalization policy mirrors vaultfs.ResolveSafePath so a path supplied
// by vaultfs (already EvalSymlinks-resolved) and the same file supplied by
// storage (a lexical filepath.Join of root and a relative path) hash to the same
// lock key and therefore contend on the same lock file.
//
// vaultlock is a leaf package: it imports only the standard library.
package vaultlock

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Acquire takes an exclusive advisory lock guarding targetAbsPath. vaultRoot is
// the absolute vault root; targetAbsPath is the absolute path of the file the
// caller is about to read-modify-write. It returns a release function that
// unlocks and closes the lock handle; release is idempotent and safe to defer.
func Acquire(vaultRoot, targetAbsPath string) (release func() error, err error) {
	if vaultRoot == "" {
		return nil, fmt.Errorf("vaultlock: vaultRoot must not be empty")
	}
	if !filepath.IsAbs(vaultRoot) {
		return nil, fmt.Errorf("vaultlock: vaultRoot must be absolute, got %q", vaultRoot)
	}

	key := canonicalKey(targetAbsPath)

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

	if err := flockExclusive(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("vaultlock: acquire lock: %w", err)
	}

	var once sync.Once
	release = func() error {
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
	return release, nil
}

// canonicalKey reduces targetAbsPath to a stable identity shared by every
// spelling of the same file. It mirrors the EvalSymlinks-with-parent-fallback
// policy of vaultfs.ResolveSafePath: resolve the full path; if it does not exist
// yet, resolve the parent directory and rejoin the leaf; if the parent is also
// missing, fall back to a lexical clean.
func canonicalKey(targetAbsPath string) string {
	if real, err := filepath.EvalSymlinks(targetAbsPath); err == nil {
		return real
	}
	parent := filepath.Dir(targetAbsPath)
	if realParent, err := filepath.EvalSymlinks(parent); err == nil {
		return filepath.Join(realParent, filepath.Base(targetAbsPath))
	}
	return filepath.Clean(targetAbsPath)
}
