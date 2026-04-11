// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestGenerateConfigTOML(t *testing.T) {
	out := GenerateConfigTOML("/tmp/my-vault", true)

	// Must contain the user's vault path.
	if !strings.Contains(out, `vault_path = "/tmp/my-vault"`) {
		t.Error("vault_path not set correctly")
	}

	// Must contain git_enabled = true.
	if !strings.Contains(out, "git_enabled = true") {
		t.Error("git_enabled not set correctly")
	}

	// Must contain all key sections from template.
	for _, section := range []string{"[embedder]", "[search]", "[chunker]", "[palace]"} {
		if !strings.Contains(out, section) {
			t.Errorf("missing section %s", section)
		}
	}

	// Must round-trip parse as valid TOML.
	var raw map[string]any
	if _, err := toml.Decode(out, &raw); err != nil {
		t.Fatalf("generated config is not valid TOML: %v", err)
	}

	// Verify parsed values.
	if raw["vault_path"] != "/tmp/my-vault" {
		t.Errorf("parsed vault_path = %v, want /tmp/my-vault", raw["vault_path"])
	}
	if raw["git_enabled"] != true {
		t.Errorf("parsed git_enabled = %v, want true", raw["git_enabled"])
	}
}

func TestGenerateConfigTOMLGitDisabled(t *testing.T) {
	out := GenerateConfigTOML("/tmp/vault", false)

	if !strings.Contains(out, "git_enabled = false") {
		t.Error("git_enabled should be false when disabled")
	}
	if strings.Contains(out, "git_enabled = true") {
		t.Error("git_enabled = true should not appear when disabled")
	}
}

func TestGenerateConfigTOMLUsesTemplate(t *testing.T) {
	out := GenerateConfigTOML("~/vibe-palace-vault", true)

	// When using the default sentinel values, output should match template
	// exactly (no replacements needed for vault_path since it's the default).
	if out != templateToml {
		t.Error("with default values, output should match template.toml exactly")
	}
}

func TestWriteGlobalConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	vaultPath := filepath.Join(tmp, "my-vault")
	configPath, err := WriteGlobalConfig(vaultPath, true)
	if err != nil {
		t.Fatalf("WriteGlobalConfig: %v", err)
	}

	// File must exist.
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	// Must be under XDG_CONFIG_HOME.
	if !strings.HasPrefix(configPath, tmp) {
		t.Errorf("config path %s not under XDG dir %s", configPath, tmp)
	}

	// Must be parseable TOML with correct vault_path.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		t.Fatalf("written config is not valid TOML: %v", err)
	}
	if raw["vault_path"] != vaultPath {
		t.Errorf("vault_path = %v, want %s", raw["vault_path"], vaultPath)
	}
}
