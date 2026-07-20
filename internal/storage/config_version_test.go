// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// hermeticTestVault returns a vault wired so that VaultConfigFilePath resolves
// into a fresh temp dir. Prevents Load from reading the user's real
// ~/.config/vibe-palace/config.toml during tests.
func hermeticTestVault(t *testing.T) *Vault {
	t.Helper()
	// os.UserConfigDir honors XDG_CONFIG_HOME on Linux.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// On macOS, os.UserConfigDir uses HOME/Library/Application Support.
	t.Setenv("HOME", t.TempDir())
	return bornCurrentVault(t, t.TempDir())
}

func resetMissingMetaWarnOnce() {
	missingMetaWarnOnce = sync.Map{}
}

func TestLoadConfig_NoMeta_LoadsWithZeroVersion(t *testing.T) {
	v := hermeticTestVault(t)
	projDir := filepath.Join(v.Root, "Projects", "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte(`log_level = "warn"`+"\n"), 0o644)

	resetMissingMetaWarnOnce()
	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.MetaVersionMajor != 0 {
		t.Errorf("MetaVersionMajor = %d, want 0 for missing [meta]", cfg.MetaVersionMajor)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want warn (config value should still load)", cfg.LogLevel)
	}
}

func TestLoadConfig_VersionMatch_LoadsClean(t *testing.T) {
	v := hermeticTestVault(t)
	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0o755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte(`
[meta]
version_major = 1
version_minor = 0
kind = "vault-project"
`), 0o644)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.MetaVersionMajor != 1 || cfg.MetaVersionMinor != 0 {
		t.Errorf("version = %d.%d, want 1.0", cfg.MetaVersionMajor, cfg.MetaVersionMinor)
	}
	if cfg.MetaKind != "vault-project" {
		t.Errorf("kind = %q, want vault-project", cfg.MetaKind)
	}
}

func TestLoadConfig_MajorTooNew_Rejected(t *testing.T) {
	v := hermeticTestVault(t)
	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0o755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), []byte(`
[meta]
version_major = 999
version_minor = 0
kind = "vault-project"
`), 0o644)

	_, err := v.LoadConfig("proj")
	if err == nil {
		t.Fatal("LoadConfig should reject version_major=999")
	}
	if !strings.Contains(err.Error(), "upgrade vp") {
		t.Errorf("error should mention upgrade vp, got: %v", err)
	}
}

func TestLoadConfig_MinorForwardCompat(t *testing.T) {
	v := hermeticTestVault(t)
	projDir := filepath.Join(v.Root, "Projects", "proj")
	os.MkdirAll(projDir, 0o755)
	os.WriteFile(filepath.Join(projDir, "config.toml"), fmt.Appendf(nil, `
[meta]
version_major = %d
version_minor = 99
kind = "vault-project"
`, CurrentVersionMajor), 0o644)

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("forward-compat minor should load: %v", err)
	}
	if cfg.MetaVersionMinor != 99 {
		t.Errorf("MetaVersionMinor = %d, want 99", cfg.MetaVersionMinor)
	}
}

func TestCheckConfigVersion_FutureMajor(t *testing.T) {
	err := checkConfigVersion("/x", tomlMeta{VersionMajor: CurrentVersionMajor + 1})
	if err == nil {
		t.Fatal("future major should error")
	}
	if !errors.Is(err, err) {
		t.Fatal("sanity")
	}
}

func TestCheckConfigVersion_ZeroMajor_WarnOnce(t *testing.T) {
	resetMissingMetaWarnOnce()
	// No error for zero major; just a one-shot warning side-effect.
	// We exercise both branches; the warning itself goes to slog, not captured here.
	if err := checkConfigVersion("/x/y", tomlMeta{}); err != nil {
		t.Fatalf("zero major should not error: %v", err)
	}
	if err := checkConfigVersion("/x/y", tomlMeta{}); err != nil {
		t.Fatalf("zero major should not error (second call): %v", err)
	}
}

func TestCheckConfigVersion_CurrentMajorLoads(t *testing.T) {
	if err := checkConfigVersion("/x", tomlMeta{VersionMajor: CurrentVersionMajor, VersionMinor: 0}); err != nil {
		t.Errorf("current version should load: %v", err)
	}
}
