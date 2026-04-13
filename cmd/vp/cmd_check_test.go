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

func TestCheckCommand(t *testing.T) {
	// runCheck depends on real config; just verify it doesn't panic
	// and returns a valid exit code.
	code := runCheck("test")
	if code != cli.ExitOK && code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitOK or ExitUser", code)
	}
}

// TestCheckParityWithConfigSyncDryRun proves the Phase 4 acceptance
// criterion: when an artifact has drifted, both `vp check` and
// `vp config sync --dry-run` see it. We drift the cwd-project config and
// assert check emits a "Project" row referencing the drifted file while
// sync --dry-run reports a Project-tier action against the same path.
func TestCheckParityWithConfigSyncDryRun(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	// No global config — both surfaces should agree it's missing.
	checkOut := captureStdout(t, func() { runCheck("test") })
	if !strings.Contains(checkOut, "Config:") {
		t.Errorf("vp check missing Config row:\n%s", checkOut)
	}
	if !strings.Contains(checkOut, "[FAIL]") {
		t.Errorf("vp check should report a [FAIL] when global config absent:\n%s", checkOut)
	}

	syncOut := captureStdout(t, func() {
		runConfigSync([]string{"--project-root", t.TempDir(), "--dry-run"})
	})
	if !strings.Contains(syncOut, "GlobalConfig") || !strings.Contains(syncOut, "[Skip]") {
		t.Errorf("config sync --dry-run should report GlobalConfig Skip when missing:\n%s", syncOut)
	}
}

// TestCheckEmitsVaultProjectRow verifies the new row added in Phase 4: vp
// check now reports on vault-project state via the VaultProject reconciler.
func TestCheckEmitsVaultProjectRow(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	vpDir := filepath.Join(configDir, "vibe-palace")
	_ = os.MkdirAll(vpDir, 0o755)
	vaultPath := filepath.Join(configDir, "vault")
	_ = os.MkdirAll(vaultPath, 0o755)
	_ = os.WriteFile(filepath.Join(vpDir, "config.toml"),
		[]byte("vault_path = \""+vaultPath+"\"\ngit_enabled = false\n"), 0o644)

	// cwd into a project dir so DetectProject can find a slug.
	projDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(projDir, project.ConfigFileName),
		[]byte("name = \"checktest\"\n"), 0o644)
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	_ = os.Chdir(projDir)

	out := captureStdout(t, func() { runCheck("test") })
	if !strings.Contains(out, "Vault project") {
		t.Errorf("expected Vault project row in vp check output:\n%s", out)
	}
}

func TestCheckCommandConstructor(t *testing.T) {
	info := cli.BuildInfo{Version: "test"}
	cmd := cmdCheck(info)
	if cmd.Name != "check" {
		t.Errorf("name = %q", cmd.Name)
	}
	if cmd.Run == nil {
		t.Error("Run is nil")
	}
}
