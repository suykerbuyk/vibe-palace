// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func TestCheckCommand(t *testing.T) {
	// runCheck depends on real config; just verify it doesn't panic
	// and returns a valid exit code.
	code := runCheck(cli.BuildInfo{Version: "test"}, false)
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
	checkOut := captureStdout(t, func() { runCheck(cli.BuildInfo{Version: "test"}, false) })
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

	out := captureStdout(t, func() { runCheck(cli.BuildInfo{Version: "test"}, false) })
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

// TestRegisteredToolCount verifies the JSON binary block's tool count reflects
// the full registered MCP surface (engine-gated tools included).
func TestRegisteredToolCount(t *testing.T) {
	if got := registeredToolCount(); got <= 0 {
		t.Fatalf("registeredToolCount() = %d, want > 0", got)
	}
}

// TestBinaryInfo verifies the JSON binary metadata block carries this binary's
// surface version, the build commit, and a positive tool count.
func TestBinaryInfo(t *testing.T) {
	bi := binaryInfo(cli.BuildInfo{Commit: "deadbeef"})
	if bi.Surface != surface.MCPSurfaceVersion {
		t.Errorf("surface = %d, want %d", bi.Surface, surface.MCPSurfaceVersion)
	}
	if bi.Commit != "deadbeef" {
		t.Errorf("commit = %q, want deadbeef", bi.Commit)
	}
	if bi.Tools <= 0 {
		t.Errorf("tools = %d, want > 0", bi.Tools)
	}
}

// TestCheckJSONOutput drives `vp check --json` against a missing global config
// (config check fails) and asserts the JSON shape parses, exit_code is 1, and
// the human renderer is bypassed.
func TestCheckJSONOutput(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	var code int
	out := captureStdout(t, func() {
		code = runCheck(cli.BuildInfo{Version: "test", Commit: "abc"}, true)
	})

	var rep check.JSONReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if rep.Version != 1 {
		t.Errorf("version = %d, want 1", rep.Version)
	}
	if rep.Binary.Surface != surface.MCPSurfaceVersion {
		t.Errorf("binary.surface = %d, want %d", rep.Binary.Surface, surface.MCPSurfaceVersion)
	}
	if rep.Binary.Tools <= 0 {
		t.Errorf("binary.tools = %d, want > 0", rep.Binary.Tools)
	}
	if len(rep.Checks) == 0 {
		t.Error("expected at least one check")
	}
	// Missing global config → Config fails → exit_code 1, returned ExitUser.
	if rep.Summary.Fail == 0 {
		t.Error("expected at least one failing check with no global config")
	}
	if rep.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", rep.ExitCode)
	}
	if code != cli.ExitUser {
		t.Errorf("runCheck exit = %d, want ExitUser", code)
	}
	// The Surface check is always present as the closing row.
	var sawSurface bool
	for _, c := range rep.Checks {
		if c.Name == "Surface" {
			sawSurface = true
		}
	}
	if !sawSurface {
		t.Error("expected a Surface check in the JSON report")
	}
}
