// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// hostCacheDir is the process's real cache directory, resolved at package
// init — before any t.Setenv can shadow HOME.
//
// 🔴 THE PIN IS A -race CORRECTNESS REQUIREMENT, NOT A SPEED OPTIMIZATION.
// Deleting it does not make the suite slower; it makes it RED. The
// migrate-command tests reach setupEmbedder -> embedder.NewONNX, which
// resolves the ONNX model cache from XDG_CACHE_HOME falling back to
// $HOME/.cache. Sandboxing HOME relocates that to an empty temp dir, so the
// model is re-downloaded, and go-huggingface's concurrent downloader has a
// data race in its own code (hub/files.go, two goroutines from one
// DownloadFilesCtx) that `go test -race` reports as a failure in ours.
//
// Pinning keeps the lookup exactly where it already was, so a warm cache
// stays warm and the download path is never entered. This is deliberately
// NOT sandboxed: the model cache is a read-mostly shared artifact, not host
// state a test can corrupt.
//
// .github/workflows/ci.yml:58-69 encodes the same knowledge — it caches
// ~/.cache/huggingface and warms it in a step that runs OUTSIDE -race. That
// warm step does not sandbox HOME, so without this pin the -race job would
// sandbox HOME, miss the warm cache entirely, and go red. The pin is what
// keeps CI's existing mitigation from being silently defeated.
var hostCacheDir = func() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return d
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".cache")
	}
	return ""
}()

// setupTestVaultEnv creates a temp vault and configures the global-config
// lookup so that storage.OpenVaultGlobal (and OpenVaultFromCwd, absent any
// cwd override) find a valid config pointing to the temp vault.
//
// It sandboxes the same four vars as initTestEnv, for the same per-GOOS
// reasons documented there: HOME/USERPROFILE for os.UserHomeDir (a command
// reaching hook.Install() would otherwise rewrite the developer's real
// ~/.claude/settings.json), and XDG_CONFIG_HOME/APPDATA for
// os.UserConfigDir, which honors XDG on Linux and %AppData% on Windows.
//
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
	t.Setenv("APPDATA", configDir)
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	if hostCacheDir != "" {
		t.Setenv("XDG_CACHE_HOME", hostCacheDir)
	}

	return vaultDir
}
