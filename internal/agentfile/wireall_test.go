// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package agentfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWireAllDetectsAllTargets proves the zero-option path: every candidate
// file present in the project root is wired, and the returned Outcome slice
// has one entry per detected target.
func TestWireAllDetectsAllTargets(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"CLAUDE.md", "AGENTS.md", ".cursorrules", ".rules"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("# "+name+"\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	outcomes, skips, err := WireAll(root)
	if err != nil {
		t.Fatalf("WireAll: %v", err)
	}
	if len(outcomes) != 4 {
		t.Fatalf("outcomes = %d, want 4", len(outcomes))
	}
	// .github skip (no .github/ dir) must surface.
	if len(skips) != 1 || skips[0].DisplayName != ".github/copilot-instructions.md" {
		t.Errorf("skips = %+v, want copilot skip", skips)
	}
	for _, oc := range outcomes {
		if oc.Err != nil {
			t.Errorf("%s: err = %v", oc.Target.DisplayName, oc.Err)
		}
		if oc.Result.Kind != Added {
			t.Errorf("%s: Kind = %v, want Added", oc.Target.DisplayName, oc.Result.Kind)
		}
		data, _ := os.ReadFile(filepath.Join(root, oc.Target.DisplayName))
		if !strings.Contains(string(data), "<!-- vibe-palace:begin") {
			t.Errorf("%s: block missing after WireAll", oc.Target.DisplayName)
		}
	}
}

// TestWireAllWithTargetsOverride proves WithTargets bypasses Detect: the
// given Target is wired even for a non-canonical filename, and other
// detected files are untouched.
func TestWireAllWithTargetsOverride(t *testing.T) {
	root := t.TempDir()
	// Seed a candidate that WOULD be detected, plus an override target.
	decoy := filepath.Join(root, "CLAUDE.md")
	if err := os.WriteFile(decoy, []byte("# decoy\n"), 0o644); err != nil {
		t.Fatalf("seed decoy: %v", err)
	}
	overridePath := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(overridePath, []byte("# override\n"), 0o644); err != nil {
		t.Fatalf("seed override: %v", err)
	}

	target := Target{Path: overridePath, DisplayName: "AGENTS.md"}
	outcomes, _, err := WireAll(root, WithTargets(target))
	if err != nil {
		t.Fatalf("WireAll: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Target.Path != overridePath {
		t.Fatalf("outcomes = %+v, want only override target", outcomes)
	}

	// Decoy must be untouched — override bypassed Detect.
	decoyData, _ := os.ReadFile(decoy)
	if strings.Contains(string(decoyData), "<!-- vibe-palace:begin") {
		t.Error("decoy CLAUDE.md wired despite WithTargets override")
	}
}

// TestWireAllErrorOnNonexistentRoot proves the top-level error surface.
func TestWireAllErrorOnNonexistentRoot(t *testing.T) {
	_, _, err := WireAll(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Error("expected error for nonexistent root, got nil")
	}
}

// TestWireAllErrorOnEmptyRoot proves we don't silently accept "".
func TestWireAllErrorOnEmptyRoot(t *testing.T) {
	_, _, err := WireAll("")
	if err == nil {
		t.Error("expected error for empty root, got nil")
	}
}

// TestWireAllErrorOnFileRoot proves we reject a file posing as a root.
func TestWireAllErrorOnFileRoot(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, _, err := WireAll(f)
	if err == nil {
		t.Error("expected error for file-as-root, got nil")
	}
}

// TestWireAllNoTargetsReturnsEmpty proves that a clean project root with no
// candidate files yields zero outcomes and no error (the cmd_init "no agent
// file found" path).
func TestWireAllNoTargetsReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	outcomes, skips, err := WireAll(root)
	if err != nil {
		t.Fatalf("WireAll: %v", err)
	}
	if len(outcomes) != 0 {
		t.Errorf("outcomes = %d, want 0", len(outcomes))
	}
	// .github/copilot-instructions.md always skipped when .github/ missing.
	if len(skips) != 1 {
		t.Errorf("skips = %d, want 1 (copilot)", len(skips))
	}
}
