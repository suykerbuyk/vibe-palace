// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// Toy-repo fixtures for the pull self-heal and the post-merge untrack (task
// tidy-pull-and-audit-behaviour-keyed-on-the-migration-marker, "Plan
// revisions" R1). S-numbers name the review's evidence scripts
// (marker-behaviour-review-scratch/git/run.sh, cleanadd.sh, untrack.sh).

// mergeWorld is one origin and the unmigrated v8 vault it started from.
type mergeWorld struct {
	origin string
}

func newMergeWorld(t *testing.T) *mergeWorld {
	t.Helper()
	seed := layUnmigratedDrawerVault(t)
	writeFile(t, seed, "notes.md", "x\n")
	writeFile(t, seed, "T/wrap.md", "mirror\n")
	// Project p lives on after the migration: without these, removing its only
	// palace files reads as the project leaving the vault (a departure).
	writeFile(t, seed, "palace/p/.surface", "surface = 8\n")
	writeFile(t, seed, "Projects/p/resume.md", "r\n")
	gitRun(t, seed, "add", "--", "notes.md", "T/wrap.md", "palace/p/.surface", "Projects/p/resume.md")
	gitRun(t, seed, "commit", "-q", "-m", "notes")
	origin := initBareRemote(t)
	gitRun(t, seed, "remote", "add", "origin", origin)
	gitRun(t, seed, "push", "-q", "origin", "main")
	return &mergeWorld{origin: origin}
}

// host clones origin as one more host of the vault.
func (w *mergeWorld) host(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "clone", "-q", "-b", "main", w.origin, ".")
	gitRun(t, dir, "config", "user.email", "host@example.com")
	gitRun(t, dir, "config", "user.name", "Host")
	return dir
}

// pushMigration has a fresh host make the migration commit and push it.
func (w *mergeWorld) pushMigration(t *testing.T) {
	t.Helper()
	mig := w.host(t)
	migrateFixture(t, mig)
	gitRun(t, mig, "push", "-q", "origin", "main")
}

// editDrawer commits a lagging host's edit of the tracked drawer.
func editDrawer(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, dir, fixtureDrawer, `{"id":"d1"}`+"\n"+`{"id":"lag"}`+"\n")
	gitRun(t, dir, "commit", "-q", "-am", "lag drawer edit")
}

// spyHeal counts calls of the conflict heal.
func spyHeal(t *testing.T) *int {
	t.Helper()
	n := 0
	prev := healConflicts
	healConflicts = func(v string, e []unmergedEntry) ([]string, []string, bool, error) {
		n++
		return prev(v, e)
	}
	t.Cleanup(func() { healConflicts = prev })
	return &n
}

func tracked(t *testing.T, dir, rel string) bool {
	t.Helper()
	return gitRun(t, dir, "ls-files", "--", rel) != ""
}

func onDisk(dir, rel string) bool {
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil
}

func mergeInProgress(dir string) bool {
	return onDisk(dir, ".git/MERGE_HEAD")
}

func originTracks(t *testing.T, w *mergeWorld, rel string) bool {
	t.Helper()
	return gitRun(t, w.origin, "ls-tree", "-r", "--name-only", "main", "--", rel) != ""
}

// Heal, a lagging host merges the migration (UD, S1), through pullCore. The
// merge concludes; the drawer is gone from the index and the working tree;
// the remote's result is overwritten to success; no healed/ directory exists;
// and SyncVault then goes on to push. Mutant: RemoteResults not overwritten;
// the bytes kept.
func TestHeal_LaggingHostPullsTheMigration(t *testing.T) {
	w := newMergeWorld(t)
	lag := w.host(t)
	editDrawer(t, lag)
	w.pushMigration(t)

	res, err := Pull(lag, []string{"origin"})
	if err != nil {
		t.Fatal(err)
	}
	if rerr := res.RemoteResults["origin"]; rerr != nil {
		t.Fatalf("RemoteResults[origin] = %v, want nil after the heal", rerr)
	}
	if !slices.Equal(res.Derived.Healed, []string{fixtureDrawer}) {
		t.Errorf("Healed = %q", res.Derived.Healed)
	}
	if mergeInProgress(lag) || tracked(t, lag, fixtureDrawer) || onDisk(lag, fixtureDrawer) {
		t.Errorf("merge in progress %v, tracked %v, on disk %v; want none", mergeInProgress(lag), tracked(t, lag, fixtureDrawer), onDisk(lag, fixtureDrawer))
	}
	if onDisk(lag, "palace/.local/index/p/healed") || onDisk(lag, "palace/p/healed") {
		t.Error("a healed/ quarantine exists")
	}
	sync, err := SyncVault(lag, []string{"origin"})
	if err != nil {
		t.Fatalf("SyncVault after the heal: %v", err)
	}
	if v := RemoteVerdict(OpPush, sync.Push.RemoteResults, "HEAD"); v != "" {
		t.Fatalf("push after the heal: %s", v)
	}
	if gitRun(t, w.origin, "rev-parse", "main") != gitRun(t, lag, "rev-parse", "HEAD") {
		t.Error("the healed merge did not reach origin")
	}
	if originTracks(t, w, "palace/p/drawers") {
		t.Error("origin tracks a drawer")
	}
}

