// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// We are, by construction, running under `go test` here, so
// runningUnderGoTest() is true for every case below.

func TestGuardTestVaultWrite_PanicsOnNonTempVault(t *testing.T) {
	// A vault path well outside os.TempDir() must panic.
	nonTemp := filepath.Join(string(filepath.Separator), "home", "someone", "obsidian", "real-vault")
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on non-temp vault write under test, got none")
		}
	}()
	GuardTestVaultWrite(nonTemp, filepath.Join(nonTemp, "Projects", "p", ".surface"))
}

func TestGuardTestVaultWrite_AllowsTempVault(t *testing.T) {
	// t.TempDir() lives under os.TempDir(); the guard must not fire.
	vault := t.TempDir()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic on temp vault write: %v", r)
		}
	}()
	GuardTestVaultWrite(vault, filepath.Join(vault, "Projects", "demo", ".surface"))
}

func TestGuardTestVaultWrite_OverrideBypasses(t *testing.T) {
	t.Setenv(allowNonTempVaultEnv, "1")
	nonTemp := filepath.Join(string(filepath.Separator), "home", "someone", "obsidian", "real-vault")
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("override should suppress panic, got: %v", r)
		}
	}()
	GuardTestVaultWrite(nonTemp, filepath.Join(nonTemp, "Projects", "p", ".surface"))
}

func TestGuardTestVaultWrite_EmptyVaultNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("empty vault path should be a no-op, got: %v", r)
		}
	}()
	GuardTestVaultWrite("", "")
}

// TestStampForPath_GuardsNonTempVaultUnderTest used to live here. The guard no
// longer runs from StampForPath — it is seated at the first statement of
// atomicfile.writeAtomic and storage.appendUnderLock — so an end-to-end arm
// cannot live in package surface, which is BELOW both of them. Its job is done
// by TestWriteAtomicRefusesAmbientVaultUnderTest (internal/atomicfile),
// TestAppendUnderLockRefusesAmbientVault (internal/storage) and
// TestVaultfsWriteRefusesAmbientVault_NoStorageLink (internal/templates).

// mustPanic runs f and fails unless it panics, returning the recovered value so
// the caller can assert on the message.
func mustPanic(t *testing.T, what string, f func()) (recovered any) {
	t.Helper()
	defer func() {
		recovered = recover()
		if recovered == nil {
			t.Fatalf("%s: expected a panic, got none", what)
		}
	}()
	f()
	return nil
}

// mustNotPanic runs f and fails if it panics.
func mustNotPanic(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s: unexpected panic: %v", what, r)
		}
	}()
	f()
}

// TestGuardTestVaultWrite_RefusesAmbientRootEvenUnderTempDir is arm A. The
// ambient root is injected as a TEMP directory on purpose: arm B cannot fire on
// a temp path, so a panic here can only have come from arm A.
//
// The different_root sub-test is the non-vacuity control and must stay in the
// same package run: without it, this test passes against a guard that panics
// unconditionally.
func TestGuardTestVaultWrite_RefusesAmbientRootEvenUnderTempDir(t *testing.T) {
	t.Run("equal_root", func(t *testing.T) {
		root := t.TempDir()
		restore := SetAmbientVaultRoot(root)
		defer restore()
		if !pathUnderTempDir(root) {
			t.Fatalf("fixture invariant broken: ambient root %q is not under os.TempDir(), so arm B could fire", root)
		}
		r := mustPanic(t, "write for the ambient vault root", func() {
			GuardTestVaultWrite(root, filepath.Join(root, "Projects", "p", ".surface"))
		})
		if msg, _ := r.(string); !strings.Contains(msg, "CONFIGURED vault") {
			t.Errorf("panic came from the wrong arm: %v", r)
		}
	})

	t.Run("different_root", func(t *testing.T) {
		ambient := t.TempDir()
		target := t.TempDir()
		restore := SetAmbientVaultRoot(ambient)
		defer restore()
		if ambient == target {
			t.Fatal("fixture invariant broken: ambient and target are the same directory")
		}
		mustNotPanic(t, "write for a root that is not the ambient one", func() {
			GuardTestVaultWrite(target, filepath.Join(target, "Projects", "p", ".surface"))
		})
	})
}

// TestGuardTestVaultWrite_RefusesAmbientRootThroughASymlink is arm A's symlink
// arm. The fixture is the case that matters: an ALIAS of the configured vault
// and the real vault BOTH under the temp root, so arm B allows the write and
// only a symlink-resolving arm A can refuse it.
func TestGuardTestVaultWrite_RefusesAmbientRootThroughASymlink(t *testing.T) {
	real := filepath.Join(t.TempDir(), "real-vault")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("mkdir real vault: %v", err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	restore := SetAmbientVaultRoot(real)
	defer restore()

	// Non-vacuity, asserted rather than assumed: the two spellings differ, and
	// arm B would have allowed this write, so a pass cannot come from arm B.
	if alias == real {
		t.Fatal("fixture invariant broken: alias and real are the same string")
	}
	if !pathUnderTempDir(alias) {
		t.Fatalf("fixture invariant broken: alias %q is not under os.TempDir(), so arm B could fire", alias)
	}

	r := mustPanic(t, fmt.Sprintf("alias %q of ambient root %q", alias, real), func() {
		GuardTestVaultWrite(alias, filepath.Join(alias, "Projects", "p", "x.md"))
	})
	if msg, _ := r.(string); !strings.Contains(msg, "CONFIGURED vault") {
		t.Errorf("panic came from the wrong arm: %v", r)
	}
}

// TestGuardTestVaultWrite_AmbientRootIgnoresOverrideEnv pins that arm A is NOT
// waivable.
//
// 🔴 FIXTURE INVARIANT: the target MUST equal the injected ambient root. With
// any other path arm A never fires, so moving the override above arm A changes
// nothing and this test would pass under its own mutation. A temp root is also
// required so arm B cannot fire and mask the result.
func TestGuardTestVaultWrite_AmbientRootIgnoresOverrideEnv(t *testing.T) {
	root := t.TempDir()
	t.Setenv(allowNonTempVaultEnv, "1")
	restore := SetAmbientVaultRoot(root)
	defer restore()
	if !pathUnderTempDir(root) {
		t.Fatalf("fixture invariant broken: root %q is not under os.TempDir()", root)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("%s must not waive the ambient-root refusal, but no panic occurred (A=%s T=%s)",
				allowNonTempVaultEnv, root, root)
		}
	}()
	GuardTestVaultWrite(root, filepath.Join(root, "Projects", "p", ".surface"))
}

// TestGuardTestVaultWrite_AmbientRootEmptyDoesNotFire pins the no-vault-
// configured case, and with it the clause ordering: sameRoot must never be
// handed an empty root.
//
// 🔴 The vaultPath MUST be non-empty. With both sides empty the guard returns
// at clause (1) and the mutation is invisible.
func TestGuardTestVaultWrite_AmbientRootEmptyDoesNotFire(t *testing.T) {
	restore := SetAmbientVaultRoot("")
	defer restore()
	vault := t.TempDir()
	t.Logf("fixture: vaultPath=%q ambient=%q", vault, "")
	if vault == "" {
		t.Fatal("fixture invariant broken: vaultPath must be non-empty")
	}
	mustNotPanic(t, "write with no ambient root configured", func() {
		GuardTestVaultWrite(vault, filepath.Join(vault, "Projects", "p", ".surface"))
	})
}
