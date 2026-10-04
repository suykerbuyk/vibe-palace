// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// Fixtures for split's migrated-into-migrated rule (task
// split-and-merge-exclude-derived-palace-paths, Scope 4). Split never creates
// or migrates a destination, so its destination is made the way an operator
// makes one: a v9 `vp vault init`, born migrated.

const splitMarkerDate = "2026-10-04"

// splitMarkMigrated writes the migration marker beside the current data
// format into root's vault.toml.
func splitMarkMigrated(t *testing.T, root string) {
	t.Helper()
	if err := surface.WriteVaultManifest(root, surface.VaultManifest{
		Format: surface.RequiredDataFormat, AuthoredOnly: splitMarkerDate,
	}); err != nil {
		t.Fatalf("write the migration marker: %v", err)
	}
}

// splitGitEnv isolates git config and gives commits an identity, as `vp vault
// init` needs one.
func splitGitEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	home := t.TempDir()
	gc := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(gc, []byte("[user]\n\tname = T\n\temail = t@example.invalid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", gc)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

// splitInitDest creates a destination with storage.InitVault against one
// local bare remote, "origin", and returns its path.
func splitInitDest(t *testing.T) string {
	t.Helper()
	splitGitEnv(t)
	bare := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %s: %v", out, err)
	}
	dest := filepath.Join(t.TempDir(), "new-vault")
	if _, err := storage.InitVault(context.Background(), storage.InitVaultRequest{
		Path:     dest,
		Remotes:  []storage.RecordedRemote{{Name: "origin", URL: "file://" + filepath.ToSlash(bare)}},
		Scaffold: reconcile.ScaffoldNewVault,
	}); err != nil {
		t.Fatalf("vp vault init the destination: %v", err)
	}
	return dest
}