// Heal, a migrated host merges a lagging drawer commit (DU, S2): the marker is
// on HEAD's side only, so MERGE_HEAD carries none; the merged index does.
// Driven through both reconcile callers of mergeFetchedTip. Mutant: reading
// the marker from MERGE_HEAD.
func TestHeal_MigratedHostMergesALaggingDrawerCommit(t *testing.T) {
	setup := func(t *testing.T) (*mergeWorld, string) {
		w := newMergeWorld(t)
		mig := w.host(t)
		migrateFixture(t, mig) // local, not pushed
		lag := w.host(t)
		editDrawer(t, lag)
		gitRun(t, lag, "push", "-q", "origin", "main")
		return w, mig
	}
	check := func(t *testing.T, w *mergeWorld, mig string, res *PushResult) {
		t.Helper()
		if v := RemoteVerdict(OpPush, res.RemoteResults, "HEAD"); v != "" {
			t.Fatalf("push: %s", v)
		}
		if !slices.Contains(res.Derived.Healed, fixtureDrawer) {
			t.Errorf("Healed = %q", res.Derived.Healed)
		}
		if mergeInProgress(mig) || tracked(t, mig, fixtureDrawer) || originTracks(t, w, fixtureDrawer) {
			t.Errorf("merge in progress %v, drawer tracked %v, on origin %v", mergeInProgress(mig), tracked(t, mig, fixtureDrawer), originTracks(t, w, fixtureDrawer))
		}
	}
	t.Run("rejected push (reconcileRejectedPush)", func(t *testing.T) {
		w, mig := setup(t)
		// No tracking ref: the already-ahead reconcile skips (fail-open), so the
		// push is rejected and the push-rejection reconcile merges.
		gitRun(t, mig, "update-ref", "-d", "refs/remotes/origin/main")
		writeFile(t, mig, "Projects/p/sessions/s.md", "s\n")
		res, err := CommitAndPushPaths(mig, "a session", []string{"Projects/p/sessions/s.md"}, true)
		if err != nil {
			t.Fatal(err)
		}
		check(t, w, mig, res)
	})
	t.Run("commit-and-push (reconcileIfAhead)", func(t *testing.T) {
		w, mig := setup(t)
		writeFile(t, mig, "Projects/p/sessions/s.md", "s\n")
		res, err := CommitAndPushPaths(mig, "a session", []string{"Projects/p/sessions/s.md"}, true)
		if err != nil {
			t.Fatal(err)
		}
		check(t, w, mig, res)
	})
	t.Run("mirror prune (reconcileIfAhead)", func(t *testing.T) {
		w, mig := setup(t)
		res, out, err := pruneMirrors(mig, []string{"T/wrap.md"}, true, true, acceptOnly(nil, "mirror\n"))
		if err != nil {
			t.Fatalf("prune: %v (%+v)", err, out)
		}
		check(t, w, mig, res)
	})
}

