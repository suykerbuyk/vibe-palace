// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func TestConfigUpgradeDryRun(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	// Write a config missing git_enabled.
	vpDir := filepath.Join(configDir, "vibe-palace")
	os.MkdirAll(vpDir, 0o755)
	configPath := filepath.Join(vpDir, "config.toml")
	content := `vault_path = "/tmp"
http_port = 7423
log_level = "info"

[embedder]
model = "test"
max_sequence_length = 256
batch_size = 32

[search]
default_limit = 10
structural_boost_wing = 0.12
structural_boost_hall = 0.24
structural_boost_room = 0.34

[chunker]
max_chars = 800
overlap = 100
`
	os.WriteFile(configPath, []byte(content), 0o644)

	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{"--dry-run"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}

	// Config should NOT have been modified.
	after, _ := os.ReadFile(configPath)
	if string(after) != content {
		t.Error("dry-run should not modify the config file")
	}

	// No backup should have been created.
	if _, err := os.Stat(configPath + ".bak"); err == nil {
		t.Error("dry-run should not create a backup")
	}
}

func TestConfigUpgradeWritesChanges(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	vpDir := filepath.Join(configDir, "vibe-palace")
	os.MkdirAll(vpDir, 0o755)
	configPath := filepath.Join(vpDir, "config.toml")
	content := `vault_path = "/tmp"
http_port = 7423
`
	os.WriteFile(configPath, []byte(content), 0o644)

	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}

	// Config should have been modified.
	after, _ := os.ReadFile(configPath)
	if string(after) == content {
		t.Error("upgrade should modify the config")
	}

	// Backup should exist.
	if _, err := os.Stat(configPath + ".bak"); err != nil {
		t.Error("upgrade should create a backup")
	}
}

func TestConfigUpgradeUpToDate(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	vpDir := filepath.Join(configDir, "vibe-palace")
	os.MkdirAll(vpDir, 0o755)
	configPath := filepath.Join(vpDir, "config.toml")
	content := `vault_path = "/tmp"
git_enabled = true
http_port = 7423
log_level = "info"

[embedder]
model = "test"
max_sequence_length = 256
batch_size = 32

[search]
default_limit = 10
structural_boost_wing = 0.12
structural_boost_hall = 0.24
structural_boost_room = 0.34

[chunker]
max_chars = 800
overlap = 100
`
	os.WriteFile(configPath, []byte(content), 0o644)

	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}

	// No backup when nothing changed.
	if _, err := os.Stat(configPath + ".bak"); err == nil {
		t.Error("should not create backup when config is up to date")
	}
}

func TestConfigUpgradeNoConfig(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (missing config)", code, cli.ExitUser)
	}
}

func TestConfigUpgradeBadFlags(t *testing.T) {
	cmd := cmdConfigUpgrade()
	code := cmd.Run([]string{"--bogus"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

func TestConfigUpgradeIdempotent(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	vpDir := filepath.Join(configDir, "vibe-palace")
	os.MkdirAll(vpDir, 0o755)
	configPath := filepath.Join(vpDir, "config.toml")
	os.WriteFile(configPath, []byte(`vault_path = "/tmp"`+"\n"), 0o644)

	// First upgrade.
	cmd := cmdConfigUpgrade()
	cmd.Run([]string{})
	first, _ := os.ReadFile(configPath)

	// Remove backup so we can check if second run creates one.
	os.Remove(configPath + ".bak")

	// Second upgrade — should be no-op.
	cmd2 := cmdConfigUpgrade()
	code := cmd2.Run([]string{})
	if code != cli.ExitOK {
		t.Errorf("second upgrade exit code = %d", code)
	}

	second, _ := os.ReadFile(configPath)
	if string(first) != string(second) {
		t.Error("second upgrade should not modify config")
	}
	if _, err := os.Stat(configPath + ".bak"); err == nil {
		t.Error("second upgrade should not create backup (no changes)")
	}
}

func TestInitGlobalCreatesVaultWithGit(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	vaultDir := filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit()
	code := cmd.Run([]string{t.TempDir(), "--vault-path", vaultDir, "--name", "test"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	// Vault should have .git directory.
	if _, err := os.Stat(filepath.Join(vaultDir, ".git")); err != nil {
		t.Error("vault should have .git directory when git_enabled=true")
	}

	// Vault should have .gitignore.
	data, err := os.ReadFile(filepath.Join(vaultDir, ".gitignore"))
	if err != nil {
		t.Error("vault should have .gitignore")
	} else if !strings.Contains(string(data), "palace/.local/") {
		t.Error(".gitignore should exclude palace/.local/")
	}
}

func TestInitGlobalNoGitSkipsRepo(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	vaultDir := filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit()
	code := cmd.Run([]string{t.TempDir(), "--vault-path", vaultDir, "--no-git", "--name", "test"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	// Vault should NOT have .git directory.
	if _, err := os.Stat(filepath.Join(vaultDir, ".git")); err == nil {
		t.Error("vault should NOT have .git when --no-git is used")
	}
}
