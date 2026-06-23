// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
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

// TestPushResult_Stranded covers the four-way truth table of Stranded():
// (a) commit + all-remotes-fail → true; (b) commit + ≥1 success → false;
// (c) no remotes / nil RemoteResults (downgrade) → false; (d) empty CommitSHA
// → false. These are pure literal-driven method tests — no git repo needed.
func TestPushResult_Stranded(t *testing.T) {
	failed := fmt.Errorf("push rejected")
	cases := []struct {
		name string
		res  PushResult
		want bool
	}{
		{
			name: "commit_all_remotes_fail",
			res:  PushResult{CommitSHA: "abc123", RemoteResults: map[string]error{"github": failed, "vault": failed}},
			want: true,
		},
		{
			name: "commit_one_remote_success",
			res:  PushResult{CommitSHA: "abc123", RemoteResults: map[string]error{"github": failed, "vault": nil}},
			want: false,
		},
		{
			name: "no_remotes_downgrade_nil_results",
			res:  PushResult{CommitSHA: "abc123", RemoteResults: nil},
			want: false,
		},
		{
			name: "empty_commit_sha",
			res:  PushResult{CommitSHA: "", RemoteResults: map[string]error{"github": failed}},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.res.Stranded(); got != tc.want {
				t.Errorf("Stranded() = %v, want %v (results=%#v)", got, tc.want, tc.res.RemoteResults)
			}
		})
	}
}

