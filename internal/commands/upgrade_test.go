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

	// An embedded command with NO vault copy is Unneeded, never New: the
	// embedded floor already serves it, so writing a byte-identical mirror
	// would create the Tier 4 shadow ADR-008 Phase 3 pruned. This is the
	// assertion that goes red if planOne's !haveVault short-circuit to
	// ChangeNew is restored.
	foundUnneeded := false
	for _, c := range plan {
		if c.Kind == commands.ChangeNew {
			t.Errorf("plan emitted ChangeNew for %q: an absent vault copy is "+
				"not work to do (embedded floor serves it)", c.Name)
		}
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

// TestApply_EmptyVaultStaysEmpty is the acceptance line: a run against a
// vault with no Templates/commands/ must leave it with no
// Templates/commands/. Accepting the WHOLE plan is deliberate — it models
// the operator hitting accept-all to reach the shim prompts, which is how
// the 14 mirrors got written in the first place.
//
// Restoring planOne's !haveVault -> ChangeNew short-circuit makes this red.
func TestApply_EmptyVaultStaysEmpty(t *testing.T) {
	vault := t.TempDir()
	r := vpctx.NewResolver(vault)

	plan, err := commands.Plan(r, commands.PlanOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan) == 0 {
		t.Fatal("empty plan: the embedded corpus should never be empty")
	}
	if err := commands.Apply(plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for _, c := range plan {
		if c.Kind != commands.ChangeUnneeded {
			t.Errorf("%s: kind=%q on an empty vault, want unneeded", c.Name, c.Kind)
		}
		if _, err := os.Stat(c.VaultPath); !os.IsNotExist(err) {
			t.Errorf("%s: Apply materialized a byte-identical mirror at %s "+
				"(stat err=%v); the embedded floor already served it",
				c.Name, c.VaultPath, err)
		}
	}

	// The directory itself must not have been created either.
	if _, err := os.Stat(filepath.Join(vault, "Templates", "commands")); !os.IsNotExist(err) {
		t.Errorf("Templates/commands/ came into existence (stat err=%v); "+
			"vp config sync would plan to prune every file in it", err)
	}
}

// TestApply_WritesOverridesAndSkipsUnchanged proves the fix did not make
// genuine overrides unreachable: a vault copy that DIFFERS from embedded is
// still Updated, still written, and re-planning settles to Unchanged.
func TestApply_WritesOverridesAndSkipsUnchanged(t *testing.T) {
	vault := t.TempDir()
	r := vpctx.NewResolver(vault)

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
	if err := commands.Apply(plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The override was upgraded to embedded bytes...
	got, err := os.ReadFile(filepath.Join(vault, "Templates/commands/wrap.md"))
	if err != nil {
		t.Fatalf("read wrap: %v", err)
	}
	embWrap, err := r.EmbeddedContent("command:wrap")
	if err != nil {
		t.Fatalf("read embedded wrap: %v", err)
	}
	if string(got) != embWrap {
		t.Error("wrap.md: genuine override was not upgraded to embedded content")
	}

	// ...and nothing else was materialized alongside it.
	entries, err := os.ReadDir(filepath.Join(vault, "Templates", "commands"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("Templates/commands/ holds %d files %v, want exactly the 2 seeded",
			len(entries), names)
	}

	// Re-plan: the two seeded files are unchanged, the rest still unneeded.
	plan2, err := commands.Plan(r, commands.PlanOptions{})
	if err != nil {
		t.Fatalf("Plan2: %v", err)
	}
	for _, c := range plan2 {
		switch c.Name {
		case "restart", "wrap":
			if c.Kind != commands.ChangeUnchanged {
				t.Errorf("%s: kind=%q after Apply, want unchanged", c.Name, c.Kind)
			}
		default:
			if c.Kind != commands.ChangeUnneeded {
				t.Errorf("%s: kind=%q after Apply, want unneeded", c.Name, c.Kind)
			}
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

// TestApplyWithBackup_EmitsBakForUpdated covers the dirty-vault code
// path: ApplyWithBackup renames the existing vault copy to .bak before
// overwriting with embedded content.
func TestApplyWithBackup_EmitsBakForUpdated(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "f.md")
	if err := os.WriteFile(path, []byte("USER EDIT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := commands.Change{
		Kind:            commands.ChangeUpdated,
		Name:            "f",
		EmbeddedContent: "new body\n",
		VaultContent:    "USER EDIT\n",
		VaultPath:       path,
	}
	if err := commands.ApplyWithBackup([]commands.Change{c}); err != nil {
		t.Fatalf("ApplyWithBackup: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new body\n" {
		t.Errorf("primary not updated: %q", got)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("missing .bak: %v", err)
	}
	if string(bak) != "USER EDIT\n" {
		t.Errorf(".bak content: %q", bak)
	}
}

// TestApplyWithBackup_NewFileNoBak covers the New path: there is no
// prior file, so no .bak should appear.
func TestApplyWithBackup_NewFileNoBak(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "new.md")
	c := commands.Change{
		Kind:            commands.ChangeNew,
		Name:            "new",
		EmbeddedContent: "fresh\n",
		VaultPath:       path,
	}
	if err := commands.ApplyWithBackup([]commands.Change{c}); err != nil {
		t.Fatalf("ApplyWithBackup: %v", err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("unexpected .bak for new file (err=%v)", err)
	}
}
