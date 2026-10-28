// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// TestGCLocksRemovesUnheldSidecar is the storage-layer wrapper's contract: it
// delegates to vaultlock.SweepDeadLocks over this vault's root, dry-running when
// asked and removing provably-unheld litter otherwise.
func TestGCLocksRemovesUnheldSidecar(t *testing.T) {
	root := t.TempDir()
	v := NewVault(root)

	// Seed an UNHELD sidecar via a real acquire+release.
	release, err := vaultlock.Acquire(root, filepath.Join(root, "notes.md"))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if countLockFiles(t, root) != 1 {
		t.Fatalf("expected 1 seeded sidecar")
	}

	// Dry run: removes nothing.
	rep, err := v.GCLocks(true)
	if err != nil {
		t.Fatalf("GCLocks(dry): %v", err)
	}
	if !rep.Quiescent || len(rep.Removed()) != 0 {
		t.Fatalf("dry run: quiescent=%v removed=%v; want quiescent + nothing removed", rep.Quiescent, rep.Removed())
	}
	if countLockFiles(t, root) != 1 {
		t.Fatalf("dry run removed a sidecar")
	}

	// Apply: removes the unheld sidecar.
	rep, err = v.GCLocks(false)
	if err != nil {
		t.Fatalf("GCLocks(apply): %v", err)
	}
	if !rep.Quiescent || len(rep.Removed()) != 1 {
		t.Fatalf("apply: quiescent=%v removed=%v; want 1 removed", rep.Quiescent, rep.Removed())
	}
	if countLockFiles(t, root) != 0 {
		t.Fatalf("apply left litter behind")
	}
}

func countLockFiles(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".vp-locks"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read lock dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".lock" {
			n++
		}
	}
	return n
}
