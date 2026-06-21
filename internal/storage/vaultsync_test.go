// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRun runs a git command in dir, failing the test on error.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

// initTestRepo creates a git repo with a committer identity and a seed commit.
func initTestRepo(t *testing.T) string {
	t.Helper()
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "seed")
	return dir
}

// initBareRemote creates a bare repo usable as a push target.
func initBareRemote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "--bare", "-b", "main")
	return dir
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHasUncommittedChanges(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}

	// Not a git repo → false, no error.
	bare := t.TempDir()
	if dirty, err := HasUncommittedChanges(bare, "anything"); err != nil || dirty {
		t.Fatalf("non-repo should be clean: dirty=%v err=%v", dirty, err)
	}

	dir := initTestRepo(t)

	// Clean repo, watched path absent → false.
	if dirty, err := HasUncommittedChanges(dir, "memory"); err != nil || dirty {
		t.Fatalf("clean repo should report clean: dirty=%v err=%v", dirty, err)
	}

	// Untracked file under a watched DIR path → dirty (recursive).
	writeFile(t, dir, "memory/new.md", "hello\n")
	if dirty, err := HasUncommittedChanges(dir, "memory"); err != nil || !dirty {
		t.Fatalf("new file under dir should be dirty: dirty=%v err=%v", dirty, err)
	}

	// A change OUTSIDE the watched paths is invisible to the scoped check.
	writeFile(t, dir, "other.md", "elsewhere\n")
	if dirty, err := HasUncommittedChanges(dir, "memory/new.md"); err != nil || !dirty {
		t.Fatalf("watched file still dirty: dirty=%v err=%v", dirty, err)
	}
	gitRun(t, dir, "add", "memory/new.md")
	gitRun(t, dir, "commit", "-m", "add memory")
	if dirty, err := HasUncommittedChanges(dir, "memory"); err != nil || dirty {
		t.Fatalf("committed memory dir should be clean: dirty=%v err=%v", dirty, err)
	}
}

func TestCommitAndPushPaths_EmptyPaths(t *testing.T) {
	dir := initTestRepo(t)
	if _, err := CommitAndPushPaths(dir, "msg", nil, false); err == nil {
		t.Fatal("expected error for empty paths")
	}
}

func TestCommitAndPushPaths_SkipsNeverExistedPath(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "real.txt", "real")

	res, err := CommitAndPushPaths(dir, "mixed", []string{"real.txt", "ghost.txt"}, false)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if res.CommitSHA == "" {
		t.Error("expected a commit SHA for the existing path")
	}
	if len(res.SkippedPaths) != 1 || res.SkippedPaths[0] != "ghost.txt" {
		t.Errorf("expected SkippedPaths=[ghost.txt], got %#v", res.SkippedPaths)
	}
	// real.txt committed, clean working tree.
	if status := gitRun(t, dir, "status", "--porcelain", "real.txt"); status != "" {
		t.Errorf("real.txt still dirty: %q", status)
	}
}

func TestCommitAndPushPaths_DeletionIsStagedNotSkipped(t *testing.T) {
	dir := initTestRepo(t)
	// Track a file, then delete it from the worktree.
	writeFile(t, dir, "tracked.txt", "data")
	gitRun(t, dir, "add", "tracked.txt")
	gitRun(t, dir, "commit", "-m", "add tracked")
	if err := os.Remove(filepath.Join(dir, "tracked.txt")); err != nil {
		t.Fatal(err)
	}

	res, err := CommitAndPushPaths(dir, "remove tracked", []string{"tracked.txt"}, false)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(res.SkippedPaths) != 0 {
		t.Errorf("tracked-deleted path must NOT be skipped, got %#v", res.SkippedPaths)
	}
	if res.CommitSHA == "" {
		t.Error("expected a commit SHA for the staged deletion")
	}
	// The deletion must be committed: file no longer tracked.
	if tracked := gitRun(t, dir, "ls-files", "tracked.txt"); tracked != "" {
		t.Errorf("deletion not committed, still tracked: %q", tracked)
	}
}

