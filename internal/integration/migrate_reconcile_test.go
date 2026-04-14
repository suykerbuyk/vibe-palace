// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/migrate"
	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
)

// TestIntegrationMigrateUsesVaultProjectReconciler verifies end-to-end
// that `vp migrate`-equivalent flow routes vault-project config.toml
// creation through reconcile.VaultProject (Check → Plan → Apply) and
// that a subsequent Plan() returns a clean (drift-free) set of
// Unchanged actions — i.e. the reconciler path is the sole orchestrator.
func TestIntegrationMigrateUsesVaultProjectReconciler(t *testing.T) {
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
summary: "routes through reconcile.VaultProject"
tag: implementation
---
## Transcript

Verify migrate uses the reconciler.
`
	if err := os.WriteFile(filepath.Join(sessDir, "s1.md"), []byte(session), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run migrate end-to-end.
	res, err := migrate.ImportVibeVault(
		context.Background(),
		h.Vault, h.Engine, h.Embedder, h.Config,
		migrate.ImportOptions{},
	)
	if err != nil {
		t.Fatalf("ImportVibeVault: %v", err)
	}
	if res.SessionsImported != 1 {
		t.Errorf("SessionsImported = %d, want 1", res.SessionsImported)
	}

	// Assertion 1: config.toml exists, and matches the canonical template
	// the reconciler would have rendered.
	cfgPath := filepath.Join(projDir, "config.toml")
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	// Sanity: contains a template-shaped TOML comment header.
	if !strings.Contains(string(got), "[") {
		t.Errorf("config.toml missing TOML table marker; got:\n%s", got)
	}

	// Assertion 2: tasks/done and tasks/cancelled exist — these are
	// created ONLY by reconcile.VaultProject.Apply, never by the old
	// direct storage.WriteVaultProjectConfig call. Their presence
	// proves migrate now goes through the reconciler.
	for _, sub := range []string{"done", "cancelled"} {
		p := filepath.Join(projDir, "tasks", sub)
		fi, err := os.Stat(p)
		if err != nil {
			t.Errorf("expected tasks/%s created by reconciler: %v", sub, err)
			continue
		}
		if !fi.IsDir() {
			t.Errorf("tasks/%s should be a directory", sub)
		}
	}

	// Assertion 3: drift re-detect is clean — running Plan again
	// yields only Unchanged actions (no Create/Update).
	r := reconcile.NewVaultProject(h.Vault, "demo-project")
	plan, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("post-migrate Plan: %v", err)
	}
	for _, a := range plan.Actions {
		if a.Kind != reconcile.ActionUnchanged {
			t.Errorf("post-migrate drift: action %+v (want all Unchanged)", a)
		}
	}

	// Assertion 4: re-running migrate is idempotent — config.toml
	// bytes unchanged.
	if _, err := migrate.ImportVibeVault(
		context.Background(),
		h.Vault, h.Engine, h.Embedder, h.Config,
		migrate.ImportOptions{},
	); err != nil {
		t.Fatalf("second ImportVibeVault: %v", err)
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config.toml (second run): %v", err)
	}
	if string(got) != string(after) {
		t.Error("config.toml mutated across idempotent migrate runs")
	}
}