// A conflict on a non-derived file still aborts and strands, as before the heal
// existed.
func TestHeal_NonDerivedConflictStillAborts(t *testing.T) {
	w := newMergeWorld(t)
	mig := w.host(t)
	migrateFixture(t, mig)
	writeFile(t, mig, "notes.md", "migrator\n")
	gitRun(t, mig, "commit", "-q", "-am", "migrator notes")
	other := w.host(t)
	writeFile(t, other, "notes.md", "other\n")
	gitRun(t, other, "commit", "-q", "-am", "other notes")
	gitRun(t, other, "push", "-q", "origin", "main")
	writeFile(t, mig, "Projects/p/sessions/s.md", "s\n")
	res, err := CommitAndPushPaths(mig, "a session", []string{"Projects/p/sessions/s.md"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteResults["origin"] == nil || !res.Stranded() {
		t.Errorf("want the reconcile recorded and the commit stranded: %+v", res)
	}
	if mergeInProgress(mig) {
		t.Error("the conflicted merge was left in progress")
	}
}

// Fast-forward of the migration (S3): no heal call, exit 0, drawers removed by
// git itself. Mutant: entering the heal with no unmerged paths.
func TestHeal_FastForwardOfTheMigrationNeverCallsTheHeal(t *testing.T) {
	w := newMergeWorld(t)
	ff := w.host(t)
	w.pushMigration(t)
	calls := spyHeal(t)
	res, err := Pull(ff, []string{"origin"})
	if err != nil || res.RemoteResults["origin"] != nil {
		t.Fatalf("pull: %v / %v", err, res.RemoteResults["origin"])
	}
	if *calls != 0 {
		t.Errorf("heal called %d times on a fast-forward", *calls)
	}
	if onDisk(ff, fixtureDrawer) {
		t.Error("the drawer is still on disk after the fast-forward")
	}
}

// Refused merge (S4): an uncommitted drawer edit blocks the fast-forward. The
// result keeps git's own message and there is no heal call. Mutant: treating
// a refusal as a conflict.
func TestHeal_RefusedMergeIsLeftAlone(t *testing.T) {
	w := newMergeWorld(t)
	ff := w.host(t)
	writeFile(t, ff, fixtureDrawer, `{"id":"d1"}`+"\n"+`{"id":"enriched"}`+"\n")
	w.pushMigration(t)
	calls := spyHeal(t)
	res, err := Pull(ff, []string{"origin"})
	if err != nil {
		t.Fatal(err)
	}
	rerr := res.RemoteResults["origin"]
	if rerr == nil || !strings.Contains(res.RemoteOutput["origin"]+rerr.Error(), "would be overwritten by merge") {
		t.Errorf("want git's refusal kept: %v / %q", rerr, res.RemoteOutput["origin"])
	}
	if strings.Contains(rerr.Error(), "derived") {
		t.Errorf("the refusal was decorated by the heal: %v", rerr)
	}
	if *calls != 0 {
		t.Errorf("heal called %d times on a refused merge", *calls)
	}
}

// Mixed conflict, migrated (S6): a drawer UD plus a note UU. The heal is
// all-or-nothing, so nothing is healed: both pullCore and mergeFetchedTip
// abort, the drawer is restored, and the error names both paths. Mutant: a
// partial heal.
func TestHeal_MixedConflictIsNotHealed(t *testing.T) {
	setup := func(t *testing.T) string {
		w := newMergeWorld(t)
		lag := w.host(t)
		writeFile(t, lag, "notes.md", "lag\n")
		editDrawer(t, lag)
		mig := w.host(t)
		migrateFixture(t, mig)
		writeFile(t, mig, "notes.md", "migrator\n")
		gitRun(t, mig, "commit", "-q", "-am", "migrator notes")
		gitRun(t, mig, "push", "-q", "origin", "main")
		gitRun(t, lag, "fetch", "-q", "origin")
		return lag
	}
	t.Run("pullCore", func(t *testing.T) {
		lag := setup(t)
		res, err := Pull(lag, []string{"origin"})
		if err != nil {
			t.Fatal(err)
		}
		rerr := res.RemoteResults["origin"]
		var conflict *mergeConflictError
		if !errors.As(rerr, &conflict) || len(res.Derived.Healed) > 0 || mergeInProgress(lag) {
			t.Errorf("want the conflict aborted, nothing healed: %v, healed %q, in progress %v",
				rerr, res.Derived.Healed, mergeInProgress(lag))
		}
		if rerr != nil && (!strings.Contains(rerr.Error(), "notes.md") || !strings.Contains(rerr.Error(), fixtureDrawer)) {
			t.Errorf("the error must name both conflicting paths: %v", rerr)
		}
		if got, _ := os.ReadFile(filepath.Join(lag, fixtureDrawer)); !strings.Contains(string(got), "lag") {
			t.Errorf("drawer not restored by the abort: %q", got)
		}
	})
	t.Run("mergeFetchedTip", func(t *testing.T) {
		lag := setup(t)
		var rep DerivedMergeReport
		if _, err := mergeFetchedTip(lag, "origin", "main", &rep); err == nil {
			t.Fatal("want the conflict to fail the merge")
		}
		if mergeInProgress(lag) || len(rep.Healed) > 0 {
			t.Errorf("in progress %v, healed %q", mergeInProgress(lag), rep.Healed)
		}
		if got, _ := os.ReadFile(filepath.Join(lag, fixtureDrawer)); !strings.Contains(string(got), "lag") {
			t.Errorf("drawer not restored by the abort: %q", got)
		}
	})
}

// vault.toml itself unmerged: it is a non-derived path, so the heal does not
// run; and the merged-index reader refuses it rather than reading "unmigrated".
// Mutant: a missing stage 0 read as unmigrated.
func TestHeal_UnmergedManifestFailsClosed(t *testing.T) {
	w := newMergeWorld(t)
	lag := w.host(t)
	writeFile(t, lag, ".vibe-palace/vault.toml", "format = 2\n# lag\n")
	editDrawer(t, lag)
	w.pushMigration(t)
	gitRun(t, lag, "fetch", "-q", "origin")
	_, _ = gitCmd(lag, mergeTimeout, "merge", "origin/main")
	if _, err := migratedInTree(lag, fromMergedIndex); err == nil || !strings.Contains(err.Error(), "vault.toml is itself unmerged") {
		t.Fatalf("migratedInTree = %v; want an error naming the unmerged vault.toml", err)
	}
	gitRun(t, lag, "merge", "--abort")
	res, err := Pull(lag, []string{"origin"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Derived.Healed) > 0 || res.RemoteResults["origin"] == nil {
		t.Errorf("healed %q with vault.toml unmerged", res.Derived.Healed)
	}
}

// Non-ASCII derived path: git C-quotes it without -z. Mutant: reusing
// unmergedPaths.
func TestHeal_NonASCIIDerivedPath(t *testing.T) {
	const cafe = "palace/p/drawers/w/r/café.jsonl"
	w := newMergeWorld(t)
	seed := w.host(t)
	writeFile(t, seed, cafe, "1\n")
	gitRun(t, seed, "add", "--", cafe)
	gitRun(t, seed, "commit", "-q", "-m", "café")
	gitRun(t, seed, "push", "-q", "origin", "main")
	lag := w.host(t)
	writeFile(t, lag, cafe, "1\n2\n")
	gitRun(t, lag, "commit", "-q", "-am", "lag café")
	w.pushMigration(t)
	res, err := Pull(lag, []string{"origin"})
	if err != nil || res.RemoteResults["origin"] != nil {
		t.Fatalf("pull: %v / %v", err, res.RemoteResults["origin"])
	}
	if !slices.Contains(res.Derived.Healed, cafe) || onDisk(lag, cafe) {
		t.Errorf("café not healed: %q, on disk %v", res.Derived.Healed, onDisk(lag, cafe))
	}
}

// Clean merge re-tracks a drawer (cleanadd), at both sites: the path is
// untracked, the file is KEPT on disk (ignored), the vault is clean, and sync
// pushes. Mutant: no untrack step; one site only; the file deleted.
func TestUntrack_CleanMergeReTracksADrawer(t *testing.T) {
	const added = "palace/p/drawers/w2/b.jsonl"
	t.Run("pullCore", func(t *testing.T) {
		w := newMergeWorld(t)
		lag := w.host(t)
		writeFile(t, lag, added, "new\n")
		gitRun(t, lag, "add", "--", added)
		gitRun(t, lag, "commit", "-q", "-m", "lag adds a new drawer")
		gitRun(t, lag, "rm", "-q", "--", fixtureDrawer) // no conflict with the migration
		gitRun(t, lag, "commit", "-q", "-m", "lag drops the old drawer")
		w.pushMigration(t)
		res, err := Pull(lag, []string{"origin"})
		if err != nil || res.RemoteResults["origin"] != nil {
			t.Fatalf("pull: %v / %v", err, res.RemoteResults["origin"])
		}
		if !slices.Equal(res.Derived.Untracked, []string{added}) || tracked(t, lag, added) || !onDisk(lag, added) {
			t.Errorf("untracked %q; tracked %v; on disk %v", res.Derived.Untracked, tracked(t, lag, added), onDisk(lag, added))
		}
		if st := gitRun(t, lag, "status", "--porcelain"); st != "" {
			t.Errorf("vault not clean: %q", st)
		}
		if _, err := SyncVault(lag, []string{"origin"}); err != nil {
			t.Fatal(err)
		}
		if originTracks(t, w, added) {
			t.Error("origin tracks the re-tracked drawer")
		}
	})
	t.Run("mergeFetchedTip", func(t *testing.T) {
		w := newMergeWorld(t)
		mig := w.host(t)
		migrateFixture(t, mig)
		lag := w.host(t)
		writeFile(t, lag, added, "new\n")
		gitRun(t, lag, "add", "--", added)
		gitRun(t, lag, "commit", "-q", "-m", "lag adds a new drawer")
		gitRun(t, lag, "push", "-q", "origin", "main")
		writeFile(t, mig, "Projects/p/sessions/s.md", "s\n")
		res, err := CommitAndPushPaths(mig, "a session", []string{"Projects/p/sessions/s.md"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if v := RemoteVerdict(OpPush, res.RemoteResults, "HEAD"); v != "" {
			t.Fatalf("push: %s", v)
		}
		if !slices.Contains(res.Derived.Untracked, added) || originTracks(t, w, added) || !onDisk(mig, added) {
			t.Errorf("untracked %q; origin tracks it %v; on disk %v", res.Derived.Untracked, originTracks(t, w, added), onDisk(mig, added))
		}
	})
}

// The same clean merge on an unmigrated vault untracks nothing.
func TestUntrack_UnmigratedCleanMergeUntracksNothing(t *testing.T) {
	const added = "palace/p/drawers/w2/b.jsonl"
	w := newMergeWorld(t)
	lag := w.host(t)
	writeFile(t, lag, added, "new\n")
	gitRun(t, lag, "add", "--", added)
	gitRun(t, lag, "commit", "-q", "-m", "lag adds a new drawer")
	advanceRemote(t, w.origin, "notes2.md", "unrelated\n")
	res, err := Pull(lag, []string{"origin"})
	if err != nil || res.RemoteResults["origin"] != nil {
		t.Fatalf("pull: %v / %v", err, res.RemoteResults["origin"])
	}
	if len(res.Derived.Untracked) > 0 || !tracked(t, lag, added) {
		t.Errorf("untracked %q on an unmigrated vault", res.Derived.Untracked)
	}
}

// laggingAddWorld is a migrated origin and a host whose next pull merges a
// lagging drawer add, cleanly.
func laggingAddWorld(t *testing.T) (*mergeWorld, string, string) {
	t.Helper()
	const added = "palace/p/drawers/w2/b.jsonl"
	w := newMergeWorld(t)
	lag := w.host(t)
	writeFile(t, lag, added, "new\n")
	gitRun(t, lag, "add", "--", added)
	gitRun(t, lag, "commit", "-q", "-m", "lag adds a new drawer")
	gitRun(t, lag, "rm", "-q", "--", fixtureDrawer)
	gitRun(t, lag, "commit", "-q", "-m", "lag drops the old drawer")
	w.pushMigration(t)
	return w, lag, added
}

// Untrack retried on an up-to-date run. Run 1: the untrack fails and the push
// is skipped. Run 2, with no new remote commits ("Already up to date"): the
// untrack runs and the push goes ahead. Mutant: a "HEAD did not move" shortcut.
func TestUntrack_RetriedOnAnUpToDateRun(t *testing.T) {
	w, lag, added := laggingAddWorld(t)
	prev := untrackBeforeCommit
	untrackBeforeCommit = func() error { return errors.New("injected untrack failure") }
	res, err := SyncVault(lag, []string{"origin"})
	untrackBeforeCommit = prev
	if err == nil && (res.Push != nil && RemoteVerdict(OpPush, res.Push.RemoteResults, "HEAD") == "") {
		t.Fatal("run 1 pushed despite the failed untrack")
	}
	if originTracks(t, w, added) {
		t.Fatal("run 1 published the re-tracked drawer")
	}
	res, err = SyncVault(lag, []string{"origin"})
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if !slices.Contains(res.Pull.Derived.Untracked, added) {
		t.Errorf("run 2 did not untrack: %+v", res.Pull.Derived)
	}
	if v := RemoteVerdict(OpPush, res.Push.RemoteResults, "HEAD"); v != "" {
		t.Fatalf("run 2 push: %s", v)
	}
	if originTracks(t, w, added) {
		t.Error("origin tracks the drawer after run 2")
	}
}

// A drawer rewritten between the drop and the commit: the commit is from the
// index, so it still succeeds and leaves no staged deletion. Mutant: a
// path-scoped commit ("nothing to commit", a staged D left behind).
func TestUntrack_DrawerRewrittenBeforeTheCommit(t *testing.T) {
	_, lag, added := laggingAddWorld(t)
	prev := untrackBeforeCommit
	untrackBeforeCommit = func() error {
		return os.WriteFile(filepath.Join(lag, added), []byte("new\nenriched\n"), 0o644)
	}
	t.Cleanup(func() { untrackBeforeCommit = prev })
	res, err := Pull(lag, []string{"origin"})
	if err != nil || res.RemoteResults["origin"] != nil {
		t.Fatalf("pull: %v / %v", err, res.RemoteResults["origin"])
	}
	if tracked(t, lag, added) {
		t.Error("still tracked")
	}
	if st := gitRun(t, lag, "status", "--porcelain"); st != "" {
		t.Errorf("left %q", st)
	}
}

// A failure between the drop and the commit restores the index: no staged
// deletion is left to block the next merge. Mutant: no restore.
func TestUntrack_FailureRestoresTheIndex(t *testing.T) {
	_, lag, added := laggingAddWorld(t)
	prev := untrackBeforeCommit
	var before string
	untrackBeforeCommit = func() error { return errors.New("injected commit failure") }
	t.Cleanup(func() { untrackBeforeCommit = prev })
	res, err := Pull(lag, []string{"origin"})
	if err != nil {
		t.Fatal(err)
	}
	var ue *derivedUntrackError
	if rerr := res.RemoteResults["origin"]; !errors.As(rerr, &ue) {
		t.Fatalf("RemoteResults[origin] = %v, want the untrack failure", rerr)
	}
	before = gitRun(t, lag, "ls-tree", "HEAD", "--", added)
	if !tracked(t, lag, added) || before == "" {
		t.Error("the drawer's index entry was not restored")
	}
	if staged := gitRun(t, lag, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("staged after the failure: %q", staged)
	}
}

// Malformed manifest in the MERGED TREE: the incoming migration carries
// `authored_only = 5`. The heal fails closed: pullCore and mergeFetchedTip
// both abort with the error. Mutant: the error read as
// unmigrated (pullCore result without authored_only).
func TestHeal_MalformedMarkerInTheMergedTree(t *testing.T) {
	setup := func(t *testing.T) string {
		w := newMergeWorld(t)
		lag := w.host(t)
		editDrawer(t, lag)
		mig := w.host(t)
		migrateFixture(t, mig)
		writeMalformedMarker(t, mig)
		gitRun(t, mig, "commit", "-q", "-am", "a bad hand edit")
		gitRun(t, mig, "push", "-q", "origin", "main")
		gitRun(t, lag, "fetch", "-q", "origin")
		return lag
	}
	t.Run("pullCore", func(t *testing.T) {
		lag := setup(t)
		res, err := Pull(lag, []string{"origin"})
		if err != nil {
			t.Fatal(err)
		}
		wantMarkerErr(t, "pull", res.RemoteResults["origin"])
		if len(res.Derived.Healed) > 0 || mergeInProgress(lag) {
			t.Errorf("healed %q, in progress %v", res.Derived.Healed, mergeInProgress(lag))
		}
		if !tracked(t, lag, fixtureDrawer) {
			t.Error("the drawer was not restored by the abort")
		}
	})
	t.Run("mergeFetchedTip", func(t *testing.T) {
		lag := setup(t)
		_, err := mergeFetchedTip(lag, "origin", "main", nil)
		wantMarkerErr(t, "mergeFetchedTip", err)
		if mergeInProgress(lag) {
			t.Error("merge left in progress")
		}
	})
}

// Post-merge untrack with a malformed HEAD manifest: nothing is untracked, and
// the push is skipped.
func TestUntrack_MalformedHEADManifest(t *testing.T) {
	const added = "palace/p/drawers/w2/b.jsonl"
	w := newMergeWorld(t)
	lag := w.host(t)
	writeFile(t, lag, added, "new\n")
	gitRun(t, lag, "add", "--", added)
	gitRun(t, lag, "rm", "-q", "--", fixtureDrawer)
	gitRun(t, lag, "commit", "-q", "-m", "lag adds a new drawer")
	mig := w.host(t)
	migrateFixture(t, mig)
	writeMalformedMarker(t, mig)
	gitRun(t, mig, "commit", "-q", "-am", "a bad hand edit")
	gitRun(t, mig, "push", "-q", "origin", "main")
	res, err := Pull(lag, []string{"origin"})
	if err != nil {
		t.Fatal(err)
	}
	wantMarkerErr(t, "pull", res.RemoteResults["origin"])
	if !tracked(t, lag, added) {
		t.Error("untracked despite the malformed marker")
	}
	if v := RemoteVerdict(OpPull, res.RemoteResults, ""); v == "" {
		t.Error("the pull verdict would let SyncVault push")
	}
}

// UU on kg/entities.jsonl is reported by name and never resolved: the merge
// is aborted with the lag host's own entities kept.
func TestHeal_EntitiesBothChangedIsNamed(t *testing.T) {
	const ent = "palace/p/kg/entities.jsonl"
	w := newMergeWorld(t)
	seed := w.host(t)
	writeFile(t, seed, ent, `{"id":"a"}`+"\n")
	gitRun(t, seed, "add", "--", ent)
	gitRun(t, seed, "commit", "-q", "-m", "entities")
	gitRun(t, seed, "push", "-q", "origin", "main")
	lag := w.host(t)
	writeFile(t, lag, ent, `{"id":"lag"}`+"\n")
	gitRun(t, lag, "commit", "-q", "-am", "lag entities")
	other := w.host(t)
	writeFile(t, other, ent, `{"id":"other"}`+"\n")
	gitRun(t, other, "commit", "-q", "-am", "other entities")
	gitRun(t, other, "push", "-q", "origin", "main")
	res, err := Pull(lag, []string{"origin"})
	if err != nil {
		t.Fatal(err)
	}
	if rerr := res.RemoteResults["origin"]; rerr == nil || !strings.Contains(rerr.Error(), "unresolved conflict on "+ent) {
		t.Errorf("want the entities conflict named: %v", rerr)
	}
	if mergeInProgress(lag) {
		t.Error("the conflicted merge was left in progress")
	}
	if got := readFile(t, lag, ent); got != `{"id":"lag"}`+"\n" {
		t.Errorf("entities = %q, want the lag host's own line kept", got)
	}
}

// isDerivedPath requires a valid project slug segment.
func TestIsDerivedPath(t *testing.T) {
	for rel, want := range map[string]bool{
		"palace/p/drawers/w/r/drawers.jsonl":     true,
		"palace/p/ingested-archives.jsonl":       true,
		"palace/p/drawers":                       false,
		"palace/Bad Slug/drawers/x.jsonl":        false,
		"palace/.local/drawers/x.jsonl":          false,
		"palace/p/kg/drawers/x":                  false,
		"palace/p/kg/ingested-archives.jsonl":    false,
		"Projects/p/drawers/x.jsonl":             false,
		"palace/p/kg/triples/s/a.json":           false,
		"palace/p/drawers/w/r/café.jsonl":        true,
		"palace/p-2/drawers/w/r/drawers.jsonl":   true,
		"palace/-bad/drawers/w/r/drawers.jsonl":  false,
		"palace/p/ingested-archives.jsonl.extra": false,
		"palace/p/drawers/../kg/triples/x.json":  false,
		"palace/p/drawers/./x":                   false,
		"palace/p/drawers/":                      false,
		"palace/p/drawers//x":                    false,
	} {
		if got := isDerivedPath(rel); got != want {
			t.Errorf("isDerivedPath(%q) = %v, want %v", rel, got, want)
		}
	}
}

// R6: `vp vault pull` stays the way out of a malformed marker. SyncVault
// refuses at its tidy scan; Pull, which runs no tidy, fast-forwards to a
// repaired manifest; SyncVault then proceeds. Mutant: a marker read added to
// Pull before the merge.
func TestPull_IsTheWayOutOfAMalformedMarker(t *testing.T) {
	w := newMergeWorld(t)
	broken := w.host(t)
	writeMalformedMarker(t, broken)
	gitRun(t, broken, "commit", "-q", "-am", "a bad hand edit")
	gitRun(t, broken, "push", "-q", "origin", "main")
	fixer := w.host(t)
	if err := surface.WriteVaultManifest(fixer, surface.VaultManifest{Format: surface.RequiredDataFormat}); err != nil {
		t.Fatal(err)
	}
	gitRun(t, fixer, "commit", "-q", "-am", "repair the manifest")
	gitRun(t, fixer, "push", "-q", "origin", "main")

	_, err := SyncVault(broken, []string{"origin"})
	wantMarkerErr(t, "SyncVault before the repair", err)
	res, err := Pull(broken, []string{"origin"})
	if err != nil || res.RemoteResults["origin"] != nil {
		t.Fatalf("pull of the repair: %v / %v", err, res.RemoteResults["origin"])
	}
	if _, err := SyncVault(broken, []string{"origin"}); err != nil {
		t.Fatalf("SyncVault after the repair: %v", err)
	}
}

// R3: the heal leaves an UNMIGRATED vault alone. Before the migration drawers
// are tracked records, so a pulled drawer modify/delete conflict stays the
// operator's: pullCore and mergeFetchedTip both abort with the drawer kept,
// tracked, and named in the error. Mutant: the heal run
// without the migrated check.
func TestHeal_UnmigratedDrawerConflictIsLeftAlone(t *testing.T) {
	setup := func(t *testing.T) string {
		w := newMergeWorld(t)
		lag := w.host(t)
		editDrawer(t, lag)
		other := w.host(t)
		gitRun(t, other, "rm", "-q", "--", fixtureDrawer)
		gitRun(t, other, "commit", "-q", "-m", "an unmigrated host removes the drawer")
		gitRun(t, other, "push", "-q", "origin", "main")
		gitRun(t, lag, "fetch", "-q", "origin")
		return lag
	}
	t.Run("pullCore", func(t *testing.T) {
		lag := setup(t)
		res, err := Pull(lag, []string{"origin"})
		if err != nil {
			t.Fatal(err)
		}
		if res.RemoteResults["origin"] == nil || len(res.Derived.Healed) > 0 {
			t.Errorf("want the conflict left alone: %v, healed %q", res.RemoteResults["origin"], res.Derived.Healed)
		}
		if mergeInProgress(lag) || !onDisk(lag, fixtureDrawer) || !tracked(t, lag, fixtureDrawer) {
			t.Errorf("in progress %v, drawer on disk %v, tracked %v; want aborted with the drawer kept",
				mergeInProgress(lag), onDisk(lag, fixtureDrawer), tracked(t, lag, fixtureDrawer))
		}
		if rerr := res.RemoteResults["origin"]; rerr == nil || !strings.Contains(rerr.Error(), fixtureDrawer) {
			t.Errorf("the error must name the drawer: %v", rerr)
		}
	})
	t.Run("mergeFetchedTip", func(t *testing.T) {
		lag := setup(t)
		var rep DerivedMergeReport
		if _, err := mergeFetchedTip(lag, "origin", "main", &rep); err == nil {
			t.Fatal("want the conflict to fail the merge")
		}
		if mergeInProgress(lag) || len(rep.Healed) > 0 || !onDisk(lag, fixtureDrawer) || !tracked(t, lag, fixtureDrawer) {
			t.Errorf("in progress %v, healed %q, on disk %v, tracked %v", mergeInProgress(lag), rep.Healed, onDisk(lag, fixtureDrawer), tracked(t, lag, fixtureDrawer))
		}
	})
}

// N2: the untrack refuses to commit when the index holds anything but the
// dropped derived paths, and restores the index. Mutant: the staged-set check
// removed (the unrelated staged file would be committed with the untrack).
func TestUntrack_RefusesWhenTheIndexHoldsMoreThanTheDrop(t *testing.T) {
	_, lag, added := laggingAddWorld(t)
	prev := untrackBeforeCommit
	untrackBeforeCommit = func() error {
		writeFile(t, lag, "stray.md", "not the untrack's\n")
		gitRun(t, lag, "add", "--", "stray.md")
		return nil
	}
	t.Cleanup(func() { untrackBeforeCommit = prev })
	head := ""
	res, err := Pull(lag, []string{"origin"})
	if err != nil {
		t.Fatal(err)
	}
	var ue *derivedUntrackError
	if rerr := res.RemoteResults["origin"]; !errors.As(rerr, &ue) || !strings.Contains(rerr.Error(), "refusing to commit the untrack") {
		t.Fatalf("RemoteResults[origin] = %v, want the staged-set refusal", rerr)
	}
	head = gitRun(t, lag, "show", "--name-only", "--format=", "HEAD")
	if strings.Contains(head, "stray.md") {
		t.Errorf("the stray staged file was committed: %q", head)
	}
	if !tracked(t, lag, added) {
		t.Error("the drawer's index entry was not restored")
	}
}
