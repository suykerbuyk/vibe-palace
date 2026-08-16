// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
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

// TestResolveVaultPath_SwallowedVaultPathRefused is the iteration-210 specimen:
// a [table] header written ABOVE vault_path scopes the key into that table, so
// the top-level override is unset, the decode succeeds, and resolution falls
// through to the global config — the LIVE vault.
//
// MUTATION CONTRACT: delete the swallowedVaultPathTable branch in
// readCwdVaultPath and this test must go RED at the first check below, with
// path == the global vault. A test that only asserted "err != nil" would pass
// against a guard that refused for the wrong reason, so the fallback path is
// asserted explicitly.
func TestResolveVaultPath_SwallowedVaultPathRefused(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/live-vault")
	projectDir := filepath.Join(home, "code", "throwaway")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The natural writing order, and the whole bug.
	doc := "[project]\nname = \"throwaway\"\nvault_path = \"/tmp/throwaway-vault\"\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".vibe-palace.toml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	path, _, err := ResolveVaultPath(projectDir)
	if err == nil {
		t.Fatalf("ResolveVaultPath succeeded and returned %q; want refusal "+
			"(a swallowed vault_path must never fall through to the live vault)", path)
	}
	if path != "" {
		t.Errorf("path = %q on refusal, want empty", path)
	}

	// Clause 3: this error IS the rule's delivery channel. A reader who never
	// saw the deleted workflow.md paragraph must be able to act on it alone.
	msg := err.Error()
	for _, want := range []string{
		"[project]",     // which table captured it
		"top level",     // why it did not apply
		"LIVE vault",    // what the silent fallback would have hit
		"iteration 210", // where the history is
		"vp check",      // how to confirm the fix
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q — the why must survive at the enforcement site.\ngot: %s", want, msg)
		}
	}
}

// TestResolveVaultPath_TableBelowVaultPathStillResolves pins the correct
// ordering so the guard cannot be satisfied by refusing every file that has
// both a vault_path and a table.
func TestResolveVaultPath_TableBelowVaultPathStillResolves(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/live-vault")
	projectDir := filepath.Join(home, "code", "ok")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "vault_path = \"/tmp/alt-vault\"\n\n[project]\nname = \"ok\"\n"
	if err := os.WriteFile(filepath.Join(projectDir, ".vibe-palace.toml"), []byte(doc), 0o644); err != nil {
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
}

// TestErrSwallowedVaultPath_DistinctFromAbsentConfig pins the difference the
// callers switch on.
//
// Both arrive as an error from ResolveVaultPath, and collapsing them makes
// `vp check --check surface` refuse to run on an un-set-up machine — the exact
// machine that preflight exists to diagnose.
func TestErrSwallowedVaultPath_DistinctFromAbsentConfig(t *testing.T) {
	t.Run("swallowed is the sentinel", func(t *testing.T) {
		home := setupHomeAndGlobal(t, "/tmp/live-vault")
		dir := filepath.Join(home, "code", "p")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		doc := "[project]\nname = \"p\"\nvault_path = \"/tmp/tw\"\n"
		if err := os.WriteFile(filepath.Join(dir, ".vibe-palace.toml"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := ResolveVaultPath(dir)
		if !errors.Is(err, ErrSwallowedVaultPath) {
			t.Fatalf("errors.Is(err, ErrSwallowedVaultPath) = false; err = %v", err)
		}
	})

	t.Run("absent global config is NOT the sentinel", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		dir := filepath.Join(home, "code", "p")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, _, err := ResolveVaultPath(dir)
		if err == nil {
			t.Skip("no global config yet resolution succeeded; nothing to distinguish")
		}
		if errors.Is(err, ErrSwallowedVaultPath) {
			t.Errorf("absent config classified as swallowed vault_path: %v", err)
		}
	})
}
