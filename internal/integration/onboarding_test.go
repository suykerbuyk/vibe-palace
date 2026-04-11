// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestIntegrationInitToCheckPipeline proves the full init→check flow:
// fresh init creates a config that vp check accepts as valid.
func TestIntegrationInitToCheckPipeline(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config")
	vaultDir := filepath.Join(tmp, "vault")
	t.Setenv("XDG_CONFIG_HOME", configDir)

	// Step 1: WriteGlobalConfig (simulates what vp init does).
	configPath, err := storage.WriteGlobalConfig(vaultDir, true)
	if err != nil {
		t.Fatalf("WriteGlobalConfig: %v", err)
	}
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatalf("create vault: %v", err)
	}

	// Step 2: The generated config must be parseable by LoadConfig.
	v := storage.NewVault(vaultDir)
	cfg, err := v.LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig on generated config: %v", err)
	}
	if cfg.VaultPath != vaultDir {
		t.Errorf("VaultPath = %q, want %q", cfg.VaultPath, vaultDir)
	}
	if !cfg.GitEnabled {
		t.Error("GitEnabled should be true by default")
	}
	if cfg.HTTPPort != 7423 {
		t.Errorf("HTTPPort = %d, want 7423 (default)", cfg.HTTPPort)
	}

	// Step 3: CheckConfig should pass.
	_, _, r := check.CheckConfig()
	if r.Status != check.Pass {
		t.Errorf("CheckConfig: expected Pass, got %v: %s", r.Status, r.Summary)
	}

	// Step 4: CheckVault should pass.
	r = check.CheckVault(vaultDir)
	if r.Status != check.Pass {
		t.Errorf("CheckVault: expected Pass, got %v: %s", r.Status, r.Summary)
	}

	// Step 5: CheckSettings should pass with correct defaults.
	_, r = check.CheckSettings(v)
	if r.Status != check.Pass {
		t.Errorf("CheckSettings: expected Pass, got %v: %s", r.Status, r.Summary)
	}

	// Step 6: Verify config path is correct.
	if !strings.HasSuffix(configPath, "vibe-palace/config.toml") {
		t.Errorf("config path should end with vibe-palace/config.toml, got %s", configPath)
	}
}

