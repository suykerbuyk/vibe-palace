// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

func failingPreCommit(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell hooks need a POSIX shell")
	}
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho refused >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestCommitRemovals_CommitsOnlyThePaths: the commit carries exactly the
// removed paths; another file's staged change and another removal stay as they
// were, and nothing is pushed.
func TestCommitRemovals_CommitsOnlyThePaths(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mine\n", "T/other.md": "o\n", "U/x.md": "x\n"})
	head := gitRun(t, dir, "rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(dir, "T", "wrap.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "T", "other.md")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "U/x.md", "staged edit\n")
	gitRun(t, dir, "add", "U/x.md")

	res, err := CommitRemovals(dir, "remove wrap", []string{"T/wrap.md"})
	if err != nil {
		t.Fatalf("CommitRemovals: %v", err)
	}
	if res.CommitSHA == "" {
		t.Fatal("no commit")
	}
	if parent := gitRun(t, dir, "rev-parse", "HEAD~1"); parent != head {
		t.Errorf("not one commit on top of %s", head)
	}
	if names := gitRun(t, dir, "show", "--name-status", "--format=", "HEAD"); names != "D\tT/wrap.md" {
		t.Errorf("commit carries %q, want only the removal of T/wrap.md", names)
	}
	msg := gitRun(t, dir, "log", "-1", "--format=%B")
	if !strings.HasPrefix(msg, "remove wrap") || !strings.Contains(msg, "[") {
		t.Errorf("message = %q, want the caller's message plus the host stamp", msg)
	}
	if staged := gitRun(t, dir, "diff", "--cached", "--name-only"); staged != "U/x.md" {
		t.Errorf("staged set = %q, want only the other file's staged change", staged)
	}
	if unstaged := gitRun(t, dir, "diff", "--name-only"); unstaged != "T/other.md" {
		t.Errorf("unstaged set = %q, want the other removal left alone", unstaged)
	}
}

// TestCommitRemovals_FailureUnstagesExactlyThePaths is H1: a refused commit
// leaves the removal unstaged (" D"), and someone else's staged file staged.
func TestCommitRemovals_FailureUnstagesExactlyThePaths(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mine\n", "U/x.md": "x\n"})
	head := gitRun(t, dir, "rev-parse", "HEAD")
	failingPreCommit(t, dir)
	if err := os.Remove(filepath.Join(dir, "T", "wrap.md")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "U/x.md", "staged edit\n")
	gitRun(t, dir, "add", "U/x.md")

	if _, err := CommitRemovals(dir, "remove wrap", []string{"T/wrap.md"}); err == nil {
		t.Fatal("no error from a refused commit")
	}
	if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
	if staged := gitRun(t, dir, "diff", "--cached", "--name-only", "--", "T/wrap.md"); staged != "" {
		t.Errorf("the removal is still staged: %q", staged)
	}
	if st := gitRun(t, dir, "status", "--porcelain", "--", "T/wrap.md"); st != "D T/wrap.md" {
		t.Errorf("status = %q, want an unstaged deletion", st)
	}
	if staged := gitRun(t, dir, "diff", "--cached", "--name-only"); staged != "U/x.md" {
		t.Errorf("someone else's staged change was disturbed: %q", staged)
	}
}

// TestCommitRemovals_UnstageRunsUnderTheCommitLock is N1: the unstage after a
// failed commit runs inside the vault commit lock, where no other vp committer
// can race it — and when the unstage itself fails, the error says so and names
// the manual command instead of leaving the removal silently staged.
func TestCommitRemovals_UnstageRunsUnderTheCommitLock(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mine\n"})
	failingPreCommit(t, dir)
	if err := os.Remove(filepath.Join(dir, "T", "wrap.md")); err != nil {
		t.Fatal(err)
	}
	var lockHeld bool
	indexLock := filepath.Join(dir, ".git", "index.lock")
	old := commitRemovalsBeforeUnstageHook
	commitRemovalsBeforeUnstageHook = func() {
		release, ok, err := vaultlock.TryAcquire(dir, dir)
		if err != nil {
			t.Error(err)
			return
		}
		lockHeld = !ok
		if ok {
			_ = release()
		}
		// Another git process holds the index: the unstage must fail loudly.
		if err := os.WriteFile(indexLock, nil, 0o644); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { commitRemovalsBeforeUnstageHook = old })

	_, err := CommitRemovals(dir, "remove wrap", []string{"T/wrap.md"})
	if err == nil {
		t.Fatal("no error from a refused commit")
	}
	if !lockHeld {
		t.Error("the unstage ran without the vault commit lock held")
	}
	for _, want := range []string{"unstaging the removal failed", "git -C " + dir + " reset -q -- T/wrap.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if err := os.Remove(indexLock); err != nil {
		t.Fatal(err)
	}
}

