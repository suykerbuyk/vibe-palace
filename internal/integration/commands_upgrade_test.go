// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// TestIntegrationCommandsUpgradeFullLoop exercises the full surface through
// the real binary: a vault with one byte-identical command, one user-edited
// one, and every other command absent. Plan reports unchanged / override /
// unneeded; `vp commands upgrade --overwrite` applies the vp-owned changes
// (shims), keeps the override byte-for-byte and reports it, and creates no
// mirror; a second run has nothing left to do.
func TestIntegrationCommandsUpgradeFullLoop(t *testing.T) {
	bin := buildVPBinary(t)
	env := setupFreshEnv(t)
	runVP(t, bin, env, nil, "init", env.projectDir,
		"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")
	vault := env.vaultPath
	r := vpctx.NewResolver(vault)

	embRestart, err := r.EmbeddedContent("command:restart")
	if err != nil {
		t.Fatalf("read embedded restart: %v", err)
	}
	const userWrap = "# user-edited wrap\n"
	writeFile(t, filepath.Join(vault, "Templates/commands/restart.md"), embRestart)
	writeFile(t, filepath.Join(vault, "Templates/commands/wrap.md"), userWrap)

	plan, err := commands.Plan(r, commands.PlanOptions{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	kinds := map[string]commands.ChangeKind{}
	for _, c := range plan {
		kinds[c.Name] = c.Kind
	}
	if kinds["restart"] != commands.ChangeUnchanged {
		t.Errorf("restart kind = %q, want unchanged", kinds["restart"])
	}
	if kinds["wrap"] != commands.ChangeOverride {
		t.Errorf("wrap kind = %q, want override", kinds["wrap"])
	}
	unneededCount := 0
	for _, k := range kinds {
		if k == commands.ChangeUnneeded {
			unneededCount++
		}
	}
	if unneededCount == 0 {
		t.Errorf("expected at least one unneeded template; kinds=%v", kinds)
	}

	out := runVP(t, bin, env, nil, "commands", "upgrade", "--overwrite")
	if !strings.Contains(out, "[keep] Templates/commands/wrap.md — override of a built-in") {
		t.Errorf("the override is not reported as kept:\n%s", out)
	}
	got, err := os.ReadFile(filepath.Join(vault, "Templates/commands/wrap.md"))
	if err != nil || string(got) != userWrap {
		t.Errorf("wrap.md changed: %q (err=%v)", got, err)
	}

	// Fixed point: a second, non-TTY run (stdin a pipe) has nothing to do,
	// and the plan is as it was.
	out, code := runVPCode(t, bin, env, []byte{}, nil, "commands", "upgrade")
	if code != 0 || !strings.Contains(out, "Nothing to do") {
		t.Errorf("a second run found work (exit %d):\n%s", code, out)
	}
	plan2, err := commands.Plan(r, commands.PlanOptions{})
	if err != nil {
		t.Fatalf("re-Plan: %v", err)
	}
	for _, c := range plan2 {
		if c.Kind != kinds[c.Name] {
			t.Errorf("%s: kind %q -> %q across an upgrade", c.Name, kinds[c.Name], c.Kind)
		}
	}
	entries, err := os.ReadDir(filepath.Join(vault, "Templates", "commands"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("Templates/commands/ holds %d files %v, want exactly the 2 seeded", len(entries), names)
	}
}

// TestIntegrationCommandsUpgradeManagedBlock proves that ScanAgentBlocks
// correctly surfaces stale and missing managed blocks, and ApplyAgentBlocks
// re-wires them via the atomic-write path — without touching the user's
// hand-authored content.
func TestIntegrationCommandsUpgradeManagedBlock(t *testing.T) {
	proj := t.TempDir()

	// Seed an agent file with user content AND a stale block.
	handAuthored := "# Project Notes\n\nHand-authored content.\n\n"
	stale := "<!-- vibe-palace:begin v=1 sha=000aaaa -->\nstale body\n<!-- vibe-palace:end -->\n"
	path := filepath.Join(proj, "CLAUDE.md")
	writeFile(t, path, handAuthored+stale)

	changes, err := commands.ScanAgentBlocks(proj)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Kind != commands.BlockStale {
		t.Errorf("kind = %q, want stale", changes[0].Kind)
	}
	if changes[0].PresentSha != "000aaaa" {
		t.Errorf("PresentSha = %q, want 000aaaa", changes[0].PresentSha)
	}
	if changes[0].ExpectedSha != agentfile.ExpectedSha() {
		t.Errorf("ExpectedSha = %q, want %q",
			changes[0].ExpectedSha, agentfile.ExpectedSha())
	}

	if err := commands.ApplyAgentBlocks(changes); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Hand-authored content must survive.
	if !strings.HasPrefix(string(got), handAuthored) {
		t.Errorf("hand-authored content not preserved at head:\n%s", got)
	}
	// New block must be present with the current sha.
	if !strings.Contains(string(got), "sha="+agentfile.ExpectedSha()) {
		t.Errorf("expected sha %s not in file:\n%s", agentfile.ExpectedSha(), got)
	}

	// Re-scan reports Current.
	again, err := commands.ScanAgentBlocks(proj)
	if err != nil {
		t.Fatalf("re-scan: %v", err)
	}
	if len(again) != 1 || again[0].Kind != commands.BlockCurrent {
		t.Errorf("after Apply: %+v", again)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}
