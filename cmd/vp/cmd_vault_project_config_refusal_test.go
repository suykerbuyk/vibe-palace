// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

// The CLI half of the per-project config refuse-gate.
//
// The gate lives in vaultfs and is unit-tested there. This file exists because
// `vp vault write` and vp_vault_write are two call sites of ONE function: a
// gate asserted only through the MCP tools would stay green if a refactor gave
// the CLI its own path, and the CLI is the surface a human reaches for when an
// agent has just been refused. The MCP half is in
// internal/tools/vault_project_config_refusal_test.go.

const cliProjCfg = "Projects/p/config.toml"

// seedVaultFile writes into the vault WITHOUT vaultfs, which is what refuses
// this path.
func seedVaultFile(t *testing.T, vaultDir, rel, content string) string {
	t.Helper()
	abs := filepath.Join(vaultDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("seed %s: %v", rel, err)
	}
	return abs
}

func TestVaultCLIRefusesTheVaultProjectConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		run  func(args []string) int
	}{
		{"vault write", []string{cliProjCfg, "--content", "clobbered"},
			func(a []string) int { return cmdVaultWrite().Run(a) }},
		{"vault edit", []string{cliProjCfg, "--old", "1.0", "--new", "9.9"},
			func(a []string) int { return cmdVaultEdit().Run(a) }},
		{"vault move onto it", []string{"Projects/p/staged.toml", cliProjCfg},
			func(a []string) int { return cmdVaultMove().Run(a) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vaultDir := setupTestVaultEnv(t)
			abs := seedVaultFile(t, vaultDir, cliProjCfg, "min_score = 1.0\n")
			seedVaultFile(t, vaultDir, "Projects/p/staged.toml", "[palace.scoring]\n")

			var code int
			stderr := captureStderr(t, func() { code = tc.run(tc.args) })

			if code != cli.ExitSystem {
				t.Errorf("vp %s: exit code = %d, want %d (refusal)", tc.name, code, cli.ExitSystem)
			}
			// The refusal must reach the operator's terminal, not just the
			// exit code, and it must name where to go instead.
			if !strings.Contains(stderr, "projects/<slug>.toml") {
				t.Errorf("vp %s: stderr must name the host-local file, got %q", tc.name, stderr)
			}
			if !strings.Contains(stderr, "vp tune rooms --apply") {
				t.Errorf("vp %s: stderr must name the writer, got %q", tc.name, stderr)
			}
			if got, _ := os.ReadFile(abs); string(got) != "min_score = 1.0\n" {
				t.Errorf("vp %s changed the file: %q", tc.name, got)
			}
		})
	}
}

// TestVaultCLIStillAllowsTheSanctionedOperations is the counterweight: a
// predicate or a placement that refuses too much passes every refusal row
// above while breaking a verified purge.
func TestVaultCLIStillAllowsTheSanctionedOperations(t *testing.T) {
	t.Run("vault delete", func(t *testing.T) {
		vaultDir := setupTestVaultEnv(t)
		abs := seedVaultFile(t, vaultDir, cliProjCfg, "[palace.scoring]\n")

		if code := cmdVaultDelete().Run([]string{cliProjCfg}); code != cli.ExitOK {
			t.Fatalf("vp vault delete must be allowed: exit code = %d", code)
		}
		if _, err := os.Lstat(abs); !os.IsNotExist(err) {
			t.Errorf("file survived delete (stat err = %v)", err)
		}
	})

	t.Run("vault move out of it", func(t *testing.T) {
		vaultDir := setupTestVaultEnv(t)
		seedVaultFile(t, vaultDir, cliProjCfg, "[palace.scoring]\n")

		code := cmdVaultMove().Run([]string{cliProjCfg, "Projects/p/config.toml.retired"})
		if code != cli.ExitOK {
			t.Fatalf("moving the config OUT must be allowed: exit code = %d", code)
		}
	})

	t.Run("vault write to a near miss", func(t *testing.T) {
		setupTestVaultEnv(t)
		code := cmdVaultWrite().Run([]string{"Projects/p/doc/config.toml", "--content", "x"})
		if code != cli.ExitOK {
			t.Errorf("Projects/p/doc/config.toml must still be writable: exit code = %d", code)
		}
	})
}
