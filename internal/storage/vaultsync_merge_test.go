// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The commit-and-push reconcile MERGES the fetched remote tip; it never
// rebases, never autostashes and never force-pushes. These tests pin that
// against the 2026-10-02 incident, in which a 990-commit `rebase --autostash`
// was SIGKILLed by gitCmd's deadline at pick 590 and stranded the shared vault
// mid-rebase with the caller's own files held in the autostash.

// reconcileGitShim puts a `git` first on PATH that logs every argv (one
// invocation per line) and otherwise runs the real git. Mode "fail-merge-abort"
// makes `git merge --abort` exit 1 without running. Mode "hang-merge" makes
// `git merge` (not --abort) leave .git/index.lock and hang, as a merge does
// when gitCmd's deadline SIGKILLs it mid-write; `exec` makes the sleep the
// process the deadline kills. Mode "signal-merge" leaves index.lock and kills
// itself with SIGTERM, as a merge does when something else kills it. It returns the log path and
// a mode setter.
func reconcileGitShim(t *testing.T) (logPath string, setMode func(string)) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the git shim is a shell script")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	logPath = filepath.Join(dir, "argv.log")
	mode := filepath.Join(dir, "mode")
	script := `#!/bin/sh
echo "$*" >> "` + logPath + `"
mode=$(cat "` + mode + `" 2>/dev/null)
merge=0; abort=0
for a in "$@"; do
  case "$a" in merge) merge=1 ;; --abort) abort=1 ;; esac
done
if [ "$mode" = fail-merge-abort ] && [ $merge = 1 ] && [ $abort = 1 ]; then
  echo "shim: merge --abort refused" >&2
  exit 1
fi
if [ "$mode" = hang-merge ] && [ $merge = 1 ] && [ $abort = 0 ]; then
  : > .git/index.lock
  exec sleep 30
fi
if [ "$mode" = signal-merge ] && [ $merge = 1 ] && [ $abort = 0 ]; then
  : > .git/index.lock
  kill -TERM $$
fi
exec "` + real + `" "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath, func(m string) {
		t.Helper()
		if err := os.WriteFile(mode, []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// forcePushes returns every logged push invocation that would rewrite a remote
// ref: --force, --force-with-lease, -f, or a +refspec.
func forcePushes(t *testing.T, logPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		t.Fatal("the git shim logged nothing, so a no-force verdict would prove nothing")
	}
	var bad []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		fields := strings.Fields(line)
		isPush := false
		for _, f := range fields {
			if f == "push" {
				isPush = true
			}
		}
		if !isPush {
			continue
		}
		for _, f := range fields {
			if strings.HasPrefix(f, "--force") || f == "-f" || strings.HasPrefix(f, "+") {
				bad = append(bad, line)
				break
			}
		}
	}
	return bad
}

// committedSHA returns the full SHA HEAD had right after the commit whose
// reflog subject is "commit: <msg>", read from the reflog so it survives any
// later rewrite of the branch.
func committedSHA(t *testing.T, dir, msg string) string {
	t.Helper()
	out := gitRun(t, dir, "log", "-g", "--format=%H%x00%gs", "HEAD")
	for line := range strings.SplitSeq(out, "\n") {
		sha, subj, ok := strings.Cut(line, "\x00")
		if ok && subj == "commit: "+msg {
			return sha
		}
	}
	t.Fatalf("no reflog entry %q in:\n%s", "commit: "+msg, out)
	return ""
}

func isAncestor(t *testing.T, dir, ancestor, of string) bool {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", ancestor, of)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run() == nil
}

func gitPathExists(t *testing.T, dir, name string) bool {
	t.Helper()
	p := gitRun(t, dir, "rev-parse", "--git-path", name)
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	_, err := os.Lstat(p)
	return err == nil
}

// TestCommitAndPushPaths_ReconcileKeepsLocalCommitSHAs pins that the reconcile
// replays nothing: every local commit keeps its SHA and reaches the remote as
// an ancestor of the merge. A rebase rewrites them, and its cost grows with the
// number of commits ahead — the property that let a deadline kill it mid-run.
func TestCommitAndPushPaths_ReconcileKeepsLocalCommitSHAs(t *testing.T) {
	t.Run("rejected_push", func(t *testing.T) {
		dir := initTestRepo(t)
		bare := initBareRemote(t)
		gitRun(t, dir, "remote", "add", "origin", bare)
		gitRun(t, dir, "push", "origin", "main")
		advanceRemote(t, bare, "remote.txt", "from other\n")

		writeFile(t, dir, "local.txt", "from local\n")
		if _, err := CommitAndPushPaths(dir, "local change", []string{"local.txt"}, true); err != nil {
			t.Fatalf("commit+push: %v", err)
		}
		x := committedSHA(t, dir, "local change")
		tip := gitRun(t, bare, "rev-parse", "main")
		if !isAncestor(t, dir, x, "HEAD") || !isAncestor(t, bare, x, tip) {
			t.Errorf("the caller's commit %s was rewritten: not an ancestor of HEAD and the remote tip %s", x, tip)
		}
		if head := gitRun(t, dir, "rev-parse", "HEAD"); head != tip {
			t.Errorf("remote tip %s != HEAD %s", tip, head)
		}
	})

	t.Run("already_ahead", func(t *testing.T) {
		dir := initTestRepo(t)
		bare := initBareRemote(t)
		gitRun(t, dir, "remote", "add", "origin", bare)
		gitRun(t, dir, "push", "origin", "main")
		commitLocal(t, dir, "a.txt", "A\n")
		a := gitRun(t, dir, "rev-parse", "HEAD")
		commitLocal(t, dir, "b.txt", "B\n")
		b := gitRun(t, dir, "rev-parse", "HEAD")
		advanceRemote(t, bare, "remote.txt", "from other\n")

		writeFile(t, dir, "new.txt", "new\n")
		res, err := CommitAndPushPaths(dir, "new work", []string{"new.txt"}, true)
		if err != nil {
			t.Fatalf("commit+push: %v", err)
		}
		if len(FailedRemotes(res.RemoteResults)) > 0 {
			t.Fatalf("expected the push to land: %#v", res.RemoteResults)
		}
		tip := gitRun(t, bare, "rev-parse", "main")
		for name, sha := range map[string]string{"A": a, "B": b} {
			if !isAncestor(t, dir, sha, "HEAD") || !isAncestor(t, bare, sha, tip) {
				t.Errorf("local commit %s (%s) was rewritten by the reconcile", name, sha)
			}
		}
		if head := gitRun(t, dir, "rev-parse", "HEAD"); head != tip {
			t.Errorf("remote tip %s != HEAD %s", tip, head)
		}
	})
}

// TestCommitAndPushPaths_ReconcileNeverStashesTheCallersWork is the 15:42
// shape: a dirty tracked file the remote also changed. The reconcile must leave
// it exactly as the caller has it — no stash, no conflict markers, no merge or
// rebase left in progress — and strand the commit instead of pushing over it.
func TestCommitAndPushPaths_ReconcileNeverStashesTheCallersWork(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	writeFile(t, dir, "shared-y.txt", "line1\nline2\nline3\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "add shared Y")
	gitRun(t, dir, "push", "origin", "main")
	advanceRemote(t, bare, "shared-y.txt", "OTHER\nline2\nline3\n")

	const dirty = "LOCAL\nline2\nline3\n"
	writeFile(t, dir, "local-x.txt", "from local\n")
	writeFile(t, dir, "shared-y.txt", dirty)

	res, err := CommitAndPushPaths(dir, "local change X", []string{"local-x.txt"}, true)
	if err != nil {
		t.Fatalf("commit+push must surface per remote, not fail: %v", err)
	}
	if got := readFile(t, dir, "shared-y.txt"); got != dirty {
		t.Errorf("the caller's uncommitted Y changed: got %q, want %q", got, dirty)
	}
	if stash := gitRun(t, dir, "stash", "list"); stash != "" {
		t.Errorf("the reconcile stashed work: %q", stash)
	}
	if gitPathExists(t, dir, "refs/stash") {
		t.Errorf("refs/stash exists after the reconcile")
	}
	for _, name := range []string{"MERGE_HEAD", "rebase-merge", "rebase-apply"} {
		if gitPathExists(t, dir, name) {
			t.Errorf("%s left behind by the reconcile", name)
		}
	}
	if tree := gitRun(t, dir, "ls-tree", "--name-only", "HEAD"); !strings.Contains(tree, "local-x.txt") {
		t.Errorf("the caller's commit must land locally, HEAD tree: %q", tree)
	}
	if res.RemoteResults["origin"] == nil || !res.Stranded() {
		t.Errorf("a refused merge must strand the commit, got %#v", res.RemoteResults)
	}
}

// TestCommitAndPushPaths_ReconcileAbortFailureIsReported pins that a failed
// `merge --abort` reaches the caller. The 15:43 incident was silent because the
// abort's error was discarded and only the next step's index.lock error showed.
func TestCommitAndPushPaths_ReconcileAbortFailureIsReported(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	writeFile(t, dir, "conflict-y.txt", "line1\nline2\nline3\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "add conflict Y")
	gitRun(t, dir, "push", "origin", "main")
	advanceRemote(t, bare, "conflict-y.txt", "OTHER\nline2\nline3\n")
	writeFile(t, dir, "conflict-y.txt", "LOCAL\nline2\nline3\n")

	_, setMode := reconcileGitShim(t)
	setMode("fail-merge-abort")
	res, err := CommitAndPushPaths(dir, "local conflicting Y", []string{"conflict-y.txt"}, true)
	if err != nil {
		t.Fatalf("commit+push must surface per remote, not fail: %v", err)
	}
	rerr := res.RemoteResults["origin"]
	if rerr == nil || !strings.Contains(rerr.Error(), "abort failed") || !strings.Contains(rerr.Error(), "shim: merge --abort refused") {
		t.Errorf("the abort's failure must be in the per-remote error, got %v", rerr)
	}
}

// TestCommitAndPushPaths_ConvergenceNeverForcePushes pins the multi-remote
// convergence after a reconcile: the remote that already took the caller's
// commit is FAST-FORWARDED to the merge, never rewritten, and no push anywhere
// carries a force.
func TestCommitAndPushPaths_ConvergenceNeverForcePushes(t *testing.T) {
	dir := initTestRepo(t)
	bareA := initBareRemote(t)
	bareB := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "a", bareA)
	gitRun(t, dir, "remote", "add", "b", bareB)
	gitRun(t, dir, "push", "a", "main")
	gitRun(t, dir, "push", "b", "main")
	advanceRemote(t, bareB, "remote-b.txt", "from b's other host\n")

	logPath, _ := reconcileGitShim(t)
	writeFile(t, dir, "x.txt", "X\n")
	res, err := CommitAndPushPaths(dir, "commit X", []string{"x.txt"}, true)
	if err != nil {
		t.Fatalf("commit+push: %v", err)
	}
	if len(FailedRemotes(res.RemoteResults)) > 0 {
		t.Fatalf("expected both remotes to converge: %#v", res.RemoteResults)
	}

	x := committedSHA(t, dir, "commit X")
	head := gitRun(t, dir, "rev-parse", "HEAD")
	tipA := gitRun(t, bareA, "rev-parse", "main")
	tipB := gitRun(t, bareB, "rev-parse", "main")
	if tipA != head || tipB != head {
		t.Errorf("remotes did not converge on HEAD %s: a=%s b=%s", head, tipA, tipB)
	}
	if !isAncestor(t, bareA, x, tipA) {
		t.Errorf("remote a was rewritten: its earlier tip %s (the caller's commit) is not an ancestor of %s", x, tipA)
	}
	if bad := forcePushes(t, logPath); len(bad) > 0 {
		t.Errorf("force push(es) issued: %q", bad)
	}
}

// TestCommitAndPushPaths_ConvergenceRefusesAConcurrentWriter pins that the
// protection --force-with-lease gave survives its removal: a remote moved by a
// concurrent writer between our push and the convergence is NOT overwritten.
func TestCommitAndPushPaths_ConvergenceRefusesAConcurrentWriter(t *testing.T) {
	dir := initTestRepo(t)
	bareA := initBareRemote(t)
	bareB := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "a", bareA)
	gitRun(t, dir, "remote", "add", "b", bareB)
	gitRun(t, dir, "push", "a", "main")
	gitRun(t, dir, "push", "b", "main")
	advanceRemote(t, bareB, "remote-b.txt", "from b's other host\n")

	moved := false
	prev := afterPushHook
	afterPushHook = func(remote string) {
		if remote == "a" && !moved {
			moved = true
			advanceRemote(t, bareA, "concurrent.txt", "a concurrent writer\n")
		}
	}
	t.Cleanup(func() { afterPushHook = prev })

	logPath, _ := reconcileGitShim(t)
	writeFile(t, dir, "x.txt", "X\n")
	res, err := CommitAndPushPaths(dir, "commit X", []string{"x.txt"}, true)
	if err != nil {
		t.Fatalf("commit+push: %v", err)
	}
	if !moved {
		t.Fatal("the concurrent writer never ran")
	}
	if rerr := res.RemoteResults["a"]; rerr == nil || !strings.Contains(rerr.Error(), "convergence") {
		t.Errorf("remote a must report a refused convergence, got %v", rerr)
	}
	if subj := gitRun(t, bareA, "log", "-1", "--format=%s", "main"); subj != "advance concurrent.txt" {
		t.Errorf("the concurrent writer's commit was overwritten on remote a, tip subject %q", subj)
	}
	if bad := forcePushes(t, logPath); len(bad) > 0 {
		t.Errorf("force push(es) issued: %q", bad)
	}
}

// TestCommitAndPushPaths_CommitSHAIsTheCallersCommitAfterReconcile pins that
// PushResult.CommitSHA names the commit this call made, not the merge commit the
// reconcile added on top of it.
func TestCommitAndPushPaths_CommitSHAIsTheCallersCommitAfterReconcile(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")
	advanceRemote(t, bare, "remote.txt", "from other\n")

	writeFile(t, dir, "local.txt", "from local\n")
	res, err := CommitAndPushPaths(dir, "the caller's commit", []string{"local.txt"}, true)
	if err != nil {
		t.Fatalf("commit+push: %v", err)
	}
	if len(FailedRemotes(res.RemoteResults)) > 0 {
		t.Fatalf("expected the push to land: %#v", res.RemoteResults)
	}
	if subj := gitRun(t, dir, "log", "-1", "--format=%s", res.CommitSHA); subj != "the caller's commit" {
		t.Errorf("CommitSHA %s is not the caller's commit (subject %q)", res.CommitSHA, subj)
	}
	if tip := gitRun(t, bare, "rev-parse", "main"); !isAncestor(t, bare, res.CommitSHA, tip) {
		t.Errorf("CommitSHA %s is not on the remote tip %s", res.CommitSHA, tip)
	}
}

// forceFindings reports the force-push spellings in one parsed file: any string
// literal starting "--force-with-lease" or equal to "--force", and any "-f" or
// "+"-prefixed literal argument of a call that also passes the literal "push".
func forceFindings(fset *token.FileSet, f *ast.File) []string {
	var bad []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return true
			}
			if v, err := strconv.Unquote(x.Value); err == nil && (strings.HasPrefix(v, "--force-with-lease") || v == "--force") {
				bad = append(bad, fset.Position(x.Pos()).String())
			}
		case *ast.CallExpr:
			push := false
			var lits []*ast.BasicLit
			for _, a := range x.Args {
				var lit *ast.BasicLit
				switch e := a.(type) {
				case *ast.BasicLit:
					lit = e
				case *ast.BinaryExpr:
					// The leftmost operand of a concatenation such as "+"+ref.
					for l := ast.Expr(e); ; {
						b, ok := l.(*ast.BinaryExpr)
						if !ok {
							lit, _ = l.(*ast.BasicLit)
							break
						}
						l = b.X
					}
				}
				if lit == nil || lit.Kind != token.STRING {
					continue
				}
				if v, _ := strconv.Unquote(lit.Value); v == "push" {
					push = true
				}
				lits = append(lits, lit)
			}
			if !push {
				return true
			}
			for _, lit := range lits {
				if v, _ := strconv.Unquote(lit.Value); v == "-f" || strings.HasPrefix(v, "+") {
					bad = append(bad, fset.Position(lit.Pos()).String())
				}
			}
		}
		return true
	})
	return bad
}

// TestNoVaultPushForces is the source pin: no production file in this package
// spells a force push. The vault's history is shared by every host; a push here
// only ever fast-forwards.
func TestNoVaultPushForces(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	scanned := 0
	for _, p := range files {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		bad = append(bad, forceFindings(fset, f)...)
	}
	if scanned == 0 {
		t.Fatal("scanned no source files, so the pin proves nothing")
	}
	if len(bad) > 0 {
		t.Errorf("force-push spellings in internal/storage: %v", bad)
	}

	t.Run("fires_on_a_force_push", func(t *testing.T) {
		for _, src := range []string{
			"package x\nfunc f() { gitCmd(\"\", 0, \"push\", \"--force-with-lease=refs/heads/main:abc\", \"origin\", \"main\") }\n",
			"package x\nfunc f() { gitCmd(\"\", 0, \"push\", \"-f\", \"origin\", \"main\") }\n",
			"package x\nfunc f() { gitCmd(\"\", 0, \"push\", \"origin\", \"+\"+ref) }\n",
		} {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "x.go", src, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := forceFindings(fset, f); len(got) != 1 {
				t.Errorf("the pin must fire once on %q, got %v", src, got)
			}
		}
	})
}

// TestCommitAndPushPaths_LeavesAMergeItDidNotStartAlone: pullCore leaves a
// conflicted merge for the operator, who resolves it by hand. A commit-and-push
// arriving in that state must refuse, naming the merge, and leave the
// resolution — working tree, index and MERGE_HEAD — exactly as it was. Merging
// over it fails, and aborting it would destroy the hand resolution.
func TestCommitAndPushPaths_LeavesAMergeItDidNotStartAlone(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	writeFile(t, dir, "shared.txt", "line1\nline2\nline3\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "base")
	gitRun(t, dir, "push", "origin", "main")
	commitLocal(t, dir, "shared.txt", "LOCAL\nline2\nline3\n")
	advanceRemote(t, bare, "shared.txt", "REMOTE\nline2\nline3\n")

	// The operator's pull conflicts; they resolve by hand and stage it.
	gitRun(t, dir, "fetch", "origin")
	cmd := exec.Command("git", "-C", dir, "merge", "origin/main")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	_ = cmd.Run() // expected to conflict
	const resolved = "RESOLVED BY HAND\nline2\nline3\n"
	writeFile(t, dir, "shared.txt", resolved)
	gitRun(t, dir, "add", "shared.txt")
	mergeHead := gitRun(t, dir, "rev-parse", "MERGE_HEAD")

	writeFile(t, dir, "new.txt", "new\n")
	_, err := CommitAndPushPaths(dir, "new work", []string{"new.txt"}, true)
	if err == nil || !strings.Contains(err.Error(), "a merge (MERGE_HEAD) is in progress") {
		t.Errorf("the call must refuse naming the merge in progress, got %v", err)
	}
	if got := readFile(t, dir, "shared.txt"); got != resolved {
		t.Errorf("the hand resolution was destroyed: shared.txt = %q", got)
	}
	if got := gitRun(t, dir, "show", ":shared.txt"); got+"\n" != resolved {
		t.Errorf("the staged resolution changed: %q", got)
	}
	if !gitPathExists(t, dir, "MERGE_HEAD") || gitRun(t, dir, "rev-parse", "MERGE_HEAD") != mergeHead {
		t.Errorf("MERGE_HEAD did not survive")
	}
}

// TestKilledMergeErrorNamesTheLockAndRemovesNothing pins what a merge killed at
// its deadline reports: that it was killed, that the tree may be half-updated,
// and the index.lock it left — which is NOT removed, since another process may
// own it. A non-deadline error is not a kill.
func TestKilledMergeErrorNamesTheLockAndRemovesNothing(t *testing.T) {
	dir := initTestRepo(t)
	lock := filepath.Join(dir, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	killed := &GitError{Err: fmt.Errorf("timed out after 120s: %w: %w", context.DeadlineExceeded, errors.New("signal: killed"))}
	got := killedMergeError(dir, "origin/main", killed)
	if got == nil {
		t.Fatal("a deadline kill must be reported as one")
	}
	for _, want := range []string{"killed at its deadline", "half-updated", lock, "confirm no git process holds it"} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("the report must say %q, got %v", want, got)
		}
	}
	if !errors.Is(got, context.DeadlineExceeded) {
		t.Errorf("the deadline must stay in the chain: %v", got)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("index.lock was removed: %v", err)
	}
	if other := killedMergeError(dir, "origin/main", &GitError{Err: errors.New("exit status 1")}); other != nil {
		t.Errorf("a non-deadline failure is not a kill, got %v", other)
	}
}

// TestCommitAndPushPaths_CallersOwnDirtyFileDoesNotStrandTheReconcile is the
// review probe: the already-ahead reconcile runs before the caller's paths are
// staged, so the caller's own uncommitted file makes git refuse the merge. That
// refusal must not strand the remote: after the commit the push-rejection
// reconcile merges cleanly (the two sides edit different lines).
func TestCommitAndPushPaths_CallersOwnDirtyFileDoesNotStrandTheReconcile(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	writeFile(t, dir, "f.txt", "line1\nline2\nline3\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "base")
	gitRun(t, dir, "push", "origin", "main")
	commitLocal(t, dir, "prior.txt", "prior\n") // ahead of origin/main
	advanceRemote(t, bare, "f.txt", "line1\nline2\nREMOTE3\n")

	writeFile(t, dir, "f.txt", "LOCAL1\nline2\nline3\n") // the caller's own change
	res, err := CommitAndPushPaths(dir, "the caller's change", []string{"f.txt"}, true)
	if err != nil {
		t.Fatalf("commit+push: %v", err)
	}
	if res.Stranded() || len(FailedRemotes(res.RemoteResults)) > 0 {
		t.Fatalf("a commit that merges cleanly must not strand: %#v", res.RemoteResults)
	}
	if got := gitRun(t, bare, "show", "main:f.txt"); got != "LOCAL1\nline2\nREMOTE3" {
		t.Errorf("remote f.txt = %q, want both sides' lines", got)
	}
	if tree := gitRun(t, bare, "ls-tree", "--name-only", "main"); !strings.Contains(tree, "prior.txt") {
		t.Errorf("the prior commit did not reach the remote: %q", tree)
	}
}

// TestCommitAndPushPaths_KilledMergeRefusesTheCommit drives a merge past its
// deadline end to end: the fake git leaves index.lock and hangs, gitCmd kills
// it, and the call must return the killed-merge error as a
// *vaultTreeUnsafeError naming the lock — with nothing aborted, nothing
// removed and nothing staged — instead of going on to a `git add` that fails on
// the lock and hides the cause (the 2026-10-02 incident's shape).
func TestCommitAndPushPaths_KilledMergeRefusesTheCommit(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")
	commitLocal(t, dir, "prior.txt", "prior\n") // ahead of origin/main
	advanceRemote(t, bare, "remote.txt", "from other\n")
	head := gitRun(t, dir, "rev-parse", "HEAD")

	prev := mergeTimeout
	mergeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { mergeTimeout = prev })
	logPath, setMode := reconcileGitShim(t)
	setMode("hang-merge")

	writeFile(t, dir, "new.txt", "new\n")
	_, err := CommitAndPushPaths(dir, "new work", []string{"new.txt"}, true)
	setMode("")

	var unsafe *vaultTreeUnsafeError
	if !errors.As(err, &unsafe) {
		t.Fatalf("want a *vaultTreeUnsafeError, got %T: %v", err, err)
	}
	lock := filepath.Join(dir, ".git", "index.lock")
	for _, want := range []string{"killed at its deadline", lock} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must say %q, got %v", want, err)
		}
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the deadline must stay in the chain: %v", err)
	}
	if _, statErr := os.Stat(lock); statErr != nil {
		t.Errorf("index.lock was removed: %v", statErr)
	}
	raw, _ := os.ReadFile(logPath)
	if strings.Contains(string(raw), "merge --abort") {
		t.Errorf("a killed merge was aborted:\n%s", raw)
	}
	if strings.Contains(string(raw), "add ") {
		t.Errorf("something was staged after the killed merge:\n%s", raw)
	}
	if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved %s -> %s", head, got)
	}
}

// TestCommitAndPushPaths_SignalledMergeRefusesTheCommit: a merge killed by a
// signal from anything other than gitCmd's deadline can stop mid-write just
// the same, so it takes the same path as a deadline kill — reported by name,
// nothing aborted, nothing removed, nothing staged.
func TestCommitAndPushPaths_SignalledMergeRefusesTheCommit(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "main")
	commitLocal(t, dir, "prior.txt", "prior\n") // ahead of origin/main
	advanceRemote(t, bare, "remote.txt", "from other\n")
	head := gitRun(t, dir, "rev-parse", "HEAD")

	logPath, setMode := reconcileGitShim(t)
	setMode("signal-merge")
	writeFile(t, dir, "new.txt", "new\n")
	_, err := CommitAndPushPaths(dir, "new work", []string{"new.txt"}, true)
	setMode("")

	var unsafe *vaultTreeUnsafeError
	if !errors.As(err, &unsafe) {
		t.Fatalf("want a *vaultTreeUnsafeError, got %T: %v", err, err)
	}
	lock := filepath.Join(dir, ".git", "index.lock")
	for _, want := range []string{"killed by a signal", lock} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must say %q, got %v", want, err)
		}
	}
	if _, statErr := os.Stat(lock); statErr != nil {
		t.Errorf("index.lock was removed: %v", statErr)
	}
	raw, _ := os.ReadFile(logPath)
	if strings.Contains(string(raw), "merge --abort") || strings.Contains(string(raw), "add ") {
		t.Errorf("the signalled merge was aborted or something was staged:\n%s", raw)
	}
	if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved %s -> %s", head, got)
	}
}
