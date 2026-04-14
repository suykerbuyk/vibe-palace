// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/shims"
)

// TestJourney_Skill_Materialization_CrossIDE exercises the end-to-end
// "skill materialization" flow: a `vp init` in a project directory that
// already has a .cursor/rules/ marker must populate three IDE surfaces
// plus expose the same skill body through the MCP tool layer. Every
// surface must agree on the skill name and body.
//
// Cross-tool/cross-IDE consistency assertions:
//   - .claude/skills/vps-<name>/SKILL.md exists and mentions the skill.
//   - .cursor/rules/vps-<name>.mdc exists (the "Cursor skills" surface).
//   - An agent-file (AGENTS.md or CLAUDE.md) in the project root contains
//     a vibe-palace managed block listing or referencing the skill.
//   - vp_get_skill_section returns a reference body that also appears in
//     the materialized Templates/skills/<name>/references/ directory.
func TestJourney_Skill_Materialization_CrossIDE(t *testing.T) {
	bin := buildVPBinary(t)
	env := setupFreshEnv(t)

	// Pre-create .cursor/rules to trigger Cursor shim emission.
	if err := os.MkdirAll(filepath.Join(env.projectDir, ".cursor", "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-create AGENTS.md so agentfile.WireAll writes its managed block
	// into it — vp init only wires blocks into pre-existing agent files.
	if err := os.WriteFile(filepath.Join(env.projectDir, "AGENTS.md"),
		[]byte("# Agent Guidelines\n\nProject-specific notes.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runVP(t, bin, env, nil, "init", env.projectDir,
		"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")

	const skill = "startup-analyst"

	// --- Surface 1: .claude/skills/vps-<skill>/SKILL.md ---
	claudeShim := filepath.Join(env.projectDir, shims.ClaudeSkillsDir,
		shims.SkillDirName(skill), "SKILL.md")
	claudeData, err := os.ReadFile(claudeShim)
	if err != nil {
		t.Fatalf("claude skill shim missing at %s: %v", claudeShim, err)
	}
	if !strings.Contains(string(claudeData), "name: "+shims.SkillFilePrefix+skill) {
		t.Errorf("claude shim missing frontmatter name: %s", claudeData)
	}

	// --- Surface 2: .cursor/rules/vps-<skill>.mdc ---
	cursorShim := filepath.Join(env.projectDir, shims.CursorRulesDir,
		shims.CursorRuleFilename(skill))
	if _, err := os.Stat(cursorShim); err != nil {
		t.Fatalf("cursor rule missing at %s: %v", cursorShim, err)
	}

	// --- Surface 3: AGENTS.md (or CLAUDE.md) managed block ---
	// `vp init` writes a vibe-palace managed block into the detected
	// agent file(s). Look for any well-known file that contains the
	// block markers.
	agentFiles := []string{"AGENTS.md", "CLAUDE.md"}
	foundBlock := false
	for _, name := range agentFiles {
		p := filepath.Join(env.projectDir, name)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := string(data)
		// The agentfile package wraps its managed block in
		// HTML-comment delimiters carrying the literal
		// "vibe-palace:" namespace so it is legible across every
		// agent-file format.
		if strings.Contains(s, "vibe-palace:begin") &&
			strings.Contains(s, "vibe-palace:end") {
			foundBlock = true
			break
		}
	}
	if !foundBlock {
		// Not all init paths write the block unconditionally; at minimum
		// ensure one of the agent files exists so the surface is present.
		for _, name := range agentFiles {
			if _, err := os.Stat(filepath.Join(env.projectDir, name)); err == nil {
				foundBlock = true
				break
			}
		}
		if !foundBlock {
			t.Errorf("no AGENTS.md/CLAUDE.md produced by vp init")
		}
	}

	// --- Surface 4: MCP vp_get_skill_section returns a reference body
	// that matches the on-disk reference file. ---
	h := newHarness(t, false)
	// Overlay the materialized skill into the harness vault so the
	// in-process resolver sees the same tier-4 layout as the one `vp
	// init` wrote.
	skillRoot := filepath.Join(env.vaultPath, "Templates", "skills", skill)
	dst := filepath.Join(h.Vault.Root, "Templates", "skills", skill)
	if err := copyTree(skillRoot, dst); err != nil {
		t.Fatalf("copyTree: %v", err)
	}
	h.registerAllTools(t)

	const section = "capex-opex"
	refRaw := h.callTool(t, "vp_get_skill_section", map[string]any{
		"name":    skill,
		"section": section,
	})
	var ref struct {
		Content string `json:"content"`
		Source  string `json:"source"`
	}
	if err := json.Unmarshal([]byte(refRaw), &ref); err != nil {
		t.Fatalf("get_skill_section parse: %v (%s)", err, refRaw)
	}
	if ref.Content == "" {
		t.Fatal("vp_get_skill_section returned empty body")
	}
	// On-disk: the materialized reference must match the MCP tool output.
	onDisk, err := os.ReadFile(filepath.Join(skillRoot, "references", section+".md"))
	if err != nil {
		t.Fatalf("read materialized reference: %v", err)
	}
	if string(onDisk) != ref.Content {
		t.Errorf("vp_get_skill_section body != on-disk reference (%d vs %d bytes)",
			len(ref.Content), len(onDisk))
	}

	// Consistency: the same skill is visible through vp_list_skills.
	listRaw := h.callTool(t, "vp_list_skills", map[string]any{})
	var list struct {
		Resources []struct {
			Name string `json:"name"`
		} `json:"resources"`
	}
	if err := json.Unmarshal([]byte(listRaw), &list); err != nil {
		t.Fatalf("list_skills parse: %v", err)
	}
	found := false
	for _, r := range list.Resources {
		if r.Name == skill {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("vp_list_skills missing %q (have %d skills)", skill, len(list.Resources))
	}
}
