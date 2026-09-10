// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// hostCacheDir is the process's real cache directory, resolved at package
// init — before any t.Setenv can shadow HOME. setupTestVaultEnv pins
// XDG_CACHE_HOME to it so sandboxing HOME does not also relocate the shared
// ~/.cache/huggingface ONNX model cache: the migrate-command tests reach
// setupEmbedder, and a fresh cache makes every run re-download the model.
// Pinning keeps that lookup exactly where it already was.
var hostCacheDir = func() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return d
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".cache")
	}
	return ""
}()

// setupTestVaultEnv creates a temp vault and configures XDG_CONFIG_HOME
// so that storage.OpenVaultGlobal (and OpenVaultFromCwd, absent any cwd
// override) find a valid config pointing to the temp vault. HOME is
// sandboxed to a temp dir too, so a command that reaches os.UserHomeDir —
// hook.Install() writing ~/.claude/settings.json is the live example —
// cannot touch the developer's real home. USERPROFILE gets the same value
// because that, not HOME, is what os.UserHomeDir reads on Windows, which
// .goreleaser.yml builds for; the assignment is inert elsewhere.
// Returns the vault root path; the env is restored by t.Setenv's cleanup.
func setupTestVaultEnv(t *testing.T) string {
	t.Helper()

	vaultDir := t.TempDir()
	configDir := t.TempDir()
	homeDir := t.TempDir()

	// Create the config directory structure.
	vpConfigDir := filepath.Join(configDir, "vibe-palace")
	os.MkdirAll(vpConfigDir, 0o755)

	// Write config.toml pointing to our temp vault.
	configContent := `vault_path = "` + vaultDir + `"` + "\n"
	os.WriteFile(filepath.Join(vpConfigDir, "config.toml"), []byte(configContent), 0o644)

	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	if hostCacheDir != "" {
		t.Setenv("XDG_CACHE_HOME", hostCacheDir)
	}

	return vaultDir
}
