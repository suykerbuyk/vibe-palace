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

// assertNotMoved is the half of a move refusal that matters most: a refusal
// that has already renamed is worse than no gate at all.
func assertNotMoved(t *testing.T, root, from, to string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(from))); err != nil {
		t.Errorf("source %q must survive a refused move: %v", from, err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(to))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("destination %q must not exist after a refused move, stat err = %v", to, err)
	}
}

// TestMoveRefusesTaskPathSource covers the bypass: a task file relocated by the
// generic mover reaches its LOCATION without the writer that owns it. The
// archive subdirectories are asserted explicitly rather than trusted to
// IsTaskFilePath's own unit test — done/ and cancelled/ are the case the gate's
// doc comment calls the one a bypass is most tempting for.
func TestMoveRefusesTaskPathSource(t *testing.T) {
	root := taskGateVault(t)
	for _, tc := range []struct{ from, to string }{
		// Into the archive: this is how an active task lands in done/ still
		// declaring **Status:** pending.
		{"Projects/p/tasks/a-task.md", "Projects/p/tasks/done/a-task.md"},
		// Back out of the archive: resurrects a body past the archived guard.
		{"Projects/p/tasks/done/b-task.md", "Projects/p/tasks/b-task.md"},
		{"Projects/p/tasks/cancelled/c-task.md", "Projects/p/tasks/c-task.md"},
		// Out of the regime entirely: IsTaskFilePath stops matching at the
		// destination, so the file would become freely writable by Write.
		{"Projects/p/tasks/d-task.md", "Projects/p/notes/d-task.md"},
	} {
		seed(t, root, tc.from, "# A Task\n\n**Status:** pending\n**Priority:** medium\n")

		_, err := Move(root, tc.from, tc.to)
		if err == nil {
			t.Fatalf("Move(%q -> %q) must be refused", tc.from, tc.to)
		}
		if !errors.Is(err, ErrRefusedPath) {
			t.Errorf("Move(%q): want ErrRefusedPath, got %v", tc.from, err)
		}
		// The refusal must name the sanctioned route, not merely say no.
		if !strings.Contains(err.Error(), "vp_manage_task") {
			t.Errorf("Move(%q) refusal must point at vp_manage_task, got %q", tc.from, err)
		}
		if !strings.Contains(err.Error(), "retire / cancel") {
			t.Errorf("Move(%q) refusal must name retire / cancel as the archiving route, got %q", tc.from, err)
		}
		assertNotMoved(t, root, tc.from, tc.to)
	}
}

// TestMoveRefusesTaskPathDestination covers the other direction: an ordinary
// vault file smuggled INTO the task tree, where the task readers would then
// parse it as a task nothing typed ever created.
func TestMoveRefusesTaskPathDestination(t *testing.T) {
	root := taskGateVault(t)
	for _, tc := range []struct{ from, to string }{
		{"Projects/p/notes/not-a-task.md", "Projects/p/tasks/now-a-task.md"},
		{"Projects/p/notes/other.md", "Projects/p/tasks/done/now-archived.md"},
		{"Knowledge/learnings/a.md", "Projects/p/tasks/cancelled/now-cancelled.md"},
	} {
		seed(t, root, tc.from, "just a note\n")

		_, err := Move(root, tc.from, tc.to)
		if err == nil {
			t.Fatalf("Move(%q -> %q) must be refused on the destination", tc.from, tc.to)
		}
		if !errors.Is(err, ErrRefusedPath) {
			t.Errorf("Move(-> %q): want ErrRefusedPath, got %v", tc.to, err)
		}
		// The refusal names the offending endpoint, which is the destination
		// here — a message naming the source would send the reader to the
		// wrong path.
		if !strings.Contains(err.Error(), tc.to) {
			t.Errorf("Move(-> %q) refusal must name the destination, got %q", tc.to, err)
		}
		assertNotMoved(t, root, tc.from, tc.to)
	}
}

// TestNonTaskPathsStillMove is the POSITIVE CONTROL for the move gate. A gate
// that refused every move would pass both tests above while breaking
// `vp vault move` for the whole vault.
func TestNonTaskPathsStillMove(t *testing.T) {
	root := taskGateVault(t)
	for _, tc := range []struct{ from, to string }{
		{"Projects/p/notes/a.md", "Projects/p/notes/b.md"},
		// "tasks" as a FILE name rather than the path segment: not a task path.
		{"Projects/p/notes/tasks.md", "Projects/p/archive/tasks.md"},
		{"Knowledge/learnings/x.md", "Knowledge/learnings/y.md"},
	} {
		seed(t, root, tc.from, "movable\n")

		if _, err := Move(root, tc.from, tc.to); err != nil {
			t.Fatalf("Move(%q -> %q) must still succeed: %v", tc.from, tc.to, err)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(tc.from))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("source %q should be gone after a successful move, stat err = %v", tc.from, err)
		}
		data, rerr := os.ReadFile(filepath.Join(root, filepath.FromSlash(tc.to)))
		if rerr != nil {
			t.Fatalf("read moved file %s: %v", tc.to, rerr)
		}
		if string(data) != "movable\n" {
			t.Errorf("moved file %q = %q, want %q", tc.to, data, "movable\n")
		}
	}
}

// TestDeleteStillRemovesTaskFiles pins a DELIBERATE EXCLUSION, not an oversight.
//
// The iteration-375 finding, resume.md and the 375 session note all record that
// "Move and Delete are ungated by IsTaskFilePath" as though it were one hole. It
// is two findings and only Move is a defect. Delete is ungated on a committed
// decision whose rationale lives at internal/tools/vault_split_apply.go:753 —
// the reason task paths refuse a generic WRITER is that every field has exactly
// one typed writer, and removing the file entirely mutates no field.
//
// There is a concrete regression behind that argument: vaultSplitPurge walks
// every regular file through vaultfs.Delete (vault_split_apply.go:884) to
// inherit its .git refusal, containment, per-path lock and CAS. Gate Delete and
// a purge cannot remove task files, so it cannot complete — with no sanctioned
// alternative offered to the operator.
//
// If this test ever fails, someone has "completed" the 375 finding. Do not make
// it pass by deleting it: answer vaultSplitPurge first.
func TestDeleteStillRemovesTaskFiles(t *testing.T) {
	root := taskGateVault(t)
	for _, rel := range []string{
		"Projects/p/tasks/a-task.md",
		"Projects/p/tasks/done/b-task.md",
		"Projects/p/tasks/cancelled/c-task.md",
	} {
		seed(t, root, rel, "# A Task\n\n**Status:** pending\n**Priority:** medium\n")

		if _, err := Delete(root, rel, ""); err != nil {
			t.Fatalf("Delete(%q) must still succeed — see this test's comment before changing it: %v", rel, err)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Delete(%q) left the file behind, stat err = %v", rel, err)
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
