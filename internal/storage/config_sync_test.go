// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTemplateSyncWithDefaults verifies that template.toml and defaults.toml
// declare the same set of TOML keys. This catches drift when a field is added
// to one file but not the other.
func TestTemplateSyncWithDefaults(t *testing.T) {
	// Use PresentKeys (active + commented) for both so that keys carried
	// as commented-out documentation (e.g., [meta].kind, [palace.llm]) are
	// counted equally in both files. This matches the upgrade pipeline's view.
	defaultKeys := PresentKeys(defaultsToml)
	templateKeys := PresentKeys(templateToml)

	// Every default key must appear in the template.
	for k := range defaultKeys {
		if !templateKeys[k] {
			t.Errorf("key %q in defaults.toml but missing from template.toml", k)
		}
	}

	// Every template key must appear in defaults.
	for k := range templateKeys {
		if !defaultKeys[k] {
			t.Errorf("key %q in template.toml but missing from defaults.toml", k)
		}
	}
}

func TestConfigGitEnabledDefault(t *testing.T) {
	v := testVault(t)
	cfg, err := v.LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.GitEnabled {
		t.Error("GitEnabled = false, want true from defaults")
	}
}

func TestConfigGitEnabledFalse(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "vibe-palace")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}

	configContent := `vault_path = "` + tmp + `"` + "\ngit_enabled = false\n"
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("XDG_CONFIG_HOME", tmp)

	v := NewVault(tmp)
	cfg, err := v.LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.GitEnabled {
		t.Error("GitEnabled = true, want false after override")
	}
}
