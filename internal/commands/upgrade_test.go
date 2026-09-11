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

func TestPlan_OverrideUnchangedAndUnneeded(t *testing.T) {
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
	if got := byName["wrap"].Kind; got != commands.ChangeOverride {
		t.Errorf("wrap kind = %q, want override", got)
	}
	if got := string(commands.ChangeOverride); got != "override" {
		t.Errorf("ChangeOverride = %q, want \"override\"", got)
	}

	// An embedded command with NO vault copy is Unneeded: the embedded floor
	// already serves it, so writing a byte-identical mirror would create the
	// Tier 4 shadow ADR-008 Phase 3 pruned.
	foundUnneeded := false
	for _, c := range plan {
		if c.Kind != commands.ChangeUnneeded {
			continue
		}
		foundUnneeded = true
		if c.VaultHash != "" {
			t.Errorf("unneeded change %q has VaultHash set", c.Name)
		}
		if c.VaultContent != "" {
			t.Errorf("unneeded change %q has VaultContent set", c.Name)
		}
		if c.EmbeddedContent == "" {
			t.Errorf("unneeded change %q has no EmbeddedContent", c.Name)
		}
	}
	if !foundUnneeded {
		t.Error("expected at least one ChangeUnneeded entry")
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

// TestPlan_EmptyVaultIsAllUnneeded is the acceptance line of the
// override-only plan: against a vault with no Templates/commands/, every
// embedded command is Unneeded — nothing to create, nothing to reset.
func TestPlan_EmptyVaultIsAllUnneeded(t *testing.T) {
	vault := t.TempDir()
	r := vpctx.NewResolver(vault)

	plan, err := commands.Plan(r, commands.PlanOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan) == 0 {
		t.Fatal("empty plan: the embedded corpus should never be empty")
	}
	for _, c := range plan {
		if c.Kind != commands.ChangeUnneeded {
			t.Errorf("%s: kind=%q on an empty vault, want unneeded", c.Name, c.Kind)
		}
		if c.VaultRoot != vault {
			t.Errorf("%s: VaultRoot=%q, want %q", c.Name, c.VaultRoot, vault)
		}
	}
	if _, err := os.Stat(filepath.Join(vault, "Templates", "commands")); !os.IsNotExist(err) {
		t.Errorf("planning created Templates/commands/ (stat err=%v)", err)
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

// TestPlan_SkillResourceTypes locks in Phase-5: when ResourceTypes
// includes "skill" the plan emits one Change per file under every
// embedded skill directory, with Change.Name carrying the nested
// "<skill>/<relpath>" identifier.
func TestPlan_SkillResourceTypes(t *testing.T) {
	r := vpctx.NewResolver(t.TempDir())
	plan, err := commands.Plan(r, commands.PlanOptions{ResourceTypes: []string{"skill"}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan) == 0 {
		t.Fatal("expected at least one skill change")
	}
	var found bool
	for _, c := range plan {
		if c.ResourceType != "skill" {
			t.Errorf("ResourceType = %q, want skill", c.ResourceType)
		}
		if c.Name == "startup-analyst/SKILL.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("plan missing startup-analyst/SKILL.md")
	}
}

// TestPlan_SkillOnlyMatchesSkillName proves --only <skill> picks up
// every file under that skill, not just an exact-name match.
func TestPlan_SkillOnlyMatchesSkillName(t *testing.T) {
	r := vpctx.NewResolver(t.TempDir())
	plan, err := commands.Plan(r, commands.PlanOptions{
		ResourceTypes: []string{"skill"},
		Only:          "startup-analyst",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan) < 2 {
		t.Fatalf("expected >1 files for skill-only filter, got %d", len(plan))
	}
	for _, c := range plan {
		if c.Name != "startup-analyst" &&
			!strings.HasPrefix(c.Name, "startup-analyst/") {
			t.Errorf("unexpected name for --only: %q", c.Name)
		}
	}
}
