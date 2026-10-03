// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// nestedVaultPath is a not-yet-existing vault path inside another git
// repository's work tree, with that repository's porcelain before anything ran.
func nestedVaultPath(t *testing.T) (outer, vault, before string) {
	t.Helper()
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	outer = t.TempDir()
	if out, err := exec.Command("git", "init", "-q", outer).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	return outer, filepath.Join(outer, "vault"), outerPorcelain(t, outer)
}

func outerPorcelain(t *testing.T, outer string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", outer, "status", "--porcelain", "-uall").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %s: %v", out, err)
	}
	return string(out)
}

// A seeded (create-mode) plan for a vault nested inside another repository is
// that one Skip and nothing else, so Apply writes nothing into the enclosing
// work tree — no directory, no .gitignore, no data-format stamp.
func TestVaultPlan_SeededNestedVaultPlansOnlyTheSkip(t *testing.T) {
	outer, vault, before := nestedVaultPath(t)
	r := NewVault(t.TempDir(), VaultSeed{VaultPath: vault, GitEnabled: true}.WithCreate())
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(p.Actions) != 1 || p.Actions[0].Kind != ActionSkip || !strings.Contains(p.Actions[0].Summary, "git init skipped") {
		t.Fatalf("want exactly the git-init Skip, got %+v", p.Actions)
	}
	rep, err := r.Apply(context.Background(), p)
	if err != nil || len(rep.Errors) > 0 || rep.Created+rep.Updated > 0 {
		t.Errorf("Apply wrote or failed: %+v, %v", rep, err)
	}
	if _, err := os.Lstat(vault); !os.IsNotExist(err) {
		t.Errorf("the nested vault directory was created (lstat err: %v)", err)
	}
	if after := outerPorcelain(t, outer); after != before {
		t.Errorf("the enclosing repository's work tree changed:\n%s", after)
	}
}

// An unseeded plan (`vp config sync`) over an EXISTING nested vault keeps its
// other actions: there the git Skip is a report, not a refusal.
func TestVaultPlan_UnseededNestedVaultKeepsItsOtherActions(t *testing.T) {
	_, vault, _ := nestedVaultPath(t)
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(xdg, "vibe-palace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "vibe-palace", "config.toml"),
		[]byte(`vault_path = "`+vault+`"`+"\ngit_enabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewVault(t.TempDir(), VaultSeed{})
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var skip, gitignore bool
	for _, a := range p.Actions {
		if a.Kind == ActionSkip && strings.Contains(a.Summary, "git init skipped") {
			skip = true
		}
		if filepath.Base(a.Target) == ".gitignore" && a.Kind == ActionCreate {
			gitignore = true
		}
	}
	if !skip || !gitignore {
		t.Errorf("want the git Skip reported beside the .gitignore create, got %+v", p.Actions)
	}
}

// ScaffoldNewVault never reads a seeded plan that applied nothing as a vault
// it scaffolded.
func TestScaffoldNewVault_NestedDestinationAborts(t *testing.T) {
	outer, vault, before := nestedVaultPath(t)
	err := ScaffoldNewVault(context.Background(), vault)
	if err == nil || !strings.Contains(err.Error(), "git init skipped") {
		t.Fatalf("err = %v, want the nesting refusal", err)
	}
	if after := outerPorcelain(t, outer); after != before {
		t.Errorf("the enclosing repository's work tree changed:\n%s", after)
	}
}
