// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A pull aborts a conflicted merge it started, and never touches a merge it
// did not start (task vault-pull-leaves-a-conflicted-merge-in-the-shared-tree).
// Before, a conflicted pull left MERGE_HEAD in the shared vault, and every
// typed writer failed with "cannot do a partial commit during a merge" until
// a human resolved it.

// pullConflictWorld is a host whose next pull of origin conflicts on
// notes.md: origin and the host each committed a different notes.md over a
// shared seed. Each name in mirrors is one more remote, already holding the
// seed, that would merge cleanly.
func pullConflictWorld(t *testing.T, mirrors ...string) (dir, origin string) {
	t.Helper()
	dir = initTestRepo(t)
	origin = initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", origin)
	writeFile(t, dir, "notes.md", "SEED\n")
	gitRun(t, dir, "add", "--", "notes.md")
	gitRun(t, dir, "commit", "-q", "-m", "seed notes")
	gitRun(t, dir, "push", "-q", "origin", "main")
	for _, m := range mirrors {
		gitRun(t, dir, "remote", "add", m, initBareRemote(t))
		gitRun(t, dir, "push", "-q", m, "main")
	}
	advanceRemote(t, origin, "notes.md", "REMOTE\n")
	writeFile(t, dir, "notes.md", "LOCAL\n")
	gitRun(t, dir, "commit", "-q", "-am", "local notes")
	return dir, origin
}

func unmergedIndex(t *testing.T, dir string) string {
	t.Helper()
	return gitRun(t, dir, "ls-files", "-u")
}

// The conflict is aborted: HEAD, the conflicted file and the host's unrelated
// dirt are as they were, and the error names the paths, the ref, the tip and
// the remedy. Mutants: no abort; the paths listed after the abort.
func TestPull_ConflictIsAbortedAndNamed(t *testing.T) {
	dir, _ := pullConflictWorld(t)
	writeFile(t, dir, "README.md", "the host's own unfinished edit\n")
	writeFile(t, dir, "scratch.txt", "untracked\n")
	head := gitRun(t, dir, "rev-parse", "HEAD")

	res, err := Pull(dir, []string{"origin"})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if mergeInProgress(dir) || unmergedIndex(t, dir) != "" {
		t.Fatalf("the conflicted merge was left in the vault: MERGE_HEAD %v, unmerged %q", mergeInProgress(dir), unmergedIndex(t, dir))
	}
	if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved %s -> %s", head, got)
	}
	for rel, want := range map[string]string{
		"notes.md":    "LOCAL\n",
		"README.md":   "the host's own unfinished edit\n",
		"scratch.txt": "untracked\n",
	} {
		if got := readFile(t, dir, rel); got != want {
			t.Errorf("%s = %q after the abort, want %q", rel, got, want)
		}
	}

	rerr := res.RemoteResults["origin"]
	var conflict *mergeConflictError
	if !errors.As(rerr, &conflict) {
		t.Fatalf("RemoteResults[origin] = %T %v, want a *mergeConflictError", rerr, rerr)
	}
	tip := gitRun(t, dir, "rev-parse", "--short", "origin/main")
	for _, want := range []string{
		"conflicted on notes.md",
		"origin/main at " + tip,
		"was aborted, so nothing was merged",
		"the pull from origin was NOT applied",
		"merge origin/main",
		"vp vault sync",
	} {
		if !strings.Contains(rerr.Error(), want) {
			t.Errorf("the error must say %q, got %v", want, rerr)
		}
	}
	if !strings.Contains(res.RemoteOutput["origin"], "CONFLICT") {
		t.Errorf("git's merge output was not kept: %q", res.RemoteOutput["origin"])
	}
	if v := RemoteVerdict(OpPull, res.RemoteResults, ""); v == "" {
		t.Error("an aborted pull must fail the verdict")
	}
}

// The jam itself: after a conflicted pull, a typed writer's pathspec commit
// goes through. At 6d6d91b it failed "cannot do a partial commit during a
// merge". Mutant: no abort.
func TestPull_ConflictNeverBlocksATypedWriter(t *testing.T) {
	dir, _ := pullConflictWorld(t)
	if _, err := Pull(dir, []string{"origin"}); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	const note = "Projects/p/sessions/s.md"
	writeFile(t, dir, note, "a session\n")
	if _, err := CommitAndPushPaths(dir, "session note", []string{note}, false); err != nil {
		t.Fatalf("a typed writer was blocked after the pull: %v", err)
	}
	if subj := gitRun(t, dir, "log", "-1", "--format=%s"); subj != "session note" {
		t.Errorf("HEAD subject %q, want the writer's commit", subj)
	}
}

