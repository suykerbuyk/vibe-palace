// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// TestIntegrationSkillMaterializeAndReconcile exercises Phase 1 of the
// vps-skill-artifacts-cross-ide task end to end:
//
//  1. A fresh `vp init` materializes every embedded startup-analyst
//     file into <vault>/Templates/skills/startup-analyst/ (SKILL.md +
//     5 references) with per-file entries in templates.lock — the
//     reconciler needs no skill-specific changes; nested paths flow
//     straight through WalkEmbedded.
//  2. The `vp_skill` MCP tool returns the SKILL.md body for
//     startup-analyst.
//  3. A user edit to one reference survives a second `vp init` via
//     the row-4 three-SHA branch (match/match ⇒ Unchanged) — same
//     protocol commands already prove in template_reconcile_test.go.
func TestIntegrationSkillMaterializeAndReconcile(t *testing.T) {
	bin := buildVPBinary(t)

	env := setupFreshEnv(t)
	runVP(t, bin, env, nil, "init", env.projectDir,
		"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")

	// --- Part 1: all 6 files materialized, lock has per-file entries. ---
	wantFiles := []string{
		"SKILL.md",
		"references/capex-opex.md",
		"references/competitive-landscape.md",
		"references/funding-sources.md",
		"references/reality-validation.md",
		"references/strategic-partnerships.md",
	}
	skillRoot := filepath.Join(env.vaultPath, "Templates", "skills", "startup-analyst")
	for _, rel := range wantFiles {
		p := filepath.Join(skillRoot, filepath.FromSlash(rel))
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("missing materialized %s: %v", p, err)
		}
		want := embeddedBytesFor(t, "skills/startup-analyst/"+rel)
		if !bytes.Equal(got, want) {
			t.Errorf("%s bytes != embedded (len %d vs %d)", rel, len(got), len(want))
		}
	}

	// Lock has one entry per materialized file under the nested skill path.
	lock, err := templates.ReadLock(env.vaultPath)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	for _, rel := range wantFiles {
		key := "Templates/skills/startup-analyst/" + rel
		entry, ok := lock.Entries[key]
		if !ok {
			t.Errorf("lock missing per-file entry %s", key)
			continue
		}
		embSHA, _ := templates.EmbeddedSHA("skills/startup-analyst/" + rel)
		if entry.EmbeddedSHA != embSHA {
			t.Errorf("lock %s sha mismatch: have %q want %q", key, entry.EmbeddedSHA, embSHA)
		}
	}

	// Sanity: the WalkEmbedded resource set also contains all six paths
	// — asserting this inline proves the reconciler needed no
	// skill-specific plumbing to pick them up.
	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded: %v", err)
	}
	have := map[string]bool{}
	for _, r := range resources {
		have[r.RelPath] = true
	}
	for _, rel := range wantFiles {
		if !have["skills/startup-analyst/"+rel] {
			t.Errorf("WalkEmbedded missing %s", rel)
		}
	}

	// --- Part 2: vp_skill returns SKILL.md body via MCP harness. ---
	// We drive this through the in-process harness (faster than shelling
	// through the MCP stdio protocol for one call) with the same vault.
	h := newHarness(t, false)
	// Overlay the vault that `vp init` just populated by copying the
	// startup-analyst directory into the harness vault so the resolver
	// sees the same tier-4 content. The harness's in-process resolver
	// already finds the embedded tier-5 copy without any overlay, but
	// copying exercises the vault tier and mirrors what a user sees.
	dst := filepath.Join(h.Vault.Root, "Templates", "skills", "startup-analyst")
	if err := copyTree(skillRoot, dst); err != nil {
		t.Fatalf("copyTree: %v", err)
	}
	h.registerAllTools(t)
	body := h.callTool(t, "vp_skill", map[string]any{"name": "startup-analyst"})
	if !strings.Contains(body, "Startup Business Plan Analyst") {
		t.Errorf("vp_skill body missing expected heading; got len=%d", len(body))
	}

	// --- Part 3: reference edit survives a second `vp init`. ---
	capexPath := filepath.Join(skillRoot, "references", "capex-opex.md")
	const userEdit = "# USER EDITED CAPEX\n\ncustom notes\n"
	if err := os.WriteFile(capexPath, []byte(userEdit), 0o644); err != nil {
		t.Fatalf("edit capex: %v", err)
	}
	// Run a sync rather than a second init — init on an existing vault
	// short-circuits. Sync is the reconcile entry point users hit
	// repeatedly and exercises the same three-SHA protocol.
	runVP(t, bin, env, nil, "config", "sync", "--yes",
		"--project-root", env.projectDir)

	after, err := os.ReadFile(capexPath)
	if err != nil {
		t.Fatalf("re-read capex: %v", err)
	}
	if string(after) != userEdit {
		t.Errorf("reference edit clobbered by reconcile\n got  %q\n want %q",
			after, userEdit)
	}
	if _, err := os.Stat(capexPath + ".bak"); err == nil {
		t.Error("user-edited reference should not produce .bak (embedded stable)")
	}
	// Row 4: lock keeps embedded SHA; SKILL.md stays untouched.
	lock2, _ := templates.ReadLock(env.vaultPath)
	key := "Templates/skills/startup-analyst/references/capex-opex.md"
	entry, ok := lock2.Entries[key]
	if !ok {
		t.Fatalf("lock entry disappeared for %s", key)
	}
	embSHA, _ := templates.EmbeddedSHA("skills/startup-analyst/references/capex-opex.md")
	if entry.EmbeddedSHA != embSHA {
		t.Errorf("lock should still track embedded SHA; got %q want %q",
			entry.EmbeddedSHA, embSHA)
	}
}

// copyTree is a minimal recursive file copy used by the skill
// materialize integration test to seed a harness vault from an
// existing vault layout.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
