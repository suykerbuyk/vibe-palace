// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCursorPresentRulesDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".cursor", "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !CursorPresent(root) {
		t.Error(".cursor/rules/ present but CursorPresent = false")
	}
}

func TestCursorPresentCursorDirOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !CursorPresent(root) {
		t.Error(".cursor/ present (no rules/ subdir) but CursorPresent = false")
	}
}

func TestCursorAbsent(t *testing.T) {
	root := t.TempDir()
	if CursorPresent(root) {
		t.Error("empty project reported as Cursor-present")
	}
}

func TestCursorRCFlatFileIsNotATrigger(t *testing.T) {
	// Strict rule: a flat .cursorrules file does NOT trigger Cursor rule
	// shim emission — agentfile.Detect already manages that surface, and
	// bootstrapping .cursor/rules/ from a .cursorrules-only project
	// presumes a Cursor directory surface the user never opted into.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".cursorrules"), []byte("rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if CursorPresent(root) {
		t.Error(".cursorrules flat file triggered CursorPresent — must not")
	}
}

func TestCursorPresentRejectsFileNamedCursor(t *testing.T) {
	// Someone with an unrelated `.cursor` regular file should not be
	// classified as Cursor-present.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".cursor"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if CursorPresent(root) {
		t.Error(".cursor file (not dir) triggered CursorPresent — must not")
	}
}
