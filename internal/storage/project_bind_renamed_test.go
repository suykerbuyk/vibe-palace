// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bindRenamedHome sets up a host config dir with a [project_vaults] section and
// returns the config path. Each entry is slug -> vault path.
func bindRenamedHome(t *testing.T, section string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	dir := filepath.Join(home, ".config", "vibe-palace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte(section), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func readCfg(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRenameProjectVaultBinding_MovesTheKey: the [project_vaults] key moves
// old->new keeping the same vault, in one write, and other keys are untouched.
func TestRenameProjectVaultBinding_MovesTheKey(t *testing.T) {
	cfg := bindRenamedHome(t, "editor = \"zed\"\n\n[project_vaults]\nold = \"/vaults/quantum\"\nother = \"/vaults/keep\"\n")
	rep, err := RenameProjectVaultBinding("old", "new", false)
	if err != nil {
		t.Fatalf("RenameProjectVaultBinding: %v", err)
	}
	if rep.Vault != "/vaults/quantum" {
		t.Errorf("vault = %q, want /vaults/quantum", rep.Vault)
	}
	got := readCfg(t, cfg)
	if strings.Contains(got, "old =") || strings.Contains(got, "\nold ") {
		t.Errorf("old key survived:\n%s", got)
	}
	if !strings.Contains(got, `new = "/vaults/quantum"`) {
		t.Errorf("new key missing or wrong vault:\n%s", got)
	}
	if !strings.Contains(got, `other = "/vaults/keep"`) || !strings.Contains(got, `editor = "zed"`) {
		t.Errorf("an unrelated key changed:\n%s", got)
	}
	// Resolver sees new, not old.
	bindings, _, err := readProjectVaults()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bindings["old"]; ok {
		t.Error("resolver still binds old")
	}
	if bindings["new"] != "/vaults/quantum" {
		t.Errorf("resolver binds new to %q", bindings["new"])
	}
}

// TestRenameProjectVaultBinding_NoOpWhenUnbound: an unbound old slug is a no-op,
// and writes nothing.
func TestRenameProjectVaultBinding_NoOpWhenUnbound(t *testing.T) {
	cfg := bindRenamedHome(t, "[project_vaults]\nother = \"/vaults/keep\"\n")
	before := readCfg(t, cfg)
	rep, err := RenameProjectVaultBinding("old", "new", false)
	if err != nil {
		t.Fatalf("RenameProjectVaultBinding: %v", err)
	}
	if rep.Change != "not bound" {
		t.Errorf("change = %q, want not bound", rep.Change)
	}
	if readCfg(t, cfg) != before {
		t.Error("the config changed for an unbound slug")
	}
}

// TestRenameProjectVaultBinding_Idempotent: once old is gone and new is bound,
// a re-run reports already-bound and changes nothing.
func TestRenameProjectVaultBinding_Idempotent(t *testing.T) {
	cfg := bindRenamedHome(t, "[project_vaults]\nold = \"/vaults/quantum\"\n")
	if _, err := RenameProjectVaultBinding("old", "new", false); err != nil {
		t.Fatalf("first: %v", err)
	}
	after1 := readCfg(t, cfg)
	rep, err := RenameProjectVaultBinding("old", "new", false)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if rep.Change != "already bound" {
		t.Errorf("change = %q, want already bound", rep.Change)
	}
	if readCfg(t, cfg) != after1 {
		t.Error("a second re-point changed the config")
	}
}

// TestRenameProjectVaultBinding_DryRunWritesNothing.
func TestRenameProjectVaultBinding_DryRunWritesNothing(t *testing.T) {
	cfg := bindRenamedHome(t, "[project_vaults]\nold = \"/vaults/quantum\"\n")
	before := readCfg(t, cfg)
	rep, err := RenameProjectVaultBinding("old", "new", true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !rep.DryRun {
		t.Error("report does not say dry run")
	}
	if readCfg(t, cfg) != before {
		t.Error("the dry run wrote to the config")
	}
}

// TestRenameProjectVaultBinding_BackupIsExactPreImage: config.toml.bak is the
// exact pre-image of the single write.
func TestRenameProjectVaultBinding_BackupIsExactPreImage(t *testing.T) {
	cfg := bindRenamedHome(t, "[project_vaults]\nold = \"/vaults/quantum\"\n")
	before := readCfg(t, cfg)
	rep, err := RenameProjectVaultBinding("old", "new", false)
	if err != nil {
		t.Fatalf("RenameProjectVaultBinding: %v", err)
	}
	if rep.BackupPath == "" {
		t.Fatal("no backup path reported")
	}
	bak, err := os.ReadFile(rep.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(bak) != before {
		t.Errorf("backup is not the exact pre-image:\n got %q\nwant %q", string(bak), before)
	}
}

// TestRenameProjectVaultBinding_RefusesRepointOfNew: old still bound AND new
// already bound is an ambiguous re-point; refuse and change nothing.
func TestRenameProjectVaultBinding_RefusesRepointOfNew(t *testing.T) {
	cfg := bindRenamedHome(t, "[project_vaults]\nold = \"/vaults/quantum\"\nnew = \"/vaults/elsewhere\"\n")
	before := readCfg(t, cfg)
	_, err := RenameProjectVaultBinding("old", "new", false)
	if err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("want an already-bound refusal, got %v", err)
	}
	if readCfg(t, cfg) != before {
		t.Error("the config changed despite the refusal")
	}
}
