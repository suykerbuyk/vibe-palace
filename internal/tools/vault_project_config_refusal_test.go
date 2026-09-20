// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// The MCP half of the per-project config refuse-gate.
//
// The gate itself lives in vaultfs and is unit-tested there. These rows exist
// because the MCP tools and `vp vault` are two call sites of the same
// functions, and a gate asserted at only one of them would stay green if a
// refactor ever gave one surface its own path. The CLI half is in
// cmd/vp/cmd_vault_project_config_refusal_test.go.

const toolProjCfg = "Projects/p/config.toml"

// seedRaw writes a vault file WITHOUT going through vaultfs, because vaultfs is
// what refuses this path. mustWrite cannot be used here.
func seedRaw(t *testing.T, vault *storage.Vault, rel, content string) string {
	t.Helper()
	abs := filepath.Join(vault.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("seed %s: %v", rel, err)
	}
	return abs
}

func assertToolRefused(t *testing.T, tool string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s on %s must be refused", tool, toolProjCfg)
	}
	if !errors.Is(err, vaultfs.ErrRefusedPath) {
		t.Errorf("%s: want ErrRefusedPath, got %v", tool, err)
	}
	if !strings.Contains(err.Error(), "projects/<slug>.toml") {
		t.Errorf("%s refusal must name the host-local file, got %q", tool, err)
	}
}

func TestVaultToolsRefuseTheVaultProjectConfig(t *testing.T) {
	t.Run("vp_vault_write", func(t *testing.T) {
		vault := newVaultRoot(t)
		abs := seedRaw(t, vault, toolProjCfg, "[palace.scoring]\n")

		p, _ := json.Marshal(map[string]any{"path": toolProjCfg, "content": "clobbered"})
		_, err := VaultWriteTool(vault).Handler(context.Background(), p)
		assertToolRefused(t, "vp_vault_write", err)

		if got, _ := os.ReadFile(abs); string(got) != "[palace.scoring]\n" {
			t.Errorf("refused write changed the file: %q", got)
		}
	})

	t.Run("vp_vault_edit", func(t *testing.T) {
		vault := newVaultRoot(t)
		abs := seedRaw(t, vault, toolProjCfg, "min_score = 1.0\n")

		p, _ := json.Marshal(map[string]any{
			"path": toolProjCfg, "old_string": "1.0", "new_string": "9.9",
		})
		_, err := VaultEditTool(vault).Handler(context.Background(), p)
		assertToolRefused(t, "vp_vault_edit", err)

		if got, _ := os.ReadFile(abs); string(got) != "min_score = 1.0\n" {
			t.Errorf("refused edit changed the file: %q", got)
		}
	})

	t.Run("vp_vault_move onto it", func(t *testing.T) {
		vault := newVaultRoot(t)
		seedRaw(t, vault, "Projects/p/staged.toml", "[palace.scoring]\n")

		p, _ := json.Marshal(map[string]any{
			"from_path": "Projects/p/staged.toml", "to_path": toolProjCfg,
		})
		_, err := VaultMoveTool(vault).Handler(context.Background(), p)
		assertToolRefused(t, "vp_vault_move", err)

		if _, serr := os.Lstat(filepath.Join(vault.Root, filepath.FromSlash(toolProjCfg))); !os.IsNotExist(serr) {
			t.Errorf("refused move created the destination (stat err = %v)", serr)
		}
	})
}

// TestVaultToolsStillAllowTheSanctionedOperations is the counterweight. A gate
// placed one line too high — on IsRefusedWritePath, say — would refuse these
// too, and every refusal row above would still pass.
func TestVaultToolsStillAllowTheSanctionedOperations(t *testing.T) {
	t.Run("vp_vault_delete", func(t *testing.T) {
		vault := newVaultRoot(t)
		abs := seedRaw(t, vault, toolProjCfg, "[palace.scoring]\n")

		p, _ := json.Marshal(map[string]any{"path": toolProjCfg})
		if _, err := VaultDeleteTool(vault).Handler(context.Background(), p); err != nil {
			t.Fatalf("vp_vault_delete must be allowed: %v", err)
		}
		if _, err := os.Lstat(abs); !os.IsNotExist(err) {
			t.Errorf("file survived delete (stat err = %v)", err)
		}
	})

	t.Run("vp_vault_move out of it", func(t *testing.T) {
		vault := newVaultRoot(t)
		seedRaw(t, vault, toolProjCfg, "[palace.scoring]\n")

		p, _ := json.Marshal(map[string]any{
			"from_path": toolProjCfg, "to_path": "Projects/p/config.toml.retired",
		})
		if _, err := VaultMoveTool(vault).Handler(context.Background(), p); err != nil {
			t.Fatalf("moving the config OUT must be allowed: %v", err)
		}
	})

	t.Run("vp_vault_write to a near miss", func(t *testing.T) {
		vault := newVaultRoot(t)
		p, _ := json.Marshal(map[string]any{"path": "Projects/p/doc/config.toml", "content": "x"})
		if _, err := VaultWriteTool(vault).Handler(context.Background(), p); err != nil {
			t.Errorf("Projects/p/doc/config.toml must still be writable: %v", err)
		}
	})
}
