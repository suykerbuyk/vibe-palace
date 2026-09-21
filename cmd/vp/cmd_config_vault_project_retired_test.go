// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
)

// TestConfigSyncCreatesNoVaultProjectConfig is criterion A5, and the one that
// makes deleting Projects/<slug>/config.toml durable: an unflagged
// `vp config sync --yes` — every tier — over a vault whose projects carry no
// config.toml creates none, for the cwd's project or any enumerated one.
// Before the vault-project reconciler retired, this run created one for the
// cwd's project.
func TestConfigSyncCreatesNoVaultProjectConfig(t *testing.T) {
	vaultDir, projDir := phase4ConfigSyncSetup(t)
	writeVaultFile(t, vaultDir, "Projects/alpha/resume.md", "# resume\n")
	writeVaultFile(t, vaultDir, "Projects/beta/commands/README.md", "stub\n")
	if err := os.WriteFile(filepath.Join(projDir, project.ConfigFileName),
		[]byte("[project]\nname = \"gamma\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := runConfigSync([]string{"--project-root", projDir, "--yes"}); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want ExitOK", code)
	}

	found, err := filepath.Glob(filepath.Join(vaultDir, "Projects", "*", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("config sync re-created the retired per-project vault config: %v", found)
	}
}

// TestConfigSyncTierProjectStillReconcilesTheCwdProject is criterion A11:
// removing the vault-project reconciler from --tier project must leave the
// tier itself, and the repo-local .vibe-palace.toml reconciler it drives, in
// place. A drifted .vibe-palace.toml (no [meta]) is still upgraded.
func TestConfigSyncTierProjectStillReconcilesTheCwdProject(t *testing.T) {
	_, projDir := phase4ConfigSyncSetup(t)
	cfg := filepath.Join(projDir, project.ConfigFileName)
	if err := os.WriteFile(cfg, []byte("[project]\nname = \"alpha\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := runConfigSync([]string{"--project-root", projDir, "--tier", "project", "--yes"}); code != cli.ExitOK {
		t.Fatalf("exit code = %d, want ExitOK", code)
	}

	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "[meta]") {
		t.Errorf("--tier project did not reconcile the drifted %s:\n%s", project.ConfigFileName, got)
	}
	if !strings.Contains(string(got), `name = "alpha"`) {
		t.Errorf("the reconcile lost the operator's own key:\n%s", got)
	}
}
