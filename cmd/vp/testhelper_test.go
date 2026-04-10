// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// setupTestVaultEnv creates a temp vault and configures XDG_CONFIG_HOME
// so that storage.OpenVault("") finds a valid config pointing to the temp vault.
// Returns the vault root path and a cleanup function that restores the env.
func setupTestVaultEnv(t *testing.T) string {
	t.Helper()

	vaultDir := t.TempDir()
	configDir := t.TempDir()

	// Create the config directory structure.
	vpConfigDir := filepath.Join(configDir, "vibe-palace")
	os.MkdirAll(vpConfigDir, 0o755)

	// Write config.toml pointing to our temp vault.
	configContent := `vault_path = "` + vaultDir + `"` + "\n"
	os.WriteFile(filepath.Join(vpConfigDir, "config.toml"), []byte(configContent), 0o644)

	// Override XDG_CONFIG_HOME.
	old := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", configDir)
	t.Cleanup(func() {
		if old == "" {
			os.Unsetenv("XDG_CONFIG_HOME")
		} else {
			os.Setenv("XDG_CONFIG_HOME", old)
		}
	})

	return vaultDir
}
