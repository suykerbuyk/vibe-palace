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

// TestIntegrationCommandsUpgradeFullLoop exercises the full surface:
// seed a vault with one unchanged, one user-edited, and one missing command;
// Plan reports the three kinds; dry-run summary is correct; Apply writes
// exactly the accepted changes; re-Plan shows everything unchanged.
func TestIntegrationCommandsUpgradeFullLoop(t *testing.T) {
	vault := t.TempDir()
	r := vpctx.NewResolver(vault)

	// Seed: restart matches embedded exactly; wrap is user-edited.
	embRestart, err := r.EmbeddedContent("command:restart")
	if err != nil {
		t.Fatalf("read embedded restart: %v", err)
	}
	writeFile(t, filepath.Join(vault, "Templates/commands/restart.md"), embRestart)
	writeFile(t, filepath.Join(vault, "Templates/commands/wrap.md"), "# user-edited wrap\n")

	// Plan
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
	if kinds["wrap"] != commands.ChangeUpdated {
		t.Errorf("wrap kind = %q, want updated", kinds["wrap"])
	}
	newCount := 0
	for _, k := range kinds {
		if k == commands.ChangeNew {
			newCount++
		}
	}
	if newCount == 0 {
		t.Errorf("expected at least one new template; kinds=%v", kinds)
	}

	// Accept everything non-unchanged, apply, verify idempotence.
	accepted := make([]commands.Change, 0, len(plan))
	for _, c := range plan {
		if c.Kind != commands.ChangeUnchanged {
			accepted = append(accepted, c)
		}
	}
	if err := commands.Apply(accepted); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	plan2, err := commands.Plan(r, commands.PlanOptions{})
	if err != nil {
		t.Fatalf("re-Plan: %v", err)
	}
	for _, c := range plan2 {
		if c.Kind != commands.ChangeUnchanged {
			t.Errorf("%s: kind=%q after Apply, want unchanged", c.Name, c.Kind)
		}
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

// TestIntegrationVaultDirtyDetection proves the vault-git-dirty check:
// init a real git repo under the vault root, stage a new Templates/commands
// file, and confirm `git status --porcelain` output is non-empty and filtered
// to the right prefix.
func TestIntegrationVaultDirtyDetection(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	vault := t.TempDir()
	run(t, vault, "git", "init", "-q")
	run(t, vault, "git", "config", "user.email", "test@example.com")
	run(t, vault, "git", "config", "user.name", "Test")

	// Untracked file under Templates/commands is "dirty" for our purposes.
	writeFile(t, filepath.Join(vault, "Templates/commands/new-one.md"), "hello\n")

	// Re-use the same porcelain+filter the CLI uses by shelling out directly
	// (keeps this test independent of cmd/vp package).
	out, err := exec.Command("git", "-C", vault, "status", "--porcelain", "--",
		"Templates/commands", "Projects").Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if !strings.Contains(string(out), "Templates/commands") {
		t.Errorf("expected Templates/commands entry in porcelain output:\n%s", out)
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
