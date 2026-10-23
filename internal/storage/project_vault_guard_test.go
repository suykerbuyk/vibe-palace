// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// A cross-vault content write is REFUSED without the exemption, but ALLOWED when
// the lifecycle command passes atomicfile.ForMisboundExempt with the destination
// vault's live root-lock token — the per-write-site exemption that lets
// vault copy / vp_vault_copy relocate a project into its newly-bound vault.
func TestForMisboundExempt_AllowsCrossVaultWrite(t *testing.T) {
	f := newBindFixture(t)
	vaultB := makeTestVault(t, filepath.Join(f.home, "vault-b"))
	f.writeConfig(t, "\n[project_vaults]\np = \""+vaultB+"\"\n")
	installSnap(t)
	t.Cleanup(ArmProjectVaultGuard())

	abs := filepath.Join(f.global, "Projects", "p", "x.md")

	// WITHOUT the exemption: the cross-vault write is refused.
	if err := atomicfile.Write(f.global, abs, []byte("x"), atomicfile.CreatingProject()); !errors.Is(err, ErrCrossVaultWrite) {
		t.Fatalf("without ForMisboundExempt: want ErrCrossVaultWrite, got %v", err)
	}

	// WITH the exemption (destination root-lock token): allowed.
	held, err := vaultlock.AcquireHeld(f.global, f.global)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if err := atomicfile.Write(f.global, abs, []byte("x"),
		atomicfile.CreatingProject(), atomicfile.ForMisboundExempt(held)); err != nil {
		t.Fatalf("with ForMisboundExempt: want allow, got %v", err)
	}
	if _, serr := os.Lstat(abs); serr != nil {
		t.Fatal("the exempted cross-vault write should have created the file")
	}
}

// The exemption only applies with a token for the WRITE TARGET vault: a token
// for another vault does not exempt (cannot be spoofed).
func TestForMisboundExempt_WrongVaultTokenDoesNotExempt(t *testing.T) {
	f := newBindFixture(t)
	vaultB := makeTestVault(t, filepath.Join(f.home, "vault-b"))
	f.writeConfig(t, "\n[project_vaults]\np = \""+vaultB+"\"\n")
	installSnap(t)
	t.Cleanup(ArmProjectVaultGuard())

	held, err := vaultlock.AcquireHeld(vaultB, vaultB) // token for B, writing into A
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	abs := filepath.Join(f.global, "Projects", "p", "x.md")
	if err := atomicfile.Write(f.global, abs, []byte("x"),
		atomicfile.CreatingProject(), atomicfile.ForMisboundExempt(held)); !errors.Is(err, ErrCrossVaultWrite) {
		t.Fatalf("a token for another vault must NOT exempt: got %v", err)
	}
}

// installSnap builds the snapshot from the current config and installs it in the
// process-global holder, restoring on cleanup.
func installSnap(t *testing.T) {
	t.Helper()
	snap, err := BuildProjectVaultSnapshot(context.Background())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	t.Cleanup(SetActiveProjectVaultSnapshot(snap))
}