// TestCommitRemovals_SkipsPathsGitNoLongerTracks: a path someone else already
// committed the removal of is dropped; with none left the call is a no-op.
func TestCommitRemovals_SkipsPathsGitNoLongerTracks(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mine\n"})
	gitRun(t, dir, "rm", "-q", "T/wrap.md")
	gitRun(t, dir, "commit", "-qm", "someone else removed it")
	head := gitRun(t, dir, "rev-parse", "HEAD")
	res, err := CommitRemovals(dir, "remove wrap", []string{"T/wrap.md"})
	if err != nil {
		t.Fatalf("CommitRemovals: %v", err)
	}
	if res.CommitSHA != "" || len(res.SkippedPaths) != 1 {
		t.Errorf("res = %+v, want a no-op with the path skipped", res)
	}
	if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
		t.Error("a commit was made")
	}
	if _, err := CommitRemovals(dir, "x", nil); err == nil {
		t.Error("no error for an empty path list")
	}
}

// TestCommitRemovals_NothingStagedIsANoOp: a tracked path that is still present
// and unchanged stages nothing, so no commit is made.
func TestCommitRemovals_NothingStagedIsANoOp(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mine\n"})
	head := gitRun(t, dir, "rev-parse", "HEAD")
	res, err := CommitRemovals(dir, "remove wrap", []string{"T/wrap.md"})
	if err != nil || res.CommitSHA != "" {
		t.Fatalf("res=%+v err=%v, want a no-op", res, err)
	}
	if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
		t.Error("a commit was made")
	}
}

// TestCommitRemovals_ReportsPathStillInHEAD: a path the commit did not remove
// is an error naming the manual commit. A file still present in the worktree,
// passed alongside a removed one, is the timeable way to leave one in HEAD.
func TestCommitRemovals_ReportsPathStillInHEAD(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mine\n", "T/keep.md": "k\n"})
	if err := os.Remove(filepath.Join(dir, "T", "wrap.md")); err != nil {
		t.Fatal(err)
	}
	res, err := CommitRemovals(dir, "remove", []string{"T/wrap.md", "T/keep.md"})
	if err == nil {
		t.Fatal("no error although T/keep.md is still in HEAD")
	}
	if res == nil || res.CommitSHA == "" {
		t.Errorf("res = %+v, want the commit that did land", res)
	}
	var left *RemovalsLeftInHEADError
	if !errors.As(err, &left) || left.SHA != res.CommitSHA || strings.Join(left.Paths, ",") != "T/keep.md" {
		t.Fatalf("err = %#v, want a *RemovalsLeftInHEADError naming only T/keep.md and the landed commit", err)
	}
	if !strings.Contains(err.Error(), "does not remove T/keep.md") {
		t.Errorf("error text: %v", err)
	}
	if strings.Contains(err.Error(), "T/wrap.md") {
		t.Errorf("the committed removal is reported as missing: %v", err)
	}
}

func TestCommitRemovals_NoIdentity(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mine\n"})
	isolateIdentity(t, dir)
	if err := os.Remove(filepath.Join(dir, "T", "wrap.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := CommitRemovals(dir, "remove", []string{"T/wrap.md"}); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("err = %v, want an identity error", err)
	}
	if staged := gitRun(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("something was staged without an identity: %q", staged)
	}
}

func TestCheckCommitIdentity(t *testing.T) {
	dir := committedRepo(t, map[string]string{"a.md": "a\n"})
	if err := CheckCommitIdentity(dir); err != nil {
		t.Fatalf("configured identity: %v", err)
	}
	isolateIdentity(t, dir)
	if err := CheckCommitIdentity(dir); err == nil || !strings.Contains(err.Error(), "git config") {
		t.Errorf("no identity: err = %v, want the fix named", err)
	}
}

func TestStagedChange(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/clean.md": "c\n", "T/mod.md": "m\n", "T/del.md": "d\n"})
	writeFile(t, dir, "T/mod.md", "modified\n")
	gitRun(t, dir, "add", "T/mod.md")
	writeFile(t, dir, "T/new.md", "new\n")
	gitRun(t, dir, "add", "T/new.md")
	gitRun(t, dir, "rm", "-q", "T/del.md")
	writeFile(t, dir, "T/untracked.md", "u\n")
	// An unstaged worktree deletion is not a staged change.
	if err := os.Remove(filepath.Join(dir, "T", "clean.md")); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]bool{
		"T/clean.md":     false,
		"T/mod.md":       true,
		"T/new.md":       true,
		"T/del.md":       true,
		"T/untracked.md": false,
		"T/absent.md":    false,
	} {
		got, err := StagedChange(dir, rel)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if got != want {
			t.Errorf("StagedChange(%s) = %v, want %v", rel, got, want)
		}
	}
	// A git error is an error, never "not staged".
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("not an index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := StagedChange(dir, "T/mod.md"); err == nil {
		t.Error("a corrupt index read as not staged")
	}
}

