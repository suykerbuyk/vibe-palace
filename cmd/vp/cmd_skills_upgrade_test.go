// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// seedMatchingSkillsVault copies every embedded skill file into the
// vault so a fresh plan finds every change Unchanged.
func seedMatchingSkillsVault(t *testing.T, vault string) {
	t.Helper()
	r := vpctx.NewResolver(vault)
	names, err := r.ListEmbedded("skill")
	if err != nil {
		t.Fatalf("ListEmbedded: %v", err)
	}
	for _, n := range names {
		body, err := r.EmbeddedContent("skill:" + n)
		if err != nil {
			t.Fatalf("EmbeddedContent %s: %v", n, err)
		}
		rel := filepath.Join("Templates", "skills", filepath.FromSlash(n))
		p := filepath.Join(vault, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

// TestRunSkillsUpgrade_DryRun_NonZeroOnPendingWork seeds a GENUINE override
// (a vault skill file that differs from embedded) because that is now the
// only thing that counts as pending work. An empty vault is not pending
// work: the embedded floor already serves every skill, so there is nothing
// to do and the dry run correctly exits zero.
// seedStaleSkillGroup writes every file of the given embedded skill into the
// vault with divergent content, making the whole group a genuine override.
//
// Tests that want prompts or writes need this: since commands.Plan became
// override-only, an ABSENT vault copy is ChangeUnneeded and offers nothing.
// Only a vault copy that differs from embedded is actionable.
func seedStaleSkillGroup(t *testing.T, vault, skill string) {
	t.Helper()
	r := vpctx.NewResolver(vault)
	names, err := r.ListEmbedded("skill")
	if err != nil {
		t.Fatalf("ListEmbedded(skill): %v", err)
	}
	n := 0
	for _, name := range names {
		if name != skill && !strings.HasPrefix(name, skill+"/") {
			continue
		}
		writeVaultFile(t, vault,
			filepath.Join("Templates", "skills", filepath.FromSlash(name)),
			"stale user-edited content\n")
		n++
	}
	if n == 0 {
		t.Fatalf("no embedded files under skill %q", skill)
	}
}

func TestRunSkillsUpgrade_DryRun_NonZeroOnPendingWork(t *testing.T) {
	vault := t.TempDir()
	writeVaultFile(t, vault, "Templates/skills/startup-analyst/SKILL.md",
		"stale user-edited skill\n")
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		DryRun:            true,
		Stdin:             strings.NewReader(""),
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride: vault,
	})
	if code != cli.ExitUser {
		t.Fatalf("dry-run with new skills: exit=%d, want ExitUser", code)
	}
	if !strings.Contains(out.String(), "skill startup-analyst") {
		t.Errorf("dry-run output missing startup-analyst group:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Summary (dry run):") {
		t.Errorf("missing summary line:\n%s", out.String())
	}
}

func TestRunSkillsUpgrade_DryRun_ZeroWhenClean(t *testing.T) {
	vault := t.TempDir()
	seedMatchingSkillsVault(t, vault)
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		DryRun:            true,
		Stdin:             strings.NewReader(""),
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride: vault,
	})
	if code != cli.ExitOK {
		t.Fatalf("clean dry-run: exit=%d\nstderr=%s", code, errb.String())
	}
}

// TestRunSkillsUpgrade_Overwrite_AppliesActionable is the commands-path test
// of the same name applied to skills: cmd_skills.go shares commands.Plan, so
// --overwrite must upgrade the genuine override and leave the rest absent.
func TestRunSkillsUpgrade_Overwrite_AppliesActionable(t *testing.T) {
	vault := t.TempDir()
	writeVaultFile(t, vault, "Templates/skills/startup-analyst/SKILL.md",
		"stale user-edited skill\n")
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		Overwrite:         true,
		Stdin:             strings.NewReader(""),
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride: vault,
	})
	if code != cli.ExitOK {
		t.Fatalf("overwrite: exit=%d\nstderr=%s", code, errb.String())
	}
	// Replan: the override settled to unchanged, everything else is still
	// unneeded and no mirror was written for it.
	r := vpctx.NewResolver(vault)
	plan, err := commands.Plan(r, commands.PlanOptions{ResourceTypes: []string{"skill"}})
	if err != nil {
		t.Fatalf("re-plan: %v", err)
	}
	for _, c := range plan {
		want := commands.ChangeUnneeded
		if c.Name == "startup-analyst/SKILL.md" {
			want = commands.ChangeUnchanged
		}
		if c.Kind != want {
			t.Errorf("%s kind=%q after overwrite, want %q", c.Name, c.Kind, want)
		}
		if c.Kind != commands.ChangeUnneeded {
			continue
		}
		if _, err := os.Stat(c.VaultPath); !os.IsNotExist(err) {
			t.Errorf("%s: --overwrite materialized a byte-identical mirror at %s (stat err=%v)",
				c.Name, c.VaultPath, err)
		}
	}
}

