// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"path/filepath"
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

func TestStampForPath_GuardsNonTempVaultUnderTest(t *testing.T) {
	// End-to-end through the choke point: a non-temp vault write panics.
	nonTemp := filepath.Join(string(filepath.Separator), "home", "someone", "obsidian", "real-vault")
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("StampForPath should panic for a non-temp vault write under test")
		}
	}()
	_ = StampForPath(nonTemp, filepath.Join(nonTemp, "Projects", "p", "commands", "README.md"))
}
