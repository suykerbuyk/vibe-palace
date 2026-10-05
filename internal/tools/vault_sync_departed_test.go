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

// A conflicted pull is aborted, and vp_vault_sync's error — the only thing
// the caller sees, since a handler error discards the body — names the
// conflicting path and the remedy, in pull and in sync mode. Mutant: the sync
// error without the per-remote failures.
func TestVaultSyncToolNamesTheAbortedConflict(t *testing.T) {
	for _, action := range []string{"pull", "sync"} {
		t.Run(action, func(t *testing.T) {
			sandboxHostEnv(t)
			root := initVaultRepo(t)
			bare := t.TempDir()
			gitT(t, bare, "init", "--bare", "-b", "main")
			gitT(t, root, "remote", "add", "origin", bare)
			write := func(dir, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write(root, "SEED\n")
			gitT(t, root, "add", "--", "notes.md")
			gitT(t, root, "commit", "-m", "seed notes")
			gitT(t, root, "push", "origin", "main")
			other := t.TempDir()
			gitT(t, other, "clone", "-q", "-b", "main", bare, ".")
			gitT(t, other, "config", "user.email", "other@example.com")
			gitT(t, other, "config", "user.name", "Other")
			write(other, "REMOTE\n")
			gitT(t, other, "commit", "-am", "remote notes")
			gitT(t, other, "push", "origin", "main")
			write(root, "LOCAL\n")
			gitT(t, root, "commit", "-am", "local notes")
			head := gitT(t, root, "rev-parse", "HEAD")

			params, _ := json.Marshal(vaultSyncParams{Action: action})
			_, err := VaultSyncTool(storage.NewVault(root)).Handler(context.Background(), params)
			if err == nil {
				t.Fatalf("vp_vault_sync %s must fail on a conflict", action)
			}
			for _, want := range []string{"conflicted on notes.md and was aborted", "the pull from origin was NOT applied"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s error must contain %q, got:\n%v", action, want, err)
				}
			}
			if got := gitT(t, root, "rev-parse", "HEAD"); got != head {
				t.Errorf("HEAD moved %s -> %s", head, got)
			}
			if u := gitT(t, root, "ls-files", "-u"); u != "" {
				t.Errorf("the conflicted merge was left in the vault: %q", u)
			}
		})
	}
}
