// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file pins the property CommitAndPushPaths is NAMED for: it commits the
// paths it was given, and nothing else.
//
// 🔴 IT DID NOT, AND THE HOLE WAS INVISIBLE FROM THE STAGING SIDE. The staging
// half was always scoped — `git add -- <paths>`, never `git add -A`, asserted in
// several places. The COMMIT half was `git commit -m <msg>` with no pathspec,
// and a pathspec-less commit records EVERYTHING IN THE INDEX. So any content a
// human (or another tool) had already staged rode out under the caller's own
// machine-authored message: tidy's "sweep N capture artifacts", the memory
// harvest's message, or a task writer's "vault: amend task p/s".
//
// That is precisely the outcome the option-1 rejection in
// `task-writes-leave-the-vault-dirty-with-no-sweeper` turns on — a commit message
// asserting machine provenance over content nothing checked — reproduced through
// the back door of a caller that had scoped its paths correctly.
//
// Two predicates had to move together. Scoping the commit without scoping the
// "is there anything to commit?" check would have made the function ERROR on the
// benign case where our paths are unchanged and somebody else's are staged: the
// whole-index predicate would say "yes, changes staged", and the scoped commit
// would then find nothing of its own to record. The question the predicate asks
// must be the question the commit answers.

// removeFile deletes a vault-relative path from the worktree.
func removeFile(t *testing.T, dir, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

// stageDecoy writes a file, stages it, and returns its vault-relative path. It
// stands in for the state this whole file is about: content already in the index
// that the caller never asked to commit.
func stageDecoy(t *testing.T, dir string) string {
	t.Helper()
	const rel = "Knowledge/human-secret.md"
	writeFile(t, dir, rel, "an unreviewed human edit\n")
	gitRun(t, dir, "add", "--", rel)
	// Premise, asserted rather than assumed: it really is staged.
	if out := gitRun(t, dir, "diff", "--cached", "--name-only"); !strings.Contains(out, rel) {
		t.Fatalf("test premise broken: %s is not staged; index: %q", rel, out)
	}
	return rel
}

// headFiles lists every path in HEAD's tree.
func headFiles(t *testing.T, dir string) string {
	t.Helper()
	return gitRun(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
}

// TestCommitAndPushPathsDoesNotSweepAPreStagedIndex is the headline. A caller
// hands a scoped path list; a decoy is already staged; the commit must carry the
// caller's path and NOT the decoy, and the decoy must still be staged afterwards
// so the human who staged it has not silently lost it.
func TestCommitAndPushPathsDoesNotSweepAPreStagedIndex(t *testing.T) {
	dir := initTestRepo(t)
	decoy := stageDecoy(t, dir)

	const mine = "Projects/p/tasks/t.md"
	writeFile(t, dir, mine, "# a task\n")

	if _, err := CommitAndPushPaths(dir, "vault: create task p/t", []string{mine}, false); err != nil {
		t.Fatalf("CommitAndPushPaths: %v", err)
	}

	tree := headFiles(t, dir)
	if !strings.Contains(tree, mine) {
		t.Errorf("%s is missing from HEAD; tree:\n%s", mine, tree)
	}
	if strings.Contains(tree, decoy) {
		t.Errorf("the commit swept a pre-staged file the caller never named.\n"+
			"HEAD subject: %q\nHEAD files:\n%s\n"+
			"A pathspec-less `git commit` records the whole index, so unreviewed content rides out "+
			"under a machine-authored message that names something else entirely.",
			gitRun(t, dir, "log", "-1", "--pretty=%s"), tree)
	}

	// The decoy is untouched: still staged, not lost, still the human's to
	// review. Dropping it from the index would be a different way of taking it
	// away from them.
	if out := gitRun(t, dir, "diff", "--cached", "--name-only"); !strings.Contains(out, decoy) {
		t.Errorf("the pre-staged file is no longer staged after the commit; index: %q", out)
	}
}

// TestCommitAndPushPathsNoOpsWhenOnlyForeignContentIsStaged is the predicate half.
//
// Our paths are unchanged; somebody else's content is staged. The correct answer
// is a benign no-op — no commit, no error — and reaching it requires the
// "anything to commit?" check to ask about OUR paths. A whole-index check answers
// "yes" here and sends a scoped commit off to find nothing of its own.
func TestCommitAndPushPathsNoOpsWhenOnlyForeignContentIsStaged(t *testing.T) {
	dir := initTestRepo(t)

	const mine = "Projects/p/tasks/t.md"
	writeFile(t, dir, mine, "# a task\n")
	if _, err := CommitAndPushPaths(dir, "vault: create task p/t", []string{mine}, false); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	before := gitRun(t, dir, "rev-parse", "HEAD")

	decoy := stageDecoy(t, dir)

	// Same call again, with nothing of ours changed.
	res, err := CommitAndPushPaths(dir, "vault: amend task p/t", []string{mine}, false)
	if err != nil {
		t.Fatalf("a no-op call errored instead of returning cleanly: %v", err)
	}
	if res.CommitSHA != "" {
		t.Errorf("CommitSHA = %q on a no-op call", res.CommitSHA)
	}
	if after := gitRun(t, dir, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved on a no-op call: %s -> %s (subject %q)",
			before, after, gitRun(t, dir, "log", "-1", "--pretty=%s"))
	}
	if tree := headFiles(t, dir); strings.Contains(tree, decoy) {
		t.Errorf("the no-op call committed the pre-staged decoy; tree:\n%s", tree)
	}
}

// TestCommitAndPushPathsStillRecordsADeletion guards the edge the pathspec form
// could plausibly have broken: retire and cancel MOVE a task file, so the source
// deletion has to land in the same commit as the destination. A pathspec that
// silently declined to match a path no longer in the worktree would commit half a
// rename — the destination created, the source still on disk at HEAD.
func TestCommitAndPushPathsStillRecordsADeletion(t *testing.T) {
	// The gitignore-reconciled seed, because this test asserts a CLEAN worktree
	// at the end and the repo-root commit lock's sidecar persists past release.
	dir, _ := syncSeedRemote(t)

	const src = "Projects/p/tasks/t.md"
	const dst = "Projects/p/tasks/done/t.md"
	writeFile(t, dir, src, "# a task\n")
	if _, err := CommitAndPushPaths(dir, "seed task", []string{src}, false); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	// The move, as moveTask performs it: content at the destination, source gone.
	writeFile(t, dir, dst, "# a task\n")
	removeFile(t, dir, src)

	if _, err := CommitAndPushPaths(dir, "vault: retire task p/t", []string{src, dst}, false); err != nil {
		t.Fatalf("CommitAndPushPaths: %v", err)
	}

	tree := headFiles(t, dir)
	if !strings.Contains(tree, dst) {
		t.Errorf("the destination is missing from HEAD; tree:\n%s", tree)
	}
	if strings.Contains(tree, src) {
		t.Errorf("the source deletion was not recorded — half a rename is committed; tree:\n%s", tree)
	}
	if status := gitRun(t, dir, "status", "--porcelain", "-uall"); status != "" {
		t.Errorf("worktree is not clean after the move commit:\n%s", status)
	}
}

// TestTidyDoesNotSweepAPreStagedIndex is the same probe against the shared caller
// whose message makes the strongest claim: "vault tidy: sweep N capture
// artifacts". Committing a human's staged prose under that sentence is the exact
// falsehood the option-1 rejection refused to introduce deliberately.
func TestTidyDoesNotSweepAPreStagedIndex(t *testing.T) {
	dir := initTestRepo(t)
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatalf("reconcile vault gitignore: %v", err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "seed gitignore")

	decoy := stageDecoy(t, dir)
	writeFile(t, dir, "Projects/p/sessions/x.md", "session body\n")

	res, err := TidyVault(dir, false)
	if err != nil {
		t.Fatalf("TidyVault: %v", err)
	}
	if !res.Committed {
		t.Fatalf("tidy did not commit the artifact: %#v", res)
	}
	tree := headFiles(t, dir)
	if !strings.Contains(tree, "Projects/p/sessions/x.md") {
		t.Errorf("the swept artifact is missing from HEAD; tree:\n%s", tree)
	}
	if strings.Contains(tree, decoy) {
		t.Errorf("tidy committed a pre-staged human file under %q; tree:\n%s",
			gitRun(t, dir, "log", "-1", "--pretty=%s"), tree)
	}
}

// TestSyncVaultDoesNotSweepAPreStagedIndex covers the flow, not just the tidy
// step: SyncVault classifies, refuses on genuine dirt, then commits artifacts.
//
// 🔴 THE STAGED DECOY IS ALSO REPORTED DIRT, so this asserts the honest outcome
// rather than a convenient one: SyncVault must REFUSE, because a staged
// non-artifact file is exactly the "needs human eyes" case. What must not happen
// is the other branch — sailing past and folding it into the artifact commit.
func TestSyncVaultDoesNotSweepAPreStagedIndex(t *testing.T) {
	dir, _ := syncSeedRemote(t)
	decoy := stageDecoy(t, dir)
	writeFile(t, dir, "Projects/p/sessions/x.md", "session body\n")
	before := gitRun(t, dir, "rev-parse", "HEAD")

	res, err := SyncVault(dir, []string{"origin"})
	if !res.Refused {
		t.Fatalf("SyncVault did not refuse on a staged non-artifact file (err %v, dirt %v)", err, res.GenuineDirt)
	}
	if after := gitRun(t, dir, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved on a refused sync: %s -> %s", before, after)
	}
	if tree := headFiles(t, dir); strings.Contains(tree, decoy) {
		t.Errorf("a refused sync still committed the decoy; tree:\n%s", tree)
	}
}

// TestCommitAndPushPathsRefusesDuringAMerge pins the one behaviour the pathspec
// fix deliberately CHANGED, so that a later maintainer meeting
// `fatal: cannot do a partial commit during a merge.` does not "fix" it by
// dropping the pathspec again.
//
// The fixture is the sharp case, not the easy one: a CLEAN merge in progress
// (--no-commit, no conflicts) touching a file this commit never names. git
// refuses anyway — the guard is on MERGE_HEAD existing, not on overlap — and the
// unscoped form would have succeeded here, producing a merge commit carrying the
// human's whole in-flight merge under a machine-authored message.
//
// 🔴 A FALLBACK TO THE UNSCOPED COMMIT WOULD BE THE WORST POSSIBLE REPAIR. A
// merge in progress is exactly the state where the index holds the most content
// this caller never chose. Refusing is the outcome; the caller decides what to do
// with it (vp_manage_task, for one, reports commit=failed and keeps the write).
func TestCommitAndPushPathsRefusesDuringAMerge(t *testing.T) {
	dir := initTestRepo(t)
	gitRun(t, dir, "checkout", "-b", "side")
	writeFile(t, dir, "side-only.txt", "side\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "side")
	gitRun(t, dir, "checkout", "main")
	writeFile(t, dir, "main-only.txt", "main\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "main")

	// A clean, conflict-free merge, deliberately left uncommitted.
	gitRun(t, dir, "merge", "--no-commit", "--no-ff", "side")
	if _, err := gitCmd(dir, 5*time.Second, "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); err != nil {
		t.Fatalf("test premise broken: no merge is in progress, so nothing is being measured: %v", err)
	}

	const mine = "Projects/p/tasks/t.md"
	writeFile(t, dir, mine, "# a task\n")
	before := gitRun(t, dir, "rev-parse", "HEAD")

	_, err := CommitAndPushPaths(dir, "vault: create task p/t", []string{mine}, false)
	if err == nil {
		t.Fatalf("the commit succeeded during a merge; HEAD moved %s -> %s with subject %q — "+
			"a human's in-flight merge was folded into a machine-authored commit",
			before, gitRun(t, dir, "rev-parse", "HEAD"), gitRun(t, dir, "log", "-1", "--pretty=%s"))
	}
	if !strings.Contains(err.Error(), "merge") {
		t.Errorf("the refusal does not tell the caller a merge is in the way: %v", err)
	}
	if after := gitRun(t, dir, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved on a refused commit: %s -> %s", before, after)
	}
}
