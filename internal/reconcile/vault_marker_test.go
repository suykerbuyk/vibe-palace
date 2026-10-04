// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// gitignoreHasDerived reports whether the vault .gitignore holds any
// marker-gated derived-index line.
func gitignoreHasDerived(t *testing.T, vault string) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(vault, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		for _, p := range storage.MigratedVaultGitignorePatterns {
			if line == p {
				return true
			}
		}
	}
	return false
}

func applyVault(t *testing.T, seed VaultSeed) Report {
	t.Helper()
	r := NewVault(t.TempDir(), seed)
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	rep, err := r.Apply(context.Background(), p)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return rep
}

// Born migrated is InitVault-only (half a): the reconciler's create branch,
// as `vp init` and onboarding drive it, gives no marker and no derived lines.
// Mutant: the marker written in the reconciler's create branch.
func TestVaultReconcile_CreateBranchIsNotBornMigrated(t *testing.T) {
	vault := filepath.Join(t.TempDir(), "vault")
	rep := applyVault(t, VaultSeed{VaultPath: vault}.WithCreate())
	if len(rep.Errors) > 0 {
		t.Fatalf("Apply errors: %v", rep.Errors)
	}
	m, err := surface.ReadVaultManifest(vault)
	if err != nil {
		t.Fatal(err)
	}
	if m.AuthoredOnly != "" {
		t.Errorf("the create branch wrote the marker %q", m.AuthoredOnly)
	}
	if gitignoreHasDerived(t, vault) {
		t.Error("the create branch wrote derived ignore lines")
	}
}

// The top-up (`vp init` over an existing vault; `vp config sync` plans the
// same Update): no derived lines without the marker; both lines with it.
func TestVaultReconcile_TopUpFollowsTheMarker(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, ".gitignore"), []byte("# own\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := applyVault(t, VaultSeed{VaultPath: vault}.WithCreate())
	if len(rep.Errors) > 0 || gitignoreHasDerived(t, vault) {
		t.Fatalf("unmigrated top-up: errors %v, derived lines %v", rep.Errors, gitignoreHasDerived(t, vault))
	}
	if err := surface.WriteVaultManifest(vault, surface.VaultManifest{Format: surface.RequiredDataFormat, AuthoredOnly: "2026-10-04"}); err != nil {
		t.Fatal(err)
	}
	rep = applyVault(t, VaultSeed{VaultPath: vault}.WithCreate())
	if len(rep.Errors) > 0 || !gitignoreHasDerived(t, vault) {
		t.Fatalf("migrated top-up: errors %v, derived lines %v", rep.Errors, gitignoreHasDerived(t, vault))
	}
}

// A malformed marker: the plan skips the .gitignore with the error named, and
// Apply writes nothing (R3, writer gates fail closed).
func TestVaultReconcile_MalformedMarkerWritesNothing(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, ".gitignore"), []byte("# own\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(vault, ".vibe-palace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, ".vibe-palace", "vault.toml"), []byte("format = 2\nauthored_only = 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewVault(t.TempDir(), VaultSeed{VaultPath: vault}.WithCreate())
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var skip *Action
	for i, a := range p.Actions {
		if filepath.Base(a.Target) == ".gitignore" {
			skip = &p.Actions[i]
		}
	}
	if skip == nil || skip.Kind != ActionSkip || !strings.Contains(skip.Summary, "authored_only") {
		t.Fatalf("want the .gitignore action skipped naming authored_only, got %+v", skip)
	}
	if _, err := r.Apply(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(vault, ".gitignore")); string(got) != "# own\n" {
		t.Errorf(".gitignore was written: %q", got)
	}
}

// Born migrated is InitVault-only (half b): ScaffoldNewVault alone — the
// scaffold `vp vault init` passes, and the split destination's scaffold at
// a32a2d4 — gives no marker and no derived lines. The marker comes from
// storage.InitVault after the scaffold returns. Mutant: the marker written
// in ScaffoldNewVault.
func TestScaffoldNewVault_IsNotBornMigrated(t *testing.T) {
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	vault := filepath.Join(t.TempDir(), "dest")
	if err := ScaffoldNewVault(context.Background(), vault); err != nil {
		t.Fatalf("ScaffoldNewVault: %v", err)
	}
	if migrated, err := storage.VaultMigrated(vault); err != nil || migrated {
		t.Errorf("VaultMigrated = %v, %v; want an unmarked vault", migrated, err)
	}
	if gitignoreHasDerived(t, vault) {
		t.Error("ScaffoldNewVault wrote derived ignore lines")
	}
}
