// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Vault holds the resolved root path to the vault directory and provides
// path-building methods for all vault artifacts.
type Vault struct {
	Root string // Absolute, tilde-expanded path to vault root

	// migratorExempt, when true, bypasses the vault data-format READ gate (see
	// checkFormatGate and internal/surface). ONLY the KG data migration sets it:
	// the migration must read format-0 (unmigrated) data to rewrite it, so it
	// cannot be gated against the very format it is about to advance. Zero-value
	// false ⇒ every normal caller (all existing NewVault call sites) is gated by
	// default.
	migratorExempt bool

	// clock, when set, replaces time.Now() for every CreateTime/ModTime stamp
	// this Vault writes (see now, SetClock). Zero-value nil ⇒ every normal
	// caller (all existing NewVault call sites) reads the real clock by
	// default — the same zero-value-correct seam as migratorExempt above.
	// ONLY test code sets it, to pin a stamp to a fixed instant instead of
	// racing the wall clock.
	clock func() time.Time
}

// SetClock overrides the instant this Vault stamps into CreateTime/ModTime,
// replacing the default time.Now(). It exists for test code that needs a
// fixed, advanceable instant to observe a restamp — mirroring
// SetMigratorExempt's seam shape exactly: an unexported field, zero-value
// nil for every normal caller, and one exported setter for the one caller
// (tests) that needs to override it.
func (v *Vault) SetClock(fn func() time.Time) { v.clock = fn }

// now returns the instant this Vault stamps into CreateTime/ModTime: the
// injected clock if SetClock was called, else the real time.Now().
//
// Call this exactly ONCE per stamping method body and reuse the result — see
// clock.go's CalendarDay doc comment: "one logical operation cannot disagree
// with itself by reading the clock twice across midnight."
func (v *Vault) now() time.Time {
	if v.clock != nil {
		return v.clock()
	}
	return time.Now()
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
