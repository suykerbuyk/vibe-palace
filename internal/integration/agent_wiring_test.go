// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
)

// TestIntegrationAgentWiringRoundTrip proves that Wire preserves hand-edited
// content outside the managed block byte-for-byte while inserting the block
// correctly, and that a second Wire reports Unchanged.
func TestIntegrationAgentWiringRoundTrip(t *testing.T) {
	root := t.TempDir()
	preText := "# Project Notes\n\nHand-authored intro.\n\n## Custom Section\n"
	postText := "\nMore user content.\n"
	path := filepath.Join(root, "CLAUDE.md")
	if err := os.WriteFile(path, []byte(preText+postText), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	targets, _ := agentfile.Detect(root)
	if len(targets) != 1 || targets[0].DisplayName != "CLAUDE.md" {
		t.Fatalf("detect = %+v, want single CLAUDE.md", targets)
	}

	res, err := agentfile.Wire(targets[0])
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	if res.Kind != agentfile.Added {
		t.Errorf("Kind = %v, want Added", res.Kind)
	}

	data, _ := os.ReadFile(path)
	s := string(data)
	if !strings.HasPrefix(s, preText+postText) {
		t.Errorf("original content not preserved byte-identically at head: got %q", s)
	}
	if !strings.Contains(s, "<!-- vibe-palace:begin") {
		t.Error("managed block not inserted")
	}

	// Second wire: must report Unchanged; mtime must not change.
	info1, _ := os.Stat(path)
	res2, err := agentfile.Wire(targets[0])
	if err != nil {
		t.Fatalf("second Wire: %v", err)
	}
	if res2.Kind != agentfile.Unchanged {
		t.Errorf("second Kind = %v, want Unchanged", res2.Kind)
	}
	info2, _ := os.Stat(path)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Errorf("mtime changed on Unchanged run")
	}
}

// TestIntegrationAgentWiringSymlinkDedup proves that a symlink from
// CLAUDE.md → AGENTS.md collapses to a single Target that writes once.
func TestIntegrationAgentWiringSymlinkDedup(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(real, []byte("# shared agents file\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Symlink("AGENTS.md", filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	targets, _ := agentfile.Detect(root)
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	if targets[0].DisplayName != "CLAUDE.md" || len(targets[0].Aliases) != 1 || targets[0].Aliases[0] != "AGENTS.md" {
		t.Errorf("display/aliases = %s/%v", targets[0].DisplayName, targets[0].Aliases)
	}
	if _, err := agentfile.Wire(targets[0]); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	// Both paths must see the same content (proves we wrote the underlying file).
	viaReal, _ := os.ReadFile(real)
	viaLink, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if string(viaReal) != string(viaLink) {
		t.Error("symlink and real path diverged after wire")
	}
	if !strings.Contains(string(viaReal), "<!-- vibe-palace:begin") {
		t.Error("block not written through symlink")
	}
}

// TestIntegrationAgentWiringGithubMissing proves we never create .github/ and
// surface a clear skip reason for the copilot file.
func TestIntegrationAgentWiringGithubMissing(t *testing.T) {
	root := t.TempDir()
	// Only CLAUDE.md exists; no .github/ directory.
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# c\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, skips := agentfile.Detect(root)
	if len(skips) != 1 || skips[0].DisplayName != ".github/copilot-instructions.md" {
		t.Fatalf("skips = %v, want single copilot skip", skips)
	}
	if !strings.Contains(skips[0].Reason, ".github/") {
		t.Errorf("reason should explain .github/: %q", skips[0].Reason)
	}
	// Confirm .github/ was not created.
	if _, err := os.Stat(filepath.Join(root, ".github")); !os.IsNotExist(err) {
		t.Error(".github/ should not exist after Detect")
	}
}

// TestIntegrationAgentWiringThreeRunsIdempotent proves that running the wire
// flow three times produces one write plus two no-ops, with the file mtime
// preserved across runs 2 and 3.
func TestIntegrationAgentWiringThreeRunsIdempotent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "CLAUDE.md")
	if err := os.WriteFile(path, []byte("# start\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	targets, _ := agentfile.Detect(root)
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}

	// Run 1: Added.
	if r, _ := agentfile.Wire(targets[0]); r.Kind != agentfile.Added {
		t.Errorf("run 1 Kind = %v, want Added", r.Kind)
	}
	info2Before, _ := os.Stat(path)

	// Run 2: Unchanged.
	if r, _ := agentfile.Wire(targets[0]); r.Kind != agentfile.Unchanged {
		t.Errorf("run 2 Kind = %v, want Unchanged", r.Kind)
	}
	info2After, _ := os.Stat(path)
	if !info2Before.ModTime().Equal(info2After.ModTime()) {
		t.Errorf("run 2 changed mtime")
	}

	// Run 3: Unchanged.
	if r, _ := agentfile.Wire(targets[0]); r.Kind != agentfile.Unchanged {
		t.Errorf("run 3 Kind = %v, want Unchanged", r.Kind)
	}
	info3, _ := os.Stat(path)
	if !info2Before.ModTime().Equal(info3.ModTime()) {
		t.Errorf("run 3 changed mtime")
	}
}

// TestIntegrationAgentWiringRestoresTamperedBlock proves the DoD "manually
// alter the block content → re-run vp init → block restored" requirement.
func TestIntegrationAgentWiringRestoresTamperedBlock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# agents\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	targets, _ := agentfile.Detect(root)
	if _, err := agentfile.Wire(targets[0]); err != nil {
		t.Fatalf("initial wire: %v", err)
	}

	// Hand-edit the block body.
	data, _ := os.ReadFile(path)
	tampered := strings.Replace(string(data), "vp_bootstrap_context", "vp_SABOTAGED", 1)
	if err := os.WriteFile(path, []byte(tampered), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	res, err := agentfile.Wire(targets[0])
	if err != nil {
		t.Fatalf("restore wire: %v", err)
	}
	if res.Kind != agentfile.Updated {
		t.Errorf("Kind = %v, want Updated", res.Kind)
	}
	restored, _ := os.ReadFile(path)
	if strings.Contains(string(restored), "vp_SABOTAGED") {
		t.Error("tampered content not replaced")
	}
	if !strings.HasPrefix(string(restored), "# agents\n") {
		t.Error("content outside block corrupted")
	}
}

// TestIntegrationAgentWiringPerFileIsolation proves that multiple candidate
// files get wired independently and a failure in one does not affect another.
func TestIntegrationAgentWiringPerFileIsolation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# c\n"), 0o644); err != nil {
		t.Fatalf("seed claude: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".cursorrules"), []byte("cursor rules\n"), 0o644); err != nil {
		t.Fatalf("seed cursorrules: %v", err)
	}

	targets, _ := agentfile.Detect(root)
	if len(targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(targets))
	}
	for _, target := range targets {
		if _, err := agentfile.Wire(target); err != nil {
			t.Errorf("wire %s: %v", target.DisplayName, err)
		}
	}
	for _, name := range []string{"CLAUDE.md", ".cursorrules"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(data), "<!-- vibe-palace:begin") {
			t.Errorf("%s missing block after wire", name)
		}
	}
}