// R1 and the HEAD bug: a merge vp did not start is never touched. (a) The
// template heal used to run first and discard a human's staged resolution of
// a template whose resolved bytes equal the remote's. (b) The pull used to
// feed the human's OLD unmerged entries to the derived heal, which concluded
// their merge with `commit --no-edit`. Mutant: pullCore's in-progress refusal
// removed.
func TestPull_NeverAbortsAMergeItDidNotStart(t *testing.T) {
	t.Run("a human's staged template resolution survives", func(t *testing.T) {
		const tmpl = "Templates/commands/x.md"
		dir := initTestRepo(t)
		origin := initBareRemote(t)
		gitRun(t, dir, "remote", "add", "origin", origin)
		writeFile(t, dir, "notes.md", "SEED\n")
		writeFile(t, dir, tmpl, "SEED\n")
		gitRun(t, dir, "add", "--", "notes.md", tmpl)
		gitRun(t, dir, "commit", "-q", "-m", "seed")
		gitRun(t, dir, "push", "-q", "origin", "main")
		advanceRemoteMulti(t, origin, func(clone string) {
			writeFile(t, clone, "notes.md", "REMOTE\n")
			writeFile(t, clone, tmpl, "REMOTE\n")
		})
		writeFile(t, dir, "notes.md", "LOCAL\n")
		writeFile(t, dir, tmpl, "LOCAL\n")
		gitRun(t, dir, "commit", "-q", "-am", "local")

		// A human merges by hand, and resolves the template to the remote's
		// version, leaving notes.md for later.
		gitRun(t, dir, "fetch", "-q", "origin")
		if _, err := gitCmd(dir, mergeTimeout, "merge", "origin/main"); err == nil {
			t.Fatal("the hand merge was meant to conflict")
		}
		gitRun(t, dir, "checkout", "--theirs", "--", tmpl)
		gitRun(t, dir, "add", "--", tmpl)
		head := gitRun(t, dir, "rev-parse", "HEAD")
		staged := gitRun(t, dir, "ls-files", "-s", "--", tmpl)

		res, err := Pull(dir, []string{"origin"})
		var unsafe *vaultTreeUnsafeError
		if !errors.As(err, &unsafe) || !strings.Contains(err.Error(), "a merge (MERGE_HEAD)") {
			t.Errorf("Pull = %v, want a refusal naming the merge in progress", err)
		}
		if got := readFile(t, dir, tmpl); got != "REMOTE\n" {
			t.Errorf("the human's resolution of %s was discarded: %q", tmpl, got)
		}
		if got := gitRun(t, dir, "ls-files", "-s", "--", tmpl); got != staged {
			t.Errorf("the staged resolution changed: %q -> %q", staged, got)
		}
		if len(res.HealedTemplates) > 0 || len(res.RemoteResults) > 0 {
			t.Errorf("the pull ran: healed %q, results %v", res.HealedTemplates, res.RemoteResults)
		}
		if !mergeInProgress(dir) || gitRun(t, dir, "ls-files", "-u", "--", "notes.md") == "" {
			t.Error("the human's merge was concluded or aborted")
		}
		if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
			t.Errorf("HEAD moved %s -> %s", head, got)
		}
	})

	t.Run("a human's derived-only conflict is not healed and committed", func(t *testing.T) {
		w := newMergeWorld(t)
		lag := w.host(t)
		editDrawer(t, lag)
		w.pushMigration(t)
		gitRun(t, lag, "fetch", "-q", "origin")
		if _, err := gitCmd(lag, mergeTimeout, "merge", "origin/main"); err == nil {
			t.Fatal("the hand merge was meant to conflict")
		}
		head := gitRun(t, lag, "rev-parse", "HEAD")
		calls := spyHeal(t)

		_, err := Pull(lag, []string{"origin"})
		var unsafe *vaultTreeUnsafeError
		if !errors.As(err, &unsafe) || !strings.Contains(err.Error(), "a merge (MERGE_HEAD)") {
			t.Errorf("Pull = %v, want a refusal naming the merge in progress", err)
		}
		if *calls != 0 {
			t.Errorf("the heal ran %d times on a merge vp did not start", *calls)
		}
		if got := gitRun(t, lag, "rev-parse", "HEAD"); got != head {
			t.Errorf("HEAD moved %s -> %s: the human's merge was concluded", head, got)
		}
		if !mergeInProgress(lag) || gitRun(t, lag, "ls-files", "-u", "--", fixtureDrawer) == "" {
			t.Error("the human's merge is no longer in progress with the drawer unmerged")
		}
	})
}

