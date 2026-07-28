// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// PluginCommandsRel is the commands/ directory inside a host plugin package.
const PluginCommandsRel = "commands"

// PluginSkillsRel is the skills/ directory inside a host plugin package.
const PluginSkillsRel = "skills"

// GrokUserPluginRel is the user-global Grok plugin path under $HOME.
const GrokUserPluginRel = ".grok/plugins/vibe-palace"

// GlobalInstallOptions controls InstallGlobalSurfaces.
type GlobalInstallOptions struct {
	// VaultRoot is the vault filesystem root for command/skill resolution.
	// Empty yields embedded-only names (D6 / O2). Prefer the global-config
	// vault, not a cwd override, for machine-wide surfaces (M1).
	VaultRoot string
	// VaultSource is a human-readable label for VaultRoot (e.g. "global:…"),
	// printed by installers so the operator sees which vault shaped the menu.
	VaultSource string
	// ClaudePluginRoot is the absolute Claude marketplace plugin directory
	// (…/claude-plugin/vibe-palace). Empty skips Claude marketplace write.
	ClaudePluginRoot string
	// ClaudeCacheRoot is the operative Claude cache install path Claude Code
	// actually loads. Empty skips cache write.
	ClaudeCacheRoot string
	// GrokPluginRoot is the absolute Grok user plugin directory
	// (~/.grok/plugins/vibe-palace). Empty skips Grok surfaces.
	GrokPluginRoot string
	// AllowStaleRemoval deletes managed shims for removed commands/skills.
	// When true, removals are counted on GlobalInstallReport and should be
	// printed by the caller (M2).
	AllowStaleRemoval bool
}

// GlobalInstallReport summarizes what InstallGlobalSurfaces wrote.
type GlobalInstallReport struct {
	ClaudeCommands Report
	ClaudeSkills   Report
	ClaudeCacheCmd Report
	ClaudeCacheSkl Report
	GrokCommands   Report
	GrokSkills     Report
	GrokHub        Report
	Errors         []string
}

// RemovedTotal sums Apply Removed counts across all host trees.
func (r GlobalInstallReport) RemovedTotal() int {
	return r.ClaudeCommands.Removed + r.ClaudeSkills.Removed +
		r.ClaudeCacheCmd.Removed + r.ClaudeCacheSkl.Removed +
		r.GrokCommands.Removed + r.GrokSkills.Removed + r.GrokHub.Removed
}

// InstallGlobalSurfaces writes user-global thin shims into the configured
// host plugin trees using Plan/Apply (never blind WriteFile). Command names
// come from commands.List with an empty project slug (embedded + vault-global).
func InstallGlobalSurfaces(opts GlobalInstallOptions) GlobalInstallReport {
	var rep GlobalInstallReport
	resolver := vpctx.NewResolver(opts.VaultRoot)

	summaries, err := commands.List(resolver, "command", "", "", "", 60)
	if err != nil {
		rep.Errors = append(rep.Errors, "list commands: "+err.Error())
		return rep
	}
	items := listSkillItems(resolver, &rep)

	applyOpts := ApplyOptions{AllowStaleRemoval: opts.AllowStaleRemoval}

	if opts.ClaudePluginRoot != "" {
		cr, sr, errs := installHostPluginSurfaces(opts.ClaudePluginRoot, summaries, items, ClaudeSkill, applyOpts)
		rep.ClaudeCommands = cr
		rep.ClaudeSkills = sr
		rep.Errors = append(rep.Errors, errs...)
	}
	if opts.ClaudeCacheRoot != "" {
		cr, sr, errs := installHostPluginSurfaces(opts.ClaudeCacheRoot, summaries, items, ClaudeSkill, applyOpts)
		rep.ClaudeCacheCmd = cr
		rep.ClaudeCacheSkl = sr
		rep.Errors = append(rep.Errors, errs...)
	}
	if opts.GrokPluginRoot != "" {
		cr, sr, errs := installHostPluginSurfaces(opts.GrokPluginRoot, summaries, items, GrokSkill, applyOpts)
		rep.GrokCommands = cr
		rep.GrokSkills = sr
		rep.Errors = append(rep.Errors, errs...)
		if hubRep, err := applyGrokHub(opts.GrokPluginRoot, applyOpts); err != nil {
			rep.Errors = append(rep.Errors, err.Error())
		} else {
			rep.GrokHub = hubRep
		}
	}
	return rep
}

