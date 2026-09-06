// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// --- git-backed vault harness ----------------------------------------------

// gitVaultRun runs git in the vault and fails the test on a non-zero exit.
func gitVaultRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

// newGitBackedTestVault returns a vault that is a real git repository with a
// committer identity, a reconciled .gitignore, and a clean worktree.
//
// 🔴 THE SEED TASK IS LOAD-BEARING, NOT DECORATION. The first task write into a
// project stamps Projects/<p>/.surface as a side effect (surface.StampForPath),
// and an UNTRACKED .surface is classified as reported dirt by tidy's status gate.
// Creating and committing one task in the fixture gets that stamp onto disk and
// into the index up front, so a test that writes a second task is measuring the
// task file and nothing else. Without it every "one dirty file" assertion in this
// package would silently be measuring two.
//
// `git add -A` here is fixture setup, not production behaviour; the invariant
// that vp never runs it applies to the code under test, which is what
// TestTaskWriteCommitStagesOnlyTheTaskPaths pins.
func newGitBackedTestVault(t *testing.T) *storage.Vault {
	t.Helper()
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}
	root := t.TempDir()
	gitVaultRun(t, root, "init", "-b", "main")
	gitVaultRun(t, root, "config", "user.email", "test@example.com")
	gitVaultRun(t, root, "config", "user.name", "Test User")
	if err := storage.ReconcileVaultGitignore(root); err != nil {
		t.Fatalf("reconcile vault gitignore: %v", err)
	}

	vault := bornCurrentTestVault(t, root)
	if err := vault.CreateTask("test-proj", storage.TaskSpec{
		Slug: "seed-task", Title: "Seed", Content: seedTaskBody(), Priority: "medium",
	}); err != nil {
		t.Fatalf("seed CreateTask: %v", err)
	}
	gitVaultRun(t, root, "add", "-A")
	gitVaultRun(t, root, "commit", "-m", "seed vault")
	assertVaultClean(t, root)
	return vault
}

func seedTaskBody() string {
	return strings.Repeat("Seed body text that comfortably clears the content floor.\n", 8)
}

// assertVaultClean fails unless `git status --porcelain` is empty.
func assertVaultClean(t *testing.T, root string) {
	t.Helper()
	if out := gitVaultRun(t, root, "status", "--porcelain"); out != "" {
		t.Fatalf("vault worktree is not clean:\n%s", out)
	}
}

// genuineDirtOf is the set the bootstrap alert reports and SyncVault refuses on.
func genuineDirtOf(t *testing.T, root string) []string {
	t.Helper()
	scan, err := storage.TidyScan(root)
	if err != nil {
		t.Fatalf("TidyScan: %v", err)
	}
	return scan.GenuineDirt()
}

// manageTask drives the real registered tool and returns its result map.
func manageTask(t *testing.T, vault *storage.Vault, p manageTaskParams) map[string]any {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ManageTaskTool(vault).Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("vp_manage_task %s: %v", p.Action, err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", res)
	}
	return m
}

// --- the enforcement half ---------------------------------------------------

// TestTaskWriteCommitsItsOwnWrite is the headline for option 2: a typed task
// write leaves the vault COMMITTED, with no sweeper, no hook and no explicit
// sync in between.
func TestTaskWriteCommitsItsOwnWrite(t *testing.T) {
	vault := newGitBackedTestVault(t)

	m := manageTask(t, vault, manageTaskParams{
		Project: "test-proj", Action: "create", Task: "filed-task",
		Title: "Filed", Content: unitTaskBody(), Priority: "high",
	})

	if m["commit"] != taskCommitCommitted {
		t.Errorf("commit = %v, want %q (result %#v)", m["commit"], taskCommitCommitted, m)
	}
	if sha, _ := m["commit_sha"].(string); sha == "" {
		t.Errorf("commit_sha is empty on a committed write: %#v", m)
	}
	if _, ok := m["commit_error"]; ok {
		t.Errorf("commit_error present on a successful commit: %#v", m)
	}

	// The write is in the tree, not merely on disk.
	const rel = "Projects/test-proj/tasks/filed-task.md"
	if tree := gitVaultRun(t, vault.Root, "ls-tree", "-r", "--name-only", "HEAD"); !strings.Contains(tree, rel) {
		t.Errorf("%s is not in HEAD; tree:\n%s", rel, tree)
	}
	assertVaultClean(t, vault.Root)
}

