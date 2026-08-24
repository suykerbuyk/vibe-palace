// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The refuse-gate on task paths. Task files have a typed writer for every field
// they carry, and the value of that design is that each field has exactly ONE
// writer. A generic Write or Edit reaches all of them at once, so it is a bypass
// for all of those rules simultaneously.
//
// The gate lives HERE rather than in the MCP tool so `vp vault write` / `vp vault
// edit` are covered by the same rule — a guard only an agent can trip is not a
// guard.

func taskGateVault(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval tempdir: %v", err)
	}
	return root
}

func seed(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("seed %s: %v", rel, err)
	}
}

func TestIsTaskFilePath(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"Projects/vibe-palace/tasks/some-task.md", true},
		{"Projects/vibe-palace/tasks/done/some-task.md", true},
		{"Projects/vibe-palace/tasks/cancelled/some-task.md", true},
		{"projects/vibe-palace/TASKS/some-task.md", true}, // case-insensitive
		// Not task paths.
		{"Projects/vibe-palace/resume.md", false},
		{"Projects/vibe-palace/iterations.md", false},
		{"Projects/vibe-palace/notes/tasks.md", false},
		{"Knowledge/tasks/whatever.md", false},
		{"tasks/some-task.md", false},
		{"Projects/vibe-palace/tasks", false}, // the directory itself, not a file under it
	} {
		if got := IsTaskFilePath(tc.path); got != tc.want {
			t.Errorf("IsTaskFilePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestWriteRefusesTaskPaths(t *testing.T) {
	root := taskGateVault(t)
	for _, rel := range []string{
		"Projects/p/tasks/a-task.md",
		"Projects/p/tasks/done/a-task.md",
		"Projects/p/tasks/cancelled/a-task.md",
	} {
		seed(t, root, rel, "# A Task\n\n**Status:** pending\n**Priority:** medium\n")

		_, err := Write(root, rel, "clobbered", "")
		if err == nil {
			t.Fatalf("Write(%q) must be refused", rel)
		}
		if !errors.Is(err, ErrRefusedPath) {
			t.Errorf("Write(%q): want ErrRefusedPath, got %v", rel, err)
		}
		// The refusal must name the sanctioned route. A refusal that only says
		// no gets worked around.
		if !strings.Contains(err.Error(), "vp_manage_task") {
			t.Errorf("Write(%q) refusal must point at vp_manage_task, got %q", rel, err)
		}
		if !strings.Contains(err.Error(), "overwrite") {
			t.Errorf("Write(%q) refusal must name action=overwrite, got %q", rel, err)
		}

		// The file must be untouched.
		data, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if rerr != nil {
			t.Fatalf("re-read %s: %v", rel, rerr)
		}
		if string(data) == "clobbered" {
			t.Errorf("Write(%q) modified the file despite the refusal", rel)
		}
	}
}

func TestEditRefusesTaskPaths(t *testing.T) {
	root := taskGateVault(t)
	for _, rel := range []string{
		"Projects/p/tasks/a-task.md",
		"Projects/p/tasks/done/a-task.md",
		"Projects/p/tasks/cancelled/a-task.md",
	} {
		const body = "# A Task\n\n**Status:** pending\n**Priority:** medium\n"
		seed(t, root, rel, body)

		_, err := Edit(root, rel, "**Status:** pending", "**Status:** in_progress", false, "")
		if err == nil {
			t.Fatalf("Edit(%q) must be refused", rel)
		}
		if !errors.Is(err, ErrRefusedPath) {
			t.Errorf("Edit(%q): want ErrRefusedPath, got %v", rel, err)
		}
		if !strings.Contains(err.Error(), "vp_manage_task") {
			t.Errorf("Edit(%q) refusal must point at vp_manage_task, got %q", rel, err)
		}

		data, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if rerr != nil {
			t.Fatalf("re-read %s: %v", rel, rerr)
		}
		if string(data) != body {
			t.Errorf("Edit(%q) modified the file despite the refusal", rel)
		}
	}
}

// TestNonTaskPathsStillWriteAndEdit is the POSITIVE CONTROL. A gate that
// refused everything would pass every test above while breaking the vault.
func TestNonTaskPathsStillWriteAndEdit(t *testing.T) {
	root := taskGateVault(t)

	for _, rel := range []string{
		"Projects/p/resume.md",
		"Projects/p/notes/tasks.md", // "tasks" as a FILE name, not the segment
		"Knowledge/learnings/a.md",
	} {
		if _, err := Write(root, rel, "hello\n", ""); err != nil {
			t.Fatalf("Write(%q) must still succeed: %v", rel, err)
		}
		res, err := Edit(root, rel, "hello", "goodbye", false, "")
		if err != nil {
			t.Fatalf("Edit(%q) must still succeed: %v", rel, err)
		}
		if res.Replacements != 1 {
			t.Errorf("Edit(%q) replacements = %d, want 1", rel, res.Replacements)
		}
		data, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if rerr != nil {
			t.Fatalf("re-read %s: %v", rel, rerr)
		}
		if !strings.Contains(string(data), "goodbye") {
			t.Errorf("Edit(%q) did not land: %q", rel, data)
		}
	}
}
