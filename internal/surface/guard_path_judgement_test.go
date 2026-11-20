// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func pathArmPanic(f func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}

// W1: GuardTestVaultWrite judges the write PATH against the ambient root even
// when vaultPath is a different, non-empty root. Before this arm, a temp vault
// with a target under the operator's configured vault was admitted (arm A only
// compares the ROOT), and atomicfile then created a directory there. Two temp
// dirs, no rename, no symlink: portable.
func TestGuardTestVaultWritePathArmRefusesATargetInsideTheAmbientVault(t *testing.T) {
	amb, vault := t.TempDir(), t.TempDir()
	t.Cleanup(SetAmbientVaultRoot(amb))

	// Control: a write inside the (non-ambient) temp vault is allowed.
	if msg := pathArmPanic(func() { GuardTestVaultWrite(vault, filepath.Join(vault, "x", "y.md")) }); msg != "" {
		t.Fatalf("a write inside a temp vault was refused: %s", msg)
	}

	// The hole: temp vault, target under the ambient vault.
	target := filepath.Join(amb, "Projects", "p", "y.md")
	msg := pathArmPanic(func() { GuardTestVaultWrite(vault, target) })
	if !strings.Contains(msg, "a write inside the CONFIGURED vault") {
		t.Fatalf("a target inside the ambient vault was not refused by the path arm, got %q", msg)
	}

	// Arm A still comes first and keeps its own text.
	msg = pathArmPanic(func() { GuardTestVaultWrite(amb, filepath.Join(amb, "y.md")) })
	if !strings.Contains(msg, "a write to the CONFIGURED vault") {
		t.Fatalf("arm A lost its own refusal text, got %q", msg)
	}

	// The path arm is not waivable (only arm B is).
	t.Setenv(allowNonTempVaultEnv, "1")
	msg = pathArmPanic(func() { GuardTestVaultWrite(vault, target) })
	if !strings.Contains(msg, "a write inside the CONFIGURED vault") {
		t.Fatalf("%s waived the path arm, got %q", allowNonTempVaultEnv, msg)
	}
}