// R4: a killed merge is reported by the killed-merge wording and its
// index.lock clause, is never aborted, removes nothing, and stops the sweep.
// "Nothing removed" alone passes at 6d6d91b; the wording does not. Mutant:
// the killedMergeError check removed.
func TestPull_KilledMergeIsNotAborted(t *testing.T) {
	for _, tc := range []struct{ mode, how string }{
		{"hang-merge", "killed at its deadline"},
		{"signal-merge", "killed by a signal"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			dir := initTestRepo(t)
			origin := initBareRemote(t)
			gitRun(t, dir, "remote", "add", "origin", origin)
			gitRun(t, dir, "remote", "add", "backup", initBareRemote(t))
			gitRun(t, dir, "push", "-q", "origin", "main")
			gitRun(t, dir, "push", "-q", "backup", "main")
			advanceRemote(t, origin, "remote.txt", "from other\n")
			head := gitRun(t, dir, "rev-parse", "HEAD")

			prev := mergeTimeout
			mergeTimeout = 300 * time.Millisecond
			t.Cleanup(func() { mergeTimeout = prev })
			logPath, setMode := reconcileGitShim(t)
			setMode(tc.mode)
			res, err := Pull(dir, []string{"origin", "backup"})
			setMode("")
			if err != nil {
				t.Fatalf("Pull: %v", err)
			}

			rerr := res.RemoteResults["origin"]
			var unsafe *vaultTreeUnsafeError
			if !errors.As(rerr, &unsafe) {
				t.Fatalf("RemoteResults[origin] = %T %v, want a *vaultTreeUnsafeError", rerr, rerr)
			}
			lock := filepath.Join(dir, ".git", "index.lock")
			for _, want := range []string{"the merge of origin/main was " + tc.how, lock + " was left behind"} {
				if !strings.Contains(rerr.Error(), want) {
					t.Errorf("the error must say %q, got %v", want, rerr)
				}
			}
			if tc.mode == "hang-merge" && !errors.Is(rerr, context.DeadlineExceeded) {
				t.Errorf("the deadline must stay in the chain: %v", rerr)
			}
			if _, statErr := os.Stat(lock); statErr != nil {
				t.Errorf("index.lock was removed: %v", statErr)
			}
			raw, _ := os.ReadFile(logPath)
			if strings.Contains(string(raw), "merge --abort") {
				t.Errorf("a killed merge was aborted:\n%s", raw)
			}
			if strings.Contains(string(raw), "fetch backup") {
				t.Errorf("the sweep went on to backup after a killed merge:\n%s", raw)
			}
			if skip := res.RemoteResults["backup"]; skip == nil || !strings.Contains(skip.Error(), "skipped: origin left the vault in a state nothing may merge into") {
				t.Errorf("backup = %v, want skipped after the killed merge", skip)
			}
			if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
				t.Errorf("HEAD moved %s -> %s", head, got)
			}
		})
	}
}

// R3: a failed abort leaves the conflicted merge in the vault, so it is
// reported as unsafe, names the abort's own failure, and stops the sweep.
// Mutant: the failed abort returned as a plain conflict.
func TestPull_FailedAbortIsTreeUnsafe(t *testing.T) {
	dir, _ := pullConflictWorld(t, "backup")
	_, setMode := reconcileGitShim(t)
	setMode("fail-merge-abort")
	res, err := Pull(dir, []string{"origin", "backup"})
	setMode("")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	rerr := res.RemoteResults["origin"]
	var unsafe *vaultTreeUnsafeError
	if !errors.As(rerr, &unsafe) {
		t.Fatalf("RemoteResults[origin] = %T %v, want a *vaultTreeUnsafeError", rerr, rerr)
	}
	for _, want := range []string{"abort failed", "shim: merge --abort refused", "still in the vault", "notes.md"} {
		if !strings.Contains(rerr.Error(), want) {
			t.Errorf("the error must say %q, got %v", want, rerr)
		}
	}
	if strings.Contains(rerr.Error(), "was aborted") {
		t.Errorf("the error claims an abort that failed: %v", rerr)
	}
	if skip := res.RemoteResults["backup"]; skip == nil || !strings.Contains(skip.Error(), "skipped: origin left the vault in a state nothing may merge into") {
		t.Errorf("backup = %v, want skipped", skip)
	}
}

