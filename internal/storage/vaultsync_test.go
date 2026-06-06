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

func TestCommitAndPushPaths_EmptyPaths(t *testing.T) {
	dir := initTestRepo(t)
	if _, err := CommitAndPushPaths(dir, "msg", nil, false); err == nil {
		t.Fatal("expected error for empty paths")
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
