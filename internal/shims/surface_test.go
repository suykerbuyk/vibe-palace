// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/skills"
)

func TestPlanCommandsAt_PluginLayout(t *testing.T) {
	root := t.TempDir()
	cmdDir := filepath.Join(root, "commands")
	sums := []commands.Summary{
		{Name: "restart", Brief: "boot"},
		{Name: "wrap", Brief: "close"},
	}
	changes, err := PlanCommandsAt(sums, CommandSurface{CommandsDir: cmdDir})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Apply(changes, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Added != 2 {
		t.Fatalf("added = %d, want 2", rep.Added)
	}
	// Must NOT create .claude under the plugin root.
	if _, err := os.Stat(filepath.Join(root, ".claude")); !os.IsNotExist(err) {
		t.Errorf("plugin root must not grow .claude/; err=%v", err)
	}
	body, err := os.ReadFile(filepath.Join(cmdDir, "vpc-restart.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "vibe-palace:shim") {
		t.Error("missing shim marker")
	}
}

func TestPlanSkillsAt_ClaudeRenderUnderPluginSkills(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, "skills")
	items := []SkillItem{{
		Name:        "pairing",
		Frontmatter: skills.SkillFrontmatter{Description: "pair"},
	}}
	changes, err := PlanSkillsAt(items, SkillSurface{SkillsDir: skillsDir, Render: ClaudeSkill})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApplySkills(changes, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(skillsDir, "vps-pairing", "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s: %v", path, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude")); !os.IsNotExist(err) {
		t.Error("must not nest .claude/skills under plugin root")
	}
}

func TestInstallGlobalSurfaces_ClaudeAndGrok(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	claudePlug := filepath.Join(home, "claude-plugin", "vibe-palace")
	grokPlug := filepath.Join(home, ".grok", "plugins", "vibe-palace")

	rep := InstallGlobalSurfaces(GlobalInstallOptions{
		VaultRoot:         "", // embedded only
		ClaudePluginRoot:  claudePlug,
		GrokPluginRoot:    grokPlug,
		AllowStaleRemoval: true,
	})
	if len(rep.Errors) > 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	if rep.ClaudeCommands.Added == 0 {
		t.Error("expected Claude command shims")
	}
	if rep.GrokCommands.Added == 0 {
		t.Error("expected Grok command shims")
	}
	if !UserSurfacesOK(filepath.Join(claudePlug, "commands")) {
		t.Error("Claude marketplace surface not healthy")
	}
	if !GrokUserCommandsHealthy() {
		t.Error("Grok surface not healthy")
	}
	// Hub
	if _, err := os.Stat(filepath.Join(grokPlug, "skills", "vpc", "SKILL.md")); err != nil {
		t.Errorf("grok hub missing: %v", err)
	}
}

func TestReconcile_SkipClaudeKeepsGrok(t *testing.T) {
	// C1: SkipClaude must not suppress Grok project emit.
	home := t.TempDir()
	t.Setenv("HOME", home)
	// GrokPresent via ~/.grok
	if err := os.MkdirAll(filepath.Join(home, ".grok"), 0o755); err != nil {
		t.Fatal(err)
	}

	vault := t.TempDir()
	projectRoot := t.TempDir()
	resolver := vpctx.NewResolver(vault)

	rep := Reconcile(projectRoot, resolver, "", ReconcileOptions{SkipClaude: true})
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	// No Claude project tree
	if _, err := os.Stat(filepath.Join(projectRoot, ShimDir)); !os.IsNotExist(err) {
		t.Error("SkipClaude must not write .claude/commands")
	}
	// Grok project tree should exist
	grokCmd := filepath.Join(projectRoot, GrokCommandsPluginDir)
	if _, err := os.Stat(grokCmd); err != nil {
		t.Fatalf("SkipClaude must still emit Grok project commands: %v", err)
	}
}

func TestReconcile_SkipGrokKeepsClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".grok"), 0o755); err != nil {
		t.Fatal(err)
	}

	vault := t.TempDir()
	projectRoot := t.TempDir()
	resolver := vpctx.NewResolver(vault)

	rep := Reconcile(projectRoot, resolver, "", ReconcileOptions{SkipGrok: true})
	if len(rep.Errors) != 0 {
		t.Fatalf("errors: %v", rep.Errors)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, ShimDir)); err != nil {
		t.Fatalf("SkipGrok must still emit Claude commands: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, GrokCommandsPluginDir)); !os.IsNotExist(err) {
		t.Error("SkipGrok must not write project Grok plugin commands")
	}
}
