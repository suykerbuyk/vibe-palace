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

// bindingProject writes a .vibe-palace.toml naming vaultPath into a fresh
// project dir under a hermetic $HOME, and returns that dir.
func bindingProject(t *testing.T, home, vaultPath string) string {
	t.Helper()
	dir := filepath.Join(home, "code", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBindingConfig(t, dir, "vault_path = \""+vaultPath+"\"\n")
	return dir
}

func writeBindingConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".vibe-palace.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A vault_path change under a running server's feet is the whole specimen.
func TestCheckVaultBinding_PathDiffersIsDrift(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	dir := bindingProject(t, home, "/tmp/vault-a")

	if err := CheckVaultBinding("/tmp/vault-a", dir); err != nil {
		t.Fatalf("bound root matches config, want nil, got %v", err)
	}

	// The operator repoints the config; the server is still bound to A.
	writeBindingConfig(t, dir, "vault_path = \"/tmp/vault-b\"\n")

	err := CheckVaultBinding("/tmp/vault-a", dir)
	var sbe *StaleBindingError
	if !errors.As(err, &sbe) {
		t.Fatalf("want *StaleBindingError, got %v", err)
	}
	if sbe.Bound != "/tmp/vault-a" {
		t.Errorf("Bound = %q, want /tmp/vault-a", sbe.Bound)
	}
	if sbe.Resolved != "/tmp/vault-b" {
		t.Errorf("Resolved = %q, want /tmp/vault-b", sbe.Resolved)
	}
	// Clause 3: the enforcement site's own output must carry the why, not just
	// the verdict. Both paths, the config that supplies the new one, and the fix.
	msg := sbe.Error()
	for _, want := range []string{"/tmp/vault-a", "/tmp/vault-b", ".vibe-palace.toml", "ONCE", "restart the MCP server"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error text missing %q:\n%s", want, msg)
		}
	}
}

// Unarmed must be inert: every server handed a vault directly (all of the
// NewServer(vault) tests) has no launch directory to compare against.
func TestCheckVaultBinding_UnarmedIsNil(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	dir := bindingProject(t, home, "/tmp/vault-b")

	if err := CheckVaultBinding("", dir); err != nil {
		t.Errorf("empty bound root: want nil, got %v", err)
	}
	if err := CheckVaultBinding("/tmp/vault-a", ""); err != nil {
		t.Errorf("empty launch cwd: want nil, got %v", err)
	}
}

// A vault_path swallowed by a table is NOT this guard's condition: such a file
// is refused everywhere it is read, so it governs nothing and the bound vault is
// still the only one in effect. Reporting drift here would strand a healthy
// session over a file nobody is bound to.
func TestCheckVaultBinding_SwallowedConfigIsNotDrift(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	dir := bindingProject(t, home, "/tmp/vault-a")

	writeBindingConfig(t, dir, "[project]\nname = \"throwaway\"\nvault_path = \"/tmp/vault-b\"\n")

	err := CheckVaultBinding("/tmp/vault-a", dir)
	if err == nil {
		t.Fatal("want the resolution error surfaced, got nil")
	}
	var sbe *StaleBindingError
	if errors.As(err, &sbe) {
		t.Fatalf("a swallowed vault_path must NOT report as drift: %v", err)
	}
	if !errors.Is(err, ErrSwallowedVaultPath) {
		t.Fatalf("want ErrSwallowedVaultPath, got %v", err)
	}
}

// Absent config is the other half of absent-vs-rejected: it is not drift
// either, and the caller degrades rather than blocking.
func TestCheckVaultBinding_AbsentGlobalConfigIsNotDrift(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := filepath.Join(home, "code", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	err := CheckVaultBinding("/tmp/vault-a", dir)
	if err == nil {
		t.Fatal("want the resolution error surfaced, got nil")
	}
	var sbe *StaleBindingError
	if errors.As(err, &sbe) {
		t.Fatalf("an absent config must NOT report as drift: %v", err)
	}
}

// A symlinked relocation that lands on the same tree changes no bytes, and this
// gate blocks writes — a false positive is the expensive direction.
func TestCheckVaultBinding_SymlinkToSameTreeIsNotDrift(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	real := filepath.Join(t.TempDir(), "vault-real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "vault-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	dir := bindingProject(t, home, link)
	if err := CheckVaultBinding(real, dir); err != nil {
		t.Errorf("symlink to the same tree reported as drift: %v", err)
	}
}

// A trailing slash is the cheapest false positive available, and Clean settles
// it without paying for EvalSymlinks.
func TestCheckVaultBinding_TrailingSlashIsNotDrift(t *testing.T) {
	home := setupHomeAndGlobal(t, "/tmp/global-vault")
	dir := bindingProject(t, home, "/tmp/vault-a/")

	if err := CheckVaultBinding("/tmp/vault-a", dir); err != nil {
		t.Errorf("trailing slash reported as drift: %v", err)
	}
}
