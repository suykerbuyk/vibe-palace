// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// The untracked palace/<p>/.surface sweep (sweepableUntrackedPalaceStamp,
// task untracked-project-stamps-from-writers-that-never-commit, F1). One
// untracked .surface sweeps — the palace stamp of a project the vault has
// committed, holding exactly the bytes surface.WriteStamp writes — and every
// near miss is still reported, the H2 rule it narrows.

const palaceStamp = "palace/p/.surface"

// committedProjectVault is a git vault in which project p is initialised and
// Projects/p/.surface is COMMITTED: the state the sweep requires.
func committedProjectVault(t *testing.T) string {
	t.Helper()
	dir := initTestRepo(t)
	testutil.InitProject(t, dir, "p")
	writeFile(t, dir, "Projects/p/.surface", "surface = 1\n")
	gitRun(t, dir, "add", "Projects/p")
	gitRun(t, dir, "commit", "-m", "init p")
	return dir
}

// classifyUntracked classifies one untracked path and reports whether it was
// swept and whether it was reported.
func classifyUntracked(t *testing.T, dir, rel string) (swept, reported bool) {
	t.Helper()
	s, r, d := classifyDirty(dir, []PorcelainEntry{{Status: "??", Path: rel}}, false)
	if hasPath(d, rel) {
		t.Fatalf("%q deferred, want swept or reported (swept=%v reported=%v)", rel, s, r)
	}
	return hasPath(s, rel), hasPath(r, rel)
}

func TestClassifyDirty_UntrackedPalaceStampOfCommittedProjectSwept(t *testing.T) {
	dir := committedProjectVault(t)
	for _, stamp := range []string{"surface = 1\n", "surface = 42\n"} {
		writeFile(t, dir, palaceStamp, stamp)
		swept, reported := classifyUntracked(t, dir, palaceStamp)
		if !swept || reported {
			t.Errorf("stamp %q: swept=%v reported=%v, want swept and not reported", stamp, swept, reported)
		}
	}
}

// TestTidyVault_CommitsUntrackedPalaceStamp runs the sweep end to end: the
// stamp is in Swept, not Reported, and the tidy commit leaves it tracked.
func TestTidyVault_CommitsUntrackedPalaceStamp(t *testing.T) {
	dir := committedProjectVault(t)
	writeFile(t, dir, palaceStamp, "surface = 1\n")

	res, err := TidyVault(dir, false)
	if err != nil {
		t.Fatalf("TidyVault: %v", err)
	}
	if !res.Committed {
		t.Fatalf("expected a commit, got %+v", res)
	}
	if !hasPath(res.Swept, palaceStamp) || hasPath(res.Reported, palaceStamp) {
		t.Errorf("swept=%v reported=%v, want %q swept only", res.Swept, res.Reported, palaceStamp)
	}
	tracked, err := GitPathIsTracked(dir, palaceStamp)
	if err != nil || !tracked {
		t.Errorf("%q tracked = %v (err %v) after the tidy commit, want tracked", palaceStamp, tracked, err)
	}
	if status := gitRun(t, dir, "status", "--porcelain", "-uall"); strings.Contains(status, palaceStamp) {
		t.Errorf("%q still dirty after tidy:\n%s", palaceStamp, status)
	}
}

