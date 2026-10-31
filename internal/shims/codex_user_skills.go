// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/skills"
)

// InstallCodexUserSkills reconciles the Vibe-Palace-owned subset of a user
// Agent Skills root. It never removes stale entries in this shared directory;
// unmarked siblings are always treated as custom, regardless of their names.
func InstallCodexUserSkills(resolver *vpctx.Resolver, skillsDir string) (Report, error) {
	if resolver == nil {
		return Report{}, fmt.Errorf("Codex skill resolver is required")
	}
	summaries, err := commands.List(resolver, "command", "", "", "", 60)
	if err != nil {
		return Report{}, fmt.Errorf("list Codex commands: %w", err)
	}
	items := make([]SkillItem, 0, len(summaries))
	for _, summary := range summaries {
		items = append(items, SkillItem{
			Name:        summary.Alias,
			Frontmatter: skillsFrontmatter(summary.Brief),
		})
	}
	var skillReport GlobalInstallReport
	personaItems := listSkillItems(resolver, &skillReport)
	if len(skillReport.Errors) > 0 {
		return Report{}, fmt.Errorf("list Codex personas: %s", skillReport.Errors[0])
	}
	for _, item := range personaItems {
		item.Name = "vps-" + item.Name
		items = append(items, item)
	}
	plan, err := PlanSkillsAt(items, SkillSurface{SkillsDir: skillsDir, Render: CodexSkill})
	if err != nil {
		return Report{}, err
	}
	// User-global Agent Skills are shared with other tools. Apply only the
	// desired and custom/current rows; stale VP rows remain until uninstall.
	var current []SkillChange
	for _, change := range plan {
		if change.Kind != Stale {
			current = append(current, change)
		}
	}
	report, _, err := ApplySkills(current, ApplyOptions{})
	return report, err
}

// RemoveCodexUserSkills removes only skill directories with a current
// Vibe-Palace marker. It is the explicit host-uninstall path, not stale
// reconciliation, and leaves every unmarked sibling untouched.
func RemoveCodexUserSkills(skillsDir string) error {
	changes, err := PlanSkillsAt(nil, SkillSurface{SkillsDir: skillsDir, Render: CodexSkill})
	if err != nil {
		return err
	}
	for _, change := range changes {
		if change.Kind != Stale {
			continue
		}
		removed, err := Remove(change.Path)
		if err != nil {
			return err
		}
		if removed {
			if err := os.RemoveAll(filepath.Dir(change.Path)); err != nil {
				return fmt.Errorf("remove Codex skill directory %s: %w", filepath.Dir(change.Path), err)
			}
		}
	}
	return nil
}

func skillsFrontmatter(description string) skills.SkillFrontmatter {
	return skills.SkillFrontmatter{Description: description}
}
