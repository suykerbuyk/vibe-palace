// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// initTestEnv sets up an isolated XDG_CONFIG_HOME so init tests don't
// touch the real config. If preCreateConfig is true, writes a minimal
// config so global init is skipped (tests that focus on project init).
func initTestEnv(t *testing.T, preCreateConfig bool) string {
	t.Helper()
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	if preCreateConfig {
		vpDir := filepath.Join(configDir, "vibe-palace")
		os.MkdirAll(vpDir, 0o755)
		vaultDir := t.TempDir()
		content := `vault_path = "` + vaultDir + `"` + "\ngit_enabled = true\n"
		os.WriteFile(filepath.Join(vpDir, "config.toml"), []byte(content), 0o644)
	}
	return configDir
}

func TestInitCreatesConfig(t *testing.T) {
	initTestEnv(t, true) // skip global init
	dir := t.TempDir()
	cmd := cmdInit()
	code := cmd.Run([]string{dir, "--name", "test-proj"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	configPath := filepath.Join(dir, project.ConfigFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config not created: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `name = "test-proj"`) {
		t.Errorf("config missing project name: %s", content)
	}
}

func TestInitWithDomainAndTags(t *testing.T) {
	initTestEnv(t, true)
	dir := t.TempDir()
	cmd := cmdInit()
	code := cmd.Run([]string{dir, "--name", "myapp", "--domain", "work", "--tags", "go,cli"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	data, _ := os.ReadFile(filepath.Join(dir, project.ConfigFileName))
	content := string(data)
	if !strings.Contains(content, `domain = "work"`) {
		t.Errorf("missing domain: %s", content)
	}
	if !strings.Contains(content, "tags") {
		t.Errorf("missing tags: %s", content)
	}
}

func TestInitRefusesOverwrite(t *testing.T) {
	initTestEnv(t, true)
	dir := t.TempDir()
	// Create existing project config.
	os.WriteFile(filepath.Join(dir, project.ConfigFileName), []byte("exists"), 0o644)

	cmd := cmdInit()
	code := cmd.Run([]string{dir, "--name", "test"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d (should skip existing project)", code, cli.ExitOK)
	}
}

func TestInitInvalidName(t *testing.T) {
	initTestEnv(t, true)
	dir := t.TempDir()
	cmd := cmdInit()
	code := cmd.Run([]string{dir, "--name", "INVALID NAME!"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (invalid name)", code, cli.ExitUser)
	}
}

func TestInitAutoDetectsName(t *testing.T) {
	initTestEnv(t, true)
	dir := filepath.Join(t.TempDir(), "my-project")
	os.MkdirAll(dir, 0o755)

	cmd := cmdInit()
	code := cmd.Run([]string{dir})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	data, _ := os.ReadFile(filepath.Join(dir, project.ConfigFileName))
	if !strings.Contains(string(data), "my-project") {
		t.Errorf("expected auto-detected name: %s", data)
	}
}

func TestInitBadFlags(t *testing.T) {
	cmd := cmdInit()
	code := cmd.Run([]string{"--unknown-flag"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (bad flags)", code, cli.ExitUser)
	}
}

func TestInitGlobalAndProject(t *testing.T) {
	configDir := initTestEnv(t, false) // no pre-created config
	projDir := t.TempDir()
	vaultDir := filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit()
	code := cmd.Run([]string{projDir, "--name", "myapp", "--vault-path", vaultDir})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	// Global config must exist.
	globalConfig := filepath.Join(configDir, "vibe-palace", "config.toml")
	if _, err := os.Stat(globalConfig); err != nil {
		t.Errorf("global config not created: %v", err)
	}

	// Project config must exist.
	projConfig := filepath.Join(projDir, project.ConfigFileName)
	if _, err := os.Stat(projConfig); err != nil {
		t.Errorf("project config not created: %v", err)
	}
}

func TestInitSkipsExistingConfig(t *testing.T) {
	configDir := initTestEnv(t, true) // pre-create config

	// Read the existing config content.
	globalConfig := filepath.Join(configDir, "vibe-palace", "config.toml")
	before, _ := os.ReadFile(globalConfig)

	projDir := t.TempDir()
	cmd := cmdInit()
	cmd.Run([]string{projDir, "--name", "test"})

	// Config must not have been overwritten.
	after, _ := os.ReadFile(globalConfig)
	if string(before) != string(after) {
		t.Error("global config was overwritten, should have been skipped")
	}
}

func TestInitSkipsProjectInHomeDir(t *testing.T) {
	initTestEnv(t, true)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	cmd := cmdInit()
	code := cmd.Run([]string{home, "--name", "test"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	// Must NOT create .vibe-palace.toml in home.
	if _, err := os.Stat(filepath.Join(home, project.ConfigFileName)); err == nil {
		// Clean up if accidentally created.
		os.Remove(filepath.Join(home, project.ConfigFileName))
		t.Error("should not create project config in $HOME")
	}
}

func TestInitVaultPathFlag(t *testing.T) {
	configDir := initTestEnv(t, false)
	vaultDir := filepath.Join(t.TempDir(), "custom-vault")

	cmd := cmdInit()
	// Run from a temp dir (not home) so project init is attempted but fails gracefully.
	projDir := t.TempDir()
	code := cmd.Run([]string{projDir, "--vault-path", vaultDir, "--name", "test"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	// Verify config has custom vault path.
	data, _ := os.ReadFile(filepath.Join(configDir, "vibe-palace", "config.toml"))
	if !strings.Contains(string(data), vaultDir) {
		t.Errorf("config does not contain custom vault path %s: %s", vaultDir, data)
	}

	// Vault directory must have been created.
	if _, err := os.Stat(vaultDir); err != nil {
		t.Errorf("vault directory not created: %v", err)
	}

	// The cwd .vibe-palace.toml must also carry the vault_path override.
	cwdFile := filepath.Join(projDir, project.ConfigFileName)
	cwdData, err := os.ReadFile(cwdFile)
	if err != nil {
		t.Fatalf("cwd config not created: %v", err)
	}
	if !strings.Contains(string(cwdData), "vault_path = \""+vaultDir+"\"") {
		t.Errorf("cwd config missing vault_path override:\n%s", cwdData)
	}
}

// When the global config already exists, passing --vault-path to vp init
// records the override only in the cwd .vibe-palace.toml, leaving global
// untouched (matching the work/personal split use case).
func TestInitVaultPathWritesCwdOverride(t *testing.T) {
	configDir := initTestEnv(t, true) // global config already exists
	altVault := filepath.Join(t.TempDir(), "alt-vault")

	projDir := t.TempDir()
	cmd := cmdInit()
	code := cmd.Run([]string{projDir, "--vault-path", altVault, "--name", "alt-proj"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	// Global config must NOT have been rewritten to the new path.
	globalData, _ := os.ReadFile(filepath.Join(configDir, "vibe-palace", "config.toml"))
	if strings.Contains(string(globalData), altVault) {
		t.Errorf("global config unexpectedly contains override path: %s", globalData)
	}

	// Cwd file must carry the override.
	cwdData, err := os.ReadFile(filepath.Join(projDir, project.ConfigFileName))
	if err != nil {
		t.Fatalf("cwd config not created: %v", err)
	}
	if !strings.Contains(string(cwdData), "vault_path = \""+altVault+"\"") {
		t.Errorf("cwd config missing vault_path override:\n%s", cwdData)
	}

	// And resolving from the project dir picks up the override.
	path, source, err := storage.ResolveVaultPath(projDir)
	if err != nil {
		t.Fatalf("ResolveVaultPath: %v", err)
	}
	if path != altVault {
		t.Errorf("resolved path = %q, want %q", path, altVault)
	}
	if !strings.HasPrefix(source, "cwd:") {
		t.Errorf("source = %q, want cwd: prefix", source)
	}
}

func TestInitNoGitFlag(t *testing.T) {
	configDir := initTestEnv(t, false)
	projDir := t.TempDir()
	vaultDir := filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit()
	code := cmd.Run([]string{projDir, "--no-git", "--name", "test", "--vault-path", vaultDir})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	data, _ := os.ReadFile(filepath.Join(configDir, "vibe-palace", "config.toml"))
	if !strings.Contains(string(data), "git_enabled = false") {
		t.Errorf("config should have git_enabled = false: %s", data)
	}
}
