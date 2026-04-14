// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/absorb"
	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestIntegrationWireAllOrchestratorConvergence proves that regardless of
// which of the three orchestrators last wrote the agent files, every one of
// them ends with a managed block whose body is the embedded golden. The
// sequence exercises the full matrix:
//
//  1. cmd_init path: agentfile.WireAll(projectRoot) — wires all detected
//     targets from scratch.
//  2. absorb path: agentfile.WireAll(projectRoot, WithTargets(t)) — wires
//     a single target chosen by the caller after rewriting the file.
//  3. commands upgrade path: commands.ApplyAgentBlocks — thin wrapper that
//     routes a pre-computed change list through WireAll(WithTargets(…)).
//
// After each step, every candidate file is expected to carry the current
// embedded block (same sha, same body). That is the behavior-preservation
// contract the refactor had to uphold.
func TestIntegrationWireAllOrchestratorConvergence(t *testing.T) {
	root := t.TempDir()

	// Seed all four non-copilot candidates with distinct user content so we
	// can verify the content-outside-the-block is preserved by every path.
	seeds := map[string]string{
		"CLAUDE.md":    "# Project Claude\n\nHand-authored intro.\n",
		"AGENTS.md":    "# Agents\n\nUser rules.\n",
		".cursorrules": "cursor house rules\n",
		".rules":       "generic rules\n",
	}
	for name, body := range seeds {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	goldenBody, err := os.ReadFile(filepath.Join("..", "agentfile", "testdata", "block_body.golden"))
	if err != nil {
		t.Fatalf("read golden body: %v", err)
	}
	expectedSha := agentfile.ExpectedSha()

	assertAllFilesHaveCurrentBlock := func(stage string) {
		t.Helper()
		for name, seededBody := range seeds {
			path := filepath.Join(root, name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s: read %s: %v", stage, name, err)
			}
			// Golden block body must appear.
			if !bytes.Contains(data, goldenBody) {
				t.Errorf("%s/%s: missing golden block body", stage, name)
			}
			// Current sha must appear.
			if !bytes.Contains(data, []byte("sha="+expectedSha)) {
				t.Errorf("%s/%s: missing sha=%s", stage, name, expectedSha)
			}
			// Pre-seeded user content above the block must survive every
			// step (absorb is the stage that replaces preamble, so we
			// check seeded-body survival only at stages where it should).
			if stage == "after-init" || stage == "after-upgrade" {
				if !bytes.HasPrefix(data, []byte(seededBody)) {
					t.Errorf("%s/%s: seeded preamble lost", stage, name)
				}
			}
		}
	}

	// ---- Stage 1: cmd_init path (WireAll with no options) ----
	outcomes, _, err := agentfile.WireAll(root)
	if err != nil {
		t.Fatalf("WireAll (init): %v", err)
	}
	if len(outcomes) != 4 {
		t.Fatalf("init outcomes = %d, want 4", len(outcomes))
	}
	assertAllFilesHaveCurrentBlock("after-init")

	// ---- Stage 2: commands upgrade path (ApplyAgentBlocks wrapper) ----
	// Tamper one block's sha so the change-list has one stale entry; the
	// wrapper must route it through WireAll and restore the current sha.
	claudePath := filepath.Join(root, "CLAUDE.md")
	data, _ := os.ReadFile(claudePath)
	tampered := bytes.Replace(data, []byte("sha="+expectedSha), []byte("sha=0000000"), 1)
	if err := os.WriteFile(claudePath, tampered, 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	changes, err := commands.ScanAgentBlocks(root)
	if err != nil {
		t.Fatalf("ScanAgentBlocks: %v", err)
	}
	if err := commands.ApplyAgentBlocks(changes); err != nil {
		t.Fatalf("ApplyAgentBlocks: %v", err)
	}
	assertAllFilesHaveCurrentBlock("after-upgrade")

	// ---- Stage 3: absorb path (Apply → WireAll(WithTargets)) ----
	// Seed one source file with a proper absorbable heading so the plan is
	// non-empty. We target AGENTS.md specifically. Absorb replaces the
	// file's body with the source's preamble + refreshed managed block.
	agentsPath := filepath.Join(root, "AGENTS.md")
	absorbable := "# Agents\n\nUser rules.\n\n## Vibe-Palace Integration\n\nDo not absorb me — section body.\n"
	if err := os.WriteFile(agentsPath, []byte(absorbable), 0o644); err != nil {
		t.Fatalf("seed absorbable: %v", err)
	}
	plan, err := absorb.BuildPlan(root)
	if err != nil {
		t.Fatalf("absorb.BuildPlan: %v", err)
	}
	vaultRoot := t.TempDir()
	vault := storage.NewVault(vaultRoot)
	projDir := filepath.Join(vaultRoot, "Projects", "convergence")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir vault proj: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "config.toml"),
		[]byte("[project]\nname = \"convergence\"\n"), 0o644); err != nil {
		t.Fatalf("seed vault config: %v", err)
	}
	if _, err := absorb.Apply(plan, absorb.WriteOptions{
		Vault:       vault,
		Project:     "convergence",
		ProjectRoot: root,
	}); err != nil {
		t.Fatalf("absorb.Apply: %v", err)
	}
	// After absorb, AGENTS.md was rewritten; the block must still be
	// current. The other files were not touched by absorb, so they
	// should still carry the current block from stage 2.
	data, err = os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("read AGENTS.md post-absorb: %v", err)
	}
	if !bytes.Contains(data, goldenBody) {
		t.Errorf("post-absorb AGENTS.md: missing golden body")
	}
	if !bytes.Contains(data, []byte("sha="+expectedSha)) {
		t.Errorf("post-absorb AGENTS.md: missing sha=%s", expectedSha)
	}
	for name := range seeds {
		if name == "AGENTS.md" {
			continue
		}
		d, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s post-absorb: %v", name, err)
		}
		if !bytes.Contains(d, goldenBody) || !bytes.Contains(d, []byte("sha="+expectedSha)) {
			t.Errorf("post-absorb %s: block drift (absorb touched a non-target file)", name)
		}
	}

	// ---- Final: a re-run of WireAll must report all Unchanged ----
	final, _, err := agentfile.WireAll(root)
	if err != nil {
		t.Fatalf("final WireAll: %v", err)
	}
	for _, oc := range final {
		if oc.Err != nil {
			t.Errorf("final %s: err = %v", oc.Target.DisplayName, oc.Err)
		}
		if oc.Result.Kind != agentfile.Unchanged {
			t.Errorf("final %s: Kind = %v, want Unchanged — orchestrators diverged", oc.Target.DisplayName, oc.Result.Kind)
		}
	}
}