// TestTaskWriteLeavesTheVaultSyncable is the assertion that actually matches the
// harm. The task body framed this as "the task file survives only if someone
// remembers to sync"; source says worse. SyncVault refuses on genuine dirt at
// step 2 and returns BEFORE the capture-artifact commit at step 3
// (internal/storage/vaultsyncflow.go:96-103), so ONE dirty task file wedges
// sessions, transcripts, drawers and KG triples too.
//
// So the property under test is not "the task file is committed" — it is "the
// vault can still sync", asserted through the same predicate SyncVault gates on.
func TestTaskWriteLeavesTheVaultSyncable(t *testing.T) {
	vault := newGitBackedTestVault(t)

	manageTask(t, vault, manageTaskParams{
		Project: "test-proj", Action: "create", Task: "filed-task",
		Title: "Filed", Content: unitTaskBody(), Priority: "high",
	})

	if dirt := genuineDirtOf(t, vault.Root); len(dirt) != 0 {
		t.Fatalf("genuine dirt after a typed task write: %v — vp_vault_sync would refuse, "+
			"blocking sessions, transcripts, drawers and KG triples as well as this file", dirt)
	}

	// Drive the real refusal gate, not only its predicate. The remote name is
	// never resolved: a refusal happens before any network I/O.
	res, err := storage.SyncVault(vault.Root, []string{"origin"})
	if res.Refused {
		t.Errorf("SyncVault refused after a typed task write: %v (dirt %v)", err, res.GenuineDirt)
	}
}

// TestEveryMutatingTaskActionCommits is the pin that keeps this a property of
// the handler rather than a habit. Eight actions mutate a task file; a per-case
// call to the committer would be eight places for the ninth to forget, so every
// mutating return routes through taskWriteResult — and this asserts none of them
// escapes it, by measuring the worktree rather than by reading the code.
//
// The cases run in sequence against ONE vault on purpose: retire and cancel move
// the file, so they can only be exercised after something has created it, and
// running them in order also proves the MOVE commits both halves (source
// deletion and destination creation) rather than leaving half a rename dirty.
func TestEveryMutatingTaskActionCommits(t *testing.T) {
	vault := newGitBackedTestVault(t)
	parent := "an-epic"
	depends := []string{}

	cases := []struct {
		name   string
		params manageTaskParams
	}{
		{"create", manageTaskParams{Action: "create", Task: "t1", Title: "T1", Content: unitTaskBody(), Priority: "high"}},
		{"amend", manageTaskParams{Action: "amend", Task: "t1", Section: "Decision", Content: "A decision was recorded.\n"}},
		{"overwrite", manageTaskParams{Action: "overwrite", Task: "t1", Content: overwriteBodyFor("t1", "T1", "high")}},
		{"set_meta", manageTaskParams{Action: "set_meta", Task: "t1", Title: "T1 renamed", Priority: "medium"}},
		{"update_status", manageTaskParams{Action: "update_status", Task: "t1", Status: "in_progress"}},
		{"set_relations", manageTaskParams{Action: "set_relations", Task: "t1", Parent: &parent, DependsOn: &depends}},
		{"retire", manageTaskParams{Action: "retire", Task: "t1", ApprovedByHuman: true}},
		{"create-again", manageTaskParams{Action: "create", Task: "t2", Title: "T2", Content: unitTaskBody(), Priority: "low"}},
		{"cancel", manageTaskParams{Action: "cancel", Task: "t2"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.params
			p.Project = "test-proj"
			m := manageTask(t, vault, p)
			if m["commit"] != taskCommitCommitted {
				t.Errorf("commit = %v, want %q — this action does not route through the commit seam (%#v)",
					m["commit"], taskCommitCommitted, m)
			}
			if dirt := genuineDirtOf(t, vault.Root); len(dirt) != 0 {
				t.Errorf("action %q left genuine dirt: %v", c.name, dirt)
			}
			assertVaultClean(t, vault.Root)
		})
	}

	// A retire is a MOVE: both halves must be in the tree, and neither may be
	// left behind as dirt. Asserting the destination alone would pass on a
	// commit that recorded the new file and not the deletion of the old.
	tree := gitVaultRun(t, vault.Root, "ls-tree", "-r", "--name-only", "HEAD")
	if !strings.Contains(tree, "Projects/test-proj/tasks/done/t1.md") {
		t.Errorf("retired task missing from HEAD; tree:\n%s", tree)
	}
	if strings.Contains(tree, "Projects/test-proj/tasks/t1.md") {
		t.Errorf("retire committed the destination but not the source deletion; tree:\n%s", tree)
	}
	if !strings.Contains(tree, "Projects/test-proj/tasks/cancelled/t2.md") {
		t.Errorf("cancelled task missing from HEAD; tree:\n%s", tree)
	}
}

// overwriteBodyFor renders a whole task file whose header matches what the
// handler's smuggling guard expects to find unchanged.
func overwriteBodyFor(slug, title, priority string) string {
	return "# " + title + "\n\n**Status:** pending\n**Priority:** " + priority + "\n\n" +
		"## Context\n\nA rewritten preamble and body for " + slug + ", long enough to be a real plan. " +
		strings.Repeat("More prose. ", 12) + "\n"
}

