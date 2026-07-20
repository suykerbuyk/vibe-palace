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

func TestRunSkillsUpgrade_DryRun_NonZeroOnPendingWork(t *testing.T) {
	vault := t.TempDir()
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

func TestRunSkillsUpgrade_Overwrite_AppliesAll(t *testing.T) {
	vault := t.TempDir()
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
	// Replan: everything unchanged.
	r := vpctx.NewResolver(vault)
	plan, err := commands.Plan(r, commands.PlanOptions{ResourceTypes: []string{"skill"}})
	if err != nil {
		t.Fatalf("re-plan: %v", err)
	}
	for _, c := range plan {
		if c.Kind != commands.ChangeUnchanged {
			t.Errorf("%s kind=%q after overwrite, want unchanged", c.Name, c.Kind)
		}
	}
}

func TestRunSkillsUpgrade_Interactive_AcceptGroupOnce(t *testing.T) {
	vault := t.TempDir()
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
	// All 6 files now exist.
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
	// Skip every file ("s" × 6).
	input := strings.Repeat("s\n", 6)
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
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
	// Nothing was accepted: vault Templates/skills should remain empty.
	if _, err := os.Stat(filepath.Join(vault, "Templates", "skills", "startup-analyst", "SKILL.md")); !os.IsNotExist(err) {
		t.Errorf("expected no files written when all skipped (err=%v)", err)
	}
}

func TestRunSkillsUpgrade_OnlyFiltersSkill(t *testing.T) {
	vault := t.TempDir()
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
	// SKILL.md exists.
	if _, err := os.Stat(filepath.Join(vault, "Templates/skills/startup-analyst/SKILL.md")); err != nil {
		t.Errorf("--only startup-analyst did not write SKILL.md: %v", err)
	}
}

func TestRunSkillsUpgrade_NonInteractive_RefusesWithoutOverwrite(t *testing.T) {
	vault := t.TempDir()
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
