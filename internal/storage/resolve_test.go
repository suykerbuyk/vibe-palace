// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupHomeAndGlobal prepares a hermetic $HOME with a global config
// pointing at globalVault. Returns the resolved home path.
func setupHomeAndGlobal(t *testing.T, globalVault string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	cfgDir := filepath.Join(home, ".config", "vibe-palace")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	cfgFile := filepath.Join(cfgDir, "config.toml")
	content := "vault_path = \"" + globalVault + "\"\n"
	if err := os.WriteFile(cfgFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	return home
}

func TestResolveVaultPath_CwdOverride(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	projectDir := filepath.Join(home, "code", "myproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cwdFile := filepath.Join(projectDir, ".vibe-palace.toml")
	if err := os.WriteFile(cwdFile, []byte(`vault_path = "/tmp/alt-vault"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, source, err := ResolveVaultPath(projectDir)
	if err != nil {
		t.Fatalf("ResolveVaultPath: %v", err)
	}
	if path != "/tmp/alt-vault" {
		t.Errorf("path = %q, want /tmp/alt-vault", path)
	}
	if !strings.HasPrefix(source, "cwd:") {
		t.Errorf("source = %q, want cwd: prefix", source)
	}
	if !strings.HasSuffix(source, cwdFile) {
		t.Errorf("source = %q, want suffix %q", source, cwdFile)
	}
}

func TestResolveVaultPath_CwdWithoutVaultPathFallsThrough(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	projectDir := filepath.Join(home, "code", "myproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cwdFile := filepath.Join(projectDir, ".vibe-palace.toml")
	if err := os.WriteFile(cwdFile, []byte(`[project]`+"\n"+`name = "x"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, source, err := ResolveVaultPath(projectDir)
	if err != nil {
		t.Fatalf("ResolveVaultPath: %v", err)
	}
	if path != "/tmp/global-vault" {
		t.Errorf("path = %q, want /tmp/global-vault", path)
	}
	if !strings.HasPrefix(source, "global:") {
		t.Errorf("source = %q, want global: prefix", source)
	}
}

func TestResolveVaultPath_NoCwdFile(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	projectDir := filepath.Join(home, "code", "myproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	path, source, err := ResolveVaultPath(projectDir)
	if err != nil {
		t.Fatalf("ResolveVaultPath: %v", err)
	}
	if path != "/tmp/global-vault" {
		t.Errorf("path = %q, want /tmp/global-vault", path)
	}
	if !strings.HasPrefix(source, "global:") {
		t.Errorf("source = %q, want global: prefix", source)
	}
}

func TestResolveVaultPath_WalkUpFindsParent(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	parentDir := filepath.Join(home, "code", "myproj")
	subDir := filepath.Join(parentDir, "a", "b", "c")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cwdFile := filepath.Join(parentDir, ".vibe-palace.toml")
	if err := os.WriteFile(cwdFile, []byte(`vault_path = "/tmp/parent-vault"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, source, err := ResolveVaultPath(subDir)
	if err != nil {
		t.Fatalf("ResolveVaultPath: %v", err)
	}
	if path != "/tmp/parent-vault" {
		t.Errorf("path = %q, want /tmp/parent-vault", path)
	}
	if !strings.HasSuffix(source, cwdFile) {
		t.Errorf("source = %q, want suffix %q", source, cwdFile)
	}
}

func TestResolveVaultPath_HomeBoundaryStopsWalk(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	// Planting .vibe-palace.toml at $HOME itself.
	homeCwdFile := filepath.Join(home, ".vibe-palace.toml")
	if err := os.WriteFile(homeCwdFile, []byte(`vault_path = "/tmp/should-not-be-used"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(home, "code", "foo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	path, source, err := ResolveVaultPath(projectDir)
	if err != nil {
		t.Fatalf("ResolveVaultPath: %v", err)
	}
	if path != "/tmp/global-vault" {
		t.Errorf("path = %q, want /tmp/global-vault (home-boundary should block $HOME/.vibe-palace.toml)", path)
	}
	if !strings.HasPrefix(source, "global:") {
		t.Errorf("source = %q, want global: prefix", source)
	}
}

func TestResolveVaultPath_MalformedCwdFile(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	projectDir := filepath.Join(home, "code", "myproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cwdFile := filepath.Join(projectDir, ".vibe-palace.toml")
	if err := os.WriteFile(cwdFile, []byte("not valid [[[toml"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := ResolveVaultPath(projectDir)
	if err == nil {
		t.Fatal("expected error for malformed cwd file, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error = %v, want it to mention parse", err)
	}
}

func TestResolveVaultPath_UnknownKeysIgnored(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	projectDir := filepath.Join(home, "code", "myproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cwdFile := filepath.Join(projectDir, ".vibe-palace.toml")
	content := `vault_path = "/tmp/alt"
future_field = "ignored"

[project]
name = "x"

[future_section]
key = 42
`
	if err := os.WriteFile(cwdFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	path, _, err := ResolveVaultPath(projectDir)
	if err != nil {
		t.Fatalf("ResolveVaultPath: %v", err)
	}
	if path != "/tmp/alt" {
		t.Errorf("path = %q, want /tmp/alt", path)
	}
}

func TestResolveVaultPath_TildeExpansion(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	projectDir := filepath.Join(home, "code", "myproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cwdFile := filepath.Join(projectDir, ".vibe-palace.toml")
	if err := os.WriteFile(cwdFile, []byte(`vault_path = "~/my-vault"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, _, err := ResolveVaultPath(projectDir)
	if err != nil {
		t.Fatalf("ResolveVaultPath: %v", err)
	}
	want := filepath.Join(home, "my-vault")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestOpenVaultGlobal_IgnoresCwdOverride(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	projectDir := filepath.Join(home, "code", "myproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cwdFile := filepath.Join(projectDir, ".vibe-palace.toml")
	if err := os.WriteFile(cwdFile, []byte(`vault_path = "/tmp/alt-vault"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Even though we have a cwd override, OpenVaultGlobal ignores it
	// and goes straight to the global config.
	v, err := OpenVaultGlobal()
	if err != nil {
		t.Fatalf("OpenVaultGlobal: %v", err)
	}
	if v.Root != "/tmp/global-vault" {
		t.Errorf("Root = %q, want /tmp/global-vault", v.Root)
	}
}

func TestOpenVaultFromCwd_UsesOverride(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	projectDir := filepath.Join(home, "code", "myproj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cwdFile := filepath.Join(projectDir, ".vibe-palace.toml")
	if err := os.WriteFile(cwdFile, []byte(`vault_path = "/tmp/alt-vault"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	v, err := OpenVaultFromCwd(projectDir)
	if err != nil {
		t.Fatalf("OpenVaultFromCwd: %v", err)
	}
	if v.Root != "/tmp/alt-vault" {
		t.Errorf("Root = %q, want /tmp/alt-vault", v.Root)
	}
}
