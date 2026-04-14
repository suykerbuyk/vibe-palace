// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/shims"
)

// TestIntegrationInitEmitsSkillShims proves that a fresh `vp init`
// emits ClaudeSkill shims for every embedded skill directory and,
// when `.cursor/rules/` pre-exists, also emits CursorRule shims.
// Without `.cursor/` the Cursor rules must not be written.
func TestIntegrationInitEmitsSkillShims(t *testing.T) {
	t.Run("claude_only", func(t *testing.T) {
		bin := buildVPBinary(t)
		env := setupFreshEnv(t)
		runVP(t, bin, env, nil, "init", env.projectDir,
			"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")

		// ClaudeSkill: .claude/skills/vps-startup-analyst/SKILL.md
		skillShim := filepath.Join(env.projectDir, shims.ClaudeSkillsDir,
			shims.SkillDirName("startup-analyst"), "SKILL.md")
		data, err := os.ReadFile(skillShim)
		if err != nil {
			t.Fatalf("vp init did not emit %s: %v", skillShim, err)
		}
		if !strings.Contains(string(data), "name: vps-startup-analyst") {
			t.Errorf("claude skill shim missing frontmatter name:\n%s", data)
		}
		if !strings.Contains(string(data), "vibe-palace:shim v=") {
			t.Errorf("claude skill shim missing managed marker:\n%s", data)
		}
		// No cursor rules without .cursor/.
		if _, err := os.Stat(filepath.Join(env.projectDir, shims.CursorRulesDir)); !os.IsNotExist(err) {
			t.Errorf("cursor rules dir written without .cursor/ signal (err=%v)", err)
		}
	})

	t.Run("claude_and_cursor", func(t *testing.T) {
		bin := buildVPBinary(t)
		env := setupFreshEnv(t)
		// Pre-create .cursor/rules so shims.CursorPresent(projectRoot) fires.
		if err := os.MkdirAll(filepath.Join(env.projectDir, ".cursor", "rules"), 0o755); err != nil {
			t.Fatal(err)
		}
		runVP(t, bin, env, nil, "init", env.projectDir,
			"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")

		rulePath := filepath.Join(env.projectDir, shims.CursorRulesDir,
			shims.CursorRuleFilename("startup-analyst"))
		data, err := os.ReadFile(rulePath)
		if err != nil {
			t.Fatalf("vp init did not emit cursor rule %s: %v", rulePath, err)
		}
		if !strings.Contains(string(data), "alwaysApply: false") {
			t.Errorf("cursor rule missing alwaysApply: false:\n%s", data)
		}
	})
}
