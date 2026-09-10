// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package project

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRootedGate_KeepsForceSkip pins the three properties that make
// HasRootedSignal safe to hand a remote caller, each of which a hand-rolled
// predicate loses in a different way.
func TestRootedGate_KeepsForceSkip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// (1) The force-skip survives. isForceSkipDir is unexported and reachable
	// only through DetectSignal, so a predicate written from scratch drops it
	// silently — and $HOME with a go.mod in it is not hypothetical (a
	// dotfiles repo, a stray `go mod init`).
	if err := os.WriteFile(filepath.Join(home, "go.mod"), []byte("module home\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if HasRootedSignal(home) {
		t.Errorf("HasRootedSignal(%q) = true for $HOME carrying go.mod AND .git; the force-skip was lost", home)
	}
	if HasRootedSignal("/") {
		t.Error(`HasRootedSignal("/") = true; the filesystem root must never be a project root`)
	}

	// (2) The predicate ORs over all three signals. .vibe-palace.toml wins
	// DetectSignal's precedence, so "DetectSignal(dir) != SignalNone AND the
	// marker is in dir" wrongly REJECTS a directory that has its own .git
	// under a marked ancestor. That is the real shape: a monorepo with a
	// vibe-palace marker at the top and a nested repo beneath it.
	parent := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, ConfigFileName),
		[]byte("[project]\nname = \"parent\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(filepath.Join(child, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !HasRootedSignal(child) {
		t.Errorf("HasRootedSignal(%q) = false; a directory with its own .git is a project root even under a marked ancestor", child)
	}

	// (3) No upward walk. DetectSignal returns SignalVibeConfig from
	// findMarkerUpward, which climbs parents until the exact home boundary —
	// effectively unbounded outside $HOME. A sibling of `child` with NO signal
	// of its own must be refused even though `parent` is marked.
	bare := filepath.Join(parent, "bare")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	if HasRootedSignal(bare) {
		t.Errorf("HasRootedSignal(%q) = true; the gate must not inherit an ANCESTOR's marker", bare)
	}
	// DetectSignal, by contrast, still walks upward — the two predicates are
	// deliberately different and this pins that they are.
	if got := DetectSignal(bare); got != SignalVibeConfig {
		t.Errorf("DetectSignal(%q) = %q, want %q — DetectSignal's upward walk must be untouched",
			bare, got, SignalVibeConfig)
	}

	// A path that is not a directory at all never passes.
	notDir := filepath.Join(parent, "file.txt")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if HasRootedSignal(notDir) {
		t.Errorf("HasRootedSignal(%q) = true for a regular file", notDir)
	}
	if HasRootedSignal(filepath.Join(parent, "does-not-exist")) {
		t.Error("HasRootedSignal returned true for a nonexistent path")
	}
	if HasRootedSignal("") {
		t.Error(`HasRootedSignal("") = true`)
	}
}
