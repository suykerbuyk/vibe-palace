// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func reconcilePanic(f func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}

// A2: the init scaffold's directory creation (applyScaffold cannot route through
// EnsureVaultDir, which refuses the project it creates) is refused under the
// ambient vault before the directory exists; a non-ambient vault gets it.
func TestApplyScaffoldDirectoryIsRefusedInTheAmbientVault(t *testing.T) {
	plan := func(root string) Plan {
		return Plan{Actions: []Action{{Kind: ActionCreate, Target: filepath.Join(root, "Projects", "p", "commands")}}}
	}
	ctl := t.TempDir()
	rep, err := NewTemplateTree(ctl, "Projects/p", TemplateTreeSeed{Mode: TemplateModeScaffold}).Apply(context.Background(), plan(ctl))
	if err != nil || len(rep.Errors) > 0 {
		t.Fatalf("control apply: %v %v", err, rep.Errors)
	}
	if _, err := os.Stat(filepath.Join(ctl, "Projects", "p", "commands")); err != nil {
		t.Fatalf("control did not create the directory: %v", err)
	}

	amb := t.TempDir()
	t.Cleanup(surface.SetAmbientVaultRoot(amb))
	msg := reconcilePanic(func() {
		_, _ = NewTemplateTree(amb, "Projects/p", TemplateTreeSeed{Mode: TemplateModeScaffold}).Apply(context.Background(), plan(amb))
	})
	if !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("a scaffold directory in the ambient vault was not refused, got %q", msg)
	}
	if _, err := os.Lstat(filepath.Join(amb, "Projects")); err == nil {
		t.Error("Projects/ was created in the ambient vault before the refusal")
	}
}

// A7: the vault ROOT itself, when absent, is not created under the tripwire
// either: the ambient configured path can itself be absent.
func TestVaultReconcilerDoesNotCreateAnAbsentAmbientRoot(t *testing.T) {
	apply := func(vault string) (Report, error) {
		r := NewVault(t.TempDir(), VaultSeed{VaultPath: vault}.WithCreate())
		plan, err := r.Plan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return r.Apply(context.Background(), plan)
	}
	ctl := filepath.Join(t.TempDir(), "vault")
	if _, err := apply(ctl); err != nil {
		t.Fatalf("control apply: %v", err)
	}
	if _, err := os.Stat(ctl); err != nil {
		t.Fatalf("control did not create the vault root: %v", err)
	}

	ambient := filepath.Join(t.TempDir(), "configured-vault") // does not exist
	t.Cleanup(surface.SetAmbientVaultRoot(ambient))
	msg := reconcilePanic(func() { _, _ = apply(ambient) })
	if !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("an absent ambient vault root was not refused, got %q", msg)
	}
	if _, err := os.Lstat(ambient); err == nil {
		t.Error("the absent ambient vault root was created")
	}
}
