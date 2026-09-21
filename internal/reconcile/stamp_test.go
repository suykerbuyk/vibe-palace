// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// assertStamped fails unless stampDir/.surface records the current surface.
func assertStamped(t *testing.T, stampDir string) {
	t.Helper()
	s, err := surface.ReadStamp(stampDir)
	if err != nil {
		t.Fatalf("ReadStamp(%s): %v", stampDir, err)
	}
	if s.Surface != surface.MCPSurfaceVersion {
		t.Fatalf("stamp at %s = surface %d, want %d", stampDir, s.Surface, surface.MCPSurfaceVersion)
	}
}

// TestMaterializeWritesNoStamp: materialize Apply used to stamp
// Templates/.surface when the prompt resolver's `o` answer drove an Update
// that overwrote a template. That write is gone — the Templates reconcile never
// writes a template — so a refused Update leaves no stamp and no changed file.
func TestMaterializeWritesNoStamp(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Templates", TemplateTreeSeed{Mode: TemplateModeMaterialize})

	target, _ := seedOverride(t, root, "commands/wrap.md", []byte("# override\n"))
	plan := Plan{Actions: []Action{{Kind: ActionUpdate, Target: target, Summary: "overwrite"}}}
	rep, err := r.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Updated != 0 || len(rep.Errors) != 1 {
		t.Fatalf("Update should be refused, got %+v", rep)
	}
	if got, _ := os.ReadFile(target); string(got) != "# override\n" {
		t.Errorf("override changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "Templates", ".surface")); !os.IsNotExist(err) {
		t.Errorf("a refused Update stamped Templates/.surface (err=%v)", err)
	}
}

// TestScaffoldStamps proves a scaffold Apply (commands/ + skills/ README stubs
// under Projects/<slug>/) leaves a .surface stamp at the project root.
func TestScaffoldStamps(t *testing.T) {
	root := t.TempDir()
	r := NewTemplateTree(root, "Projects/foo", TemplateTreeSeed{Mode: TemplateModeScaffold})
	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	assertStamped(t, filepath.Join(root, "Projects", "foo"))
}

// TestApplyUpgradeStamps proves applyUpgrade does NOT stamp: every config it
// upgrades is host-local (CWD project, global config) and lives outside the
// vault. The vault-config case it also covered went with the per-project vault
// config (task move-per-project-config-out-of-the-shared-vault).
func TestApplyUpgradeStamps(t *testing.T) {
	target := upgradeTarget{
		canonicalText: storage.CwdProjectTemplateContent(),
		templateText:  storage.CwdProjectTemplateContent(),
	}

	t.Run("host-local config is not stamped", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(cfgPath, []byte("# project overrides\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := applyUpgrade(cfgPath, target); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".surface")); !os.IsNotExist(err) {
			t.Fatalf("host-local upgrade left a .surface stamp (err=%v)", err)
		}
	})
}
