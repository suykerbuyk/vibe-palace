// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
)

func TestRegistryFlags(t *testing.T) {
	want := map[string]string{
		"claude": "--claude-plugin",
		"grok":   "--grok",
		"zed":    "--zed",
	}
	got := map[string]string{}
	for _, h := range Registry() {
		got[h.Name()] = h.Flag()
	}
	if len(got) != len(want) {
		t.Fatalf("registry has %d hosts, want %d: %v", len(got), len(want), got)
	}
	for name, flag := range want {
		if got[name] != flag {
			t.Errorf("host %q flag = %q, want %q", name, got[name], flag)
		}
	}
}

func TestClaudeHostFlagAndName(t *testing.T) {
	var h ClaudeHost
	if h.Name() != "claude" || h.Flag() != "--claude-plugin" {
		t.Errorf("ClaudeHost name/flag = %q/%q", h.Name(), h.Flag())
	}
	// Installed delegates to plugin.IsInstalled; in a clean temp dir it must not
	// error (the precise bool depends on the host, so only the no-error contract
	// is asserted here).
	if _, err := h.Installed(); err != nil {
		t.Errorf("Installed() error: %v", err)
	}
	// Detected delegates to plugin.Detected (~/.claude presence) — invoke for
	// the contract (no panic); the bool is host-dependent.
	_ = h.Detected()
}

func TestEnsureAgentsFile(t *testing.T) {
	dir := t.TempDir()

	// First call creates AGENTS.md with the managed block.
	res, err := ensureAgentsFile(dir)
	if err != nil {
		t.Fatalf("ensureAgentsFile: %v", err)
	}
	if res.Kind != agentfile.Added {
		t.Errorf("first wire = %v, want Added", res.Kind)
	}
	path := filepath.Join(dir, "AGENTS.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("AGENTS.md not created: %v", err)
	}

	// Second call is idempotent (block already present and current).
	res2, err := ensureAgentsFile(dir)
	if err != nil {
		t.Fatalf("second ensureAgentsFile: %v", err)
	}
	if res2.Kind != agentfile.Unchanged {
		t.Errorf("second wire = %v, want Unchanged", res2.Kind)
	}
}
