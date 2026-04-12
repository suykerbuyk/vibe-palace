// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package commands_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

func TestPlan_NewAndUnchangedAndUpdated(t *testing.T) {
	vault := t.TempDir()
	r := vpctx.NewResolver(vault)

	// Seed vault: one unchanged copy (exact match of an embedded command),
	// one user-edited copy (differs from embedded).
	unchanged, err := r.EmbeddedContent("command:restart")
	if err != nil {
		t.Fatalf("read embedded restart: %v", err)
	}
	writeVault(t, vault, "Templates/commands/restart.md", unchanged)
	writeVault(t, vault, "Templates/commands/wrap.md", "user-edited wrap content")

	plan, err := commands.Plan(r, commands.PlanOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	byName := map[string]commands.Change{}
	for _, c := range plan {
		byName[c.Name] = c
	}

	if got := byName["restart"].Kind; got != commands.ChangeUnchanged {
		t.Errorf("restart kind = %q, want unchanged", got)
	}
	if got := byName["wrap"].Kind; got != commands.ChangeUpdated {
		t.Errorf("wrap kind = %q, want updated", got)
	}
	// Any embedded command without a vault copy should be "new".
	foundNew := false
	for _, c := range plan {
		if c.Kind == commands.ChangeNew {
			foundNew = true
			if c.VaultHash != "" {
				t.Errorf("new change %q has VaultHash set", c.Name)
			}
			if c.VaultContent != "" {
				t.Errorf("new change %q has VaultContent set", c.Name)
			}
		}
	}
	if !foundNew {
		t.Error("expected at least one ChangeNew entry")
	}
}

func TestPlan_OnlyFilter(t *testing.T) {
	r := vpctx.NewResolver(t.TempDir())
	plan, err := commands.Plan(r, commands.PlanOptions{Only: "restart"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan) != 1 || plan[0].Name != "restart" {
		t.Fatalf("Plan(Only=restart) = %+v", plan)
	}

	if _, err := commands.Plan(r, commands.PlanOptions{Only: "no-such-command"}); err == nil {
		t.Error("expected error for unknown Only target")
	}
}

func TestApply_WritesAcceptedAndSkipsUnchanged(t *testing.T) {
	vault := t.TempDir()
	r := vpctx.NewResolver(vault)

	plan, err := commands.Plan(r, commands.PlanOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := commands.Apply(plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Every embedded command should now exist in the vault with matching bytes.
	for _, c := range plan {
		got, err := os.ReadFile(c.VaultPath)
		if err != nil {
			t.Fatalf("read %s: %v", c.VaultPath, err)
		}
		if string(got) != c.EmbeddedContent {
			t.Errorf("%s: content mismatch after Apply", c.Name)
		}
	}

	// Re-plan: everything should now be unchanged.
	plan2, err := commands.Plan(r, commands.PlanOptions{})
	if err != nil {
		t.Fatalf("Plan2: %v", err)
	}
	for _, c := range plan2 {
		if c.Kind != commands.ChangeUnchanged {
			t.Errorf("%s: kind=%q after Apply, want unchanged", c.Name, c.Kind)
		}
	}
}

func TestRenderUnified(t *testing.T) {
	if got := commands.RenderUnified("a", "b", "same\n", "same\n"); got != "" {
		t.Errorf("identical inputs: got %q, want empty", got)
	}
	diff := commands.RenderUnified("old", "new", "line1\nline2\n", "line1\nchanged\n")
	if !strings.Contains(diff, "-line2") || !strings.Contains(diff, "+changed") {
		t.Errorf("unified diff missing expected markers:\n%s", diff)
	}
	if !strings.Contains(diff, "old") || !strings.Contains(diff, "new") {
		t.Errorf("unified diff missing labels:\n%s", diff)
	}
}

func writeVault(t *testing.T, vault, rel, content string) {
	t.Helper()
	p := filepath.Join(vault, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
