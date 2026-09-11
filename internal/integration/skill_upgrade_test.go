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

// TestIntegrationSkillUpgrade exercises `vp skills upgrade` end to end
// through the binary, with the interactive gate forced open. It is report-only:
// it lists each vault override of a built-in skill as [keep], never prompts,
// never consumes stdin, and writes nothing — the backup assertion it used to
// carry now belongs to `vp skills reset` (template_reset_test.go). It never
// reads or writes templates.lock; the lock belongs to `vp config sync`.
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
	seedAllEmbeddedSkills(t, env.vaultPath)

	t.Run("group_report_keeps_every_file", func(t *testing.T) {
		// Two edited files make a multi-file override; a third is removed, and
		// under the override-only rule it stays removed (unneeded, not new).
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

		// "a" is offered on stdin, as the old group prompt would have taken
		// it. Nothing may read it: there is no prompt.
		out := runVPWithTTY(t, bin, env, []byte("a\n"), "skills", "upgrade")
		if !strings.Contains(out, "[keep] Templates/skills/startup-analyst/ — override of a built-in (2 file(s) differ") {
			t.Errorf("no per-skill [keep] line:\n%s", out)
		}
		if strings.Contains(out, "=== skill startup-analyst") || strings.Contains(out, "[a]ccept") {
			t.Errorf("a prompt was shown:\n%s", out)
		}
		if _, err := os.Stat(toRemove); !os.IsNotExist(err) {
			t.Errorf("upgrade re-created the deleted mirror %s (stat err=%v)", toRemove, err)
		}
		for p, want := range map[string]string{toEdit: "USER EDIT\n", toEdit2: "USER EDIT 2\n"} {
			if got, err := os.ReadFile(p); err != nil || string(got) != want {
				t.Errorf("%s changed: %q (err=%v)", p, got, err)
			}
			if _, err := os.Stat(p + ".bak"); !os.IsNotExist(err) {
				t.Errorf("%s.bak written (err=%v)", p, err)
			}
		}
	})

	t.Run("granular_flag_fans_out", func(t *testing.T) {
		for _, rel := range wantFiles {
			p := filepath.Join(skillRoot, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(p, []byte("USER EDIT\n"), 0o644); err != nil {
				t.Fatalf("seed stale %s: %v", rel, err)
			}
		}
		out := runVPWithTTY(t, bin, env, []byte(strings.Repeat("a\n", 6)), "skills", "upgrade", "--granular")
		if cnt := strings.Count(out, "[keep] Templates/skills/startup-analyst/"); cnt != 6 {
			t.Errorf("expected 6 per-file [keep] lines with --granular, got %d\n%s", cnt, out)
		}
		got, err := os.ReadFile(filepath.Join(skillRoot, "SKILL.md"))
		if err != nil {
			t.Fatalf("read SKILL.md: %v", err)
		}
		if string(got) != "USER EDIT\n" {
			t.Errorf("the report wrote files:\n%s", got)
		}
	})
}
