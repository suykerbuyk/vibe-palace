// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
