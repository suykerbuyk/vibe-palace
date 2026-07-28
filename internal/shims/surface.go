// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"fmt"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
)

// CommandSurface is an absolute directory that holds vpc-*.md command shims.
// Layout is always flat files under CommandsDir (no project-relative join).
// Used for project .claude/commands, project .grok/plugins/.../commands, and
// user-global plugin commands/ trees alike (Phase 1: root ≠ render).
type CommandSurface struct {
	// CommandsDir is the absolute directory of vpc-<name>.md files.
	CommandsDir string
}

// SkillSurface is an absolute skills (or rules) parent directory plus the
// render kind that produces file bodies. Layout paths are under SkillsDir;
// Render selects ClaudeSkill / GrokSkill / CursorRule body templates.
//
// Example — Claude user plugin:
//
//	SkillSurface{SkillsDir: ".../vibe-palace/skills", Render: ClaudeSkill}
//	→ .../skills/vps-<name>/SKILL.md with Claude skill body
//
// Example — project Claude tree (legacy):
//
//	SkillSurface{SkillsDir: project+"/.claude/skills", Render: ClaudeSkill}
type SkillSurface struct {
	// SkillsDir is the absolute parent of skill subdirs (or of .mdc files for
	// CursorRule). For Claude/Grok plugins this is "<plugin>/skills".
	SkillsDir string
	// Render selects the body template and hash inputs (ClaudeSkill, GrokSkill,
	// or CursorRule). Path layout for persona skills is always
	// SkillsDir/vps-<name>/SKILL.md except the Grok hub (SkillsDir/vpc/SKILL.md).
	Render TargetKind
}

// PlanCommandsAt plans command shims into an absolute commands directory.
// Prefer this over Plan/PlanGrokCommands when the root is not a project tree
// (user-global plugin package, tests, etc.).
func PlanCommandsAt(summaries []commands.Summary, surface CommandSurface) ([]Change, error) {
	if surface.CommandsDir == "" {
		return nil, nil
	}
	return planCommandShims(summaries, surface.CommandsDir)
}

// PlanSkillsAt plans skill shims under surface.SkillsDir using surface.Render
// for bodies. Separates layout (SkillsDir) from render (TargetKind) so a
// Claude-rendered skill can live under a plugin's skills/ tree rather than
// forcing .claude/skills under the plugin root (review M1).
func PlanSkillsAt(items []SkillItem, surface SkillSurface) ([]SkillChange, error) {
	if surface.SkillsDir == "" {
		return nil, fmt.Errorf("PlanSkillsAt: empty SkillsDir")
	}
	if surface.Render != ClaudeSkill && surface.Render != CursorRule && surface.Render != GrokSkill {
		return nil, fmt.Errorf("PlanSkillsAt: unsupported render %s", surface.Render)
	}
	render := surface.Render
	dir := surface.SkillsDir
	return planSkillsAt(render, items, dir, func(name string) string {
		return skillFilePath(dir, render, name)
	})
}

// PlanGrokHubAt plans the /vpc hub SKILL.md under skillsDir/vpc/.
func PlanGrokHubAt(skillsDir string) (SkillChange, error) {
	if skillsDir == "" {
		return SkillChange{}, fmt.Errorf("PlanGrokHubAt: empty skillsDir")
	}
	item := GrokHubItem()
	path := skillFilePath(skillsDir, GrokSkill, item.Name)
	return planOneSkillFile(GrokSkill, item, path)
}

// skillFilePath joins SkillsDir with the layout for render+name.
func skillFilePath(skillsDir string, render TargetKind, name string) string {
	switch render {
	case CursorRule:
		return filepath.Join(skillsDir, CursorRuleFilename(name))
	case ClaudeSkill, GrokSkill:
		if render == GrokSkill && name == GrokHubName {
			return filepath.Join(skillsDir, GrokHubName, "SKILL.md")
		}
		return filepath.Join(skillsDir, SkillDirName(name), "SKILL.md")
	default:
		return ""
	}
}
