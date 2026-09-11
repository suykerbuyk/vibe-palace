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
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// `vp skills upgrade` is report-only: it lists every vault Templates/skills
// override of a built-in as [keep] and never writes, removes or prompts. These
// tests replace the ones that pinned its old accept/overwrite behaviour —
// which reset an override to the embedded bytes, keeping one .bak that the
// next reset overwrote.

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
		writeVaultFile(t, vault, filepath.Join("Templates", "skills", filepath.FromSlash(n)), body)
	}
}

// seedStaleSkillGroup writes every file of the given embedded skill into the
// vault with divergent content, making the whole group a genuine override.
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

// bodyOnlySkillOverride is the embedded SKILL.md with a line appended after
// its body: the frontmatter — and so the skill shim's description — is
// unchanged, so the override moves no shim.
func bodyOnlySkillOverride(t *testing.T, skill string) string {
	t.Helper()
	emb, err := vpctx.NewResolver(t.TempDir()).EmbeddedContent("skill:" + skill + "/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	return emb + "\nAn operator's addition to the body.\n"
}

// assertNoBakUnder fails if any *.bak file exists under root.
func assertNoBakUnder(t *testing.T, root string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".bak") {
			t.Errorf("a backup was written: %s", p)
		}
		return nil
	})
}

// TestRunSkillsUpgrade_OverwriteKeepsOverrides is the skills must-fail: at
// 1f3bb62 `--overwrite` replaced the override with the embedded bytes and kept
// one overwritable SKILL.md.bak.
func TestRunSkillsUpgrade_OverwriteKeepsOverrides(t *testing.T) {
	vault := t.TempDir()
	override := bodyOnlySkillOverride(t, "startup-analyst")
	skillMD := filepath.Join(vault, "Templates", "skills", "startup-analyst", "SKILL.md")
	writeVaultFile(t, vault, "Templates/skills/startup-analyst/SKILL.md", override)
	writeVaultFile(t, vault, "Templates/skills/chair/SKILL.md", bodyOnlySkillOverride(t, "chair"))

	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		Overwrite:         true,
		Only:              "startup-analyst",
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride: vault,
	})
	if code != cli.ExitOK {
		t.Fatalf("overwrite: exit=%d\nstderr=%s", code, errb.String())
	}
	assertFileBytes(t, skillMD, override)
	assertNoBakUnder(t, vault)
	for _, want := range []string{
		"[keep] Templates/skills/startup-analyst/ — override of a built-in",
		"vp skills reset startup-analyst",
		"--overwrite: nothing to accept — vp skills upgrade never resets an override; use vp skills reset NAME",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, out.String())
		}
	}
	// --only scopes the report: chair's override is not listed.
	if strings.Contains(out.String(), "Templates/skills/chair") {
		t.Errorf("--only startup-analyst listed chair:\n%s", out.String())
	}
	entries, err := os.ReadDir(filepath.Dir(skillMD))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "SKILL.md" {
		t.Errorf("Templates/skills/startup-analyst/ = %v, want only SKILL.md", entries)
	}
}

// TestRunSkillsUpgrade_DryRunReportsOverrideAndExitsZero: an override is not
// pending work — `vp skills upgrade` has no work at all — so the dry run
// reports it and exits 0.
func TestRunSkillsUpgrade_DryRunReportsOverrideAndExitsZero(t *testing.T) {
	vault := t.TempDir()
	writeVaultFile(t, vault, "Templates/skills/startup-analyst/SKILL.md", "stale user-edited skill\n")
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		DryRun:            true,
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride: vault,
	})
	if code != cli.ExitOK {
		t.Fatalf("dry-run over an override: exit=%d, want 0", code)
	}
	for _, want := range []string{
		"skill startup-analyst:",
		"override  SKILL.md",
		"kept — vp skills reset startup-analyst removes it",
		"Summary (dry run): 1 override(s) kept,",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), " new,") || strings.Contains(out.String(), "updated") {
		t.Errorf("the dry run still reports new/updated counts:\n%s", out.String())
	}
}

func TestRunSkillsUpgrade_DryRun_ZeroWhenClean(t *testing.T) {
	vault := t.TempDir()
	seedMatchingSkillsVault(t, vault)
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		DryRun:            true,
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride: vault,
	})
	if code != cli.ExitOK {
		t.Fatalf("clean dry-run: exit=%d\nstderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "unchanged SKILL.md") {
		t.Errorf("no unchanged row:\n%s", out.String())
	}
}

