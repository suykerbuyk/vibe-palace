// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// seedAllEmbeddedSkills writes every embedded skills/ file into the vault
// Templates/ tree byte-identically, reconstructing the on-disk skill corpus
// that `vp init` used to materialize before the override-only model. This is
// the precondition `vp skills upgrade` reconciles against.
func seedAllEmbeddedSkills(t *testing.T, vaultPath string) {
	t.Helper()
	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded: %v", err)
	}
	for _, res := range resources {
		if !strings.HasPrefix(res.RelPath, "skills/") {
			continue
		}
		p := filepath.Join(vaultPath, "Templates", filepath.FromSlash(res.RelPath))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir skill fixture: %v", err)
		}
		if err := os.WriteFile(p, res.Bytes, 0o644); err != nil {
			t.Fatalf("seed skill fixture %s: %v", res.RelPath, err)
		}
	}
}

// runVPWithTTY is runVP + VP_ASSUME_TTY=1 so the binary's interactive
// prompt gate opens even though the subprocess stdin is a pipe. The
// env-var escape hatch is documented in cmd/vp/cmd_commands.go.
func runVPWithTTY(t *testing.T, bin string, env *testEnv, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(),
		"HOME="+env.home,
		"XDG_CONFIG_HOME="+env.xdgConfig,
		"VP_ASSUME_TTY=1",
	)
	cmd.Dir = env.projectDir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vp %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// TestIntegrationSkillUpgrade exercises the two-SHA `vp skills upgrade`
// path end to end: bump the lock SHA to force an upgrade, run `vp skills
// upgrade` with stdin "a\n", and verify every file in the group lands
// and the lock refreshes on the next `vp config sync`.
//
// Also proves:
//   - a dirty vault edit on a reference triggers the .bak sidecar.
//   - `--granular` fans out to 6 per-file prompts.
//   - the three-SHA materialize path (via `vp init` + `vp config sync`)
//     continues to cover skills, reusing existing lock bookkeeping.
func TestIntegrationSkillUpgrade(t *testing.T) {
	bin := buildVPBinary(t)
	env := setupFreshEnv(t)
	runVP(t, bin, env, nil, "init", env.projectDir,
		"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")

	skillRoot := filepath.Join(env.vaultPath, "Templates", "skills", "startup-analyst")
	wantFiles := []string{
		"SKILL.md",
		"references/capex-opex.md",
		"references/competitive-landscape.md",
		"references/funding-sources.md",
		"references/reality-validation.md",
		"references/strategic-partnerships.md",
	}

	// Under the override-only materialization model a fresh `vp init` writes
	// no skill mirror (the embedded floor serves it). `vp skills upgrade` is
	// a separate command that reconciles the on-disk skill corpus against
	// the embedded corpus across ALL skills, so seed the vault with every
	// embedded skill byte-identically first — the precondition init used to
	// provide. (Seeding only startup-analyst would leave the other embedded
	// skills surfacing as "new", stealing the grouped prompt's stdin.)
	seedAllEmbeddedSkills(t, env.vaultPath)

	t.Run("group_accept_with_a", func(t *testing.T) {
		// Edit two files so the plan has two "updated" entries — a genuine
		// multi-member group, which is what the single "a" is here to test.
		// Remove a third: under the override-only rule that file is now
		// UNNEEDED, not "new", so the upgrade must leave it deleted.
		//
		// Removing a mirror used to make it "new" and get it re-created. That
		// is the defect this unit fixes, and it is the same shape as the
		// 2026-07-31 delete/modify conflict over four re-created code-digger
		// mirrors: `vp config sync` prunes the mirror, `vp skills upgrade`
		// puts it back, and the two commands fight over the vault forever.
		toRemove := filepath.Join(skillRoot, "references", "competitive-landscape.md")
		toEdit := filepath.Join(skillRoot, "references", "funding-sources.md")
		toEdit2 := filepath.Join(skillRoot, "references", "capex-opex.md")
		if err := os.Remove(toRemove); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if err := os.WriteFile(toEdit, []byte("USER EDIT\n"), 0o644); err != nil {
			t.Fatalf("edit: %v", err)
		}
		if err := os.WriteFile(toEdit2, []byte("USER EDIT 2\n"), 0o644); err != nil {
			t.Fatalf("edit2: %v", err)
		}

		out := runVPWithTTY(t, bin, env, []byte("a\n"), "skills", "upgrade")
		if !strings.Contains(out, "=== skill startup-analyst") {
			t.Errorf("expected skill group header in output:\n%s", out)
		}
		// The deleted mirror stays deleted: the embedded floor serves it, and
		// re-materializing it would shadow the binary. Goes red if planOne's
		// !haveVault -> ChangeNew short-circuit is restored.
		if _, err := os.Stat(toRemove); !os.IsNotExist(err) {
			t.Errorf("upgrade re-created the deleted mirror %s (stat err=%v); "+
				"vp config sync would prune it right back", toRemove, err)
		}
		// Every file the vault still holds was upgraded to embedded content.
		for _, rel := range wantFiles {
			if rel == "references/competitive-landscape.md" {
				continue
			}
			p := filepath.Join(skillRoot, filepath.FromSlash(rel))
			if _, err := os.Stat(p); err != nil {
				t.Errorf("missing after group-accept: %s (%v)", rel, err)
			}
		}
		// .bak preserved the dirty vault edit on funding-sources.md.
		bak, err := os.ReadFile(toEdit + ".bak")
		if err != nil {
			t.Fatalf("expected .bak beside %s: %v", toEdit, err)
		}
		if string(bak) != "USER EDIT\n" {
			t.Errorf(".bak did not preserve user edit:\n%s", bak)
		}
	})

	t.Run("granular_flag_fans_out", func(t *testing.T) {
		// Overwrite all 6 files so every one surfaces as "updated", then skip
		// every prompt; output must contain 6 per-file headers and no file is
		// rewritten. Deleting them (the old setup) no longer produces prompts
		// at all — an absent mirror is unneeded, which is the point of the
		// group_accept assertion above.
		for _, rel := range wantFiles {
			p := filepath.Join(skillRoot, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(p, []byte("USER EDIT\n"), 0o644); err != nil {
				t.Fatalf("seed stale %s: %v", rel, err)
			}
		}
		stdin := []byte(strings.Repeat("s\n", 6))
		out := runVPWithTTY(t, bin, env, stdin, "skills", "upgrade", "--granular")
		cnt := strings.Count(out, "=== startup-analyst/")
		if cnt != 6 {
			t.Errorf("expected 6 per-file headers with --granular, got %d\n%s", cnt, out)
		}
		got, err := os.ReadFile(filepath.Join(skillRoot, "SKILL.md"))
		if err != nil {
			t.Fatalf("read SKILL.md: %v", err)
		}
		if string(got) != "USER EDIT\n" {
			t.Errorf("granular skip wrote files:\n%s", got)
		}
	})
}
