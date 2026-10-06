// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

const departedTo = "git@gitlab.example.com:q/vibe-palace-vault.git"

// departedFixture is a vault in which project p was deleted by the delete's
// own commit path (CommitSplitPurgeLocked: the record plus `git rm`, one
// commit). That commit is itself the first "not refused" case.
func departedFixture(t *testing.T) (dir string) {
	t.Helper()
	dir = initTestRepo(t)
	writeFile(t, dir, "Projects/p/resume.md", "p\n")
	writeFile(t, dir, "Projects/keep/resume.md", "k\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "projects")
	head := gitRun(t, dir, "rev-parse", "HEAD")
	rel, _, err := NewVault(dir).writeDeparture(nil, departure.Record{Slug: "p", Kind: departure.MovedToVault, To: departedTo}, false)
	if err != nil {
		t.Fatal(err)
	}
	held, err := vaultlock.AcquireHeld(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = CommitSplitPurgeLocked(held, SplitPurgeCommit{Slugs: []string{"p"}, Records: []string{rel}, Message: "vault project delete: p", ExpectHead: head})
	held.Release()
	if err != nil {
		t.Fatalf("not refused (1): the delete's own git rm commit was refused: %v", err)
	}
	if out := gitRun(t, dir, "ls-files", "Projects/p"); out != "" {
		t.Fatalf("fixture: Projects/p still tracked: %s", out)
	}
	return dir
}

// The commit backstop: a file that reached the departed tree without vaultfs
// (a raw editor write) is not committed, by a named path or by a directory
// pathspec that covers it; HEAD does not move.
func TestDepartedCommitBackstop(t *testing.T) {
	dir := departedFixture(t)
	head := gitRun(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, "Projects/p/stale-session.md", "written by a stale host\n")
	writeFile(t, dir, "Projects/keep/ok.md", "fine\n")
	for _, paths := range [][]string{{"Projects/p/stale-session.md"}, {"Projects"}, {"Projects/keep/ok.md", "Projects/p/stale-session.md"}} {
		_, err := CommitAndPushPaths(dir, "stale host session", paths, false)
		if !errors.Is(err, vaultfs.ErrDepartedProject) {
			t.Fatalf("paths %v: err = %v, want ErrDepartedProject", paths, err)
		}
		if gitRun(t, dir, "rev-parse", "HEAD") != head {
			t.Fatalf("paths %v: a commit was made", paths)
		}
		// A refused commit leaves nothing staged: a leftover index entry would
		// block the revert that restores the project, and ride the next commit.
		if staged := gitRun(t, dir, "diff", "--cached", "--name-only"); staged != "" {
			t.Fatalf("paths %v: the refusal left %q staged", paths, staged)
		}
	}
	// Another project commits as usual.
	if _, err := CommitAndPushPaths(dir, "keep", []string{"Projects/keep/ok.md"}, false); err != nil {
		t.Fatalf("an unrelated commit was refused: %v", err)
	}
}

// A deletion under a departed tree is not refused: removing a leftover mutates
// no project.
func TestDepartedCommitBackstop_DeletionPasses(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "Projects/p/resurrected.md", "r\n")
	writeFile(t, dir, departure.RelPath("p"), `{"format":"1","slug":"p","kind":"moved-to-vault","to":"`+departedTo+`","date":"2026-09-27"}`+"\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "a resurrected tree beside its record")
	if err := os.Remove(filepath.Join(dir, "Projects/p/resurrected.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitAndPushPaths(dir, "clean the resurrected tree out", []string{"Projects/p/resurrected.md"}, false); err != nil {
		t.Fatalf("a deletion under the departed tree was refused: %v", err)
	}
}

// Not refused (2): a revert of the delete removes the record in the same
// commit, so the project is live again and writes to it pass.
func TestDepartedWrite_RevertOfTheDeleteReopens(t *testing.T) {
	dir := departedFixture(t)
	gitRun(t, dir, "revert", "--no-edit", "HEAD")
	if _, err := os.Stat(filepath.Join(dir, departure.RelPath("p"))); !os.IsNotExist(err) {
		t.Fatal("fixture: the revert did not remove the record")
	}
	if _, err := vaultfs.Write(dir, "Projects/p/after-revert.md", "x\n", ""); err != nil {
		t.Fatalf("a write after the revert was refused: %v", err)
	}
	if _, err := CommitAndPushPaths(dir, "after revert", []string{"Projects/p/after-revert.md"}, false); err != nil {
		t.Fatalf("a commit after the revert was refused: %v", err)
	}
}

// Not refused (3): a copy into a vault with no record for p — even one that
// records another project's departure.
func TestDepartedWrite_CopyIntoAVaultWithNoRecordPasses(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, departure.RelPath("q"), `{"format":"1","slug":"q","kind":"deleted","to":"","date":"2026-09-27"}`+"\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "q was deleted")
	// p is initialised the way the copy itself initialises it; what is under
	// test is that q's record does not refuse p.
	testutil.InitProject(t, dir, "p")
	if _, err := vaultfs.Write(dir, "Projects/p/resume.md", "copied\n", ""); err != nil {
		t.Fatalf("a copy's write into a vault without p's record was refused: %v", err)
	}
	if _, err := CommitAndPushPaths(dir, "vault copy: p", []string{"Projects/p"}, false); err != nil {
		t.Fatalf("the copy's commit was refused: %v", err)
	}
}

// Against U6's REAL delete (ApplyDelete, which `vp vault project delete` and
// vp_vault_project_delete are thin over): a full --moved-to delete succeeds end
// to end under the departed-write guard — the record, the one published
// commit, and the leftovers removal. Afterwards every raw write into the
// departed tree refuses, and so does a commit of a raw file. A git revert of
// that delete re-opens writes.
func TestDepartedWrite_RealMovedToDeleteThenRefuseThenRevert(t *testing.T) {
	f := newDelFixture(t, "origin")
	f.copyToB(t)
	f.pushB(t)

	res, err := ApplyDelete(f.Dir, f.movedReq())
	if err != nil {
		t.Fatalf("the real delete failed under the guard: %v", err)
	}
	if res.Commit == "" || gitRun(t, f.Bares["origin"], "rev-parse", "main") != res.Commit {
		t.Fatalf("the delete commit %q is not published", res.Commit)
	}
	rec, found := departure.Read(f.Dir, "p")
	if !found || rec.Kind != departure.MovedToVault || rec.To != fileURL(f.BBare) {
		t.Fatalf("record = %+v, found %v", rec, found)
	}
	if left := f.leftoversPresent(t); len(left) != 0 {
		t.Fatalf("leftovers not removed: %v", left)
	}
	head := f.head(t)

	// Every raw write into the departed tree refuses.
	if _, err := vaultfs.Write(f.Dir, "Projects/p/resume.md", "stale\n", ""); !errors.Is(err, vaultfs.ErrDepartedProject) {
		t.Fatalf("Write: %v", err)
	}
	if _, err := vaultfs.Create(f.Dir, "palace/p/new.jsonl", "{}\n"); !errors.Is(err, vaultfs.ErrDepartedProject) {
		t.Fatalf("Create: %v", err)
	}
	writeFile(t, f.Dir, "Projects/p/raw.md", "old\n") // a raw editor write
	if _, err := vaultfs.Edit(f.Dir, "Projects/p/raw.md", "old", "new", false, ""); !errors.Is(err, vaultfs.ErrDepartedProject) {
		t.Fatalf("Edit: %v", err)
	}
	writeFile(t, f.Dir, "Projects/other/n.md", "n\n")
	if _, err := vaultfs.Move(f.Dir, "Projects/other/n.md", "Projects/p/n.md"); !errors.Is(err, vaultfs.ErrDepartedProject) {
		t.Fatalf("Move into: %v", err)
	}
	if _, err := vaultfs.Move(f.Dir, "Projects/p/raw.md", "Projects/other/raw.md"); !errors.Is(err, vaultfs.ErrDepartedProject) {
		t.Fatalf("Move out: %v", err)
	}
	if _, err := CommitAndPushPaths(f.Dir, "stale host", []string{"Projects/p/raw.md"}, false); !errors.Is(err, vaultfs.ErrDepartedProject) {
		t.Fatalf("commit of a raw file: %v", err)
	}
	if f.head(t) != head {
		t.Fatal("a refused write or commit moved HEAD")
	}
	if err := os.Remove(filepath.Join(f.Dir, "Projects/p/raw.md")); err != nil {
		t.Fatal(err)
	}

	// A git revert of that delete removes the record in the same commit, and
	// re-opens writes.
	gitRun(t, f.Dir, "revert", "--no-edit", res.Commit)
	if _, found := departure.Read(f.Dir, "p"); found {
		t.Fatal("the revert did not remove the record")
	}
	if _, err := vaultfs.Write(f.Dir, "Projects/p/after-revert.md", "live again\n", ""); err != nil {
		t.Fatalf("a write after the revert was refused: %v", err)
	}
	if _, err := CommitAndPushPaths(f.Dir, "after revert", []string{"Projects/p/after-revert.md"}, false); err != nil {
		t.Fatalf("a commit after the revert was refused: %v", err)
	}
}
