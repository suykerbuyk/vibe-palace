// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// staleHostVault: Projects/old/ pushed, renamed away by a second clone, and an
// unpushed commit under Projects/old/ on this vault.
func staleHostVault(t *testing.T) string {
	t.Helper()
	sandboxHostEnv(t)
	root := initVaultRepo(t)
	bare := t.TempDir()
	gitT(t, bare, "init", "--bare", "-b", "main")
	gitT(t, root, "remote", "add", "origin", bare)
	put := func(dir, rel, body string) {
		t.Helper()
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(root, "Projects/old/resume.md", "old\n")
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-m", "seed old")
	gitT(t, root, "push", "origin", "main")

	other := t.TempDir()
	gitT(t, other, "clone", "-q", "-b", "main", bare, ".")
	gitT(t, other, "config", "user.email", "other@example.com")
	gitT(t, other, "config", "user.name", "Other")
	gitT(t, other, "mv", "Projects/old", "Projects/new")
	gitT(t, other, "commit", "-m", "rename old -> new")
	gitT(t, other, "push", "origin", "main")

	put(root, "Projects/old/x.md", "stale host work\n")
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-m", "stale host work")
	return root
}

// T14. The MCP half: vp_vault_sync refuses in both pull and sync mode with the
// remedy, and a refused sync is the caller's to act on.
func TestVaultSyncToolReturnsTheRefusal(t *testing.T) {
	for _, action := range []string{"pull", "sync"} {
		t.Run(action, func(t *testing.T) {
			root := staleHostVault(t)
			head := gitT(t, root, "rev-parse", "HEAD")
			params, _ := json.Marshal(vaultSyncParams{Action: action})
			_, err := VaultSyncTool(storage.NewVault(root)).Handler(context.Background(), params)
			if err == nil {
				t.Fatalf("vp_vault_sync %s must refuse", action)
			}
			for _, want := range []string{"Projects/old/x.md", "branch hold/departed-old-", "where it went is not recorded"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s error must contain %q, got:\n%v", action, want, err)
				}
			}
			if action == "sync" && !apperr.IsCaller(err) {
				t.Errorf("a refused sync is a caller error, got %T", err)
			}
			if got := gitT(t, root, "rev-parse", "HEAD"); got != head {
				t.Errorf("HEAD moved %s -> %s", head, got)
			}
		})
	}
}
