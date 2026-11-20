// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

func dirSeatPanic(f func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}

// A3: EnsureDir refuses a directory inside the ambient vault before creating it,
// and still creates one elsewhere.
func TestEnsureDirRefusesADirectoryInsideTheAmbientVault(t *testing.T) {
	ctl := filepath.Join(t.TempDir(), "a", "b")
	if err := EnsureDir(ctl); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ctl); err != nil {
		t.Fatalf("control did not create %s: %v", ctl, err)
	}

	amb := t.TempDir()
	t.Cleanup(surface.SetAmbientVaultRoot(amb))
	d := filepath.Join(amb, "palace", "p", "x")
	if msg := dirSeatPanic(func() { _ = EnsureDir(d) }); !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("no tripwire refusal, got %q", msg)
	}
	if _, err := os.Lstat(filepath.Join(amb, "palace")); err == nil {
		t.Error("palace/ was created in the ambient vault")
	}
}

// W3: appendUnderLock has the same blind spot as writeAtomic had: a temp vault
// root with an absPath under the ambient vault. The lock is taken on the temp
// root FIRST (as the U1 rows do), then the append is refused and nothing is
// created or appended in the ambient vault.
func TestAppendUnderLockRefusesATargetInsideTheAmbientVaultWithADifferentRoot(t *testing.T) {
	vaultRoot, amb := t.TempDir(), t.TempDir()
	testutil.InitProject(t, vaultRoot, "p")
	testutil.InitProject(t, amb, "p")
	v := NewVault(vaultRoot)

	// Control: an append inside the temp vault works.
	own := filepath.Join(vaultRoot, "Projects", "p", "iterations.md")
	rel, err := vaultlock.Acquire(vaultRoot, own)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.appendUnderLock(own, []byte("line\n")); err != nil {
		rel()
		t.Fatalf("control append: %v", err)
	}
	rel()

	target := filepath.Join(amb, "Projects", "p", "iterations.md")
	rel, err = vaultlock.Acquire(vaultRoot, target) // the lock is on the TEMP root
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	t.Cleanup(surface.SetAmbientVaultRoot(amb))
	if msg := dirSeatPanic(func() { _ = v.appendUnderLock(target, []byte("line\n")) }); !strings.Contains(msg, "a write inside the CONFIGURED vault") {
		t.Fatalf("an append into the ambient vault was not refused by the path arm, got %q", msg)
	}
	if _, err := os.Lstat(target); err == nil {
		t.Error("the ambient vault's file was created by the refused append")
	}
}