// R2: the sweep rule, one row per failure kind. Mutants: a conflict made to
// go on; a refused merge or a failed untrack made to stop.
func TestPullSweepStops(t *testing.T) {
	conflict := &mergeConflictError{err: errors.New("exit status 1"), ref: "origin/main", paths: []string{"notes.md"}}
	failedAbort := &mergeConflictError{err: errors.New("exit status 1"), ref: "origin/main", abortErr: errors.New("abort")}
	for _, tc := range []struct {
		name string
		err  error
		stop bool
		why  string
	}{
		{"conflict", conflict, true, "origin conflicted and its merge was aborted"},
		{"failed abort", &vaultTreeUnsafeError{failedAbort}, true, "state nothing may merge into"},
		{"killed merge", &vaultTreeUnsafeError{errors.New("killed")}, true, "state nothing may merge into"},
		{"departure", &DepartedWorkError{Remote: "origin"}, true, "carries a departure"},
		{"refused before it started", &mergeNotStartedError{errors.New("would be overwritten")}, false, ""},
		{"untrack failed", &derivedUntrackError{errors.New("untrack")}, false, ""},
		{"wrapped refusal", errors.Join(errors.New("ctx"), &mergeNotStartedError{errors.New("x")}), false, ""},
		{"unrecognised", errors.New("something else"), true, "does not merge past"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			why, stop := pullSweepStops("origin", tc.err)
			if stop != tc.stop || !strings.Contains(why, tc.why) {
				t.Errorf("pullSweepStops = (%q, %v), want stop %v with %q", why, stop, tc.stop, tc.why)
			}
		})
	}
}

// R2, go on: a merge git refused before it changed anything leaves the tree
// as it was, so the next mirror is still merged. Mutant: a refusal made to
// stop the sweep.
func TestPull_RefusedMergeMovesOn(t *testing.T) {
	dir := initTestRepo(t)
	origin := initBareRemote(t)
	backup := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", origin)
	gitRun(t, dir, "remote", "add", "backup", backup)
	writeFile(t, dir, "a.md", "SEED\n")
	gitRun(t, dir, "add", "--", "a.md")
	gitRun(t, dir, "commit", "-q", "-m", "seed a")
	gitRun(t, dir, "push", "-q", "origin", "main")
	gitRun(t, dir, "push", "-q", "backup", "main")
	advanceRemote(t, origin, "a.md", "REMOTE\n")
	advanceRemote(t, backup, "b.md", "from backup\n")
	writeFile(t, dir, "a.md", "the host's uncommitted edit\n")

	res, err := Pull(dir, []string{"origin", "backup"})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	var notStarted *mergeNotStartedError
	if rerr := res.RemoteResults["origin"]; !errors.As(rerr, &notStarted) {
		t.Fatalf("RemoteResults[origin] = %T %v, want a *mergeNotStartedError", rerr, rerr)
	}
	if rerr := res.RemoteResults["backup"]; rerr != nil {
		t.Fatalf("backup = %v, want merged", rerr)
	}
	if got := readFile(t, dir, "b.md"); got != "from backup\n" {
		t.Errorf("backup's commit was not merged: b.md = %q", got)
	}
	if got := readFile(t, dir, "a.md"); got != "the host's uncommitted edit\n" {
		t.Errorf("the host's edit changed: %q", got)
	}
}