// captureWarn redirects slog to a buffer at Warn level for the test.
func captureWarn(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// WRITE path: a content write for a project bound to another vault is refused.
func TestRefuseCrossVaultContentWrite_BoundElsewhere(t *testing.T) {
	f := newBindFixture(t)
	vaultB := makeTestVault(t, filepath.Join(f.home, "vault-b"))
	f.writeConfig(t, "\n[project_vaults]\np = \""+vaultB+"\"\n")
	installSnap(t)
	t.Cleanup(ArmProjectVaultGuard())

	W := f.global // the write target, != vaultB
	err := refuseCrossVaultContentWrite(W, filepath.Join(W, "Projects", "p", "x.md"))
	if !errors.Is(err, ErrCrossVaultWrite) {
		t.Fatalf("want ErrCrossVaultWrite, got %v", err)
	}
	if !strings.Contains(err.Error(), vaultB) || !strings.Contains(err.Error(), `"p"`) {
		t.Fatalf("refusal must name the bound vault and project: %v", err)
	}
}

func TestRefuseCrossVaultContentWrite_BoundHere_Unbound_NonProject(t *testing.T) {
	f := newBindFixture(t)
	t.Cleanup(ArmProjectVaultGuard())

	t.Run("bound_here", func(t *testing.T) {
		f.writeConfig(t, "\n[project_vaults]\np = \""+f.global+"\"\n")
		installSnap(t)
		if err := refuseCrossVaultContentWrite(f.global, filepath.Join(f.global, "Projects", "p", "x.md")); err != nil {
			t.Fatalf("same-vault write must be allowed: %v", err)
		}
	})
	t.Run("unbound", func(t *testing.T) {
		f.writeConfig(t, "")
		installSnap(t)
		if err := refuseCrossVaultContentWrite(f.global, filepath.Join(f.global, "Projects", "p", "x.md")); err != nil {
			t.Fatalf("unbound project must be allowed: %v", err)
		}
	})
	t.Run("non_project_even_unset", func(t *testing.T) {
		// B2: a non-project path returns nil BEFORE reading the holder; install a
		// zero (unbuilt) holder to prove it is not consulted.
		t.Cleanup(SetActiveProjectVaultSnapshot(ProjectVaultSnapshot{}))
		if err := refuseCrossVaultContentWrite(f.global, filepath.Join(f.global, ".vibe-palace", "remotes.toml")); err != nil {
			t.Fatalf("non-project write must be allowed even with an unbuilt holder: %v", err)
		}
	})
}

// WRITE path: an unbuilt snapshot fails closed with the distinct message.
func TestRefuseCrossVaultContentWrite_UnbuiltFailsClosed(t *testing.T) {
	f := newBindFixture(t)
	t.Cleanup(ArmProjectVaultGuard())
	t.Cleanup(SetActiveProjectVaultSnapshot(ProjectVaultSnapshot{})) // unbuilt

	err := refuseCrossVaultContentWrite(f.global, filepath.Join(f.global, "Projects", "p", "x.md"))
	if !errors.Is(err, ErrCrossVaultWrite) {
		t.Fatalf("want ErrCrossVaultWrite, got %v", err)
	}
	if !strings.Contains(err.Error(), "not built") {
		t.Fatalf("unbuilt refusal must carry the distinct 'not built' message, got: %v", err)
	}
}

// WRITE path end-to-end through the atomicfile hook (the funnel every full write
// goes through), proving the injected guard fires.
func TestCrossVaultWrite_ThroughAtomicfileHook(t *testing.T) {
	f := newBindFixture(t)
	vaultB := makeTestVault(t, filepath.Join(f.home, "vault-b"))
	f.writeConfig(t, "\n[project_vaults]\np = \""+vaultB+"\"\n")
	installSnap(t)
	t.Cleanup(ArmProjectVaultGuard())

	abs := filepath.Join(f.global, "Projects", "p", "x.md")
	if err := atomicfile.Write(f.global, abs, []byte("data")); !errors.Is(err, ErrCrossVaultWrite) {
		t.Fatalf("atomicfile.Write must refuse a cross-vault write, got %v", err)
	}
	if _, serr := os.Lstat(abs); serr == nil {
		t.Fatal("the cross-vault file was created despite the refusal")
	}
	// A non-project write is allowed through the same armed hook.
	ok := filepath.Join(f.global, "top-level.md")
	if err := atomicfile.Write(f.global, ok, []byte("data")); err != nil {
		t.Fatalf("non-project write must pass the armed hook: %v", err)
	}
}

// WRITE path: appendUnderLock (the one append path that bypasses writeAtomic)
// runs the same check directly.
func TestAppendUnderLock_CrossVaultRefuses(t *testing.T) {
	f := newBindFixture(t)
	vaultB := makeTestVault(t, filepath.Join(f.home, "vault-b"))
	f.writeConfig(t, "\n[project_vaults]\np = \""+vaultB+"\"\n")
	installSnap(t)
	t.Cleanup(ArmProjectVaultGuard())

	v := NewVault(f.global)
	err := v.appendUnderLock(filepath.Join(f.global, "Projects", "p", "log.md"), []byte("line\n"))
	if !errors.Is(err, ErrCrossVaultWrite) {
		t.Fatalf("appendUnderLock must refuse a cross-vault append, got %v", err)
	}
}

// DELETE path: warnCrossVaultDerivedDelete logs on cross-vault, is silent on
// same-vault and unbuilt (operator ruling: WARN + PROCEED, never refuse).
func TestWarnCrossVaultDerivedDelete(t *testing.T) {
	f := newBindFixture(t)
	vaultB := makeTestVault(t, filepath.Join(f.home, "vault-b"))
	t.Cleanup(ArmProjectVaultGuard())

	t.Run("cross_vault_warns", func(t *testing.T) {
		f.writeConfig(t, "\n[project_vaults]\np = \""+vaultB+"\"\n")
		installSnap(t)
		buf := captureWarn(t)
		warnCrossVaultDerivedDelete(f.global, "palace/p/drawers/foo.md")
		out := buf.String()
		if !strings.Contains(out, "project=p") || !strings.Contains(out, vaultB) {
			t.Fatalf("cross-vault derived delete must warn with project + bound vault, got %q", out)
		}
	})
	t.Run("same_vault_silent", func(t *testing.T) {
		f.writeConfig(t, "\n[project_vaults]\np = \""+f.global+"\"\n")
		installSnap(t)
		buf := captureWarn(t)
		warnCrossVaultDerivedDelete(f.global, "palace/p/drawers/foo.md")
		if buf.Len() != 0 {
			t.Fatalf("same-vault derived delete must be silent, got %q", buf.String())
		}
	})
	t.Run("unbuilt_silent", func(t *testing.T) {
		t.Cleanup(SetActiveProjectVaultSnapshot(ProjectVaultSnapshot{}))
		buf := captureWarn(t)
		warnCrossVaultDerivedDelete(f.global, "palace/p/drawers/foo.md")
		if buf.Len() != 0 {
			t.Fatalf("unbuilt snapshot must not warn on the delete path, got %q", buf.String())
		}
	})
}

// DELETE path end-to-end: dropDerivedPaths on a cross-vault derived path WARNS,
// DELETES the file, and returns NO error (ALLOW + WARN).
func TestDropDerivedPaths_CrossVaultWarnsAndDeletes(t *testing.T) {
	f := newBindFixture(t)
	vaultB := makeTestVault(t, filepath.Join(f.home, "vault-b"))
	f.writeConfig(t, "\n[project_vaults]\np = \""+vaultB+"\"\n")
	installSnap(t)
	t.Cleanup(ArmProjectVaultGuard())

	// f.global is a git vault holding a tracked derived path.
	gitRun(t, f.global, "init", "-q")
	gitRun(t, f.global, "config", "user.email", "t@example.com")
	gitRun(t, f.global, "config", "user.name", "t")
	rel := "palace/p/drawers/foo.md"
	abs := filepath.Join(f.global, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("derived\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.global, "add", "-A")
	gitRun(t, f.global, "commit", "-q", "-m", "seed")

	buf := captureWarn(t)
	if err := dropDerivedPaths(f.global, []string{rel}, true); err != nil {
		t.Fatalf("dropDerivedPaths must NOT error on a cross-vault derived delete: %v", err)
	}
	if _, serr := os.Lstat(abs); serr == nil {
		t.Fatal("the derived path was not deleted (ALLOW+WARN must still delete)")
	}
	if !strings.Contains(buf.String(), "project=p") {
		t.Fatalf("the cross-vault warning did not fire, got %q", buf.String())
	}
}