// TestCommitAndPushPaths_AutostashDirtyTreeRebases is the core Fix A guard: a
// dirty (uncommitted) edit to a tracked file no longer defeats the recovery
// rebase. Local commits file X while the working tree carries an unswept dirty
// edit to tracked file Y; the remote advanced a disjoint file Z. Before Fix A
// the bare `git rebase` refused to start on the dirty tree and the commit was
// stranded; with `--autostash` it shelves Y, rebases X onto Z, and re-applies Y.
func TestCommitAndPushPaths_AutostashDirtyTreeRebases(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")

	// Y: a tracked file that will be left dirty (unswept) in the working tree.
	writeFile(t, dir, "tracked-y.txt", "original Y\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "add tracked Y")
	gitRun(t, dir, "push", "origin", "main")

	// Remote advances a DISJOINT file Z so our later push is non-fast-forward.
	other := t.TempDir()
	gitRun(t, other, "clone", "-b", "main", bare, ".")
	gitRun(t, other, "config", "user.email", "other@example.com")
	gitRun(t, other, "config", "user.name", "Other")
	writeFile(t, other, "remote-z.txt", "from other")
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-m", "remote advance Z")
	gitRun(t, other, "push", "origin", "main")

	// Local: commit X, then DIRTY the tracked Y without staging it.
	writeFile(t, dir, "local-x.txt", "from local")
	writeFile(t, dir, "tracked-y.txt", "DIRTY uncommitted edit\n")

	res, err := CommitAndPushPaths(dir, "local change X", []string{"local-x.txt"}, true)
	if err != nil {
		t.Fatalf("commit+push: %v", err)
	}
	if !res.AnyPushed() {
		t.Errorf("expected push to succeed via autostash rebase, got %#v", res.RemoteResults)
	}
	if res.Stranded() {
		t.Errorf("commit must NOT be stranded after autostash rebase: %#v", res.RemoteResults)
	}
	if res.PopConflict {
		t.Errorf("disjoint dirty edit must not pop-conflict: paths=%v", res.PopConflictPaths)
	}
	// The remote tip must carry both X and the remote's Z.
	tree := gitRun(t, bare, "ls-tree", "--name-only", "main")
	if !strings.Contains(tree, "local-x.txt") || !strings.Contains(tree, "remote-z.txt") {
		t.Errorf("remote tip missing converged files: %q", tree)
	}
	// The dirty Y edit must be re-applied to the working tree (not lost).
	got, _ := os.ReadFile(filepath.Join(dir, "tracked-y.txt"))
	if !strings.Contains(string(got), "DIRTY uncommitted edit") {
		t.Errorf("autostash did not restore the dirty Y edit, got %q", got)
	}
}

// TestCommitAndPushPaths_AutostashPopConflict proves the restructured branch
// does NOT re-strand on an autostash re-apply conflict. Local commits file X
// while the working tree carries a dirty edit to tracked file Y on the SAME
// region the remote also changed in Y. The capture commit lands and pushes; the
// stash pop conflicts, so PopConflict/PopConflictPaths surface (commit is on the
// remote, edits preserved in the stash) — this is explicitly NOT a strand.
func TestCommitAndPushPaths_AutostashPopConflict(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")

	// Y: tracked, multi-line so we can target a specific conflicting line.
	writeFile(t, dir, "shared-y.txt", "line1\nline2\nline3\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "add shared Y")
	gitRun(t, dir, "push", "origin", "main")

	// Remote changes line1 of Y.
	other := t.TempDir()
	gitRun(t, other, "clone", "-b", "main", bare, ".")
	gitRun(t, other, "config", "user.email", "other@example.com")
	gitRun(t, other, "config", "user.name", "Other")
	writeFile(t, other, "shared-y.txt", "OTHER\nline2\nline3\n")
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-m", "remote change Y line1")
	gitRun(t, other, "push", "origin", "main")

	// Local: commit X, then dirty line1 of Y differently (uncommitted).
	writeFile(t, dir, "local-x.txt", "from local")
	writeFile(t, dir, "shared-y.txt", "LOCAL\nline2\nline3\n")

	res, err := CommitAndPushPaths(dir, "local change X", []string{"local-x.txt"}, true)
	if err != nil {
		t.Fatalf("commit+push: %v", err)
	}
	if !res.AnyPushed() {
		t.Errorf("capture commit must LAND and push despite pop conflict, got %#v", res.RemoteResults)
	}
	if res.Stranded() {
		t.Errorf("pop conflict is NOT a strand: %#v", res.RemoteResults)
	}
	if !res.PopConflict {
		t.Fatalf("expected PopConflict=true after autostash re-apply conflict")
	}
	foundY := false
	for _, p := range res.PopConflictPaths {
		if p == "shared-y.txt" {
			foundY = true
		}
	}
	if !foundY {
		t.Errorf("PopConflictPaths must name shared-y.txt, got %v", res.PopConflictPaths)
	}
	// Commit X reached the remote tip.
	tree := gitRun(t, bare, "ls-tree", "--name-only", "main")
	if !strings.Contains(tree, "local-x.txt") {
		t.Errorf("remote tip missing landed commit X: %q", tree)
	}
	// The operator's edits are preserved in the stash.
	stash := gitRun(t, dir, "stash", "list")
	if stash == "" {
		t.Errorf("autostash entry should be retained after pop conflict, stash list empty")
	}
}

// TestCommitAndPushPaths_TrueRebaseConflictStrands is the regression guard for
// the state-dir discriminator: when the capture commit ITSELF conflicts with the
// remote, the rebase truly fails (state dir present), the commit does NOT land,
// the rebase is aborted (no in-progress rebase left behind), and on a single
// remote this is a strand under Fix C.
func TestCommitAndPushPaths_TrueRebaseConflictStrands(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")

	// Y: tracked file both sides will edit on the same line.
	writeFile(t, dir, "conflict-y.txt", "line1\nline2\nline3\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "add conflict Y")
	gitRun(t, dir, "push", "origin", "main")

	// Remote changes line1 of Y.
	other := t.TempDir()
	gitRun(t, other, "clone", "-b", "main", bare, ".")
	gitRun(t, other, "config", "user.email", "other@example.com")
	gitRun(t, other, "config", "user.name", "Other")
	writeFile(t, other, "conflict-y.txt", "OTHER\nline2\nline3\n")
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-m", "remote change Y line1")
	gitRun(t, other, "push", "origin", "main")

	// Local: COMMIT a conflicting change to line1 of Y (the capture commit
	// itself conflicts) — not a dirty working-tree edit.
	writeFile(t, dir, "conflict-y.txt", "LOCAL\nline2\nline3\n")

	res, err := CommitAndPushPaths(dir, "local conflicting Y", []string{"conflict-y.txt"}, true)
	if err != nil {
		t.Fatalf("commit+push returned error (should surface per-remote, not fatal): %v", err)
	}
	if rerr := res.RemoteResults["origin"]; rerr == nil {
		t.Errorf("expected a per-remote rebase error for origin, got nil")
	}
	if res.AnyPushed() {
		t.Errorf("true rebase conflict must not push on a single remote: %#v", res.RemoteResults)
	}
	if !res.Stranded() {
		t.Errorf("single-remote true rebase conflict is a strand under Fix C, got Stranded()=false")
	}
	if res.PopConflict {
		t.Errorf("a true rebase conflict is not a pop conflict: %v", res.PopConflictPaths)
	}
	// Abort must have run: no in-progress rebase left behind.
	if rebaseInProgress(dir) {
		t.Errorf("rebase was not aborted — rebase state dir still present")
	}
}

// TestRebaseInProgress exercises the state-dir probe directly: false on a clean
// repo and on a non-rebase (merge) conflict, true while a rebase is mid-conflict.
func TestRebaseInProgress(t *testing.T) {
	dir := initTestRepo(t)
	if rebaseInProgress(dir) {
		t.Errorf("clean repo must report no rebase in progress")
	}

	// main advances line1.
	writeFile(t, dir, "f.txt", "base\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "base")
	baseSHA := gitRun(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, "f.txt", "main change\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "main change")

	// feature branches off base with a conflicting line1.
	gitRun(t, dir, "checkout", "-b", "feature", baseSHA)
	writeFile(t, dir, "f.txt", "feature change\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "feature change")

	// rebase onto main conflicts → state dir present.
	cmd := exec.Command("git", "-C", dir, "rebase", "main")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	_ = cmd.Run() // expected non-zero (conflict)
	if !rebaseInProgress(dir) {
		t.Errorf("expected rebase in progress during a content conflict")
	}
	gitRun(t, dir, "rebase", "--abort")
	if rebaseInProgress(dir) {
		t.Errorf("rebase --abort should clear the in-progress state")
	}
}

// TestUnmergedPaths exercises ls-files -u parsing: empty on a clean tree, the
// conflicting path on a merge conflict (a non-rebase unmerged state, which also
// confirms rebaseInProgress stays false).
func TestUnmergedPaths(t *testing.T) {
	dir := initTestRepo(t)
	if got := unmergedPaths(dir); got != nil {
		t.Errorf("clean tree must have no unmerged paths, got %v", got)
	}

	writeFile(t, dir, "m.txt", "base\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "base")
	baseSHA := gitRun(t, dir, "rev-parse", "HEAD")
	writeFile(t, dir, "m.txt", "main\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "main")

	gitRun(t, dir, "checkout", "-b", "feature", baseSHA)
	writeFile(t, dir, "m.txt", "feature\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "feature")

	gitRun(t, dir, "checkout", "main")
	cmd := exec.Command("git", "-C", dir, "merge", "feature")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	_ = cmd.Run() // expected non-zero (conflict)

	got := unmergedPaths(dir)
	found := false
	for _, p := range got {
		if p == "m.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected m.txt in unmerged paths, got %v", got)
	}
	if rebaseInProgress(dir) {
		t.Errorf("a merge conflict is not a rebase — rebaseInProgress must be false")
	}
}

// TestCommitAndPushPaths_AlreadyAheadReconcilesThenPushes is Fix B (d): the local
// branch carries a prior UNPUSHED commit (ahead of the stale origin/main) while
// the remote advanced on a DISJOINT path. A new commit must NOT stack ahead-N;
// the guard fetches+rebases the prior commit onto the remote tip first, so the
// new commit fast-forwards. Both the previously-stranded commit and the new one
// land on the remote.
func TestCommitAndPushPaths_AlreadyAheadReconcilesThenPushes(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main") // origin/main tracking ref = seed

	// Prior UNPUSHED local commit → HEAD is ahead of the (stale) origin/main.
	writeFile(t, dir, "prior.txt", "stranded prior commit\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "prior stranded commit")

	// Remote advances a DISJOINT file via another clone (off the seed, since the
	// prior commit never reached the bare repo).
	other := t.TempDir()
	gitRun(t, other, "clone", "-b", "main", bare, ".")
	gitRun(t, other, "config", "user.email", "other@example.com")
	gitRun(t, other, "config", "user.name", "Other")
	writeFile(t, other, "remote.txt", "from other\n")
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-m", "remote disjoint advance")
	gitRun(t, other, "push", "origin", "main")

	// New work unit: a brand-new path. The guard must reconcile first.
	writeFile(t, dir, "new.txt", "new work\n")
	res, err := CommitAndPushPaths(dir, "new work unit", []string{"new.txt"}, true)
	if err != nil {
		t.Fatalf("commit+push: %v", err)
	}
	if !res.AnyPushed() {
		t.Errorf("expected push to succeed after already-ahead reconcile, got %#v", res.RemoteResults)
	}
	if res.Stranded() {
		t.Errorf("reconciled+pushed commit must NOT be stranded: %#v", res.RemoteResults)
	}
	// The remote tip must carry the previously-stranded commit, the new commit,
	// and the remote's disjoint advance.
	tree := gitRun(t, bare, "ls-tree", "--name-only", "main")
	for _, want := range []string{"prior.txt", "new.txt", "remote.txt"} {
		if !strings.Contains(tree, want) {
			t.Errorf("remote tip missing %q after reconcile: %q", want, tree)
		}
	}
	// The prior commit's message must be present in remote history (replayed, not
	// dropped), proving the strand was reconciled rather than discarded.
	logOut := gitRun(t, bare, "log", "--format=%s", "main")
	if !strings.Contains(logOut, "prior stranded commit") {
		t.Errorf("remote history missing replayed prior commit: %q", logOut)
	}
}

// TestCommitAndPushPaths_AlreadyAheadPersistentConflictStrands is Fix B (e): the
// prior UNPUSHED local commit conflicts with the remote on the SAME lines. The
// reconcile cannot replay cleanly, so the new artifacts still commit locally
// (nothing lost) but the push is skipped and the result strands — the operator
// gets an explicit signal instead of silent ahead-N compounding.
func TestCommitAndPushPaths_AlreadyAheadPersistentConflictStrands(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")

	// A shared base file both sides will edit on the same line.
	writeFile(t, dir, "shared.txt", "line1\nline2\nline3\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "add shared base")
	gitRun(t, dir, "push", "origin", "main") // origin/main tracking = base

	// Prior UNPUSHED local commit edits line1 → ahead of stale origin/main.
	writeFile(t, dir, "shared.txt", "PRIOR\nline2\nline3\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "prior conflicting commit")

	// Remote advances line1 differently via another clone.
	other := t.TempDir()
	gitRun(t, other, "clone", "-b", "main", bare, ".")
	gitRun(t, other, "config", "user.email", "other@example.com")
	gitRun(t, other, "config", "user.name", "Other")
	writeFile(t, other, "shared.txt", "REMOTE\nline2\nline3\n")
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-m", "remote conflicting advance")
	gitRun(t, other, "push", "origin", "main")

	// New work unit: a brand-new path. Reconcile must FAIL (persistent conflict)
	// yet the new commit must still land locally.
	writeFile(t, dir, "new.txt", "new work\n")
	res, err := CommitAndPushPaths(dir, "new work unit", []string{"new.txt"}, true)
	if err != nil {
		t.Fatalf("commit+push must surface per-remote, not fatal: %v", err)
	}
	if res.CommitSHA == "" {
		t.Errorf("new artifacts must still commit locally on persistent conflict (nothing lost)")
	}
	// The new file must be in local HEAD (committed) even though the push was skipped.
	tree := gitRun(t, dir, "ls-tree", "--name-only", "HEAD")
	if !strings.Contains(tree, "new.txt") {
		t.Errorf("new.txt must be committed to local HEAD, tree=%q", tree)
	}
	if res.AnyPushed() {
		t.Errorf("persistent reconcile conflict must skip the push: %#v", res.RemoteResults)
	}
	if !res.Stranded() {
		t.Errorf("single-remote persistent reconcile conflict is a strand, got Stranded()=false")
	}
	if rerr := res.RemoteResults["origin"]; rerr == nil {
		t.Errorf("RemoteResults[origin] must be the reconcile error, got nil")
	} else if !strings.Contains(rerr.Error(), "reconcile against origin") {
		t.Errorf("expected a reconcile error for origin, got %v", rerr)
	}
	// No rebase left mid-flight (the guard aborted).
	if rebaseInProgress(dir) {
		t.Errorf("reconcile must abort its rebase on conflict — state dir still present")
	}
}

// TestCommitAndPushPaths_AlreadyAheadGuardFailsOpen is Fix B (f): the guard must
// (i) never run on push=false (no network even with a dead remote),
// (ii) fail open when the remote-tracking ref does not resolve (never fetched),
// and (iii) be a no-op on the not-ahead happy path while the normal fast-forward
// push still works.
func TestCommitAndPushPaths_AlreadyAheadGuardFailsOpen(t *testing.T) {
	t.Run("push_false_no_network_even_with_dead_remote", func(t *testing.T) {
		dir := initTestRepo(t)
		// A remote pointed at a nonexistent path: if the guard (or any push path)
		// touched the network it would error. push=false must never go there.
		gitRun(t, dir, "remote", "add", "origin", filepath.Join(dir, "does-not-exist.git"))
		writeFile(t, dir, "local.txt", "local only\n")
		res, err := CommitAndPushPaths(dir, "local only", []string{"local.txt"}, false)
		if err != nil {
			t.Fatalf("push=false must succeed without touching the dead remote: %v", err)
		}
		if res.CommitSHA == "" {
			t.Errorf("expected a local commit, got empty CommitSHA")
		}
		if res.RemoteResults != nil {
			t.Errorf("push=false must leave RemoteResults nil, got %#v", res.RemoteResults)
		}
	})

	t.Run("unresolved_tracking_ref_skips_guard", func(t *testing.T) {
		dir := initTestRepo(t)
		bare := initBareRemote(t)
		// Freshly added remote: never pushed, never fetched → origin/main does
		// NOT resolve. The guard must fail open (skip) and the normal create-push
		// must still succeed.
		gitRun(t, dir, "remote", "add", "origin", bare)
		writeFile(t, dir, "new.txt", "new work\n")
		res, err := CommitAndPushPaths(dir, "first push", []string{"new.txt"}, true)
		if err != nil {
			t.Fatalf("fail-open guard must not error the commit: %v", err)
		}
		if !res.AnyPushed() {
			t.Errorf("normal create-push must still succeed past the fail-open guard: %#v", res.RemoteResults)
		}
		if res.Stranded() {
			t.Errorf("fail-open path must not strand: %#v", res.RemoteResults)
		}
		tree := gitRun(t, bare, "ls-tree", "--name-only", "main")
		if !strings.Contains(tree, "new.txt") {
			t.Errorf("remote tip missing new.txt: %q", tree)
		}
	})

	t.Run("not_ahead_guard_is_noop", func(t *testing.T) {
		dir := initTestRepo(t)
		bare := initBareRemote(t)
		gitRun(t, dir, "remote", "add", "origin", bare)
		gitRun(t, dir, "push", "origin", "main") // HEAD == origin/main: not ahead
		writeFile(t, dir, "new.txt", "new work\n")
		res, err := CommitAndPushPaths(dir, "fast-forward", []string{"new.txt"}, true)
		if err != nil {
			t.Fatalf("not-ahead happy path must succeed: %v", err)
		}
		if !res.AllPushed() {
			t.Errorf("not-ahead fast-forward push must succeed: %#v", res.RemoteResults)
		}
		if res.Stranded() {
			t.Errorf("not-ahead happy path must not strand: %#v", res.RemoteResults)
		}
		tree := gitRun(t, bare, "ls-tree", "--name-only", "main")
		if !strings.Contains(tree, "new.txt") {
			t.Errorf("remote tip missing new.txt after fast-forward: %q", tree)
		}
	})
}