func TestCommitAndPushPaths_MixedExistingDeletedAndGhost(t *testing.T) {
	dir := initTestRepo(t)
	// Tracked-then-deleted file.
	writeFile(t, dir, "gone.txt", "bye")
	gitRun(t, dir, "add", "gone.txt")
	gitRun(t, dir, "commit", "-m", "add gone")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	// Existing new file.
	writeFile(t, dir, "here.txt", "present")

	res, err := CommitAndPushPaths(dir, "mixed", []string{"here.txt", "gone.txt", "ghost.txt"}, false)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(res.SkippedPaths) != 1 || res.SkippedPaths[0] != "ghost.txt" {
		t.Errorf("expected only ghost.txt skipped, got %#v", res.SkippedPaths)
	}
	if res.CommitSHA == "" {
		t.Error("expected a commit SHA")
	}
	// here.txt added, gone.txt deletion committed.
	if tracked := gitRun(t, dir, "ls-files", "gone.txt"); tracked != "" {
		t.Errorf("gone.txt deletion not committed: %q", tracked)
	}
	if tracked := gitRun(t, dir, "ls-files", "here.txt"); tracked == "" {
		t.Error("here.txt should be committed/tracked")
	}
}

func TestCommitAndPushPaths_AllFilteredOutIsNoOp(t *testing.T) {
	dir := initTestRepo(t)
	res, err := CommitAndPushPaths(dir, "all ghosts", []string{"ghost1.txt", "ghost2.txt"}, false)
	if err != nil {
		t.Fatalf("expected benign no-op, got error: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil PushResult for no-op")
	}
	if res.CommitSHA != "" {
		t.Errorf("expected empty CommitSHA for no-op, got %q", res.CommitSHA)
	}
	if len(res.SkippedPaths) != 2 {
		t.Errorf("expected 2 skipped paths, got %#v", res.SkippedPaths)
	}
}

func TestCommitAndPushPaths_NeverWrittenMemoryDir(t *testing.T) {
	// Regression for the iter-134 wrap failure: an explicit paths list that
	// includes a never-written Projects/<slug>/memory dir must not fatal.
	dir := initTestRepo(t)
	writeFile(t, dir, "Projects/demo/resume.md", "resume\n")

	res, err := CommitAndPushPaths(dir, "wrap", []string{
		"Projects/demo/resume.md",
		"Projects/demo/memory",
	}, false)
	if err != nil {
		t.Fatalf("commit with absent memory dir must succeed, got: %v", err)
	}
	if res.CommitSHA == "" {
		t.Error("expected the resume.md commit to land")
	}
	if len(res.SkippedPaths) != 1 || res.SkippedPaths[0] != "Projects/demo/memory" {
		t.Errorf("expected memory dir skipped, got %#v", res.SkippedPaths)
	}
}

func TestCommitAndPushPaths_LocalOnlyNoRemote(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "a.txt", "alpha")

	res, err := CommitAndPushPaths(dir, "add a", []string{"a.txt"}, false)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if res.CommitSHA == "" {
		t.Error("expected a commit SHA")
	}
	if res.RemoteResults != nil {
		t.Error("expected nil RemoteResults for local-only commit")
	}
	// a.txt must be committed (clean working tree for it).
	if status := gitRun(t, dir, "status", "--porcelain", "a.txt"); status != "" {
		t.Errorf("a.txt still dirty: %q", status)
	}
}

func TestCommitAndPushPaths_SelectiveStaging(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "keep.txt", "keep")
	writeFile(t, dir, "leave.txt", "leave")

	if _, err := CommitAndPushPaths(dir, "only keep", []string{"keep.txt"}, false); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// keep.txt committed; leave.txt remains untracked/dirty.
	status := gitRun(t, dir, "status", "--porcelain")
	if strings.Contains(status, "keep.txt") {
		t.Errorf("keep.txt should be committed, status: %q", status)
	}
	if !strings.Contains(status, "leave.txt") {
		t.Errorf("leave.txt should remain dirty, status: %q", status)
	}
}

