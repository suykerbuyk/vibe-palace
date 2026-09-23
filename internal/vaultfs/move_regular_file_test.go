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

// Move renames ONE regular file. Before moveSourceLeaf, it Stat-ed the fully
// resolved source, so a directory moved as a tree and a symlink moved its
// target. Each test below renamed something on the unfixed code.

func mustExist(t *testing.T, root, rel string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Errorf("%s must still exist: %v", rel, err)
	}
}

func mustBeAbsent(t *testing.T, root, rel string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
		t.Errorf("%s must not exist", rel)
	}
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// A whole project directory: the rename that left palace/a/ behind and the
// embed cache to be reaped.
func TestMove_RefusesDirectorySource(t *testing.T) {
	root := taskGateVault(t)
	seed(t, root, "Projects/a/sessions/s.md", "s\n")
	seed(t, root, "Projects/a/resume.md", "r\n")

	_, err := Move(root, "Projects/a", "Projects/b")
	if !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("want ErrNotRegularFile, got %v", err)
	}
	for _, want := range []string{"directory", "one at a time", "vp_manage_task action=move", "vp migrate project-slug"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must say %q, got %q", want, err)
		}
	}
	mustExist(t, root, "Projects/a/sessions/s.md")
	mustExist(t, root, "Projects/a/resume.md")
	mustBeAbsent(t, root, "Projects/b")
}

// Projects/a/tasks has three segments, one short of IsTaskFilePath's four, so
// the task-file gate never matched it and every task moved out of the regime.
func TestMove_RefusesTasksDirectorySource(t *testing.T) {
	root := taskGateVault(t)
	seed(t, root, "Projects/a/tasks/t.md", "# T\n\n**Status:** pending\n")
	seed(t, root, "Projects/a/tasks/done/d.md", "# D\n\n**Status:** done\n")

	if _, err := Move(root, "Projects/a/tasks", "Projects/a/moved"); !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("want ErrNotRegularFile, got %v", err)
	}
	mustExist(t, root, "Projects/a/tasks/t.md")
	mustExist(t, root, "Projects/a/tasks/done/d.md")
	mustBeAbsent(t, root, "Projects/a/moved")
}

// An in-vault symlink: the unfixed Move renamed its TARGET and left the link
// dangling.
func TestMove_RefusesInVaultSymlinkSource(t *testing.T) {
	root := taskGateVault(t)
	seed(t, root, "real.md", "real\n")
	symlinkOrSkip(t, "real.md", filepath.Join(root, "link.md"))

	_, err := Move(root, "link.md", "moved.md")
	if !errors.Is(err, ErrNotRegularFile) {
		t.Fatalf("want ErrNotRegularFile, got %v", err)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("refusal must say symlink, got %q", err)
	}
	mustExist(t, root, "real.md")
	mustExist(t, root, "link.md")
	mustBeAbsent(t, root, "moved.md")
}

// A task file reached through a symlinked DIRECTORY: the leaf is a regular
// file, but the path the parent resolves to is a task path the lexical gate
// never saw.
func TestMove_RefusesTaskFileThroughSymlinkedDir(t *testing.T) {
	root := taskGateVault(t)
	seed(t, root, "Projects/a/tasks/t.md", "# T\n\n**Status:** pending\n")
	symlinkOrSkip(t, filepath.Join(root, "Projects", "a", "tasks"), filepath.Join(root, "notes"))

	_, err := Move(root, "notes/t.md", "x.md")
	if !errors.Is(err, ErrRefusedPath) {
		t.Fatalf("want the task-path refusal (ErrRefusedPath), got %v", err)
	}
	if !strings.Contains(err.Error(), "vp_manage_task") {
		t.Errorf("refusal must name vp_manage_task, got %q", err)
	}
	mustExist(t, root, "Projects/a/tasks/t.md")
	mustBeAbsent(t, root, "x.md")
}

// A dangling symlink at the destination: Stat reports it absent, and the
// rename would silently replace the link.
func TestMove_RefusesDanglingSymlinkDestination(t *testing.T) {
	root := taskGateVault(t)
	seed(t, root, "src.md", "src\n")
	symlinkOrSkip(t, filepath.Join(root, "nowhere.md"), filepath.Join(root, "dst.md"))

	if _, err := Move(root, "src.md", "dst.md"); err == nil {
		t.Fatal("a move onto a dangling symlink must be refused")
	}
	mustExist(t, root, "src.md")
	fi, err := os.Lstat(filepath.Join(root, "dst.md"))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the dangling link at dst.md must survive untouched (err=%v)", err)
	}
}
