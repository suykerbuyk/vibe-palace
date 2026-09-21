// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/migrate"
	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
)

// TestIntegrationMigrateScaffoldsTheProjectAndWritesNoVaultConfig verifies end
// to end that `vp migrate` initialises each destination project the way `vp
// init` does — the Projects/<slug>/{commands,skills}/ README scaffold — and
// writes no Projects/<slug>/config.toml. The retired vault-project reconciler
// used to write that file here; nothing does now, so a migrate that re-created
// it would undo the delete pass.
func TestIntegrationMigrateScaffoldsTheProjectAndWritesNoVaultConfig(t *testing.T) {
	h := newHarness(t, false)

	// Stand up a VibeVault-style project tree.
	projDir := filepath.Join(h.Vault.Root, "Projects", "demo-project")
	sessDir := filepath.Join(projDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	session := `---
session_id: "2026-04-14-01"
project: demo-project
date: "2026-04-14"
title: "Reconciler integration"
summary: "scaffolds the destination project"
tag: implementation
---
## Transcript

Verify migrate scaffolds the project.
`
	if err := os.WriteFile(filepath.Join(sessDir, "s1.md"), []byte(session), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run migrate end-to-end.
	res, err := migrate.ImportVibeVault(
		context.Background(),
		h.Vault, h.Vault, h.Engine, h.Embedder, h.Config,
		migrate.ImportOptions{},
	)
	if err != nil {
		t.Fatalf("ImportVibeVault: %v", err)
	}
	if res.SessionsImported != 1 {
		t.Errorf("SessionsImported = %d, want 1", res.SessionsImported)
	}

	// Assertion 1: no per-project vault config.
	cfgPath := filepath.Join(projDir, "config.toml")
	if _, err := os.Lstat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("migrate created %s (lstat err: %v); nothing may write the retired vault config", cfgPath, err)
	}

	// Assertion 2: the init scaffold is in place.
	readmes := map[string][]byte{}
	for _, rel := range []string{"commands/README.md", "skills/README.md"} {
		b, err := os.ReadFile(filepath.Join(projDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("expected %s scaffolded by migrate: %v", rel, err)
			continue
		}
		readmes[rel] = b
	}

	// Assertion 3: the scaffold is drift-free — planning it again yields only
	// Unchanged actions.
	tt := reconcile.NewTemplateTree(h.Vault.Root, "Projects/demo-project",
		reconcile.TemplateTreeSeed{Mode: reconcile.TemplateModeScaffold})
	plan, err := tt.Plan(context.Background())
	if err != nil {
		t.Fatalf("post-migrate Plan: %v", err)
	}
	for _, a := range plan.Actions {
		if a.Kind != reconcile.ActionUnchanged {
			t.Errorf("post-migrate drift: action %+v (want all Unchanged)", a)
		}
	}

	// Assertion 4: re-running migrate is idempotent — the READMEs are
	// byte-unchanged and still no config.toml appears.
	if _, err := migrate.ImportVibeVault(
		context.Background(),
		h.Vault, h.Vault, h.Engine, h.Embedder, h.Config,
		migrate.ImportOptions{},
	); err != nil {
		t.Fatalf("second ImportVibeVault: %v", err)
	}
	for rel, before := range readmes {
		after, err := os.ReadFile(filepath.Join(projDir, filepath.FromSlash(rel)))
		if err != nil || !bytes.Equal(before, after) {
			t.Errorf("%s mutated across idempotent migrate runs (err=%v)", rel, err)
		}
	}
	if _, err := os.Lstat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("second migrate created %s (lstat err: %v)", cfgPath, err)
	}
}