func TestRunSkillsUpgrade_Interactive_AcceptGroupOnce(t *testing.T) {
	vault := t.TempDir()
	// The group must be a genuine override to be offered at all; an absent
	// copy is unneeded and never prompts.
	seedStaleSkillGroup(t, vault, "startup-analyst")
	// Accept the whole startup-analyst group with a single "a". Scoped to
	// startup-analyst so the group's file layout and prompt count stay fixed
	// regardless of how many other skills the binary embeds.
	input := "a\n"
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		Only:                "startup-analyst",
		Stdin:               strings.NewReader(input),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		InteractiveOverride: boolPtr(true),
	})
	if code != cli.ExitOK {
		t.Fatalf("interactive: exit=%d\nstderr=%s", code, errb.String())
	}
	// All 6 files were upgraded to embedded content.
	skillRoot := filepath.Join(vault, "Templates", "skills", "startup-analyst")
	for _, rel := range []string{
		"SKILL.md",
		"references/capex-opex.md",
		"references/competitive-landscape.md",
		"references/funding-sources.md",
		"references/reality-validation.md",
		"references/strategic-partnerships.md",
	} {
		if _, err := os.Stat(filepath.Join(skillRoot, filepath.FromSlash(rel))); err != nil {
			t.Errorf("missing after group-accept: %s (%v)", rel, err)
		}
	}
	// Exactly one prompt surfaced (one "=== skill …" header).
	if c := strings.Count(out.String(), "=== skill startup-analyst"); c != 1 {
		t.Errorf("expected 1 skill header, got %d; stdout:\n%s", c, out.String())
	}
}

func TestRunSkillsUpgrade_Granular_FansOutPerFile(t *testing.T) {
	vault := t.TempDir()
	seedStaleSkillGroup(t, vault, "startup-analyst")
	// Skip every file ("s" × 6).
	input := strings.Repeat("s\n", 6)
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		Only:                "startup-analyst",
		Granular:            true,
		Stdin:               strings.NewReader(input),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		InteractiveOverride: boolPtr(true),
	})
	if code != cli.ExitOK {
		t.Fatalf("granular: exit=%d\nstderr=%s", code, errb.String())
	}
	// 6 per-file prompts visible: one "=== startup-analyst/..." per file.
	count := strings.Count(out.String(), "=== startup-analyst/")
	if count != 6 {
		t.Errorf("expected 6 per-file prompts, got %d; stdout:\n%s", count, out.String())
	}
	// Nothing was accepted: the seeded stale bytes are untouched.
	got, err := os.ReadFile(filepath.Join(vault, "Templates", "skills", "startup-analyst", "SKILL.md"))
	if err != nil {
		t.Fatalf("read seeded SKILL.md: %v", err)
	}
	if string(got) != "stale user-edited content\n" {
		t.Errorf("expected no write when all skipped, got:\n%s", got)
	}
}

func TestRunSkillsUpgrade_OnlyFiltersSkill(t *testing.T) {
	vault := t.TempDir()
	seedStaleSkillGroup(t, vault, "startup-analyst")
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		Only:              "startup-analyst",
		Overwrite:         true,
		Stdin:             strings.NewReader(""),
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride: vault,
	})
	if code != cli.ExitOK {
		t.Fatalf("--only: exit=%d\nstderr=%s", code, errb.String())
	}
	// SKILL.md was upgraded from the stale seed to embedded content.
	r := vpctx.NewResolver(vault)
	want, err := r.EmbeddedContent("skill:startup-analyst/SKILL.md")
	if err != nil {
		t.Fatalf("EmbeddedContent: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(vault, "Templates/skills/startup-analyst/SKILL.md"))
	if err != nil {
		t.Fatalf("--only startup-analyst did not write SKILL.md: %v", err)
	}
	if string(got) != want {
		t.Error("--only startup-analyst did not upgrade SKILL.md to embedded content")
	}
}

func TestRunSkillsUpgrade_NonInteractive_RefusesWithoutOverwrite(t *testing.T) {
	vault := t.TempDir()
	// Needs actionable work to refuse about: an empty vault now plans nothing.
	seedStaleSkillGroup(t, vault, "startup-analyst")
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		InteractiveOverride: boolPtr(false),
	})
	if code != cli.ExitUser {
		t.Fatalf("non-interactive: exit=%d, want ExitUser\nstderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "--overwrite") {
		t.Errorf("expected --overwrite hint:\n%s", errb.String())
	}
}

func TestRunSkillsUpgrade_CleanNoOp(t *testing.T) {
	vault := t.TempDir()
	seedMatchingSkillsVault(t, vault)
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		InteractiveOverride: boolPtr(false),
	})
	if code != cli.ExitOK {
		t.Fatalf("no-op: exit=%d\nstderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "Nothing to do") {
		t.Errorf("expected Nothing to do:\n%s", out.String())
	}
}

func TestRunSkillsUpgrade_BackupOnDirtyReference(t *testing.T) {
	vault := t.TempDir()
	// Seed matching first so the file is "clean", then edit one reference
	// so Plan reports Updated for that single file.
	seedMatchingSkillsVault(t, vault)
	capex := filepath.Join(vault, "Templates/skills/startup-analyst/references/capex-opex.md")
	if err := os.WriteFile(capex, []byte("USER EDIT\n"), 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		Overwrite:         true,
		Stdin:             strings.NewReader(""),
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride: vault,
	})
	if code != cli.ExitOK {
		t.Fatalf("dirty-ref overwrite: exit=%d\nstderr=%s", code, errb.String())
	}
	// .bak preserves user edit.
	bak, err := os.ReadFile(capex + ".bak")
	if err != nil {
		t.Fatalf("expected .bak beside %s: %v", capex, err)
	}
	if string(bak) != "USER EDIT\n" {
		t.Errorf(".bak did not preserve user edit:\n%s", bak)
	}
}