func installHostPluginSurfaces(
	pluginRoot string,
	summaries []commands.Summary,
	items []SkillItem,
	skillRender TargetKind,
	applyOpts ApplyOptions,
) (cmdRep, skillRep Report, errs []string) {
	cmdDir := filepath.Join(pluginRoot, PluginCommandsRel)
	changes, err := PlanCommandsAt(summaries, CommandSurface{CommandsDir: cmdDir})
	if err != nil {
		errs = append(errs, "plan commands "+cmdDir+": "+err.Error())
		return
	}
	cmdRep, err = Apply(changes, applyOpts)
	if err != nil {
		errs = append(errs, "apply commands "+cmdDir+": "+err.Error())
	}

	if len(items) == 0 {
		return
	}
	skillsDir := filepath.Join(pluginRoot, PluginSkillsRel)
	schanges, err := PlanSkillsAt(items, SkillSurface{SkillsDir: skillsDir, Render: skillRender})
	if err != nil {
		errs = append(errs, "plan skills "+skillsDir+": "+err.Error())
		return
	}
	skillRep, _, err = ApplySkills(schanges, applyOpts)
	if err != nil {
		errs = append(errs, "apply skills "+skillsDir+": "+err.Error())
	}
	return
}

func applyGrokHub(pluginRoot string, applyOpts ApplyOptions) (Report, error) {
	skillsDir := filepath.Join(pluginRoot, PluginSkillsRel)
	ch, err := PlanGrokHubAt(skillsDir)
	if err != nil {
		return Report{}, fmt.Errorf("plan grok hub: %w", err)
	}
	rep, _, err := ApplySkills([]SkillChange{ch}, applyOpts)
	if err != nil {
		return Report{}, fmt.Errorf("apply grok hub: %w", err)
	}
	return rep, nil
}

func listSkillItems(resolver *vpctx.Resolver, rep *GlobalInstallReport) []SkillItem {
	names, err := resolver.ListResourcesScoped("skill", "", "", "")
	if err != nil {
		rep.Errors = append(rep.Errors, "list skills: "+err.Error())
		return nil
	}
	items := make([]SkillItem, 0, len(names))
	for _, ri := range names {
		sd, _, err := resolver.ResolveSkillDir(ri.Name, "", "", "")
		if err != nil {
			rep.Errors = append(rep.Errors, "resolve skill "+ri.Name+": "+err.Error())
			continue
		}
		vaultPath := ""
		if resolver.VaultRoot() != "" {
			vaultPath = filepath.Join(resolver.VaultRoot(), "Templates", "skills", ri.Name, "SKILL.md")
		}
		items = append(items, SkillItem{
			Name:        ri.Name,
			Frontmatter: sd.Frontmatter,
			VaultPath:   vaultPath,
		})
	}
	return items
}

// GrokUserPluginDir returns ~/.grok/plugins/vibe-palace (absolute).
func GrokUserPluginDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, GrokUserPluginRel)
}

// RemoveGrokUserPlugin deletes the user-global Grok plugin tree. Idempotent.
func RemoveGrokUserPlugin() error {
	dir := GrokUserPluginDir()
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(dir)
}

// GrokUserCommandsHealthy reports whether the Grok user plugin has managed commands.
func GrokUserCommandsHealthy() bool {
	return UserSurfacesOK(filepath.Join(GrokUserPluginDir(), PluginCommandsRel))
}

// UserSurfacesOK reports whether a user-global command surface looks healthy:
// the commands dir exists and contains at least one managed vpc-*.md shim.
func UserSurfacesOK(commandsDir string) bool {
	if commandsDir == "" {
		return false
	}
	entries, err := os.ReadDir(commandsDir)
	if err != nil {
		return false
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasPrefix(name, FilePrefix) || !strings.HasSuffix(name, ".md") {
			continue
		}
		// Need at least vpc-X.md (prefix + one char + .md).
		if len(name) <= len(FilePrefix)+len(".md") {
			continue
		}
		scan, err := ScanShim(filepath.Join(commandsDir, name))
		if err == nil && scan.HasMarker {
			return true
		}
	}
	return false
}

// FormatApplyCounts is a shared printer fragment for install receipts.
func FormatApplyCounts(label string, rep Report) string {
	return fmt.Sprintf("%s: added %d updated %d unchanged %d removed %d",
		label, rep.Added, rep.Updated, rep.Unchanged, rep.Removed)
}
