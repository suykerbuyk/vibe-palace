// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// U15 send-back. The record wins over the directory everywhere: every storage
// writer — reached here directly, below every tool and seam — refuses a
// departed project's trees, through the write funnel (atomicfile and the
// append writer), whatever Projects/<p>/ holds.

func departedWithStray(t *testing.T) *Vault {
	t.Helper()
	dir := departedFixture(t)
	// A stray file: under the old rule it made p live again.
	writeFile(t, dir, "Projects/p/stray.md", "a stray editor write\n")
	return NewVault(dir)
}

func TestEveryStorageWriterRefusesADepartedProject(t *testing.T) {
	writers := map[string]func(v *Vault) error{
		"session capture": func(v *Vault) error {
			_, err := v.WriteSession("p", SessionMeta{ID: "2026-09-27-abcd-01", Project: "p", Date: "2026-09-27"}, "stale\n")
			return err
		},
		"append iteration": func(v *Vault) error {
			_, _, err := v.AppendIterationOwned("p", "stale", "a stale iteration", nil)
			return err
		},
		"memory write": func(v *Vault) error {
			return v.WriteMemory("p", "stale.md", MemoryMeta{Name: "n", Description: "d", Type: "project"}, "b")
		},
		"KG": func(v *Vault) error {
			return v.AddTriple("p", Triple{Subject: "a", Predicate: "uses", Object: "b"})
		},
		"task move into it": func(v *Vault) error {
			writeFile(t, v.Root, "Projects/keep/tasks/movable.md", "# movable\n\n**Status:** planning\n**Priority:** low\n\n## Context\n\nx\n")
			return v.MoveTaskToProject("keep", "movable", "p")
		},
		"drawers": func(v *Vault) error {
			_, err := v.AppendDrawers("p", "p", "general", []Drawer{{ID: "d1", Hall: "h", Content: "c", SourceType: "session"}})
			return err
		},
	}
	for name, write := range writers {
		for _, stray := range []bool{false, true} {
			var v *Vault
			if stray {
				v = departedWithStray(t)
			} else {
				v = NewVault(departedFixture(t))
			}
			err := write(v)
			if !errors.Is(err, vaultfs.ErrDepartedProject) {
				t.Errorf("%s (stray file %v): err = %v, want the departed refusal", name, stray, err)
			}
			var extra []string
			_ = filepath.WalkDir(filepath.Join(v.Root, "palace", "p"), func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					extra = append(extra, p)
				}
				return nil
			})
			if len(extra) > 0 {
				t.Errorf("%s (stray %v) wrote under palace/p: %v", name, stray, extra)
			}
		}
	}
}

// R2: tidy commits the rest, leaves a departed project's artifacts untouched
// and says so; an explicit commit of the departed path still refuses.
func TestTidySkipsADepartedProjectAndCommitsTheRest(t *testing.T) {
	dir := departedFixture(t)
	writeFile(t, dir, "Projects/keep/sessions/2026-09-27-aaaa-01.md", "keep's real session\n")
	writeFile(t, dir, "Projects/p/sessions/2026-09-27-bbbb-01.md", "a stale host's session\n")
	res, err := TidyVault(dir, false)
	if err != nil {
		t.Fatalf("tidy refused the whole batch: %v", err)
	}
	if !res.Committed || gitRun(t, dir, "ls-files", "Projects/keep/sessions") == "" {
		t.Fatalf("keep's session was not committed: %+v", res)
	}
	if gitRun(t, dir, "ls-files", "Projects/p") != "" {
		t.Fatal("the departed project's artifact was committed")
	}
	if len(res.LeftDeparted) != 1 || !strings.Contains(res.LeftDeparted[0], "Projects/p/sessions/2026-09-27-bbbb-01.md: left untouched: departed project p") {
		t.Fatalf("LeftDeparted = %q", res.LeftDeparted)
	}
	if _, err := CommitAndPushPaths(dir, "explicit", []string{"Projects/p/sessions/2026-09-27-bbbb-01.md"}, false); !errors.Is(err, vaultfs.ErrDepartedProject) {
		t.Fatalf("an explicit commit into the departed tree: err = %v", err)
	}
}

// R3: a symlink inside the vault is judged by where it lands — both a link
// inside Projects/ and a top-level link into Projects/p — in vaultfs and in the
// commit backstop.
func TestSymlinksIntoADepartedProjectAreRefused(t *testing.T) {
	dir := departedFixture(t)
	if err := os.MkdirAll(filepath.Join(dir, "Projects", "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	for link, rel := range map[string]string{"Projects/alias": "Projects/alias/x.md", "Notes-link": "Notes-link/y.md"} {
		if err := os.Symlink(filepath.Join(dir, "Projects", "p"), filepath.Join(dir, filepath.FromSlash(link))); err != nil {
			t.Fatal(err)
		}
		if _, err := vaultfs.Write(dir, rel, "through a link\n", ""); !errors.Is(err, vaultfs.ErrDepartedProject) {
			t.Errorf("vaultfs.Write %s: err = %v", rel, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "Projects", "p", filepath.Base(rel))); err == nil {
			t.Errorf("%s landed in Projects/p", rel)
		}
		// The commit backstop: committing the link itself refuses too.
		if _, err := CommitAndPushPaths(dir, "a link", []string{link}, false); !errors.Is(err, vaultfs.ErrDepartedProject) {
			t.Errorf("commit of the link %s: err = %v", link, err)
		}
	}
}

// A lexical trick is judged on the cleaned path.
func TestLexicalTricksIntoADepartedProjectAreRefused(t *testing.T) {
	dir := departedFixture(t)
	for _, rel := range []string{"./Projects/p/x.md", "Projects//p/x.md", "Projects/p/../p/x.md", "Projects/keep/../p/x.md", "palace/p/x.md", "projects/p/x.md"} {
		if _, err := vaultfs.Write(dir, rel, "x\n", ""); !errors.Is(err, vaultfs.ErrDepartedProject) {
			t.Errorf("%s: err = %v", rel, err)
		}
	}
}

// Not refused: U6's real delete; after a revert of it (which removes the
// record) writes and commits under p work again.
func TestDeleteThenRevertReopensWrites(t *testing.T) {
	f := newDelFixture(t, "origin")
	if _, err := ApplyDelete(f.Dir, DeleteRequest{Projects: []string{"p"}, Discard: true}); err != nil {
		t.Fatalf("the real delete was refused: %v", err)
	}
	if _, err := vaultfs.Write(f.Dir, "Projects/p/after.md", "x\n", ""); !errors.Is(err, vaultfs.ErrDepartedProject) {
		t.Fatalf("a write after the delete: err = %v", err)
	}
	gitRun(t, f.Dir, "revert", "--no-edit", "HEAD")
	if _, found := departure.Find(f.Dir, "p"); found {
		t.Fatal("the revert must remove the record")
	}
	if _, err := vaultfs.Write(f.Dir, "Projects/p/after-revert.md", "x\n", ""); err != nil {
		t.Fatalf("a write after the revert: %v", err)
	}
	if _, err := CommitAndPushPaths(f.Dir, "after the revert", []string{"Projects/p/after-revert.md"}, false); err != nil {
		t.Fatalf("a commit after the revert: %v", err)
	}
}