// R2, go on: a merge that stands but whose derived untrack failed is a clean
// merge result, so the next mirror is still attempted. Mutant: an untrack
// failure made to stop the sweep.
func TestPull_UntrackFailureMovesOn(t *testing.T) {
	_, lag, _ := laggingAddWorld(t)
	backup := initBareRemote(t)
	gitRun(t, lag, "remote", "add", "backup", backup)
	gitRun(t, lag, "push", "-q", "backup", "main")
	advanceRemote(t, backup, "b.md", "from backup\n")
	prev := untrackBeforeCommit
	untrackBeforeCommit = func() error { return errors.New("injected commit failure") }
	t.Cleanup(func() { untrackBeforeCommit = prev })

	res, err := Pull(lag, []string{"origin", "backup"})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	var ue *derivedUntrackError
	if rerr := res.RemoteResults["origin"]; !errors.As(rerr, &ue) {
		t.Fatalf("RemoteResults[origin] = %T %v, want the untrack failure", rerr, rerr)
	}
	if rerr := res.RemoteResults["backup"]; rerr != nil && strings.Contains(rerr.Error(), "skipped") {
		t.Fatalf("backup was skipped after an untrack failure: %v", rerr)
	}
	if got := readFile(t, lag, "b.md"); got != "from backup\n" {
		t.Errorf("backup's commit was not merged: b.md = %q", got)
	}
}

// A clean pull still carries git's merge output. Mutant: RemoteOutput not
// filled from the shared merge.
func TestPull_CleanMergeKeepsGitOutput(t *testing.T) {
	dir := initTestRepo(t)
	origin := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", origin)
	gitRun(t, dir, "push", "-q", "origin", "main")
	advanceRemote(t, origin, "remote.txt", "from other\n")
	res, err := Pull(dir, []string{"origin"})
	if err != nil || res.RemoteResults["origin"] != nil {
		t.Fatalf("Pull = %v / %v", err, res.RemoteResults["origin"])
	}
	if out := res.RemoteOutput["origin"]; !strings.Contains(out, "remote.txt") {
		t.Errorf("RemoteOutput[origin] = %q, want git's merge output", out)
	}
}

