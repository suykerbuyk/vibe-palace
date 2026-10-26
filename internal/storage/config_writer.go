// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed config/cwd_project_template.toml
var cwdProjectTemplate string

// CwdProjectTemplateContent returns the embedded cwd-project template.
func CwdProjectTemplateContent() string { return cwdProjectTemplate }

// GenerateConfigTOML produces a commented config file from the embedded
// template.toml, substituting vault_path and git_enabled with the provided
// values. Single source of truth: the output derives from template.toml.
func GenerateConfigTOML(vaultPath string, gitEnabled bool) string {
	out := templateToml

	// Replace the sentinel vault_path.
	out = strings.Replace(out, `vault_path = "~/vibe-palace-vault"`,
		fmt.Sprintf("vault_path = %q", vaultPath), 1)

	// Replace git_enabled if the user chose to disable it.
	if !gitEnabled {
		out = strings.Replace(out, "git_enabled = true",
			"git_enabled = false", 1)
	}

	return out
}

// GenerateCwdProjectTOML produces a cwd-project .vibe-palace.toml carrying the
// project's identity (name, domain, tags) only. Derived from the embedded
// cwd-project template as the single source of truth. Empty values leave the
// corresponding keys commented.
//
// The tracked marker is identity-only (ADR-012 Option (b)): it NEVER carries a
// vault_path, because the resolver refuses a git-tracked marker that does. The
// vaultPath argument is therefore ignored here and retained only for signature
// stability with the onboarding seed; a host-local tier-1 override is written
// to the gitignored .vibe-palace/local.toml by maybeWriteLocalOverride (U5),
// and the normal per-host binding lives in the global config's [project_vaults].
func GenerateCwdProjectTOML(name, domain string, tags []string, _ string) string {
	out := strings.Replace(cwdProjectTemplate, "__VP_NAME__", name, 1)

	if domain != "" {
		out = strings.Replace(out,
			`# domain = ""`,
			fmt.Sprintf("domain = %q", domain), 1)
	}

	if len(tags) > 0 {
		quoted := make([]string, len(tags))
		for i, t := range tags {
			quoted[i] = fmt.Sprintf("%q", t)
		}
		out = strings.Replace(out,
			"# tags = []",
			"tags = ["+strings.Join(quoted, ", ")+"]", 1)
	}

	return out
}

// WriteCwdProjectConfig writes .vibe-palace.toml into dir, refusing to
// overwrite an existing file. Uses atomic temp+rename. Returns the
// resolved file path.
func WriteCwdProjectConfig(dir, name, domain string, tags []string, vaultPath string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create dir: %w", err)
	}

	configPath := filepath.Join(dir, ".vibe-palace.toml")
	if _, err := os.Stat(configPath); err == nil {
		return "", fmt.Errorf("project config already exists: %s", configPath)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat config: %w", err)
	}

	content := GenerateCwdProjectTOML(name, domain, tags, vaultPath)
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("rename: %w", err)
	}
	return configPath, nil
}

// WriteGlobalConfig creates the global vibe-palace config file at the
// XDG-resolved path. Creates parent directories as needed. Returns the
// path where the config was written.
func WriteGlobalConfig(vaultPath string, gitEnabled bool) (string, error) {
	configPath, err := VaultConfigFilePath()
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}

	content := GenerateConfigTOML(vaultPath, gitEnabled)
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}

	return configPath, nil
}
