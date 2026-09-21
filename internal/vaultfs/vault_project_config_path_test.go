// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The refuse-gate on the vault's per-project config, Projects/<slug>/config.toml.
//
// Per-project config is HOST-local — it describes one machine's view of one
// checkout — and the vault is shared across machines. The file is being retired
// in favour of <config-dir>/vibe-palace/projects/<slug>.toml; this gate closes
// the generic write route ahead of the removal, so nothing new arrives in the
// vault while the old copies are still being read.
//
// taskGateVault and seed come from task_path_test.go. They are reused rather
// than copied: same package, same need (a canonicalised vault root and a seeded
// file), and a second helper doing the same thing is how two gates drift.

const vaultProjCfg = "Projects/p/config.toml"

// assertRefused checks the error is the curated refusal, not merely an error.
// A path that fails for an unrelated reason — missing file, bad sha — would
// satisfy a bare `err != nil` and pin nothing.
func assertRefused(t *testing.T, op string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s(%q) must be refused", op, vaultProjCfg)
	}
	if !errors.Is(err, ErrRefusedPath) {
		t.Errorf("%s: want ErrRefusedPath, got %v", op, err)
	}
	// The refusal must name where to go instead, or it gets worked around.
	if !strings.Contains(err.Error(), "projects/<slug>.toml") {
		t.Errorf("%s refusal must name the host-local file, got %q", op, err)
	}
	if !strings.Contains(err.Error(), "vp tune rooms --apply") {
		t.Errorf("%s refusal must name the writer, got %q", op, err)
	}
	// ...and it must say the file is read by nothing, which has been true since
	// LoadConfig stopped decoding it. A message saying an override there still
	// applies would send an operator to edit a file with no effect.
	if !strings.Contains(err.Error(), "nothing reads it") || strings.Contains(err.Error(), "still applies") {
		t.Errorf("%s refusal must say nothing reads the file, got %q", op, err)
	}
	if !strings.Contains(err.Error(), "vp status") {
		t.Errorf("%s refusal must point at vp status, got %q", op, err)
	}
}

func TestIsVaultProjectConfigPath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"Projects/vibe-palace/config.toml", true},
		{"projects/vibe-palace/CONFIG.TOML", true}, // case-insensitive on the fixed segments
		{"Projects/p/config.toml", true},

		// Near misses. Each of these is a real path something writes or could
		// write, and refusing any of them would be a bug.
		{"Projects/p/doc/config.toml", false},              // deeper: not the read path
		{"Projects/p/config.toml.bak", false},              // the old fixed-name backup
		{"Projects/p/config.toml.0123456789ab.bak", false}, // what templates.PreserveBackup writes
		{"Projects/config.toml", false},                    // two segments: no slug
		{"config.toml", false},
		{"Knowledge/p/config.toml", false}, // right shape, wrong root
		{"Projects/p/iterations.md", false},
		{"Projects/p/config.tom", false},
		{"Projects/p", false},
	} {
		if got := IsVaultProjectConfigPath(tc.path); got != tc.want {
			t.Errorf("IsVaultProjectConfigPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestWriteRefusesTheVaultProjectConfig(t *testing.T) {
	root := taskGateVault(t)
	seed(t, root, vaultProjCfg, "[palace.scoring]\n")

	_, err := Write(root, vaultProjCfg, "clobbered", "")
	assertRefused(t, "Write", err)

	// The refusal must leave the file alone, not half-write it.
	got, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(vaultProjCfg)))
	if rerr != nil || string(got) != "[palace.scoring]\n" {
		t.Errorf("refused Write changed the file: %q (err=%v)", got, rerr)
	}
}