// TestTaskWriteCommitStagesOnlyTheTaskPaths is the never-widen invariant, and it
// is written to catch the THREE ways this commit can grow past what it was asked
// to record. An earlier version of it claimed to catch all three and caught one:
// its only decoy lived in Knowledge/, so mutating commitTaskWrite to pass the
// tasks DIRECTORY instead of three explicit files left the whole suite green.
//
// A test whose docstring names failure modes it does not exercise is worse than
// one that claims less, because it is read as coverage. Each decoy below exists
// to redden a specific mutation, and each is named for it:
//
//  1. vaultRootDecoy — pass vault.Root, or any ancestor of the task file. Caught
//     by content elsewhere in the vault entirely.
//  2. tasksDirDecoy — pass Projects/<p>/tasks. This is the widening a maintainer
//     is MOST likely to reach for ("just commit the tasks dir"), and it is the
//     one the old test could not see, because a directory pathspec sweeps
//     siblings of the written file and nothing else. It stands in for the real
//     hazard: a task file a human edited in Obsidian, which the whole option-1
//     rejection turns on never auto-committing.
//  3. stagedDecoy — already in the index when the write happens. Caught only
//     once storage.CommitAndPushPaths commits with a pathspec; a pathspec-less
//     `git commit` records the whole index no matter how carefully this caller
//     scopes its paths, which is how correct scoping here bought nothing.
func TestTaskWriteCommitStagesOnlyTheTaskPaths(t *testing.T) {
	vault := newGitBackedTestVault(t)

	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(vault.Root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	const (
		vaultRootDecoy = "Knowledge/hand-written.md"
		tasksDirDecoy  = "Projects/test-proj/tasks/hand-edited-in-obsidian.md"
		stagedDecoy    = "Knowledge/already-staged.md"
	)
	write(vaultRootDecoy, "a human wrote this\n")
	write(tasksDirDecoy, "# Hand edited\n\n**Status:** pending\n**Priority:** high\n\nprose a human typed\n")
	write(stagedDecoy, "a human staged this\n")
	gitVaultRun(t, vault.Root, "add", "--", stagedDecoy)
	// Premise, asserted rather than assumed: it really is in the index, so a
	// pathspec-less commit really would carry it.
	if out := gitVaultRun(t, vault.Root, "diff", "--cached", "--name-only"); !strings.Contains(out, stagedDecoy) {
		t.Fatalf("test premise broken: %s is not staged; index: %q", stagedDecoy, out)
	}

	manageTask(t, vault, manageTaskParams{
		Project: "test-proj", Action: "create", Task: "filed-task",
		Title: "Filed", Content: unitTaskBody(), Priority: "high",
	})

	tree := gitVaultRun(t, vault.Root, "ls-tree", "-r", "--name-only", "HEAD")
	if !strings.Contains(tree, "Projects/test-proj/tasks/filed-task.md") {
		t.Fatalf("the written task is missing from HEAD, so this test is measuring nothing; tree:\n%s", tree)
	}
	for _, tc := range []struct{ decoy, mutation string }{
		{vaultRootDecoy, "the path list was widened to the vault root or a Projects ancestor"},
		{tasksDirDecoy, "the path list was widened to the tasks DIRECTORY — a human's Obsidian edit is now in a machine-authored commit"},
		{stagedDecoy, "the commit is not pathspec-scoped, so it recorded the whole index"},
	} {
		if strings.Contains(tree, tc.decoy) {
			t.Errorf("%s was committed: %s.\nsubject %q\ntree:\n%s",
				tc.decoy, tc.mutation, gitVaultRun(t, vault.Root, "log", "-1", "--pretty=%s"), tree)
		}
	}

	// All three are still the human's to deal with — uncommitted, and the staged
	// one still staged. Dropping them from the index would be a quieter way of
	// taking them away.
	status := gitVaultRun(t, vault.Root, "status", "--porcelain", "-uall")
	for _, decoy := range []string{vaultRootDecoy, tasksDirDecoy, stagedDecoy} {
		if !strings.Contains(status, decoy) {
			t.Errorf("%s is no longer dirty — it was staged away or committed; status:\n%s", decoy, status)
		}
	}
	if out := gitVaultRun(t, vault.Root, "diff", "--cached", "--name-only"); !strings.Contains(out, stagedDecoy) {
		t.Errorf("the pre-staged decoy left the index; index: %q", out)
	}
}

// TestTaskCommitFailureKeepsTheWriteAndReportsIt is the failure-mode contract,
// and it is the assertion most worth having.
//
// A commit can fail for reasons that have nothing to do with the write: a
// concurrent git process, a missing identity, a read-only .git. When it does, the
// task file is ALREADY on disk. Two outcomes are forbidden:
//
//   - returning an error, which tells the caller the WRITE failed. That is false,
//     and both reactions to it are harmful — a retried `create` errors with
//     "already exists" (two contradictory failures for one successful write), and
//     an abandoned write leaves the content on disk that the agent believes is
//     gone, so it is never synced and may be re-derived differently.
//   - rolling the write back, which destroys the thing this change exists to make
//     durable.
//
// The failure is injected the way it actually happens: an index.lock left by
// another git process, which makes `git add` fatal (exit 128) while every read
// before it still succeeds.
func TestTaskCommitFailureKeepsTheWriteAndReportsIt(t *testing.T) {
	vault := newGitBackedTestVault(t)

	lock := filepath.Join(vault.Root, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	m := manageTask(t, vault, manageTaskParams{
		Project: "test-proj", Action: "create", Task: "filed-task",
		Title: "Filed", Content: unitTaskBody(), Priority: "high",
	})

	// The WRITE is reported as the success it is.
	if m["status"] != "created" {
		t.Errorf("status = %v, want \"created\" — a failed commit must not be reported as a failed write", m["status"])
	}
	if m["commit"] != taskCommitFailed {
		t.Fatalf("commit = %v, want %q (result %#v)", m["commit"], taskCommitFailed, m)
	}
	detail, _ := m["commit_error"].(string)
	if detail == "" {
		t.Fatal("commit_error is empty — the caller is told a commit failed and not what to do about it")
	}
	if !strings.Contains(detail, "vp_vault_sync") {
		t.Errorf("commit_error names no remedy: %q", detail)
	}

	// The write survives, whole, with the content that was asked for.
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	meta, body, err := vault.GetTask("test-proj", "filed-task")
	if err != nil {
		t.Fatalf("the task is gone after a failed commit — the write was lost: %v", err)
	}
	if meta.Title != "Filed" {
		t.Errorf("title = %q, want %q", meta.Title, "Filed")
	}
	if !strings.Contains(body, "A real plan line describing what to do and why it matters.") {
		t.Errorf("body does not carry what was written:\n%s", body)
	}

	// And it is still dirty, which is what hands it to the detection half: the
	// next bootstrap raises it. Option 4 is the backstop for option 2's
	// failures, not only for the writes option 2 cannot reach.
	dirt := genuineDirtOf(t, vault.Root)
	if len(dirt) == 0 {
		t.Error("no genuine dirt after a failed commit — nothing is left for the bootstrap alert to report")
	}
}

// TestTaskWriteOnNonGitVaultIsASilentNoOp covers the vaults with no repository
// behind them (every unit-test vault in this package, and any deployment that
// has not run `git init`). The probe treats a non-repo as clean, deliberately,
// so the write must succeed and the receipt must say plainly that nothing was
// committed rather than reporting a failure nobody can act on.
func TestTaskWriteOnNonGitVaultIsASilentNoOp(t *testing.T) {
	vault := newTestVault(t)
	seedProject(t, vault, "test-proj")

	m := manageTask(t, vault, manageTaskParams{
		Project: "test-proj", Action: "create", Task: "filed-task",
		Title: "Filed", Content: unitTaskBody(), Priority: "high",
	})

	if m["status"] != "created" {
		t.Errorf("status = %v, want \"created\"", m["status"])
	}
	if m["commit"] != taskCommitNoChange {
		t.Errorf("commit = %v, want %q on a vault with no git repository", m["commit"], taskCommitNoChange)
	}
	if _, _, err := vault.GetTask("test-proj", "filed-task"); err != nil {
		t.Fatalf("GetTask: %v", err)
	}
}

// TestTaskCommitMessageNamesTheActionAndTask pins that the commit subject claims
// only what it can check.
//
// tidy's message — "vault tidy: sweep N capture artifacts" — is the counter-
// example the option-1 rejection turns on: pointed at task paths it would assert
// machine provenance the classifier cannot establish. Here the assertion is true
// by construction, because this message is only ever written on the far side of a
// vp_manage_task write that just succeeded. It must therefore stay specific to
// that write and must not borrow the artifact vocabulary.
func TestTaskCommitMessageNamesTheActionAndTask(t *testing.T) {
	vault := newGitBackedTestVault(t)
	manageTask(t, vault, manageTaskParams{
		Project: "test-proj", Action: "create", Task: "filed-task",
		Title: "Filed", Content: unitTaskBody(), Priority: "high",
	})

	subject := gitVaultRun(t, vault.Root, "log", "-1", "--pretty=%s")
	for _, want := range []string{"create", "test-proj", "filed-task"} {
		if !strings.Contains(subject, want) {
			t.Errorf("commit subject %q does not name %q", subject, want)
		}
	}
	if strings.Contains(subject, "capture artifact") {
		t.Errorf("commit subject %q borrows tidy's capture-artifact claim, which is not true of a task file", subject)
	}
}
