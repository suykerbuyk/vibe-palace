// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeProjectFile drops a .vibe-palace.toml with the given body into dir so a
// resolution walk originating there picks it up as the cwd override.
func writeProjectFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, projectFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDetectVaultPathDrift pins the mid-session config-change signal: a running
// server froze its root at startup, and DetectVaultPathDrift compares that frozen
// value against a fresh resolution from cwd. It must flag a genuine swap, stay
// quiet when they agree, and NOT cry drift when the config cannot be resolved.
func TestDetectVaultPathDrift(t *testing.T) {
	newVault := filepath.Join(t.TempDir(), "new-vault")

	t.Run("drift when frozen root differs from the configured path", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, fmt.Sprintf("vault_path = %q\n", newVault))
		t.Chdir(dir)

		frozen := filepath.Join(t.TempDir(), "old-vault")
		configured, drift := DetectVaultPathDrift(frozen)
		if !drift {
			t.Errorf("drift = false, want true (frozen %q != configured %q)", frozen, newVault)
		}
		if configured != newVault {
			t.Errorf("configured = %q, want %q", configured, newVault)
		}
	})

	t.Run("no drift when frozen root equals the configured path", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, fmt.Sprintf("vault_path = %q\n", newVault))
		t.Chdir(dir)

		configured, drift := DetectVaultPathDrift(newVault)
		if drift {
			t.Errorf("drift = true, want false (frozen == configured == %q)", newVault)
		}
		if configured != newVault {
			t.Errorf("configured = %q, want %q", configured, newVault)
		}
	})

	t.Run("resolution failure reports no drift, not a false alarm", func(t *testing.T) {
		dir := t.TempDir()
		writeProjectFile(t, dir, "this is not : valid toml {{{\n")
		t.Chdir(dir)

		configured, drift := DetectVaultPathDrift(filepath.Join(t.TempDir(), "whatever"))
		if drift {
			t.Error("drift = true on an unreadable config; an unresolvable config must not be a false alarm")
		}
		if configured != "" {
			t.Errorf("configured = %q, want empty on resolution failure", configured)
		}
	})
}
