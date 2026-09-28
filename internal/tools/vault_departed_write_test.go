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

// The MCP half of the departed-project write refusal: vp_vault_write, _edit
// and _move reach vaultfs, which refuses. The dispatch seam
// (mcp.refuseDepartedProject) reads only `project`/`to_project`, so without the
// vaultfs gate these path-taking tools re-create a moved project's tree. The
// CLI half is in cmd/vp/cmd_vault_departed_write_test.go.

const toolDepartedTo = "git@gitlab.example.com:q/vibe-palace-vault.git"

// newVaultRootAt is newVaultRoot over an existing root.
func newVaultRootAt(root string) *storage.Vault { return storage.NewVault(root) }

func departedToolVault(t *testing.T) (string, func(rel string) bool) {
	t.Helper()
	vault := newVaultRoot(t)
	seedRaw(t, vault, "Audits/departures/p.json",
		`{"format":"1","slug":"p","kind":"moved-to-vault","to":"`+toolDepartedTo+`","date":"2026-09-27"}`+"\n")
	exists := func(rel string) bool {
		_, err := os.Lstat(filepath.Join(vault.Root, filepath.FromSlash(rel)))
		return err == nil
	}
	return vault.Root, exists
}

func assertDepartedRefused(t *testing.T, tool string, err error) {
	t.Helper()
	if !errors.Is(err, vaultfs.ErrDepartedProject) {
		t.Fatalf("%s: err = %v, want ErrDepartedProject", tool, err)
	}
	if !strings.Contains(err.Error(), toolDepartedTo) || !strings.Contains(err.Error(), "vp config bind p") {
		t.Fatalf("%s: the refusal must name where p went and the bind: %v", tool, err)
	}
}

func TestVaultToolsRefuseWritesIntoADepartedProject(t *testing.T) {
	t.Run("vp_vault_write", func(t *testing.T) {
		root, exists := departedToolVault(t)
		vault := newVaultRootAt(root)
		p, _ := json.Marshal(map[string]any{"path": "Projects/p/resume.md", "content": "stale\n"})
		_, err := VaultWriteTool(vault).Handler(context.Background(), p)
		assertDepartedRefused(t, "vp_vault_write", err)
		if exists("Projects/p") {
			t.Fatal("vp_vault_write re-created Projects/p")
		}
	})
	t.Run("vp_vault_edit", func(t *testing.T) {
		root, _ := departedToolVault(t)
		vault := newVaultRootAt(root)
		abs := seedRaw(t, vault, "Projects/p/resume.md.bak", "old\n")
		p, _ := json.Marshal(map[string]any{"path": "Projects/p/resume.md.bak", "old_string": "old", "new_string": "new"})
		_, err := VaultEditTool(vault).Handler(context.Background(), p)
		assertDepartedRefused(t, "vp_vault_edit", err)
		if got, _ := os.ReadFile(abs); string(got) != "old\n" {
			t.Fatalf("vp_vault_edit changed the file: %q", got)
		}
	})
	t.Run("vp_vault_move", func(t *testing.T) {
		root, exists := departedToolVault(t)
		vault := newVaultRootAt(root)
		seedRaw(t, vault, "Projects/keep/notes.md", "n\n")
		p, _ := json.Marshal(map[string]any{"from_path": "Projects/keep/notes.md", "to_path": "Projects/p/notes.md"})
		_, err := VaultMoveTool(vault).Handler(context.Background(), p)
		assertDepartedRefused(t, "vp_vault_move", err)
		if exists("Projects/p") || !exists("Projects/keep/notes.md") {
			t.Fatal("vp_vault_move ran")
		}
	})
}