func TestCommitAndPushPaths_HostnameStamp(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "a.txt", "alpha")
	if _, err := CommitAndPushPaths(dir, "stamped", []string{"a.txt"}, false); err != nil {
		t.Fatalf("commit: %v", err)
	}
	body := gitRun(t, dir, "log", "-1", "--format=%B")
	if !strings.Contains(body, "stamped") {
		t.Errorf("commit body missing message: %q", body)
	}
	if !strings.Contains(body, "[") || !strings.Contains(body, "]") {
		t.Errorf("commit body missing hostname stamp: %q", body)
	}
}

func TestCommitAndPushPaths_NothingToCommit(t *testing.T) {
	dir := initTestRepo(t)
	// README.md is already committed and unchanged; staging it stages nothing.
	res, err := CommitAndPushPaths(dir, "noop", []string{"README.md"}, false)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if res.CommitSHA != "" {
		t.Errorf("expected empty CommitSHA for no-op, got %q", res.CommitSHA)
	}
}

func TestCommitAndPushPaths_PushNoRemote(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "a.txt", "alpha")
	if _, err := CommitAndPushPaths(dir, "msg", []string{"a.txt"}, true); err == nil {
		t.Fatal("expected error pushing with no remotes")
	}
}

func TestCommitAndPushPaths_PushSingleRemote(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")

	writeFile(t, dir, "a.txt", "alpha")
	res, err := CommitAndPushPaths(dir, "add a", []string{"a.txt"}, true)
	if err != nil {
		t.Fatalf("commit+push: %v", err)
	}
	if !res.AllPushed() {
		t.Errorf("expected all pushed, got %#v", res.RemoteResults)
	}
	// Verify the commit landed on the bare remote.
	if got := gitRun(t, bare, "log", "-1", "--format=%s"); !strings.Contains(got, "add a") {
		t.Errorf("bare remote missing commit, subject: %q", got)
	}
}

func TestCommitAndPushPaths_MultipleRemotes(t *testing.T) {
	dir := initTestRepo(t)
	bare1 := initBareRemote(t)
	bare2 := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "github", bare1)
	gitRun(t, dir, "remote", "add", "vault", bare2)
	gitRun(t, dir, "push", "github", "main")
	gitRun(t, dir, "push", "vault", "main")

	writeFile(t, dir, "a.txt", "alpha")
	res, err := CommitAndPushPaths(dir, "multi", []string{"a.txt"}, true)
	if err != nil {
		t.Fatalf("commit+push: %v", err)
	}
	if len(res.RemoteResults) != 2 {
		t.Errorf("got %d remote results, want 2", len(res.RemoteResults))
	}
	if !res.AllPushed() {
		t.Errorf("expected all pushed, got %#v", res.RemoteResults)
	}
}

func TestCommitAndPushPaths_PushRebasesOnNonFastForward(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")

	// Another clone advances the remote so our push is non-fast-forward.
	other := t.TempDir()
	gitRun(t, other, "clone", "-b", "main", bare, ".")
	gitRun(t, other, "config", "user.email", "other@example.com")
	gitRun(t, other, "config", "user.name", "Other")
	writeFile(t, other, "remote.txt", "from other")
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-m", "remote advance")
	gitRun(t, other, "push", "origin", "main")

	// Local commit a different file; push must fetch+rebase then succeed.
	writeFile(t, dir, "local.txt", "from local")
	res, err := CommitAndPushPaths(dir, "local change", []string{"local.txt"}, true)
	if err != nil {
		t.Fatalf("commit+push: %v", err)
	}
	if !res.AllPushed() {
		t.Errorf("expected all pushed after rebase, got %#v", res.RemoteResults)
	}
	// Both files must be present on the remote tip.
	tree := gitRun(t, bare, "ls-tree", "--name-only", "main")
	if !strings.Contains(tree, "local.txt") || !strings.Contains(tree, "remote.txt") {
		t.Errorf("remote tip missing converged files: %q", tree)
	}
}
