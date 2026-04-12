// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package agentfile

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDetectEmptyDir(t *testing.T) {
	// No agent files and no .github/ dir: detection produces no targets, but
	// does surface a skip for the copilot file to hint how to enable it.
	root := t.TempDir()
	targets, skips := Detect(root)
	if len(targets) != 0 {
		t.Errorf("targets = %v, want empty", targets)
	}
	if len(skips) != 1 || skips[0].DisplayName != ".github/copilot-instructions.md" {
		t.Errorf("skips = %v, want single copilot skip", skips)
	}
}

func TestDetectAllPresent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "# claude\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "# agents\n")
	writeFile(t, filepath.Join(root, ".cursorrules"), "cursor\n")
	writeFile(t, filepath.Join(root, ".rules"), "zed\n")
	writeFile(t, filepath.Join(root, ".github", "copilot-instructions.md"), "copilot\n")

	targets, skips := Detect(root)
	if len(targets) != 5 {
		t.Fatalf("targets = %d, want 5", len(targets))
	}
	if len(skips) != 0 {
		t.Errorf("skips = %v, want empty", skips)
	}

	// Order must follow the candidates list.
	wantOrder := []string{"CLAUDE.md", "AGENTS.md", ".cursorrules", ".rules", ".github/copilot-instructions.md"}
	for i, w := range wantOrder {
		if targets[i].DisplayName != w {
			t.Errorf("targets[%d].DisplayName = %q, want %q", i, targets[i].DisplayName, w)
		}
	}
}

func TestDetectSymlinkDedup(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "# shared\n")
	if err := os.Symlink("AGENTS.md", filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}

	targets, _ := Detect(root)
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1 (symlink dedup)", len(targets))
	}
	// CLAUDE.md comes first in the candidate list, so it wins DisplayName.
	if targets[0].DisplayName != "CLAUDE.md" {
		t.Errorf("DisplayName = %q, want CLAUDE.md", targets[0].DisplayName)
	}
	if len(targets[0].Aliases) != 1 || targets[0].Aliases[0] != "AGENTS.md" {
		t.Errorf("Aliases = %v, want [AGENTS.md]", targets[0].Aliases)
	}
	// Canonical path must be the real file, not the symlink.
	real, _ := filepath.EvalSymlinks(filepath.Join(root, "AGENTS.md"))
	if targets[0].Path != real {
		t.Errorf("Path = %q, want canonical %q", targets[0].Path, real)
	}
}

func TestDetectCopilotWithoutGithubDirReportsSkip(t *testing.T) {
	root := t.TempDir()
	// No .github/ dir exists.
	_, skips := Detect(root)
	if len(skips) != 1 {
		t.Fatalf("skips = %v, want one entry for copilot", skips)
	}
	if skips[0].DisplayName != ".github/copilot-instructions.md" {
		t.Errorf("skip DisplayName = %q", skips[0].DisplayName)
	}
}

func TestDetectCopilotWithGithubDirNoFileSilent(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".github"), 0o755); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	// .github/ exists but the file doesn't — no skip, no target.
	targets, skips := Detect(root)
	if len(targets) != 0 {
		t.Errorf("targets = %v, want empty", targets)
	}
	if len(skips) != 0 {
		t.Errorf("skips = %v, want empty (file absent, not skipped-for-reason)", skips)
	}
}
