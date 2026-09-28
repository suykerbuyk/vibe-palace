// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultlock

import (
	"errors"
	"path/filepath"
	"testing"
)

// A Held token passes RequireRoot only while it is live and guards its own
// vault's root key.
func TestHeldRequireRoot(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()

	held, err := AcquireHeld(rootA, rootA)
	if err != nil {
		t.Fatal(err)
	}
	if err := held.RequireRoot(); err != nil {
		t.Fatalf("root token refused: %v", err)
	}
	if held.Root() != rootA {
		t.Fatalf("Root() = %q, want %q", held.Root(), rootA)
	}
	if _, ok, _ := TryAcquire(rootA, rootA); ok {
		t.Fatal("the root lock is not held while the token is live")
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if err := held.RequireRoot(); !errors.Is(err, ErrNotRootLock) {
		t.Fatalf("released token: err = %v, want ErrNotRootLock", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}

	for name, target := range map[string][2]string{
		"per-path lock in the same vault": {rootA, filepath.Join(rootA, "Projects", "p", "resume.md")},
		"another vault's root as the key": {rootA, rootB},
	} {
		h, err := AcquireHeld(target[0], target[1])
		if err != nil {
			t.Fatal(err)
		}
		if err := h.RequireRoot(); !errors.Is(err, ErrNotRootLock) {
			t.Errorf("%s: err = %v, want ErrNotRootLock", name, err)
		}
		_ = h.Release()
	}

	var none *Held
	if err := none.RequireRoot(); !errors.Is(err, ErrNotRootLock) {
		t.Fatalf("nil token: err = %v, want ErrNotRootLock", err)
	}
}