// R7: SyncVault used to drop pullCore's error (`pull, _ :=`), so a pull that
// refused before touching anything left an empty verdict, and the sync went on
// to push. Mutant: the error dropped again.
func TestSyncVault_PullRefusalIsNotDropped(t *testing.T) {
	dir, bare := syncSeedRemote(t)
	commitLocal(t, dir, "ahead.md", "unpushed\n")
	remoteBefore := gitRun(t, bare, "rev-parse", "main")
	// A rebase stopped by someone else, with a clean tree: nothing for the
	// refuse-on-dirt step to see, so only pullCore's own refusal stands
	// between this sync and the push. git reads the state directory alone.
	if err := os.Mkdir(filepath.Join(dir, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := SyncVault(dir, []string{"origin"})
	if err == nil || !strings.Contains(err.Error(), "refusing to pull: a rebase is in progress") {
		t.Fatalf("SyncVault = %v, want pullCore's refusal", err)
	}
	if res.Push != nil {
		t.Error("the push ran after the pull refused")
	}
	if got := gitRun(t, bare, "rev-parse", "main"); got != remoteBefore {
		t.Errorf("the remote moved %s -> %s", remoteBefore, got)
	}
}

// pickWorld is a host whose origin changed notes.md and Templates/commands/x.md
// while the host committed its own notes.md, fetched but not merged.
func pickWorld(t *testing.T) string {
	t.Helper()
	const tmpl = "Templates/commands/x.md"
	dir := initTestRepo(t)
	origin := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", origin)
	writeFile(t, dir, "notes.md", "SEED\n")
	writeFile(t, dir, tmpl, "SEED\n")
	gitRun(t, dir, "add", "--", "notes.md", tmpl)
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	gitRun(t, dir, "push", "-q", "origin", "main")
	advanceRemoteMulti(t, origin, func(clone string) {
		writeFile(t, clone, "notes.md", "REMOTE\n")
		writeFile(t, clone, tmpl, "REMOTE\n")
	})
	writeFile(t, dir, "notes.md", "LOCAL\n")
	gitRun(t, dir, "commit", "-q", "-am", "local")
	gitRun(t, dir, "fetch", "-q", "origin")
	return dir
}

// Review fix 1: an unfinished operation that leaves NO marker file — here
// `cherry-pick -n` — is still someone else's. Its conflicted index entries are
// refused before anything runs. (a) The template heal used to discard the
// staged template and blame vp's merge; (b) on a migrated vault the derived
// heal used to delete the drawer and commit the human's half-finished pick.
// (c) A multi-commit cherry-pick stopped between commits leaves only
// .git/sequencer. Mutants: the unmerged-entries probe removed; the sequencer
// probe removed.
func TestPull_NeverTouchesAnOperationWithNoMarkerFile(t *testing.T) {
	const tmpl = "Templates/commands/x.md"
	wantRefused := func(t *testing.T, err error, want string) {
		t.Helper()
		var unsafe *vaultTreeUnsafeError
		if !errors.As(err, &unsafe) || !strings.Contains(err.Error(), want) {
			t.Errorf("Pull = %v, want a refusal saying %q", err, want)
		}
	}

	t.Run("cherry-pick -n with a staged template", func(t *testing.T) {
		dir := pickWorld(t)
		if _, err := gitCmd(dir, mergeTimeout, "cherry-pick", "-n", "origin/main"); err == nil {
			t.Fatal("the pick was meant to conflict")
		}
		if gitPathExists(t, dir, "CHERRY_PICK_HEAD") || gitPathExists(t, dir, "MERGE_HEAD") {
			t.Fatal("precondition: cherry-pick -n leaves no marker file")
		}
		head := gitRun(t, dir, "rev-parse", "HEAD")
		staged := gitRun(t, dir, "ls-files", "-s", "--", tmpl)
		status := gitRun(t, dir, "status", "--porcelain")

		res, err := Pull(dir, []string{"origin"})
		wantRefused(t, err, "the index holds conflicted entries vp did not create (notes.md)")
		if len(res.HealedTemplates) > 0 || len(res.RemoteResults) > 0 {
			t.Errorf("the pull ran: healed %q, results %v", res.HealedTemplates, res.RemoteResults)
		}
		if got := readFile(t, dir, tmpl); got != "REMOTE\n" {
			t.Errorf("the staged %s was discarded: %q", tmpl, got)
		}
		if got := gitRun(t, dir, "ls-files", "-s", "--", tmpl); got != staged {
			t.Errorf("the staged entry changed: %q -> %q", staged, got)
		}
		if got := gitRun(t, dir, "status", "--porcelain"); got != status {
			t.Errorf("the tree changed: %q -> %q", status, got)
		}
		if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
			t.Errorf("HEAD moved %s -> %s", head, got)
		}
	})

	t.Run("cherry-pick -n of the migration on a lagging host", func(t *testing.T) {
		w := newMergeWorld(t)
		lag := w.host(t)
		editDrawer(t, lag)
		w.pushMigration(t)
		gitRun(t, lag, "fetch", "-q", "origin")
		if _, err := gitCmd(lag, mergeTimeout, "cherry-pick", "-n", "origin/main"); err == nil {
			t.Fatal("the pick was meant to conflict")
		}
		head := gitRun(t, lag, "rev-parse", "HEAD")
		calls := spyHeal(t)

		_, err := Pull(lag, []string{"origin"})
		wantRefused(t, err, fixtureDrawer)
		if *calls != 0 {
			t.Errorf("the heal ran %d times on someone else's conflicted index", *calls)
		}
		if got := gitRun(t, lag, "rev-parse", "HEAD"); got != head {
			t.Errorf("HEAD moved %s -> %s: the human's pick was committed", head, got)
		}
		if gitRun(t, lag, "ls-files", "-u", "--", fixtureDrawer) == "" || !onDisk(lag, fixtureDrawer) {
			t.Error("the drawer is no longer conflicted and on disk")
		}
	})

	t.Run("a multi-commit cherry-pick stopped between commits", func(t *testing.T) {
		dir := pickWorld(t)
		// origin/main's notes.md change conflicts; resolving it and committing
		// by hand concludes that one commit and leaves the sequence stopped.
		advanceRemote(t, gitRun(t, dir, "remote", "get-url", "origin"), "later.md", "later\n")
		gitRun(t, dir, "fetch", "-q", "origin")
		if _, err := gitCmd(dir, mergeTimeout, "cherry-pick", "origin/main~1", "origin/main"); err == nil {
			t.Fatal("the pick was meant to conflict")
		}
		gitRun(t, dir, "checkout", "--theirs", "--", "notes.md")
		gitRun(t, dir, "add", "--", "notes.md")
		gitRun(t, dir, "commit", "-q", "--no-edit")
		if !gitPathExists(t, dir, "sequencer") || gitPathExists(t, dir, "CHERRY_PICK_HEAD") || gitRun(t, dir, "ls-files", "-u") != "" {
			t.Fatalf("precondition: want only .git/sequencer, got CHERRY_PICK_HEAD %v, unmerged %q",
				gitPathExists(t, dir, "CHERRY_PICK_HEAD"), gitRun(t, dir, "ls-files", "-u"))
		}
		head := gitRun(t, dir, "rev-parse", "HEAD")

		_, err := Pull(dir, []string{"origin"})
		wantRefused(t, err, "a multi-commit cherry-pick or revert (sequencer)")
		if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
			t.Errorf("HEAD moved %s -> %s", head, got)
		}
		if !gitPathExists(t, dir, "sequencer") {
			t.Error("the sequencer state was removed")
		}
	})
}

