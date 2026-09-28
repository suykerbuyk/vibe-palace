// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// The Undo lines a lifecycle command prints must run exactly as printed: from
// a vault whose path holds a space, with every editor set to `false`, so an
// unquoted path or a revert that opens an editor fails here.

// spaced moves the repository at dir under a parent directory whose name
// holds a space, and returns its new path.
func spaced(t *testing.T, dir string) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "my vaults")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(parent, "the vault")
	if err := os.Rename(dir, to); err != nil {
		t.Fatal(err)
	}
	return to
}

// runUndo runs each printed line through sh -c, as an operator pasting it —
// on a terminal: git revert opens an editor only when attached to one, so the
// line runs under script(1), which gives it a pseudo-terminal, with every
// editor set to `false`.
func runUndo(t *testing.T, lines []string) {
	t.Helper()
	if len(lines) == 0 {
		t.Fatal("no Undo lines were printed")
	}
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script(1) is needed to run the lines on a terminal")
	}
	for _, line := range lines {
		cmd := exec.Command("script", "-q", "-e", "-c", "sh -c "+shellQuote(line), "/dev/null")
		cmd.Env = append(os.Environ(), "GIT_EDITOR=false", "EDITOR=false", "VISUAL=false", "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("the printed Undo line failed as printed:\n  %s\n%v\n%s", line, err, out)
		}
	}
}

func TestDeleteUndoLinesRunAsPrinted(t *testing.T) {
	f := newDelFixture(t, "origin", "mirror")
	f.Dir = spaced(t, f.Dir)
	res, err := ApplyDelete(f.Dir, DeleteRequest{Projects: []string{"p"}, Discard: true})
	if err != nil {
		t.Fatal(err)
	}
	runUndo(t, res.Undo)
	if _, found := departure.Find(f.Dir, "p"); found {
		t.Fatal("the revert did not remove the record")
	}
	if _, err := os.Stat(filepath.Join(f.Dir, "Projects/p/resume.md")); err != nil {
		t.Fatalf("the revert did not restore p: %v", err)
	}
	head := f.head(t)
	for _, r := range f.Remotes {
		if got := gitRun(t, f.Bares[r], "rev-parse", "main"); got != head {
			t.Errorf("%s is at %s, not the revert %s", r, got, head)
		}
	}
}

func TestCopyUndoLinesRunAsPrinted(t *testing.T) {
	f := newCopyFix(t)
	f.V = spaced(t, f.V)
	res, err := ApplyCopy(f.req("p"))
	if err != nil {
		t.Fatal(err)
	}
	runUndo(t, res.Undo)
	if pathPresent(filepath.Join(f.V, "Projects/p")) {
		t.Fatal("the revert did not remove the copied project")
	}
	if head := gitRun(t, f.V, "rev-parse", "HEAD"); gitRun(t, f.VBare, "rev-parse", "main") != head {
		t.Fatal("the revert was not pushed")
	}
}

func TestInitUndoLinesRunAsPrinted(t *testing.T) {
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	parent := filepath.Join(t.TempDir(), "my vaults")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "quantum vault")
	rep, err := InitVault(context.Background(), initReq(path, origin, github))
	if err != nil {
		t.Fatal(err)
	}
	runUndo(t, rep.Undo)
	if pathPresent(path) {
		t.Fatal("rm -rf did not remove the new vault")
	}
	for _, l := range rep.Undo[1:] {
		if !strings.HasPrefix(l, "# ") {
			t.Errorf("a git-host step is not a comment: %q", l)
		}
	}
}

func TestCloneUndoLinesRunAsPrinted(t *testing.T) {
	f := newCloneFixture(t)
	f.target = filepath.Join(f.home, "my clone")
	pre := bindRead(t, f.cfg)
	rep, err := CloneVault(context.Background(), f.req("qa"))
	if err != nil {
		t.Fatal(err)
	}
	runUndo(t, rep.Undo)
	if pathPresent(f.target) {
		t.Fatal("rm -rf did not remove the clone")
	}
	if bindRead(t, f.cfg) != pre {
		t.Fatal("the config was not restored")
	}
}
