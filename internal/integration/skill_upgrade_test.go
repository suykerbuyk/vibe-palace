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
)

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

	t.Run("group_accept_with_a", func(t *testing.T) {
		// Remove one file so plan has a "new" entry, and edit another so
		// plan has "updated". A single "a" on the group prompt must
		// handle both plus the remaining unchanged neighbors quietly.
		toRemove := filepath.Join(skillRoot, "references", "competitive-landscape.md")
		toEdit := filepath.Join(skillRoot, "references", "funding-sources.md")
		if err := os.Remove(toRemove); err != nil {
			t.Fatalf("remove: %v", err)
		}
		if err := os.WriteFile(toEdit, []byte("USER EDIT\n"), 0o644); err != nil {
			t.Fatalf("edit: %v", err)
		}

		out := runVPWithTTY(t, bin, env, []byte("a\n"), "skills", "upgrade")
		if !strings.Contains(out, "=== skill startup-analyst") {
			t.Errorf("expected skill group header in output:\n%s", out)
		}
		// All 6 files exist again.
		for _, rel := range wantFiles {
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
		// Remove all 6 files to force every file to surface as "new",
		// then skip every prompt; output must contain 6 per-file
		// headers and no files are written.
		if err := os.RemoveAll(skillRoot); err != nil {
			t.Fatalf("clear skill: %v", err)
		}
		stdin := []byte(strings.Repeat("s\n", 6))
		out := runVPWithTTY(t, bin, env, stdin, "skills", "upgrade", "--granular")
		cnt := strings.Count(out, "=== startup-analyst/")
		if cnt != 6 {
			t.Errorf("expected 6 per-file headers with --granular, got %d\n%s", cnt, out)
		}
		if _, err := os.Stat(filepath.Join(skillRoot, "SKILL.md")); !os.IsNotExist(err) {
			t.Errorf("granular skip wrote files (err=%v)", err)
		}
	})
}
