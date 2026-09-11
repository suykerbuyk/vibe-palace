// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// removeRetiredLock re-checks the lock at apply time and removes it only
// through a compare-and-set on the bytes the plan read. These drive the
// branches a whole sync cannot time.

// lockRepo is a git vault (its own repository, one commit) with an untracked,
// un-ignored templates.lock holding body.
func lockRepo(t *testing.T, body string) (vault, lock string) {
	t.Helper()
	vault = t.TempDir()
	gitInVault(t, vault, "init", "-q", "-b", "main")
	gitInVault(t, vault, "config", "user.email", "t@t.com")
	gitInVault(t, vault, "config", "user.name", "T")
	putVaultFile(t, vault, "README.md", "seed\n")
	gitInVault(t, vault, "add", "-A")
	gitInVault(t, vault, "commit", "-qm", "seed")
	return vault, putVaultFile(t, vault, ".vibe-palace/templates.lock", body)
}

func plannedLock(t *testing.T, vault string) reconcile.Action {
	t.Helper()
	a, err := planRetiredLockRemoval(vault)
	if err != nil || a == nil {
		t.Fatalf("planRetiredLockRemoval: %v %v", a, err)
	}
	return *a
}

func TestRemoveRetiredLock(t *testing.T) {
	t.Run("removed", func(t *testing.T) {
		vault, lock := lockRepo(t, retiredLockBody)
		a := plannedLock(t, vault)
		out := captureStdout(t, func() {
			if err := removeRetiredLock(vault, a); err != nil {
				t.Error(err)
			}
		})
		if !strings.Contains(out, "removed the retired .vibe-palace/templates.lock") {
			t.Errorf("stdout = %q", out)
		}
		assertAbsent(t, lock)
	})
	t.Run("changed since plan", func(t *testing.T) {
		vault, lock := lockRepo(t, retiredLockBody)
		a := plannedLock(t, vault)
		putVaultFile(t, vault, ".vibe-palace/templates.lock", "[entries]\n# rewritten by a lagging vp\n")
		out := captureStdout(t, func() {
			if err := removeRetiredLock(vault, a); err != nil {
				t.Error(err)
			}
		})
		if !strings.Contains(out, "changed since plan; kept") {
			t.Errorf("stdout = %q", out)
		}
		assertFileBytes(t, lock, "[entries]\n# rewritten by a lagging vp\n")
	})
	t.Run("tracked since plan", func(t *testing.T) {
		vault, lock := lockRepo(t, retiredLockBody)
		a := plannedLock(t, vault)
		gitInVault(t, vault, "add", ".vibe-palace/templates.lock")
		out := captureStdout(t, func() {
			if err := removeRetiredLock(vault, a); err != nil {
				t.Error(err)
			}
		})
		if !strings.Contains(out, "no longer untracked and un-ignored") {
			t.Errorf("stdout = %q", out)
		}
		assertFileBytes(t, lock, retiredLockBody)
	})
	t.Run("gone since plan", func(t *testing.T) {
		vault, lock := lockRepo(t, retiredLockBody)
		a := plannedLock(t, vault)
		if err := os.Remove(lock); err != nil {
			t.Fatal(err)
		}
		out := captureStdout(t, func() {
			if err := removeRetiredLock(vault, a); err != nil {
				t.Error(err)
			}
		})
		if out != "" {
			t.Errorf("an absent lock printed %q", out)
		}
	})
	t.Run("git cannot read the vault", func(t *testing.T) {
		vault := t.TempDir()
		putVaultFile(t, vault, ".git", "gitdir: /nonexistent\n")
		putVaultFile(t, vault, ".vibe-palace/templates.lock", retiredLockBody)
		sum := sha256.Sum256([]byte(retiredLockBody))
		a := reconcile.Action{Kind: reconcile.ActionDelete, Details: []string{"retired_lock=true", "vault_sha=" + hex.EncodeToString(sum[:])}}
		if err := removeRetiredLock(vault, a); err == nil {
			t.Error("a git failure was not an error")
		}
		if _, err := planRetiredLockRemoval(vault); err == nil {
			t.Error("planning over a git failure was not an error")
		}
	})
}

func TestPlanRetiredLockRemovalLeavesNothingToPlan(t *testing.T) {
	vault, lock := lockRepo(t, retiredLockBody)
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if a, err := planRetiredLockRemoval(vault); err != nil || a != nil {
		t.Errorf("no lock: %v %v", a, err)
	}
}

func TestSplitRetiredLockAndBuiltinKeys(t *testing.T) {
	lock := reconcile.Action{Kind: reconcile.ActionDelete, Details: []string{"retired_lock=true"}}
	prune := reconcile.Action{Kind: reconcile.ActionDelete, Details: []string{"embedded_relpath=commands/wrap.md"}}
	keep := reconcile.Action{Kind: reconcile.ActionUnchanged}
	rest, got := splitRetiredLock([]reconcile.Action{prune, lock, keep})
	if got == nil || got.Detail("retired_lock") != "true" || len(rest) != 2 {
		t.Errorf("rest=%v lock=%v", rest, got)
	}
	if _, none := splitRetiredLock([]reconcile.Action{prune}); none != nil {
		t.Error("a plan with no lock removal returned one")
	}

	for key, want := range map[string]bool{
		"Templates/commands/wrap.md":   true,
		"Templates/commands/mine.md":   false,
		"Projects/p/commands/wrap.md":  false,
		"Templates/skills/x/SKILL.md":  false,
		"Templates/workflow.md":        true,
		".vibe-palace/templates.lock":  false,
		"Templates/commands/wrap.md.x": false,
	} {
		if got := isBuiltinTemplateKey(key); got != want {
			t.Errorf("isBuiltinTemplateKey(%q) = %v, want %v", key, got, want)
		}
	}

	if got := pruneBasisWords("commands/restart.md", templates.ProvenanceEarlier); got != "earlier shipped version of commands/restart.md" {
		t.Errorf("earlier: %q", got)
	}
	if got := pruneBasisWords("commands/wrap.md", templates.ProvenanceCurrent); got != "current embedded copy" {
		t.Errorf("current: %q", got)
	}
	if got := pruneBasisWords("commands/wrap.md", templates.ProvenanceOperator); got != "" {
		t.Errorf("operator: %q", got)
	}
}