// TestCreateRefusesTheVaultProjectConfig is a UNIT test on purpose: neither
// surface has a create verb (`grep -oE 'vp_vault_[a-z_]+' internal/tools/vault_file_tools.go`
// and the `Name: "vault ..."` list in cmd/vp/cmd_vault.go both lack one), so
// there is no surface assertion to make and writing one would be theatre.
//
// It is here because Create is the only no-clobber creator in write.go: if the
// file's retirement slips, Create is the one remaining way to materialise the
// path. Without this test, an implementation could satisfy the "vp init is not
// stranded" pin by simply never gating Create, and nothing would notice.
func TestCreateRefusesTheVaultProjectConfig(t *testing.T) {
	root := taskGateVault(t)

	_, err := Create(root, vaultProjCfg, "[palace.scoring]\n")
	assertRefused(t, "Create", err)

	if _, serr := os.Lstat(filepath.Join(root, filepath.FromSlash(vaultProjCfg))); !os.IsNotExist(serr) {
		t.Errorf("refused Create materialised the file anyway (stat err = %v)", serr)
	}
}

func TestEditRefusesTheVaultProjectConfig(t *testing.T) {
	root := taskGateVault(t)
	seed(t, root, vaultProjCfg, "min_score = 1.0\n")

	_, err := Edit(root, vaultProjCfg, "1.0", "9.9", false, "")
	assertRefused(t, "Edit", err)

	got, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(vaultProjCfg)))
	if rerr != nil || string(got) != "min_score = 1.0\n" {
		t.Errorf("refused Edit changed the file: %q (err=%v)", got, rerr)
	}
}

func TestMoveRefusesTheVaultProjectConfigAsDestination(t *testing.T) {
	root := taskGateVault(t)
	seed(t, root, "Projects/p/staged.toml", "[palace.scoring]\n")

	_, err := Move(root, "Projects/p/staged.toml", vaultProjCfg)
	assertRefused(t, "Move(dest)", err)

	if _, serr := os.Lstat(filepath.Join(root, filepath.FromSlash(vaultProjCfg))); !os.IsNotExist(serr) {
		t.Errorf("refused Move created the destination anyway (stat err = %v)", serr)
	}
}

// TestMoveAllowsMovingTheVaultProjectConfigOut pins the DEPARTURE from the task
// gate, which holds both ends of a move. Moving this file out is not an escape:
// LoadConfig reads exactly Projects/<slug>/config.toml, so a config under any
// other name is read by nothing — the move is observationally a delete, and
// Delete is deliberately allowed.
//
// This test reds if someone "restores the symmetry" by adding a source check.
func TestMoveAllowsMovingTheVaultProjectConfigOut(t *testing.T) {
	root := taskGateVault(t)
	seed(t, root, vaultProjCfg, "[palace.scoring]\n")

	if _, err := Move(root, vaultProjCfg, "Projects/p/config.toml.retired"); err != nil {
		t.Fatalf("Move OUT of the config path must be allowed, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "Projects", "p", "config.toml.retired")); err != nil {
		t.Errorf("moved file is not at the destination: %v", err)
	}
}

// TestDeleteAllowsTheVaultProjectConfig pins the other half of the departure.
// vaultSplitPurge walks regular files through Delete; gating it would leave a
// verified purge unable to complete, with no sanctioned alternative.
func TestDeleteAllowsTheVaultProjectConfig(t *testing.T) {
	root := taskGateVault(t)
	seed(t, root, vaultProjCfg, "[palace.scoring]\n")

	if _, err := Delete(root, vaultProjCfg, ""); err != nil {
		t.Fatalf("Delete of the vault project config must be allowed, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(vaultProjCfg))); !os.IsNotExist(err) {
		t.Errorf("file survived Delete (stat err = %v)", err)
	}
}

// TestNearMissPathsStillWrite is the counterweight to the refusal tests: a
// predicate that refuses far too much passes every one of them. Each path here
// is one the gate must NOT catch.
func TestNearMissPathsStillWrite(t *testing.T) {
	root := taskGateVault(t)
	for _, rel := range []string{
		"Projects/p/doc/config.toml",
		"Projects/p/config.toml.bak",
		"Projects/p/config.toml.0123456789ab.bak",
		"Projects/config.toml",
		"Projects/p/iterations.md",
		"Knowledge/p/config.toml",
	} {
		if _, err := Write(root, rel, "body\n", ""); err != nil {
			t.Errorf("Write(%q) must still be allowed, got %v", rel, err)
		}
	}
}