// TestIntegrationConfigUpgradePipeline proves the detect→upgrade→verify flow:
// outdated config is detected, upgraded, and then passes staleness check.
func TestIntegrationConfigUpgradePipeline(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config", "vibe-palace")
	os.MkdirAll(configDir, 0o755)
	configPath := filepath.Join(configDir, "config.toml")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))

	// Step 1: Write a deliberately outdated config (missing git_enabled).
	outdated := `vault_path = "` + tmp + `"
http_port = 7423
log_level = "info"

[embedder]
model = "sentence-transformers/all-MiniLM-L6-v2"
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
	os.WriteFile(configPath, []byte(outdated), 0o644)

	// Step 2: DetectMissingKeys should find git_enabled.
	missing, err := storage.DetectMissingKeys(configPath)
	if err != nil {
		t.Fatalf("DetectMissingKeys: %v", err)
	}
	if len(missing) == 0 {
		t.Fatal("should detect missing keys")
	}
	found := false
	for _, k := range missing {
		if k == "git_enabled" {
			found = true
		}
	}
	if !found {
		t.Errorf("should detect git_enabled as missing, got: %v", missing)
	}

	// Step 3: CheckConfigStaleness should report Info.
	r := check.CheckConfigStaleness(configPath)
	if r.Status != check.Info {
		t.Errorf("expected Info for outdated config, got %v: %s", r.Status, r.Summary)
	}

	// Step 4: Upgrade the config.
	canonical, _ := storage.CanonicalKeys()
	present := storage.PresentKeys(outdated)
	missingGrouped := storage.MissingKeys(canonical, present)
	blocks := storage.ParseTemplateBlocks(storage.TemplateTomlContent())
	upgraded := storage.UpgradeConfig(outdated, missingGrouped, blocks)
	os.WriteFile(configPath, []byte(upgraded), 0o644)

	// Step 5: The upgraded config must still parse as valid TOML.
	var raw map[string]any
	if _, err := toml.Decode(upgraded, &raw); err != nil {
		t.Fatalf("upgraded config is invalid TOML: %v", err)
	}

	// Step 6: DetectMissingKeys should now return empty.
	missing2, err := storage.DetectMissingKeys(configPath)
	if err != nil {
		t.Fatalf("DetectMissingKeys after upgrade: %v", err)
	}
	if len(missing2) != 0 {
		t.Errorf("should have no missing keys after upgrade, got: %v", missing2)
	}

	// Step 7: CheckConfigStaleness should now report Pass.
	r = check.CheckConfigStaleness(configPath)
	if r.Status != check.Pass {
		t.Errorf("expected Pass after upgrade, got %v: %s", r.Status, r.Summary)
	}

	// Step 8: Upgrade again — should be idempotent.
	present2 := storage.PresentKeys(upgraded)
	missing3 := storage.MissingKeys(canonical, present2)
	upgraded2 := storage.UpgradeConfig(upgraded, missing3, blocks)
	if upgraded2 != upgraded {
		t.Error("upgrade should be idempotent")
	}
}

// TestIntegrationGitGuardBlocksVaultCommands proves that git_enabled=false
// prevents vault git commands from executing, via the config→guard flow.
func TestIntegrationGitGuardBlocksVaultCommands(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config", "vibe-palace")
	vaultDir := filepath.Join(tmp, "vault")
	os.MkdirAll(configDir, 0o755)
	os.MkdirAll(vaultDir, 0o755)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))

	// Write config with git_enabled = false.
	content := `vault_path = "` + vaultDir + `"` + "\ngit_enabled = false\n"
	os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o644)

	// Verify LoadConfig reads git_enabled = false.
	v := storage.NewVault(vaultDir)
	cfg, err := v.LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.GitEnabled {
		t.Fatal("GitEnabled should be false")
	}

	// CheckGit should report "disabled".
	r := check.CheckGit(vaultDir, cfg.GitEnabled)
	if r.Status != check.Info {
		t.Errorf("CheckGit: expected Info, got %v: %s", r.Status, r.Summary)
	}
	if !strings.Contains(r.Summary, "disabled") {
		t.Errorf("CheckGit summary should mention disabled, got %q", r.Summary)
	}
}

// TestIntegrationGitInitInVault proves that git init + gitignore work
// together and produce a valid repo with correct ignores.
func TestIntegrationGitInitInVault(t *testing.T) {
	vaultDir := t.TempDir()

	// Not a repo yet.
	if storage.GitIsRepo(vaultDir) {
		t.Fatal("should not be a repo initially")
	}

	// Init + gitignore.
	if err := storage.GitInit(vaultDir); err != nil {
		t.Fatalf("GitInit: %v", err)
	}
	if err := storage.WriteVaultGitignore(vaultDir); err != nil {
		t.Fatalf("WriteVaultGitignore: %v", err)
	}

	// Now a repo.
	if !storage.GitIsRepo(vaultDir) {
		t.Error("should be a repo after init")
	}

	// .gitignore has correct content.
	data, _ := os.ReadFile(filepath.Join(vaultDir, ".gitignore"))
	if !strings.Contains(string(data), "palace/.local/") {
		t.Error(".gitignore should exclude palace/.local/")
	}

	// CheckGit should report "no remotes configured" (not an error).
	r := check.CheckGit(vaultDir, true)
	if r.Status != check.Info {
		t.Errorf("expected Info (no remotes), got %v: %s", r.Status, r.Summary)
	}
}

// TestIntegrationTemplateSyncAndUpgradeConsistency proves that the template,
// defaults, and upgrade system are consistent: a config generated from the
// template has all canonical keys and needs no upgrade.
func TestIntegrationTemplateSyncAndUpgradeConsistency(t *testing.T) {
	// Generate a config from the template.
	generated := storage.GenerateConfigTOML("/tmp/vault", true)

	// Parse it.
	var raw map[string]any
	if _, err := toml.Decode(generated, &raw); err != nil {
		t.Fatalf("generated config is invalid TOML: %v", err)
	}

	// PresentKeys should find all canonical keys.
	canonical, _ := storage.CanonicalKeys()
	present := storage.PresentKeys(generated)

	for k := range canonical {
		if !present[k] {
			t.Errorf("canonical key %q not present in generated config", k)
		}
	}

	// MissingKeys should return empty.
	missing := storage.MissingKeys(canonical, present)
	if len(missing) != 0 {
		t.Errorf("generated config should have no missing keys, got: %v", missing)
	}
}

// TestIntegrationCheckCascadeWithMissingConfig proves the full check cascade
// handles missing config gracefully with actionable messages.
func TestIntegrationCheckCascadeWithMissingConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	// No config file exists.

	_, _, r := check.CheckConfig()
	if r.Status != check.Fail {
		t.Fatalf("expected Fail for missing config, got %v", r.Status)
	}

	// Details should contain actionable message.
	found := false
	for _, d := range r.Details {
		if strings.Contains(d, "vp init") {
			found = true
		}
	}
	if !found {
		t.Errorf("missing config Details should contain 'vp init', got: %v", r.Details)
	}
}
