// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Vault holds the resolved root path to the vault directory and provides
// path-building methods for all vault artifacts.
type Vault struct {
	Root string // Absolute, tilde-expanded path to vault root
}

// config is the minimal TOML structure needed for vault resolution.
type config struct {
	VaultPath string `toml:"vault_path"`
}

// VaultRoot reads the config file at configPath and returns the resolved
// absolute vault path. If configPath is empty, it defaults to
// $XDG_CONFIG_HOME/vibe-palace/config.toml (typically ~/.config/vibe-palace/config.toml).
func VaultRoot(configPath string) (string, error) {
	if configPath == "" {
		var err error
		configPath, err = VaultConfigFilePath()
		if err != nil {
			return "", fmt.Errorf("resolve config dir: %w", err)
		}
	} else {
		expanded, err := expandTilde(configPath)
		if err != nil {
			return "", err
		}
		configPath = expanded
	}

	var cfg config
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		return "", fmt.Errorf("read config %s: %w", configPath, err)
	}

	if cfg.VaultPath == "" {
		return "", fmt.Errorf("vault_path not set in %s", configPath)
	}

	vaultPath, err := expandTilde(cfg.VaultPath)
	if err != nil {
		return "", fmt.Errorf("expand vault_path: %w", err)
	}

	abs, err := filepath.Abs(vaultPath)
	if err != nil {
		return "", fmt.Errorf("resolve absolute vault path: %w", err)
	}

	return abs, nil
}

// NewVault creates a Vault from an already-resolved absolute root path.
// It does not validate that the directory exists.
func NewVault(root string) *Vault {
	return &Vault{Root: root}
}

// OpenVault is a convenience that reads the config at the given explicit
// path (or the global default when empty) and returns a ready Vault.
// Prefer OpenVaultFromCwd for commands that run in a source-dir context
// and OpenVaultGlobal for commands that manage the vault itself.
func OpenVault(configPath string) (*Vault, error) {
	root, err := VaultRoot(configPath)
	if err != nil {
		return nil, err
	}
	return NewVault(root), nil
}

// OpenVaultFromCwd resolves the vault via ResolveVaultPath(cwd), honoring
// any cwd-local vault_path override and falling back to the global
// config. Intended for commands that run in a user source directory.
func OpenVaultFromCwd(cwd string) (*Vault, error) {
	root, _, err := ResolveVaultPath(cwd)
	if err != nil {
		return nil, err
	}
	return NewVault(root), nil
}

// OpenVaultGlobal reads the global config only, ignoring any cwd-local
// .vibe-palace.toml override. Intended for commands that manage the
// vault itself, where a cwd override would be confusing.
func OpenVaultGlobal() (*Vault, error) {
	return OpenVault("")
}

// expandTilde replaces a leading ~ with the user's home directory.
// Only handles ~/... form, not ~user/... form.
func expandTilde(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		return filepath.Join(home, path[1:]), nil
	}
	return path, nil
}
