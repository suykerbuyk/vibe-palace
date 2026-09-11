// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// TestIntegrationSkillMaterializeAndReconcile exercises skill delivery under
// the Design B (override-only) model end to end:
//
//  1. A fresh `vp init` writes NO skill mirror — the embedded floor serves
//     every startup-analyst file (SKILL.md + 5 references) directly, and no
//     host-local templates.lock is written (none is read or written by any
//     vp from this release).
//  2. The `vp_skill` MCP tool still returns the SKILL.md body for
//     startup-analyst, resolved from the embedded tier.
//  3. A genuine override of one reference (bytes vp never shipped) SURVIVES
//     a sync — it is operator content, kept — and wins resolution, while
//     non-overridden references keep resolving from embedded.
func TestIntegrationSkillMaterializeAndReconcile(t *testing.T) {
	bin := buildVPBinary(t)

	env := setupFreshEnv(t)
	runVP(t, bin, env, nil, "init", env.projectDir,
		"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")

	// --- Part 1: nothing materialized; embedded serves the corpus. ---
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
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("override-only init should not materialize %s (err=%v)", rel, err)
		}
	}

	if _, err := os.Stat(filepath.Join(env.vaultPath, retiredLockRel)); !os.IsNotExist(err) {
		t.Errorf("init wrote %s (stat err=%v)", retiredLockRel, err)
	}

	// Sanity: the WalkEmbedded resource set still contains all six paths —
	// the embedded floor is the source of truth now.
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

	// --- Part 2: vp_skill returns SKILL.md body from the embedded floor. ---
	// The in-process resolver finds the embedded tier without any vault
	// overlay — that is exactly the override-only serving path.
	h := newHarness(t, false)
	h.registerAllTools(t)
	body := h.callTool(t, "vp_skill", map[string]any{"name": "startup-analyst"})
	if !strings.Contains(body, "Startup Business Plan Analyst") {
		t.Errorf("vp_skill body missing expected heading; got len=%d", len(body))
	}

	// --- Part 3: a genuine reference override survives a sync. ---
	// Seed an override (bytes vp never shipped): the reconciler classifies it
	// as operator content and keeps it.
	const capexRel = "skills/startup-analyst/references/capex-opex.md"
	const userEdit = "# USER EDITED CAPEX\n\ncustom notes\n"
	seedIntegrationOverride(t, env.vaultPath, capexRel, []byte(userEdit))
	capexPath := filepath.Join(env.vaultPath, "Templates", filepath.FromSlash(capexRel))

	runVP(t, bin, env, nil, "config", "sync", "--yes",
		"--project-root", env.projectDir)

	after, err := os.ReadFile(capexPath)
	if err != nil {
		t.Fatalf("re-read capex: %v", err)
	}
	if string(after) != userEdit {
		t.Errorf("override clobbered by reconcile\n got  %q\n want %q", after, userEdit)
	}
	if _, err := os.Stat(capexPath + ".bak"); err == nil {
		t.Error("kept override should not produce .bak (embedded stable)")
	}
	// Keeping it wrote no host-local state.
	if _, err := os.Stat(filepath.Join(env.vaultPath, retiredLockRel)); !os.IsNotExist(err) {
		t.Errorf("sync wrote %s (stat err=%v)", retiredLockRel, err)
	}
	// A non-overridden reference still resolves from the embedded floor.
	compRel := "skills/startup-analyst/references/competitive-landscape.md"
	compPath := filepath.Join(env.vaultPath, "Templates", filepath.FromSlash(compRel))
	if _, err := os.Stat(compPath); !os.IsNotExist(err) {
		t.Errorf("non-overridden reference should not be on disk (err=%v)", err)
	}
}

// seedIntegrationOverride writes data to the vault Templates/ target for
// embeddedRel — a genuine user override under the override-only model, where a
// fresh init leaves no mirror to edit. It plants the file only.
func seedIntegrationOverride(t *testing.T, vaultPath, embeddedRel string, data []byte) {
	t.Helper()
	target := filepath.Join(vaultPath, "Templates", filepath.FromSlash(embeddedRel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
}
