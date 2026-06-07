// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package agentfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureManagedCreatesFileWithBlock: when the target file is absent,
// EnsureManaged creates it and writes the managed vibe-palace block.
func TestEnsureManagedCreatesFileWithBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")

	res, err := EnsureManaged(path, "AGENTS.md")
	if err != nil {
		t.Fatalf("EnsureManaged: %v", err)
	}
	if res.Kind != Added {
		t.Errorf("Kind = %v, want Added", res.Kind)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if !strings.Contains(string(data), "<!-- vibe-palace:begin ") {
		t.Errorf("managed block marker missing from:\n%s", data)
	}
}

// TestEnsureManagedIdempotent: a second EnsureManaged on the same file is a
// no-op (Unchanged) and does not duplicate the managed block.
func TestEnsureManagedIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")

	if _, err := EnsureManaged(path, "AGENTS.md"); err != nil {
		t.Fatalf("EnsureManaged (first): %v", err)
	}
	res, err := EnsureManaged(path, "AGENTS.md")
	if err != nil {
		t.Fatalf("EnsureManaged (second): %v", err)
	}
	if res.Kind != Unchanged {
		t.Errorf("Kind = %v, want Unchanged", res.Kind)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n := strings.Count(string(data), "<!-- vibe-palace:begin "); n != 1 {
		t.Errorf("managed block appears %d times, want 1:\n%s", n, data)
	}
}
