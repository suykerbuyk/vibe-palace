// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestIntegrationBootstrapFullContext proves the bootstrap tool assembles
// correct context from real vault data: sessions, tasks, and commands.
func TestIntegrationBootstrapFullContext(t *testing.T) {
	h := newHarness(t, false) // mock embedder — bootstrap doesn't need real ONNX
	root := h.Vault.Root

	// Write session files.
	writeVaultFile(t, root, "projects/test-proj/sessions/2026-04-01-01.md", `---
session_id: "2026-04-01-01"
project: test-proj
date: "2026-04-01"
iteration: 1
title: "First session"
summary: "Implemented storage layer"
tag: implementation
---
## Summary
Implemented the storage layer.
`)

	writeVaultFile(t, root, "projects/test-proj/sessions/2026-04-02-01.md", `---
session_id: "2026-04-02-01"
project: test-proj
date: "2026-04-02"
iteration: 1
title: "Second session"
summary: "Added search engine"
tag: implementation
---
## Summary
Added the search engine.
`)

	// Write task files.
	writeVaultFile(t, root, "Projects/test-proj/tasks/active-task.md", `# Active Task
## Status: In Progress
Implement the chunking engine.
`)

	// Write a vault-level command template.
	writeVaultFile(t, root, "Templates/commands/custom-cmd.md", `# Custom Command
A vault-level custom command for testing.
`)

	h.registerAllTools(t)

	// Call bootstrap.
	result := h.callTool(t, "vp_bootstrap_context", map[string]any{
		"project": "test-proj",
	})

	// The result is JSON — parse it and verify fields.
	var bootstrap struct {
		Project           string `json:"project"`
		Workflow          string `json:"workflow"`
		CommandInvocation string `json:"command_invocation"`
		AvailableCommands []struct {
			Name  string `json:"name"`
			Alias string `json:"alias"`
			Brief string `json:"brief"`
		} `json:"available_commands"`
	}
	if err := json.Unmarshal([]byte(result), &bootstrap); err != nil {
		t.Fatalf("parse bootstrap: %v (raw: %.200s)", err, result)
	}

	if bootstrap.Workflow == "" {
		t.Error("workflow should be non-empty")
	}
	// The embedded workflow is the thin post-ADR-008 template: generic
	// doctrine is served on demand, and the workflow's contract paragraph
	// points at the vp_get_doctrine fetch.
	if !strings.Contains(bootstrap.Workflow, "vp_get_doctrine") {
		t.Error("workflow should contain the doctrine-fetch contract from the embedded template")
	}

	// Verify commands include both embedded and vault commands.
	cmdNames := make(map[string]bool)
	for _, cmd := range bootstrap.AvailableCommands {
		cmdNames[cmd.Name] = true
	}
	if !cmdNames["restart"] {
		t.Error("available_commands should include embedded 'restart'")
	}
	if !cmdNames["capture"] {
		t.Error("available_commands should include embedded 'capture'")
	}
	if !cmdNames["custom-cmd"] {
		t.Error("available_commands should include vault 'custom-cmd'")
	}

	// Phase 1 additions: every command must carry a vpc- alias, briefs
	// must not have trailing whitespace (word-boundary truncation), and
	// the invocation directive must be populated when commands exist.
	for _, cmd := range bootstrap.AvailableCommands {
		if cmd.Alias != "vpc-"+cmd.Name {
			t.Errorf("alias for %q = %q, want %q", cmd.Name, cmd.Alias, "vpc-"+cmd.Name)
		}
		if strings.HasSuffix(cmd.Brief, " ") {
			t.Errorf("brief for %q has trailing space: %q", cmd.Name, cmd.Brief)
		}
	}
	if bootstrap.CommandInvocation == "" {
		t.Error("command_invocation should be populated when commands exist")
	}
	if !strings.Contains(bootstrap.CommandInvocation, "vpc-") {
		t.Errorf("command_invocation missing vpc- reference: %q", bootstrap.CommandInvocation)
	}
}

// TestIntegrationBootstrapWithSessions proves that sessions written to the
// vault are discoverable through the bootstrap tool's context.
func TestIntegrationBootstrapWithSessions(t *testing.T) {
	h := newHarness(t, false)
	root := h.Vault.Root

	// Write a session using the storage API.
	meta := storage.SessionMeta{
		Date:    "2026-04-05",
		Title:   "Integration test session",
		Summary: "Tested the bootstrap flow end to end.",
		Tag:     "testing",
	}
	sessionID, err := h.Vault.WriteSession("test-proj", meta, "\n## Summary\n\nBootstrap integration test.\n")
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	if sessionID == "" {
		t.Fatal("expected non-empty session ID")
	}

	// Read it back.
	sessions, err := h.Vault.ListSessions("test-proj", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Summary != "Tested the bootstrap flow end to end." {
		t.Errorf("summary = %q, want %q", sessions[0].Summary, "Tested the bootstrap flow end to end.")
	}

	_ = root // vault root used for file writes above
}

// writeVaultFile creates a file at the given path relative to the vault root.
func writeVaultFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	path := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