// TestClassifyDirty_UntrackedPalaceStampNearMissesReported pins each condition
// of the sweep: a vault that fails any one of them reports the stamp.
func TestClassifyDirty_UntrackedPalaceStampNearMissesReported(t *testing.T) {
	// Bytes that are not exactly what surface.WriteStamp writes.
	for _, c := range []struct{ name, body string }{
		{"empty file", ""},
		{"foreign key", "other = 1\n"},
		{"extra key", "surface = 1\nx = 2\n"},
		{"zero", "surface = 0\n"},
		{"leading zero", "surface = 01\n"},
		{"no trailing newline", "surface = 1"},
		{"non-digit", "surface = 1a\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := committedProjectVault(t)
			writeFile(t, dir, palaceStamp, c.body)
			if swept, reported := classifyUntracked(t, dir, palaceStamp); swept || !reported {
				t.Errorf("body %q: swept=%v reported=%v, want reported", c.body, swept, reported)
			}
		})
	}

	// A symlink is not the regular file the stamp writer leaves, even when
	// its target holds a valid stamp.
	t.Run("symlink to a valid stamp", func(t *testing.T) {
		dir := committedProjectVault(t)
		writeFile(t, dir, "palace/p/real-stamp", "surface = 1\n")
		if err := os.Symlink("real-stamp", filepath.Join(dir, "palace", "p", ".surface")); err != nil {
			t.Skipf("symlink: %v", err)
		}
		if swept, reported := classifyUntracked(t, dir, palaceStamp); swept || !reported {
			t.Errorf("symlinked stamp: swept=%v reported=%v, want reported", swept, reported)
		}
	})

	// Projects/p is initialised but its own stamp was never committed: the
	// project is not committed, so its palace stamp is not swept either. The
	// untracked Projects/p/.surface itself stays reported (the H2 rule).
	t.Run("project stamp untracked", func(t *testing.T) {
		dir := initTestRepo(t)
		testutil.InitProject(t, dir, "p")
		writeFile(t, dir, "Projects/p/.surface", "surface = 1\n")
		writeFile(t, dir, palaceStamp, "surface = 1\n")
		if swept, reported := classifyUntracked(t, dir, palaceStamp); swept || !reported {
			t.Errorf("%q: swept=%v reported=%v, want reported", palaceStamp, swept, reported)
		}
		if swept, reported := classifyUntracked(t, dir, "Projects/p/.surface"); swept || !reported {
			t.Errorf("untracked Projects/p/.surface: swept=%v reported=%v, want reported", swept, reported)
		}
	})

	// A stray: palace/q/ of a project the vault never initialised. p is
	// committed, so only q's own state can decide.
	t.Run("stray of an uninitialised project", func(t *testing.T) {
		dir := committedProjectVault(t)
		writeFile(t, dir, "palace/q/.surface", "surface = 1\n")
		if swept, reported := classifyUntracked(t, dir, "palace/q/.surface"); swept || !reported {
			t.Errorf("stray palace/q/.surface: swept=%v reported=%v, want reported", swept, reported)
		}
		if _, err := os.Stat(filepath.Join(dir, "Projects", "q")); !os.IsNotExist(err) {
			t.Errorf("classification created Projects/q (stat err %v)", err)
		}
	})

	// Projects/q/.surface IS tracked, but it is all Projects/q holds — a
	// committed stray stamp, no scaffold, no content. Only the initialised
	// check can decide this one.
	t.Run("tracked stamp of an uninitialised project", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, dir, "Projects/q/.surface", "surface = 1\n")
		gitRun(t, dir, "add", "Projects/q/.surface")
		gitRun(t, dir, "commit", "-m", "stray q stamp")
		writeFile(t, dir, "palace/q/.surface", "surface = 1\n")
		if swept, reported := classifyUntracked(t, dir, "palace/q/.surface"); swept || !reported {
			t.Errorf("palace/q/.surface of uninitialised q: swept=%v reported=%v, want reported", swept, reported)
		}
	})

	// Departed: q left this vault (a departure record) and Projects/q is gone.
	t.Run("departed project", func(t *testing.T) {
		dir := committedProjectVault(t)
		writeFile(t, dir, departure.RelPath("q"), `{"format":"1","slug":"q","kind":"deleted","to":"","date":"2026-09-27"}`+"\n")
		writeFile(t, dir, "palace/q/.surface", "surface = 1\n")
		if swept, reported := classifyUntracked(t, dir, "palace/q/.surface"); swept || !reported {
			t.Errorf("departed palace/q/.surface: swept=%v reported=%v, want reported", swept, reported)
		}
	})

	// The departure record wins even over a project that is otherwise fully
	// committed: this is the only shape in which the record is the deciding
	// condition (with Projects/<p> gone, the initialised check decides first).
	t.Run("departure record over a committed project", func(t *testing.T) {
		dir := committedProjectVault(t)
		writeFile(t, dir, departure.RelPath("p"), `{"format":"1","slug":"p","kind":"deleted","to":"","date":"2026-09-27"}`+"\n")
		writeFile(t, dir, palaceStamp, "surface = 1\n")
		if swept, reported := classifyUntracked(t, dir, palaceStamp); swept || !reported {
			t.Errorf("%q with a departure record: swept=%v reported=%v, want reported", palaceStamp, swept, reported)
		}
	})
}
