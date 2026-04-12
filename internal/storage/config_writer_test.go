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

func TestGenerateCwdProjectTOML_NameOnly(t *testing.T) {
	out := GenerateCwdProjectTOML("my-proj", "", nil, "")

	if !strings.Contains(out, `name = "my-proj"`) {
		t.Errorf("missing active name: %s", out)
	}
	// Domain, tags, vault_path must remain commented.
	if !strings.Contains(out, `# domain = ""`) {
		t.Error("domain should stay commented when empty")
	}
	if !strings.Contains(out, "# tags = []") {
		t.Error("tags should stay commented when empty")
	}
	if !strings.Contains(out, `# vault_path = "~/work-palace-vault"`) {
		t.Error("vault_path should stay commented when empty")
	}

	// Must round-trip as valid TOML.
	var raw map[string]any
	if _, err := toml.Decode(out, &raw); err != nil {
		t.Fatalf("not valid TOML: %v", err)
	}
	proj, _ := raw["project"].(map[string]any)
	if proj["name"] != "my-proj" {
		t.Errorf("parsed name = %v, want my-proj", proj["name"])
	}
	if _, hasVault := raw["vault_path"]; hasVault {
		t.Error("vault_path should not be decoded when commented")
	}
	meta, _ := raw["meta"].(map[string]any)
	if meta["version_major"] != int64(1) {
		t.Errorf("meta.version_major = %v, want 1", meta["version_major"])
	}
}

func TestGenerateCwdProjectTOML_AllOptionals(t *testing.T) {
	out := GenerateCwdProjectTOML("alpha", "work", []string{"go", "cli"}, "/tmp/work-vault")

	if !strings.Contains(out, `name = "alpha"`) {
		t.Error("missing active name")
	}
	if !strings.Contains(out, `domain = "work"`) {
		t.Error("domain not uncommented")
	}
	if !strings.Contains(out, `tags = ["go", "cli"]`) {
		t.Errorf("tags not uncommented: %s", out)
	}
	if !strings.Contains(out, `vault_path = "/tmp/work-vault"`) {
		t.Errorf("vault_path not uncommented: %s", out)
	}

	var raw map[string]any
	if _, err := toml.Decode(out, &raw); err != nil {
		t.Fatalf("not valid TOML: %v", err)
	}
	proj, _ := raw["project"].(map[string]any)
	if proj["domain"] != "work" {
		t.Errorf("parsed domain = %v, want work", proj["domain"])
	}
	if raw["vault_path"] != "/tmp/work-vault" {
		t.Errorf("parsed vault_path = %v, want /tmp/work-vault", raw["vault_path"])
	}
}

func TestWriteCwdProjectConfig_WritesThenRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteCwdProjectConfig(dir, "proj", "", nil, "")
	if err != nil {
		t.Fatalf("WriteCwdProjectConfig: %v", err)
	}
	if path != filepath.Join(dir, ".vibe-palace.toml") {
		t.Errorf("path = %s", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not written: %v", err)
	}

	// Second call must fail.
	if _, err := WriteCwdProjectConfig(dir, "proj", "", nil, ""); err == nil {
		t.Error("expected error on overwrite, got nil")
	}
}

func TestVaultProjectTemplate_ParsesAsTOML(t *testing.T) {
	var raw map[string]any
	if _, err := toml.Decode(VaultProjectTemplateContent(), &raw); err != nil {
		t.Fatalf("vault-project template is not valid TOML: %v", err)
	}
	meta, _ := raw["meta"].(map[string]any)
	if meta["version_major"] != int64(1) {
		t.Errorf("meta.version_major = %v, want 1", meta["version_major"])
	}
}

func TestCwdProjectTemplate_SentinelUnused(t *testing.T) {
	// The __VP_NAME__ sentinel must not leak into generated output
	// when a name is provided.
	out := GenerateCwdProjectTOML("real-name", "", nil, "")
	if strings.Contains(out, "__VP_NAME__") {
		t.Error("sentinel __VP_NAME__ was not replaced")
	}
}

func TestWriteVaultProjectConfig_WritesThenIdempotent(t *testing.T) {
	vaultRoot := t.TempDir()
	v := NewVault(vaultRoot)

	path, wrote, err := v.WriteVaultProjectConfig("alpha")
	if err != nil {
		t.Fatalf("first WriteVaultProjectConfig: %v", err)
	}
	if !wrote {
		t.Error("wrote = false on first call")
	}
	want := filepath.Join(vaultRoot, "Projects", "alpha", "config.toml")
	if path != want {
		t.Errorf("path = %s, want %s", path, want)
	}

	// Capture first-run modtime for unchanged check.
	info1, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// Second call must not rewrite.
	path2, wrote2, err := v.WriteVaultProjectConfig("alpha")
	if err != nil {
		t.Fatalf("second WriteVaultProjectConfig: %v", err)
	}
	if wrote2 {
		t.Error("wrote = true on second call (should be idempotent)")
	}
	if path2 != want {
		t.Errorf("path2 = %s, want %s", path2, want)
	}

	info2, _ := os.Stat(path)
	if info1.ModTime() != info2.ModTime() {
		t.Error("file was rewritten on idempotent call")
	}
}

func TestWriteVaultProjectConfig_InvalidSlug(t *testing.T) {
	v := NewVault(t.TempDir())
	if _, _, err := v.WriteVaultProjectConfig("Bad Slug"); err == nil {
		t.Error("expected error for invalid slug")
	}
}