// TestRunSkillsUpgrade_ReportsOneLinePerSkill replaces the group-accept test:
// the default listing is one [keep] line per skill naming every file that
// differs, and nothing is written.
func TestRunSkillsUpgrade_ReportsOneLinePerSkill(t *testing.T) {
	vault := t.TempDir()
	seedStaleSkillGroup(t, vault, "startup-analyst")
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		Only:              "startup-analyst",
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride: vault,
	})
	if code != cli.ExitOK {
		t.Fatalf("exit=%d\nstderr=%s", code, errb.String())
	}
	if c := strings.Count(out.String(), "[keep] "); c != 1 {
		t.Errorf("%d [keep] lines, want 1 per skill:\n%s", c, out.String())
	}
	if !strings.Contains(out.String(), "(6 file(s) differ: SKILL.md, references/capex-opex.md") {
		t.Errorf("the line does not name the differing files:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "6 override file(s) of built-in skills kept; nothing was written") {
		t.Errorf("no closing count:\n%s", out.String())
	}
	got, _ := os.ReadFile(filepath.Join(vault, "Templates", "skills", "startup-analyst", "SKILL.md"))
	if string(got) != "stale user-edited content\n" {
		t.Errorf("SKILL.md was written: %q", got)
	}
}

// TestRunSkillsUpgrade_GranularListsPerFile replaces the per-file prompt test.
func TestRunSkillsUpgrade_GranularListsPerFile(t *testing.T) {
	vault := t.TempDir()
	seedStaleSkillGroup(t, vault, "startup-analyst")
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		Only:              "startup-analyst",
		Granular:          true,
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride: vault,
	})
	if code != cli.ExitOK {
		t.Fatalf("granular: exit=%d\nstderr=%s", code, errb.String())
	}
	if c := strings.Count(out.String(), "[keep] Templates/skills/startup-analyst/"); c != 6 {
		t.Errorf("%d per-file [keep] lines, want 6:\n%s", c, out.String())
	}
	if !strings.Contains(out.String(), "vp skills reset startup-analyst/references/capex-opex.md") {
		t.Errorf("a per-file line does not name its own reset:\n%s", out.String())
	}

	out.Reset()
	if code := runSkillsUpgrade(skillsUpgradeOpts{
		DryRun: true, Granular: true, Only: "startup-analyst",
		Stdout: &out, Stderr: &errb, VaultRootOverride: vault,
	}); code != cli.ExitOK {
		t.Fatalf("granular dry-run: exit=%d", code)
	}
	if !strings.Contains(out.String(), "  override  startup-analyst/SKILL.md") {
		t.Errorf("granular dry-run rows are not per file:\n%s", out.String())
	}
}

// TestRunSkillsUpgrade_NonInteractive_OverrideAloneExitsZero is the rewritten
// refusal test: with nothing vp-owned to do, an override alone is reported and
// the run exits 0 — there is no --overwrite to demand.
func TestRunSkillsUpgrade_NonInteractive_OverrideAloneExitsZero(t *testing.T) {
	vault := t.TempDir()
	seedStaleSkillGroup(t, vault, "startup-analyst")
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride: vault,
	})
	if code != cli.ExitOK {
		t.Fatalf("exit=%d, want 0\nstderr=%s", code, errb.String())
	}
	if strings.Contains(errb.String(), "--overwrite") {
		t.Errorf("still asks for --overwrite:\n%s", errb.String())
	}
	if !strings.Contains(out.String(), "[keep] Templates/skills/startup-analyst/") {
		t.Errorf("no [keep] line:\n%s", out.String())
	}
}

func TestRunSkillsUpgrade_CleanNoOp(t *testing.T) {
	vault := t.TempDir()
	seedMatchingSkillsVault(t, vault)
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride: vault,
	})
	if code != cli.ExitOK {
		t.Fatalf("no-op: exit=%d\nstderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "Nothing to do") {
		t.Errorf("expected Nothing to do:\n%s", out.String())
	}
}

func TestRunSkillsUpgrade_UnknownOnlyIsAUsageError(t *testing.T) {
	var out, errb bytes.Buffer
	code := runSkillsUpgrade(skillsUpgradeOpts{
		Only:              "no-such-skill",
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride: t.TempDir(),
	})
	if code != cli.ExitUser {
		t.Errorf("exit=%d, want ExitUser", code)
	}
}
