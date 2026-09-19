// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// TestLockKeyAgreesWithResolveSafePath pins the agreement vaultlock's package
// doc asserts: a path supplied by vaultfs (rooted in the EvalSymlinks-resolved
// vault by ResolveSafePath) and the same file supplied by storage (a lexical
// join of the configured, possibly symlinked, root and a relative path) take
// the SAME lock.
//
// 🔴 IT LIVES HERE BECAUSE THIS IS THE ONLY PLACE BOTH SIDES CAN BE CALLED.
// vaultlock cannot import vaultfs — vaultfs imports vaultlock — and
// canonicalKey is unexported, so vaultlock's own
// TestCanonicalKeyStableAcrossMissingDirectories has to RESTATE
// ResolveSafePath's formula to compare against it. A restated formula cannot
// see this function drifting away from it: changing ResolveSafePath's
// missing-parent branch to root its cleaned join in the unresolved vaultPath
// leaves that test green and this one red. Assert by calling both sides, never
// by copying either.
//
// The shape under test is the one that broke before
// retire-racing-a-cross-project-move-duplicates-the-task: a file whose parent
// directory does not exist yet, under a vault reached through a symlink. Two
// keys for one file mean two sidecars, and a holder of one does not exclude a
// holder of the other.
func TestLockKeyAgreesWithResolveSafePath(t *testing.T) {
	base := t.TempDir()
	realVault := filepath.Join(base, "real-vault")
	linkedVault := filepath.Join(base, "vault-link")
	if err := os.MkdirAll(filepath.Join(realVault, "Projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realVault, linkedVault); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}

	// Projects/q/tasks/ does not exist: the destination of a cross-project move
	// into a project that has no tasks directory yet.
	const rel = "Projects/q/tasks/x.md"
	vaultfsPath, err := ResolveSafePath(linkedVault, rel)
	if err != nil {
		t.Fatalf("ResolveSafePath: %v", err)
	}
	storagePath := filepath.Join(linkedVault, rel)

	release, err := vaultlock.Acquire(linkedVault, storagePath)
	if err != nil {
		t.Fatalf("Acquire(storage spelling): %v", err)
	}
	defer func() {
		if rerr := release(); rerr != nil {
			t.Errorf("release: %v", rerr)
		}
	}()

	second, ok, err := vaultlock.TryAcquire(linkedVault, vaultfsPath)
	if err != nil {
		t.Fatalf("TryAcquire(vaultfs spelling): %v", err)
	}
	if ok {
		_ = second()
		t.Errorf("EXCLUSION LOST: the vaultfs spelling %q and the storage spelling %q of one file take different "+
			"locks, so a vaultfs writer and a storage writer of it do not serialise", vaultfsPath, storagePath)
	}
}
