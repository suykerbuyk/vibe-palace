// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestIntegrationVaultResolutionCwdOverride proves end-to-end that two
// source directories under the same $HOME can target two different
// vaults via per-directory .vibe-palace.toml overrides — the
// work/personal split scenario.
func TestIntegrationVaultResolutionCwdOverride(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// Global config points at the "personal" vault.
	personalVault := filepath.Join(tmp, "personal-vault")
	if err := os.MkdirAll(personalVault, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Join(home, ".config", "vibe-palace")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(cfgDir, "config.toml"),
		[]byte(`vault_path = "`+personalVault+`"`+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	// Work directory declares a different vault via cwd override.
	workVault := filepath.Join(tmp, "work-vault")
	if err := os.MkdirAll(workVault, 0o755); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(home, "code", "work-proj")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(workDir, ".vibe-palace.toml"),
		[]byte(`vault_path = "`+workVault+`"`+"\n"+`[project]`+"\n"+`name = "work-proj"`+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	// Personal directory has no cwd override.
	personalDir := filepath.Join(home, "code", "personal-proj")
	if err := os.MkdirAll(personalDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// From the work dir, OpenVaultFromCwd must pick the work vault.
	wv, err := storage.OpenVaultFromCwd(workDir)
	if err != nil {
		t.Fatalf("OpenVaultFromCwd(work): %v", err)
	}
	if wv.Root != workVault {
		t.Errorf("work vault root = %q, want %q", wv.Root, workVault)
	}

	// From the personal dir, OpenVaultFromCwd must fall back to global.
	pv, err := storage.OpenVaultFromCwd(personalDir)
	if err != nil {
		t.Fatalf("OpenVaultFromCwd(personal): %v", err)
	}
	if pv.Root != personalVault {
		t.Errorf("personal vault root = %q, want %q", pv.Root, personalVault)
	}

	// Sanity: OpenVaultGlobal ignores the work cwd override.
	gv, err := storage.OpenVaultGlobal()
	if err != nil {
		t.Fatalf("OpenVaultGlobal: %v", err)
	}
	if gv.Root != personalVault {
		t.Errorf("global vault root = %q, want %q (cwd override should be ignored)", gv.Root, personalVault)
	}

	// ResolveVaultPath source annotations are what vp check will surface.
	_, workSource, _ := storage.ResolveVaultPath(workDir)
	if !strings.HasPrefix(workSource, "cwd:") {
		t.Errorf("work source = %q, want cwd: prefix", workSource)
	}
	_, personalSource, _ := storage.ResolveVaultPath(personalDir)
	if !strings.HasPrefix(personalSource, "global:") {
		t.Errorf("personal source = %q, want global: prefix", personalSource)
	}
}

// TestIntegrationVaultResolutionHomeBoundary proves that a
// .vibe-palace.toml sitting at $HOME itself is never honored — a
// defensive guard against accidentally bound-to-nothing homes.
func TestIntegrationVaultResolutionHomeBoundary(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	globalVault := filepath.Join(tmp, "global-vault")
	os.MkdirAll(globalVault, 0o755)
	cfgDir := filepath.Join(home, ".config", "vibe-palace")
	os.MkdirAll(cfgDir, 0o755)
	os.WriteFile(
		filepath.Join(cfgDir, "config.toml"),
		[]byte(`vault_path = "`+globalVault+`"`+"\n"),
		0o644,
	)

	// A rogue .vibe-palace.toml at $HOME pointing at a bogus vault.
	os.WriteFile(
		filepath.Join(home, ".vibe-palace.toml"),
		[]byte(`vault_path = "/tmp/should-not-be-used"`+"\n"),
		0o644,
	)

	projectDir := filepath.Join(home, "code", "foo")
	os.MkdirAll(projectDir, 0o755)

	v, err := storage.OpenVaultFromCwd(projectDir)
	if err != nil {
		t.Fatalf("OpenVaultFromCwd: %v", err)
	}
	if v.Root != globalVault {
		t.Errorf("root = %q, want %q ($HOME/.vibe-palace.toml should not be honored)", v.Root, globalVault)
	}
}
