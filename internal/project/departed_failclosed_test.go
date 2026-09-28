// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package project

import (
	"os"
	"path/filepath"
	"testing"
)

// Departed fails CLOSED when Projects/<slug> cannot be inspected: a write must
// not be the way to find out.
func TestDepartedFailsClosedWhenTheDirectoryIsUninspectable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through a mode-000 directory")
	}
	root := t.TempDir()
	projects := filepath.Join(root, "Projects")
	if err := os.MkdirAll(filepath.Join(projects, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(projects, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(projects, 0o755) })
	d, ok := Departed(root, "p")
	if !ok || d.Malformed == "" {
		t.Fatalf("Departed = %+v %v, want a fail-closed departure", d, ok)
	}
}