// captureWarnings routes slog's default logger at Warn and above into a
// buffer for the rest of the test.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// Every pull abort is logged at Warn, so bootstrap health surfaces it, and a
// failed abort says the conflicted merge is still in the vault rather than
// that it was aborted. Mutants: the Warn lowered to Debug; one message for
// both outcomes.
func TestPull_AbortIsLoggedAndAFailedAbortSaysSo(t *testing.T) {
	t.Run("aborted", func(t *testing.T) {
		dir, _ := pullConflictWorld(t)
		buf := captureWarnings(t)
		if _, err := Pull(dir, []string{"origin"}); err != nil {
			t.Fatalf("Pull: %v", err)
		}
		got := buf.String()
		for _, want := range []string{"level=WARN", "vp aborted it, nothing was merged", "remote=origin", "paths=[notes.md]"} {
			if !strings.Contains(got, want) {
				t.Errorf("the log must contain %q, got:\n%s", want, got)
			}
		}
		if strings.Contains(got, "abort FAILED") {
			t.Errorf("a successful abort was logged as failed:\n%s", got)
		}
	})
	t.Run("abort failed", func(t *testing.T) {
		dir, _ := pullConflictWorld(t)
		_, setMode := reconcileGitShim(t)
		setMode("fail-merge-abort")
		buf := captureWarnings(t)
		_, err := Pull(dir, []string{"origin"})
		setMode("")
		if err != nil {
			t.Fatalf("Pull: %v", err)
		}
		got := buf.String()
		for _, want := range []string{"level=WARN", "abort FAILED", "the conflicted merge is still in the vault", "shim: merge --abort refused"} {
			if !strings.Contains(got, want) {
				t.Errorf("the log must contain %q, got:\n%s", want, got)
			}
		}
		if strings.Contains(got, "nothing was merged") {
			t.Errorf("a failed abort was logged as aborted:\n%s", got)
		}
	})
}

// The departed-cache sweep runs after a pull that ran, and not after a
// pre-flight refusal, which fetched and merged nothing. Mutant: the sweep run
// before the refusal check, at either site.
func TestPull_RefusalSkipsTheDepartedSweep(t *testing.T) {
	spy := func(t *testing.T) *int {
		t.Helper()
		n := 0
		prev := sweepDepartedAfterPull
		sweepDepartedAfterPull = func(string) { n++ }
		t.Cleanup(func() { sweepDepartedAfterPull = prev })
		return &n
	}
	refused := func(t *testing.T) string {
		t.Helper()
		dir, _ := syncSeedRemote(t)
		if err := os.Mkdir(filepath.Join(dir, ".git", "rebase-merge"), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	t.Run("Pull refused", func(t *testing.T) {
		dir := refused(t)
		calls := spy(t)
		if _, err := Pull(dir, []string{"origin"}); err == nil {
			t.Fatal("want the pull refused")
		}
		if *calls != 0 {
			t.Errorf("the sweep ran %d times after a refused pull", *calls)
		}
	})
	t.Run("SyncVault refused", func(t *testing.T) {
		dir := refused(t)
		calls := spy(t)
		if _, err := SyncVault(dir, []string{"origin"}); err == nil {
			t.Fatal("want the sync refused")
		}
		if *calls != 0 {
			t.Errorf("the sweep ran %d times after a refused sync", *calls)
		}
	})
	t.Run("Pull ran", func(t *testing.T) {
		dir, _ := syncSeedRemote(t)
		calls := spy(t)
		if _, err := Pull(dir, []string{"origin"}); err != nil {
			t.Fatal(err)
		}
		if *calls != 1 {
			t.Errorf("the sweep ran %d times after a pull, want 1", *calls)
		}
	})
}
