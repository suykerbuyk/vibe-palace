// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// A stale copy — an earlier shipped version of a built-in — is vp's bytes,
// which `vp config sync` prunes. The upgrade commands report it as such: a
// [stale] row, never an override kept, and never a bare "Nothing to do" while
// it still shadows the current built-in.

// TestRunCommandsUpgrade_OnlyStaleCopiesQualifyNothingToDo: with the shims
// current, the only vault copy a stale one, both the --overwrite report and the
// non-TTY no-op name it.
func TestRunCommandsUpgrade_OnlyStaleCopiesQualifyNothingToDo(t *testing.T) {
	grokOff(t)
	vault := t.TempDir()
	projectRoot := t.TempDir()
	writeVaultFile(t, vault, "Templates/commands/restart.md", earlierRestart(t))
	const stale = "1 stale cop(y/ies) of built-ins pending a prune by vp config sync"

	var out, errb bytes.Buffer
	if code := runCommandsUpgrade(commandsUpgradeOpts{
		Overwrite: true, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
		VaultRootOverride: vault, ProjectRootOverride: projectRoot,
	}); code != cli.ExitOK {
		t.Fatalf("--overwrite: exit %d\n%s", code, errb.String())
	}
	if !strings.Contains(out.String(), stale+".") || strings.Contains(out.String(), "override(s) of built-ins kept") {
		t.Errorf("--overwrite report:\n%s", out.String())
	}

	no := false
	out.Reset()
	if code := runCommandsUpgrade(commandsUpgradeOpts{
		Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, InteractiveOverride: &no,
		VaultRootOverride: vault, ProjectRootOverride: projectRoot,
	}); code != cli.ExitOK {
		t.Fatalf("non-TTY: exit %d\n%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "Agent blocks and shims match; "+stale+". Nothing to do.") {
		t.Errorf("the no-op does not name the stale copy:\n%s", out.String())
	}
}

func TestKeptAndStale(t *testing.T) {
	for _, tc := range []struct {
		o, s int
		want string
	}{
		{2, 0, "2 override(s) of built-ins kept"},
		{0, 1, "1 stale cop(y/ies) of built-ins pending a prune by vp config sync"},
		{1, 3, "1 override(s) of built-ins kept; 3 stale cop(y/ies) of built-ins pending a prune by vp config sync"},
	} {
		if got := keptAndStale(tc.o, tc.s); got != tc.want {
			t.Errorf("keptAndStale(%d, %d) = %q", tc.o, tc.s, got)
		}
	}
}

// staleSkillVault seeds a vault copy of the first file of the first embedded
// skill, and makes it a shipped version through the ShippedVersion seam (no
// historical skill bytes are committed as a fixture). Returns the vault, the
// skill's name and the file's nested name.
func staleSkillVault(t *testing.T) (vault, skill, name string) {
	t.Helper()
	vault = t.TempDir()
	names, err := vpctx.NewResolver(vault).ListEmbedded("skill")
	if err != nil || len(names) == 0 {
		t.Fatalf("ListEmbedded: %v %v", names, err)
	}
	name = names[0]
	skill, _, _ = strings.Cut(name, "/")
	const body = "an earlier shipped version of this skill file\n"
	writeVaultFile(t, vault, filepath.Join("Templates", "skills", filepath.FromSlash(name)), body)
	orig := templates.ShippedVersion
	t.Cleanup(func() { templates.ShippedVersion = orig })
	key := templates.ProvenanceKey([]byte(body))
	templates.ShippedVersion = func(rel, k string) bool {
		return (rel == "skills/"+name && k == key) || orig(rel, k)
	}
	return vault, skill, name
}

func TestRunSkillsUpgrade_StaleCopyIsReportedNotKept(t *testing.T) {
	vault, skill, name := staleSkillVault(t)
	stale := staleLine("Templates/skills/" + name)

	for _, granular := range []bool{false, true} {
		var out, errb bytes.Buffer
		if code := runSkillsUpgrade(skillsUpgradeOpts{DryRun: true, Granular: granular, Stdout: &out, Stderr: &errb, VaultRootOverride: vault}); code != cli.ExitOK {
			t.Fatalf("dry run: exit %d\n%s", code, errb.String())
		}
		o := out.String()
		if !strings.Contains(o, stale) || !strings.Contains(o, "Summary (dry run): 0 override(s) kept, 1 stale cop(y/ies) pending a prune by vp config sync,") {
			t.Errorf("granular=%v dry run:\n%s", granular, o)
		}
		if !granular && !strings.Contains(o, "  skill "+skill+":\n    "+stale) {
			t.Errorf("the skill group does not list its stale file:\n%s", o)
		}
	}

	var out, errb bytes.Buffer
	if code := runSkillsUpgrade(skillsUpgradeOpts{Stdout: &out, Stderr: &errb, VaultRootOverride: vault}); code != cli.ExitOK {
		t.Fatalf("exit %d\n%s", code, errb.String())
	}
	o := out.String()
	if !strings.Contains(o, stale) || strings.Contains(o, "[keep]") ||
		!strings.Contains(o, "No vault Templates/skills override of a built-in skill; 1 stale cop(y/ies) of built-in skill files pending a prune by vp config sync. Nothing was written.") {
		t.Errorf("report:\n%s", o)
	}

	// Beside an override of another skill, both are reported.
	seedStaleSkillGroup(t, vault, lastEmbeddedSkill(t, vault))
	out.Reset()
	if code := runSkillsUpgrade(skillsUpgradeOpts{Stdout: &out, Stderr: &errb, VaultRootOverride: vault}); code != cli.ExitOK {
		t.Fatalf("exit %d\n%s", code, errb.String())
	}
	if o := out.String(); !strings.Contains(o, "override file(s) of built-in skills kept; nothing was written.") ||
		!strings.Contains(o, "1 stale cop(y/ies) of built-in skill files pending a prune by vp config sync.") {
		t.Errorf("mixed report:\n%s", o)
	}
}

// lastEmbeddedSkill is the last embedded skill's directory name — a different
// skill from staleSkillVault's first one.
func lastEmbeddedSkill(t *testing.T, vault string) string {
	t.Helper()
	names, err := vpctx.NewResolver(vault).ListEmbedded("skill")
	if err != nil || len(names) == 0 {
		t.Fatal(err)
	}
	skill, _, _ := strings.Cut(names[len(names)-1], "/")
	first, _, _ := strings.Cut(names[0], "/")
	if skill == first {
		t.Skip("only one embedded skill")
	}
	return skill
}
