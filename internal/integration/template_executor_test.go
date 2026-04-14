// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// TestIntegrationTemplateExecutorBackupAsymmetry pins down the
// .bak policy centralized through templates.Executor:
//   - commands.Apply writes new content and leaves NO .bak (even
//     when the target was dirty),
//   - commands.ApplyWithBackup writes new content AND preserves the
//     pre-existing user-edited bytes in a sibling .bak.
//
// Failure of this test means either the asymmetry documented in
// doc/TEMPLATE_POLICY.md has regressed (commands emitting .bak, or
// skills suppressing it) or the Executor plumbing has been
// short-circuited.
func TestIntegrationTemplateExecutorBackupAsymmetry(t *testing.T) {
	vault := t.TempDir()
	r := vpctx.NewResolver(vault)

	// Put a user-edited vault copy in place for a known embedded command.
	target := filepath.Join(vault, "Templates/commands/wrap.md")
	writeFile(t, target, "# user-edited wrap\n")

	plan, err := commands.Plan(r, commands.PlanOptions{Only: "wrap"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan) != 1 || plan[0].Kind != commands.ChangeUpdated {
		t.Fatalf("plan = %+v, want one Updated", plan)
	}

	// --- commands.Apply: NO .bak ---
	if err := commands.Apply(plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(target + ".bak"); !os.IsNotExist(err) {
		t.Errorf("commands.Apply must not emit .bak; got err=%v", err)
	}
	// Primary has embedded content now.
	got, _ := os.ReadFile(target)
	if string(got) != plan[0].EmbeddedContent {
		t.Errorf("primary content after Apply didn't become embedded")
	}

	// Reset to user-edited state for the second leg.
	writeFile(t, target, "# user-edited wrap v2\n")
	plan2, err := commands.Plan(r, commands.PlanOptions{Only: "wrap"})
	if err != nil {
		t.Fatalf("Plan(2): %v", err)
	}
	if len(plan2) != 1 || plan2[0].Kind != commands.ChangeUpdated {
		t.Fatalf("plan2 = %+v, want one Updated", plan2)
	}

	// --- commands.ApplyWithBackup: .bak = prior bytes ---
	if err := commands.ApplyWithBackup(plan2); err != nil {
		t.Fatalf("ApplyWithBackup: %v", err)
	}
	bak, err := os.ReadFile(target + ".bak")
	if err != nil {
		t.Fatalf("read .bak: %v", err)
	}
	if string(bak) != "# user-edited wrap v2\n" {
		t.Errorf(".bak = %q, want user-edited-v2", string(bak))
	}
	got2, _ := os.ReadFile(target)
	if string(got2) != plan2[0].EmbeddedContent {
		t.Errorf("primary after ApplyWithBackup didn't become embedded")
	}
}

// TestIntegrationTemplateExecutorNewFileNoBackup covers the new-
// file edge case: no prior bytes exist, so neither policy leaves a
// .bak behind regardless of which Apply variant the CLI uses.
func TestIntegrationTemplateExecutorNewFileNoBackup(t *testing.T) {
	vault := t.TempDir()
	r := vpctx.NewResolver(vault)

	plan, err := commands.Plan(r, commands.PlanOptions{Only: "wrap"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan) != 1 || plan[0].Kind != commands.ChangeNew {
		t.Fatalf("plan = %+v, want one New", plan)
	}
	target := plan[0].VaultPath

	if err := commands.ApplyWithBackup(plan); err != nil {
		t.Fatalf("ApplyWithBackup: %v", err)
	}
	if _, err := os.Stat(target + ".bak"); !os.IsNotExist(err) {
		t.Errorf("new-file path must not produce .bak; got err=%v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read primary: %v", err)
	}
	if string(got) != plan[0].EmbeddedContent {
		t.Errorf("primary bytes didn't match embedded")
	}
}
