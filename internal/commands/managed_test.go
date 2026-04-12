// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package commands_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
)

func TestScanAgentBlocks_Missing_Stale_Current(t *testing.T) {
	proj := t.TempDir()

	// CLAUDE.md: no block → Missing.
	must(t, os.WriteFile(filepath.Join(proj, "CLAUDE.md"), []byte("# Existing\n"), 0o644))
	// AGENTS.md: current block — wire it, then scan.
	must(t, os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("# Agents\n"), 0o644))
	// .cursorrules: stale block with a fake old sha.
	stale := "## pre\n<!-- vibe-palace:begin v=1 sha=deadbee -->\nold body\n<!-- vibe-palace:end -->\n"
	must(t, os.WriteFile(filepath.Join(proj, ".cursorrules"), []byte(stale), 0o644))

	// Wire AGENTS.md to current sha.
	targets, _ := agentfile.Detect(proj)
	for _, tgt := range targets {
		if tgt.DisplayName == "AGENTS.md" {
			if _, err := agentfile.Wire(tgt); err != nil {
				t.Fatalf("wire AGENTS.md: %v", err)
			}
		}
	}

	changes, err := commands.ScanAgentBlocks(proj)
	if err != nil {
		t.Fatalf("ScanAgentBlocks: %v", err)
	}

	byDisplay := map[string]commands.BlockChange{}
	for _, c := range changes {
		byDisplay[c.Target.DisplayName] = c
	}

	if got := byDisplay["CLAUDE.md"].Kind; got != commands.BlockMissing {
		t.Errorf("CLAUDE.md: kind=%q, want missing", got)
	}
	if got := byDisplay["AGENTS.md"].Kind; got != commands.BlockCurrent {
		t.Errorf("AGENTS.md: kind=%q, want current", got)
	}
	if got := byDisplay[".cursorrules"].Kind; got != commands.BlockStale {
		t.Errorf(".cursorrules: kind=%q, want stale", got)
	}
	if byDisplay[".cursorrules"].PresentSha != "deadbee" {
		t.Errorf(".cursorrules: PresentSha=%q, want deadbee",
			byDisplay[".cursorrules"].PresentSha)
	}
}

func TestApplyAgentBlocks_WritesMissingAndStale(t *testing.T) {
	proj := t.TempDir()
	must(t, os.WriteFile(filepath.Join(proj, "CLAUDE.md"), []byte("# existing\n"), 0o644))

	changes, err := commands.ScanAgentBlocks(proj)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if err := commands.ApplyAgentBlocks(changes); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(proj, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "vibe-palace:begin") {
		t.Errorf("CLAUDE.md missing managed block after apply:\n%s", got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