func TestGitTopLevelAndPathIgnored(t *testing.T) {
	dir := committedRepo(t, map[string]string{".gitignore": "*.bak\n"})
	sub := filepath.Join(dir, "notes", "vault")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	top, err := GitTopLevel(sub)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got, _ := filepath.EvalSymlinks(top); got != want {
		t.Errorf("GitTopLevel = %q, want %q", top, dir)
	}
	if ign, err := GitPathIgnored(dir, "T/wrap.md.0123456789ab.bak"); err != nil || !ign {
		t.Errorf("a .bak: ignored=%v err=%v", ign, err)
	}
	if ign, err := GitPathIgnored(dir, "T/wrap.md"); err != nil || ign {
		t.Errorf("a .md: ignored=%v err=%v", ign, err)
	}
	if _, err := GitPathIgnored(t.TempDir(), "x.bak"); err == nil {
		t.Error("outside a repository: no error")
	}
}

// TestCommitRemovals_CommitsAStagedDeletion is review L2's storage half: a
// path already staged for deletion (`git rm`, or an earlier call whose own
// unstage failed) cannot be `git add`ed, but holds no bytes, so it is
// committed as it stands — and on a refused commit it is unstaged like any
// other, leaving " D".
func TestCommitRemovals_CommitsAStagedDeletion(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/wrap.md": "mine\n", "T/other.md": "o\n"})
	gitRun(t, dir, "rm", "-q", "T/wrap.md")
	if err := os.Remove(filepath.Join(dir, "T", "other.md")); err != nil {
		t.Fatal(err)
	}
	res, err := CommitRemovals(dir, "remove", []string{"T/wrap.md", "T/other.md"})
	if err != nil || res.CommitSHA == "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if names := gitRun(t, dir, "show", "--name-status", "--format=", "HEAD"); names != "D\tT/other.md\nD\tT/wrap.md" {
		t.Errorf("commit carries %q", names)
	}
	if len(res.SkippedPaths) != 0 {
		t.Errorf("a staged deletion was reported skipped: %v", res.SkippedPaths)
	}

	dir = committedRepo(t, map[string]string{"T/wrap.md": "mine\n"})
	failingPreCommit(t, dir)
	gitRun(t, dir, "rm", "-q", "T/wrap.md")
	if _, err := CommitRemovals(dir, "remove", []string{"T/wrap.md"}); err == nil {
		t.Fatal("no error from a refused commit")
	}
	if st := gitRun(t, dir, "status", "--porcelain", "--", "T/wrap.md"); st != "D T/wrap.md" {
		t.Errorf("status = %q, want the staged deletion left unstaged", st)
	}
}

func TestStagedDeletion(t *testing.T) {
	dir := committedRepo(t, map[string]string{"T/a.md": "a\n", "T/b.md": "b\n", "T/c.md": "c\n"})
	gitRun(t, dir, "rm", "-q", "T/a.md")
	if err := os.Remove(filepath.Join(dir, "T", "b.md")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "T/c.md", "edited\n")
	gitRun(t, dir, "add", "T/c.md")
	for rel, want := range map[string]bool{"T/a.md": true, "T/b.md": false, "T/c.md": false, "T/none.md": false} {
		got, err := StagedDeletion(dir, rel)
		if err != nil || got != want {
			t.Errorf("StagedDeletion(%s) = %v, %v; want %v", rel, got, err, want)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("not an index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := StagedDeletion(dir, "T/a.md"); err == nil {
		t.Error("a corrupt index read as no staged deletion")
	}
}
