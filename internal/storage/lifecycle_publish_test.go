// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// lcVault is a vault-shaped repo V with one or more file:// bare remotes, all
// at V's HEAD. remotes are in push order.
type lcVault struct {
	Dir     string
	Bares   map[string]string
	Remotes []string
}

func newLCVault(t *testing.T, remotes ...string) *lcVault {
	t.Helper()
	v := &lcVault{Dir: initTestRepo(t), Bares: map[string]string{}, Remotes: remotes}
	writeFile(t, v.Dir, "Projects/p/resume.md", "p\n")
	writeFile(t, v.Dir, "Projects/other/resume.md", "other\n")
	gitRun(t, v.Dir, "add", "-A")
	gitRun(t, v.Dir, "commit", "-q", "-m", "projects")
	for _, r := range remotes {
		bare := initBareRemote(t)
		v.Bares[r] = bare
		gitRun(t, v.Dir, "remote", "add", r, fileURL(bare))
		gitRun(t, v.Dir, "push", "-q", r, "main")
	}
	return v
}

func (v *lcVault) head(t *testing.T) string { return gitRun(t, v.Dir, "rev-parse", "HEAD") }

// lock takes V's root commit lock, as a lifecycle command does for its whole
// run, and releases it at test end. Release it early to stand for "the
// command stopped", so other committers in this process can run.
func (v *lcVault) lock(t *testing.T) *vaultlock.Held {
	t.Helper()
	h, err := vaultlock.AcquireHeld(v.Dir, v.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Release() })
	return h
}

// lifecycleCommit simulates a lifecycle command's own commit: marker first,
// then the write, then the commit, then the marker's commit sha.
func (v *lcVault) lifecycleCommit(t *testing.T, held *vaultlock.Held, runID string) lifecycleMarker {
	t.Helper()
	m := lifecycleMarker{Command: "vault project delete", RunID: runID, Parent: v.head(t), Rerun: "vp vault project delete p --moved-to x"}
	if err := writeLifecycleMarker(held, m); err != nil {
		t.Fatal(err)
	}
	writeFile(t, v.Dir, "Audits/departures/p.json", `{"slug":"p","kind":"moved-to-vault"}`+"\n")
	gitRun(t, v.Dir, "rm", "-q", "-r", "Projects/p")
	gitRun(t, v.Dir, "add", "Audits/departures/p.json")
	gitRun(t, v.Dir, "commit", "-q", "-m", "vault project delete: p\n\nVp-Delete-Project: p\n"+lifecycleRunTrailerLine(runID))
	m.Commit = v.head(t)
	if err := setLifecycleMarkerCommit(held, runID, m.Commit); err != nil {
		t.Fatal(err)
	}
	return m
}

// pushFromOtherHost pushes one commit writing rel to remote's main from a
// throwaway clone, as another host's session exit would.
func pushFromOtherHost(t *testing.T, bare, rel, content string) string {
	t.Helper()
	other := t.TempDir()
	gitRun(t, other, "clone", "-q", "-b", "main", fileURL(bare), ".")
	gitRun(t, other, "config", "user.email", "other@example.com")
	gitRun(t, other, "config", "user.name", "Other")
	writeFile(t, other, rel, content)
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-q", "-m", "other host "+rel)
	gitRun(t, other, "push", "-q", "origin", "main")
	return gitRun(t, other, "rev-parse", "HEAD")
}

func lockDir(t *testing.T, dir string) *vaultlock.Held {
	t.Helper()
	h, err := vaultlock.AcquireHeld(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Release() })
	return h
}

func remoteHas(t *testing.T, bare, rel string) bool {
	t.Helper()
	out := gitRun(t, bare, "ls-tree", "-r", "--name-only", "main", "--", rel)
	return out != ""
}

func markerFound(t *testing.T, dir string) bool {
	t.Helper()
	_, found, _ := readLifecycleMarker(dir)
	return found
}

func withPush(t *testing.T, fn func(vaultPath, remote, sha, branch string) error) {
	t.Helper()
	old := lifecyclePush
	lifecyclePush = fn
	t.Cleanup(func() { lifecyclePush = old })
}

func realPush(vaultPath, remote, sha, branch string) error {
	_, err := lifecycleGit(vaultPath, lifecycleNetTimeout, "push", "--quiet", remote, sha+":refs/heads/"+branch)
	return err
}

func TestRequireHeadAtEveryRemote(t *testing.T) {
	v := newLCVault(t, "origin", "github")
	h := v.lock(t)
	if _, err := requireHeadAtEveryRemote(h, "main", v.Remotes); err != nil {
		t.Fatalf("in sync: %v", err)
	}
	pushFromOtherHost(t, v.Bares["github"], "x.md", "x\n") // V is behind github
	if _, err := requireHeadAtEveryRemote(h, "main", v.Remotes); !errors.Is(err, ErrRemoteNotAtHead) {
		t.Fatalf("behind: err = %v", err)
	}
	v2 := newLCVault(t, "origin")
	writeFile(t, v2.Dir, "y.md", "y\n")
	gitRun(t, v2.Dir, "add", "-A")
	gitRun(t, v2.Dir, "commit", "-q", "-m", "unpushed")
	if _, err := requireHeadAtEveryRemote(lockDir(t, v2.Dir), "main", v2.Remotes); !errors.Is(err, ErrRemoteNotAtHead) {
		t.Fatalf("ahead: err = %v", err)
	}
	v3 := newLCVault(t, "origin")
	if err := os.RemoveAll(v3.Bares["origin"]); err != nil {
		t.Fatal(err)
	}
	if _, err := requireHeadAtEveryRemote(lockDir(t, v3.Dir), "main", v3.Remotes); !errors.Is(err, ErrRemoteUnreachable) {
		t.Fatalf("unreachable: err = %v", err)
	}
}

func TestExactPublish_PublishesToEveryRemoteAndClears(t *testing.T) {
	v := newLCVault(t, "origin", "github")
	h := v.lock(t)
	m := v.lifecycleCommit(t, h, "run-1")
	if err := exactPublish(h, "main", v.Remotes, m, nil, false); err != nil {
		t.Fatal(err)
	}
	for r, bare := range v.Bares {
		if gitRun(t, bare, "rev-parse", "main") != m.Commit {
			t.Fatalf("%s does not hold the commit", r)
		}
	}
	if markerFound(t, v.Dir) {
		t.Fatal("marker not cleared after every remote confirmed")
	}
}

// e4: another host pushes a write to <p> after the delete commit. The push is
// rejected, the live tip does not contain the commit, so the commit is reset,
// the run rolled back and nothing is published.
func TestExactPublish_E4RaceResetsAndPublishesNothing(t *testing.T) {
	v := newLCVault(t, "origin")
	h := v.lock(t)
	m := v.lifecycleCommit(t, h, "run-e4")
	pushFromOtherHost(t, v.Bares["origin"], "Projects/p/new-session.md", "written after the copy\n")
	rolledBack := false
	err := exactPublish(h, "main", v.Remotes, m, func() error { rolledBack = true; return nil }, false)
	if remoteHas(t, v.Bares["origin"], "Audits/departures/p.json") {
		t.Fatal("the remote holds the departure record: an unverified commit was published")
	}
	var pe *PublishError
	if !errors.As(err, &pe) || pe.Kind != PublishRemoteMoved {
		t.Fatalf("err = %v, want PublishRemoteMoved", err)
	}
	if !rolledBack || v.head(t) != m.Parent || markerFound(t, v.Dir) {
		t.Fatalf("rollback=%v head=%s parent=%s marker=%v", rolledBack, v.head(t), m.Parent, markerFound(t, v.Dir))
	}
	if !remoteHas(t, v.Bares["origin"], "Projects/p/new-session.md") {
		t.Fatal("fixture: the other host's write is missing")
	}
}

// The server took the push but its acknowledgement was lost: git reports an
// error, yet the live tip contains the commit. It is published; nothing is
// reset.
func TestExactPublish_LostAcknowledgementIsPublished(t *testing.T) {
	v := newLCVault(t, "origin")
	h := v.lock(t)
	m := v.lifecycleCommit(t, h, "run-ack")
	withPush(t, func(vp, r, sha, b string) error {
		if err := realPush(vp, r, sha, b); err != nil {
			return err
		}
		return errors.New("connection reset by peer after the server accepted")
	})
	if err := exactPublish(h, "main", v.Remotes, m, func() error { t.Fatal("rollback ran"); return nil }, false); err != nil {
		t.Fatalf("err = %v, want published", err)
	}
	if v.head(t) != m.Commit || gitRun(t, v.Bares["origin"], "rev-parse", "main") != m.Commit || markerFound(t, v.Dir) {
		t.Fatal("a published commit was reset, or the marker stayed")
	}
}

// Published, then another push lands on top before the confirming read: the
// live tip contains the commit without equalling it.
func TestExactPublish_OvertakenIsPublished(t *testing.T) {
	v := newLCVault(t, "origin")
	h := v.lock(t)
	m := v.lifecycleCommit(t, h, "run-over")
	withPush(t, func(vp, r, sha, b string) error {
		if err := realPush(vp, r, sha, b); err != nil {
			return err
		}
		pushFromOtherHost(t, v.Bares["origin"], "Projects/other/later.md", "on top\n")
		return nil
	})
	if err := exactPublish(h, "main", v.Remotes, m, nil, false); err != nil {
		t.Fatalf("err = %v, want published", err)
	}
	if v.head(t) != m.Commit || markerFound(t, v.Dir) {
		t.Fatal("overtaken commit not treated as published")
	}
}

// A later remote fails in transport: its tip is still the parent. Stop with
// the marker kept; the re-run pushes the same sha.
func TestExactPublish_LaterRemoteTransportThenRedo(t *testing.T) {
	v := newLCVault(t, "origin", "github")
	h := v.lock(t)
	m := v.lifecycleCommit(t, h, "run-tr")
	withPush(t, func(vp, r, sha, b string) error {
		if r == "github" {
			return errors.New("ssh: connect to host github.com port 22: Connection timed out")
		}
		return realPush(vp, r, sha, b)
	})
	err := exactPublish(h, "main", v.Remotes, m, func() error { t.Fatal("rollback ran"); return nil }, false)
	var pe *PublishError
	if !errors.As(err, &pe) || pe.Kind != PublishTransport || pe.Remote != "github" {
		t.Fatalf("err = %v, want PublishTransport at github", err)
	}
	if !markerFound(t, v.Dir) || v.head(t) != m.Commit {
		t.Fatal("marker or commit lost on a transport failure")
	}
	withPush(t, realPush)
	out, _, err := redoLifecycle(h, m.Command, "main", v.Remotes, nil)
	if err != nil || out != RedoPublished {
		t.Fatalf("redo = %s, %v", out, err)
	}
	if gitRun(t, v.Bares["github"], "rev-parse", "main") != m.Commit || markerFound(t, v.Dir) {
		t.Fatal("redo did not publish the same sha to github")
	}
}

// A mirror that holds a commit origin lacks rejects the push: named refusal,
// no reset, marker kept, and no retry that could succeed.
func TestExactPublish_DivergedMirror(t *testing.T) {
	v := newLCVault(t, "origin", "github")
	h := v.lock(t)
	m := v.lifecycleCommit(t, h, "run-div")
	pushFromOtherHost(t, v.Bares["github"], "stray.md", "written to the mirror only\n")
	err := exactPublish(h, "main", v.Remotes, m, func() error { t.Fatal("rollback ran"); return nil }, false)
	var pe *PublishError
	if !errors.As(err, &pe) || pe.Kind != PublishMirrorDiverged || pe.Remote != "github" {
		t.Fatalf("err = %v, want PublishMirrorDiverged at github", err)
	}
	if v.head(t) != m.Commit || !markerFound(t, v.Dir) {
		t.Fatal("diverged mirror reset the commit or cleared the marker")
	}
	if gitRun(t, v.Bares["origin"], "rev-parse", "main") != m.Commit {
		t.Fatal("origin should hold the commit")
	}
}

func TestExactPublish_UnreadableRemoteKeepsEverything(t *testing.T) {
	v := newLCVault(t, "origin")
	h := v.lock(t)
	m := v.lifecycleCommit(t, h, "run-unread")
	if err := os.Rename(v.Bares["origin"], v.Bares["origin"]+".gone"); err != nil {
		t.Fatal(err)
	}
	err := exactPublish(h, "main", v.Remotes, m, func() error { t.Fatal("rollback ran"); return nil }, false)
	var pe *PublishError
	if !errors.As(err, &pe) || pe.Kind != PublishStateUnknown {
		t.Fatalf("err = %v, want PublishStateUnknown", err)
	}
	if v.head(t) != m.Commit || !markerFound(t, v.Dir) {
		t.Fatal("an unreadable remote reset the commit or cleared the marker")
	}
}

// While the marker stands, every other committer refuses before it stages and
// makes NO commit, and a pull does not merge; HEAD stays the lifecycle commit.
func TestLifecycleMarker_BlocksEveryCommitterAndPull(t *testing.T) {
	v := newLCVault(t, "origin")
	h := v.lock(t)
	m := v.lifecycleCommit(t, h, "run-block")
	// Only the caller's own live token is exempt: the run's own commit passes
	// (commitPathsLocked passes its token); a committer without it is refused
	// even while the owner's token is live.
	if err := refuseOnPendingDeparturesFor(v.Dir, h); err != nil {
		t.Fatalf("own live run refused: %v", err)
	}
	if err := refuseOnPendingDepartures(v.Dir); !errors.Is(err, ErrLifecyclePending) {
		t.Fatalf("a committer without the owner's token passed while it is live: err = %v", err)
	}
	// The command stops: its token is released, the marker stays.
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, v.Dir, "Projects/other/sessions/harvest.md", "a session the exit hook would commit\n")

	if _, err := CommitAndPushPaths(v.Dir, "harvest", []string{"Projects/other/sessions/harvest.md"}, true); !errors.Is(err, ErrLifecyclePending) {
		t.Fatalf("harvest commit: err = %v, want ErrLifecyclePending", err)
	}
	if _, err := TidyVault(v.Dir, true); !errors.Is(err, ErrLifecyclePending) {
		t.Fatalf("tidy: err = %v, want ErrLifecyclePending", err)
	}
	pushFromOtherHost(t, v.Bares["origin"], "Projects/other/remote.md", "incoming\n")
	if _, err := Pull(v.Dir, v.Remotes); !errors.Is(err, ErrLifecyclePending) {
		t.Fatalf("pull: err = %v, want ErrLifecyclePending", err)
	}
	if _, err := SyncVault(v.Dir, v.Remotes); !errors.Is(err, ErrLifecyclePending) {
		t.Fatalf("sync: err = %v, want ErrLifecyclePending", err)
	}
	if v.head(t) != m.Commit {
		t.Fatalf("HEAD moved to %s: another committer or a merge ran", v.head(t))
	}
	if n := gitRun(t, v.Dir, "rev-list", "--count", m.Parent+"..HEAD"); n != "1" {
		t.Fatalf("%s commits past the parent, want only the lifecycle commit", n)
	}
	// A released token exempts nothing, not even the run's own.
	if err := refuseOnPendingDeparturesFor(v.Dir, h); !errors.Is(err, ErrLifecyclePending) {
		t.Fatalf("released token still exempts: err = %v", err)
	}
}

// A kept-local rename with the marker standing, and a concurrent append under
// <old> on the remote: no pull replays or merges it.
func TestLifecycleMarker_PullDoesNotMergeOntoUnpublishedRename(t *testing.T) {
	v := newLCVault(t, "origin")
	m := lifecycleMarker{Command: "vault rename", RunID: "run-rn", Parent: v.head(t), Rerun: "vp vault rename p p2"}
	h := v.lock(t)
	if err := writeLifecycleMarker(h, m); err != nil {
		t.Fatal(err)
	}
	gitRun(t, v.Dir, "mv", "Projects/p", "Projects/p2")
	gitRun(t, v.Dir, "commit", "-q", "-m", "vault rename: p -> p2\n\nVp-Rename-From: p\nVp-Rename-To: p2")
	commit := v.head(t)
	pushFromOtherHost(t, v.Bares["origin"], "Projects/p/resume.md", "p\nnote_path: Projects/p/notes/x.md\n")
	// The command stopped (token released, marker kept); the next pull must
	// not merge onto its commit.
	_ = h.Release()
	if _, err := Pull(v.Dir, v.Remotes); !errors.Is(err, ErrLifecyclePending) {
		t.Fatalf("pull: err = %v, want ErrLifecyclePending", err)
	}
	if v.head(t) != commit {
		t.Fatal("pull merged onto the unpublished rename")
	}
}

// The pull's check is strict: even the owning process, with the token that
// wrote the marker live, is refused — where the commit guard lets it through.
// (Today Pull also waits on the root lock, so this is defence in depth.)
func TestLifecycleMarker_PullCheckIgnoresOwner(t *testing.T) {
	v := newLCVault(t, "origin")
	h := v.lock(t)
	m := lifecycleMarker{Command: "vault rename", RunID: "run-strict", Parent: v.head(t), Rerun: "r"}
	if err := writeLifecycleMarker(h, m); err != nil {
		t.Fatal(err)
	}
	if err := refuseOnLifecyclePending(v.Dir, h); err != nil {
		t.Fatalf("commit guard refused the live owner: %v", err)
	}
	if err := refuseOnLifecyclePendingStrict(v.Dir); !errors.Is(err, ErrLifecyclePending) {
		t.Fatalf("pull check let the owner through: err = %v", err)
	}
}

func TestRedo_FastForwardAfterKill(t *testing.T) {
	v := newLCVault(t, "origin", "github")
	h := v.lock(t)
	m := v.lifecycleCommit(t, h, "run-ff") // killed before its push
	out, _, err := redoLifecycle(h, m.Command, "main", v.Remotes, nil)
	if err != nil || out != RedoPublished {
		t.Fatalf("redo = %s, %v", out, err)
	}
	for r, bare := range v.Bares {
		if gitRun(t, bare, "rev-parse", "main") != m.Commit {
			t.Fatalf("%s lacks the same sha", r)
		}
	}
	if markerFound(t, v.Dir) {
		t.Fatal("marker not cleared")
	}
}

// Killed mid-copy: the marker stands, no commit exists, the trees are partial
// and untracked. The redo rolls back this run's trees, clears the marker, and
// the command can start afresh.
func TestRedo_MarkerWithNoCommitRollsBack(t *testing.T) {
	v := newLCVault(t, "origin")
	m := lifecycleMarker{Command: "vault copy", RunID: "run-mid", Parent: v.head(t), Rerun: "vp vault copy q --from x"}
	h := v.lock(t)
	if err := writeLifecycleMarker(h, m); err != nil {
		t.Fatal(err)
	}
	writeFile(t, v.Dir, "Projects/q/resume.md", "half\n")
	writeFile(t, v.Dir, "palace/q/drawers.jsonl", "{}\n")
	rollback := func() error {
		for _, tree := range ProjectTrees("q") {
			if err := os.RemoveAll(filepath.Join(v.Dir, filepath.FromSlash(tree))); err != nil {
				return err
			}
		}
		return nil
	}
	out, _, err := redoLifecycle(h, m.Command, "main", v.Remotes, rollback)
	if err != nil || out != RedoRolledBack {
		t.Fatalf("redo = %s, %v", out, err)
	}
	if markerFound(t, v.Dir) || v.head(t) != m.Parent {
		t.Fatal("marker kept or HEAD moved")
	}
	if st := gitRun(t, v.Dir, "status", "--porcelain", "--", "Projects/q", "palace/q"); st != "" {
		t.Fatalf("partial trees left: %s", st)
	}
}

// Killed after the commit; a remote gained an unrelated commit. No remote
// holds the commit, so the redo resets, rolls back and asks for a new dry run.
// It never rebases.
func TestRedo_RemoteMovedResetsNeverRebases(t *testing.T) {
	v := newLCVault(t, "origin")
	h := v.lock(t)
	m := v.lifecycleCommit(t, h, "run-moved")
	other := pushFromOtherHost(t, v.Bares["origin"], "Projects/other/unrelated.md", "unrelated\n")
	rolledBack := false
	out, _, err := redoLifecycle(h, m.Command, "main", v.Remotes, func() error { rolledBack = true; return nil })
	if !errors.Is(err, ErrRedoRemoteMoved) || out != RedoNone {
		t.Fatalf("redo = %s, %v, want ErrRedoRemoteMoved", out, err)
	}
	if v.head(t) != m.Parent || !rolledBack || markerFound(t, v.Dir) {
		t.Fatalf("head=%s parent=%s rollback=%v marker=%v", v.head(t), m.Parent, rolledBack, markerFound(t, v.Dir))
	}
	if gitRun(t, v.Bares["origin"], "rev-parse", "main") != other {
		t.Fatal("the remote changed: something was pushed")
	}
}

// Killed between the commit and recording its sha: HEAD is one commit past the
// marker's parent and carries this run's Vp-Run trailer, so it is the run's,
// and the redo publishes it.
func TestRedo_AdoptsCommitMissingFromMarker(t *testing.T) {
	v := newLCVault(t, "origin")
	m := lifecycleMarker{Command: "vault copy", RunID: "run-adopt", Parent: v.head(t), Rerun: "vp vault copy q --from x"}
	h := v.lock(t)
	if err := writeLifecycleMarker(h, m); err != nil {
		t.Fatal(err)
	}
	writeFile(t, v.Dir, "Projects/q/resume.md", "q\n")
	gitRun(t, v.Dir, "add", "-A")
	gitRun(t, v.Dir, "commit", "-q", "-m", "vault copy: q\n\nVp-Copy-Project: q\n"+lifecycleRunTrailerLine(m.RunID))
	commit := v.head(t)
	out, _, err := redoLifecycle(h, m.Command, "main", v.Remotes, nil)
	if err != nil || out != RedoPublished || gitRun(t, v.Bares["origin"], "rev-parse", "main") != commit {
		t.Fatalf("redo = %s, %v", out, err)
	}
}

// The marker blocks vp's committers, not raw git: a commit one past the
// parent WITHOUT this run's trailer is somebody else's. The redo neither
// publishes it nor resets it away.
func TestRedo_RefusesForeignCommitOnTopOfMarker(t *testing.T) {
	v := newLCVault(t, "origin")
	m := lifecycleMarker{Command: "vault copy", RunID: "run-foreign", Parent: v.head(t), Rerun: "vp vault copy q --from x"}
	h := v.lock(t)
	if err := writeLifecycleMarker(h, m); err != nil {
		t.Fatal(err)
	}
	writeFile(t, v.Dir, "Projects/other/raw.md", "a human's raw git commit\n")
	gitRun(t, v.Dir, "add", "-A")
	gitRun(t, v.Dir, "commit", "-q", "-m", "raw git, no trailer")
	foreign := v.head(t)
	rolledBack := false
	out, _, err := redoLifecycle(h, m.Command, "main", v.Remotes, func() error { rolledBack = true; return nil })
	var pe *PublishError
	if !errors.As(err, &pe) || pe.Kind != PublishStateUnknown || out != RedoNone {
		t.Fatalf("redo = %s, %v, want a state-unknown refusal", out, err)
	}
	if gitRun(t, v.Bares["origin"], "rev-parse", "main") != m.Parent {
		t.Fatal("the foreign commit was published")
	}
	if v.head(t) != foreign || rolledBack || !markerFound(t, v.Dir) {
		t.Fatal("the foreign commit was reset away, or the run rolled back, or the marker cleared")
	}
	// A trailer naming ANOTHER run is foreign too.
	gitRun(t, v.Dir, "commit", "-q", "--amend", "-m", "x\n\n"+lifecycleRunTrailerLine("some-other-run"))
	if _, _, err := redoLifecycle(h, m.Command, "main", v.Remotes, nil); !errors.As(err, &pe) || pe.Kind != PublishStateUnknown {
		t.Fatalf("another run's trailer adopted: %v", err)
	}
}

// A rollback moves HEAD back without touching other projects' uncommitted
// edits: V holds other sessions' dirt by design.
func TestExactPublish_RollbackKeepsOtherProjectsDirt(t *testing.T) {
	v := newLCVault(t, "origin")
	h := v.lock(t)
	m := v.lifecycleCommit(t, h, "run-keep")
	writeFile(t, v.Dir, "Projects/other/resume.md", "an uncommitted edit by another session\n")
	pushFromOtherHost(t, v.Bares["origin"], "Projects/p/new-session.md", "written after the copy\n")
	err := exactPublish(h, "main", v.Remotes, m, nil, false)
	var pe *PublishError
	if !errors.As(err, &pe) || pe.Kind != PublishRemoteMoved {
		t.Fatalf("err = %v, want PublishRemoteMoved", err)
	}
	got, _ := os.ReadFile(filepath.Join(v.Dir, "Projects/other/resume.md"))
	if string(got) != "an uncommitted edit by another session\n" {
		t.Fatalf("rollback destroyed another session's edit: %q", got)
	}
	if v.head(t) != m.Parent {
		t.Fatal("not reset to the parent")
	}
}

func TestLifecycleMarker_ForeignRunAndMalformed(t *testing.T) {
	v := newLCVault(t, "origin")
	m := lifecycleMarker{Command: "vault copy", RunID: "run-a", Parent: v.head(t), Rerun: "r"}
	h := v.lock(t)
	if err := writeLifecycleMarker(h, m); err != nil {
		t.Fatal(err)
	}
	if err := writeLifecycleMarker(h, lifecycleMarker{Command: "vault copy", RunID: "run-b", Parent: m.Parent}); !errors.Is(err, ErrLifecyclePending) {
		t.Fatalf("second marker: err = %v", err)
	}
	if err := clearLifecycleMarker(h, "run-b"); err == nil {
		t.Fatal("cleared another run's marker")
	}
	if _, _, err := redoLifecycle(h, "vault project delete", "main", v.Remotes, nil); !errors.Is(err, ErrLifecyclePending) {
		t.Fatalf("redo by another command: err = %v", err)
	}
	p, _ := lifecycleMarkerPath(v.Dir)
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := refuseOnPendingDepartures(v.Dir); !errors.Is(err, ErrLifecyclePending) {
		t.Fatalf("malformed marker: err = %v, want a refusal", err)
	}
}

func TestLifecycleRemoteOrder(t *testing.T) {
	v := newLCVault(t, "github", "origin", "backup")
	got, err := lifecycleRemoteOrder(v.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"origin", "backup", "github"}) {
		t.Fatalf("default order = %v, want origin first then git remote order", got)
	}
	writeFile(t, v.Dir, ".vibe-palace/remotes.toml", "[[remote]]\nname = \"github\"\nurl = \"x\"\n[[remote]]\nname = \"origin\"\nurl = \"y\"\n")
	got, err = lifecycleRemoteOrder(v.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"github", "origin", "backup"}) {
		t.Fatalf("recorded order = %v", got)
	}
}

// U2's commitPathsLocked runs the plain commit guard (and the commitOnlyPaths
// backstop). With the run's own marker standing, the run's OWN commit through
// it must pass, because this process wrote the marker under the live token.
func TestLifecycleMarker_OwnCommitPassesCommitPathsLocked(t *testing.T) {
	v := newLCVault(t, "origin")
	h := v.lock(t)
	m := lifecycleMarker{Command: "vault copy", RunID: "run-own", Parent: v.head(t), Rerun: "vp vault copy q --from x"}
	if err := writeLifecycleMarker(h, m); err != nil {
		t.Fatal(err)
	}
	writeFile(t, v.Dir, "Projects/q/resume.md", "q\n")
	if _, err := commitPathsLocked(h, "vault copy: q\n\nVp-Copy-Project: q", "", []string{"Projects/q"}); err != nil {
		t.Fatalf("the run's own commit was refused: %v", err)
	}
	if gitRun(t, v.Dir, "rev-parse", "HEAD~1") != m.Parent {
		t.Fatal("no commit on top of the parent")
	}
	if err := setLifecycleMarkerCommit(h, m.RunID, v.head(t)); err != nil {
		t.Fatal(err)
	}
	m.Commit = v.head(t)
	if err := exactPublish(h, "main", v.Remotes, m, nil, false); err != nil {
		t.Fatal(err)
	}
	if gitRun(t, v.Bares["origin"], "rev-parse", "main") != m.Commit || markerFound(t, v.Dir) {
		t.Fatal("not published, or the marker stayed")
	}
}

// ownedRunEntry reports the ownedRuns entry for dir, if any.
func ownedRunEntry(dir string) (ownedRun, bool) {
	v, ok := ownedRuns.Load(runKey(dir))
	if !ok {
		return ownedRun{}, false
	}
	return v.(ownedRun), true
}

// The owner run stops (returns or crashes) and RELEASES its token without
// clearing the marker. Release() does not touch ownedRuns — only
// clearLifecycleMarker deletes the entry — so the entry goes stale. A later
// committer in the SAME process takes a NEW token for the same root: it must
// be refused, not exempted through the stale entry. And the other order:
// clear, then release, removes the entry at the clear.
func TestLifecycleMarker_NewTokenAfterReleaseIsNotExempt(t *testing.T) {
	t.Run("release without clear, then a new token", func(t *testing.T) {
		v := newLCVault(t, "origin")
		h1, err := vaultlock.AcquireHeld(v.Dir, v.Dir)
		if err != nil {
			t.Fatal(err)
		}
		m := lifecycleMarker{Command: "vault copy", RunID: "run-stale", Parent: v.head(t), Rerun: "r"}
		if err := writeLifecycleMarker(h1, m); err != nil {
			t.Fatal(err)
		}
		if err := h1.Release(); err != nil {
			t.Fatal(err)
		}
		if o, ok := ownedRunEntry(v.Dir); !ok || o.runID != m.RunID || o.held != h1 {
			t.Fatalf("Release() changed ownedRuns: entry=%v ok=%v", o, ok)
		}
		h2, err := vaultlock.AcquireHeld(v.Dir, v.Dir) // e.g. a vp mcp server's next committer
		if err != nil {
			t.Fatal(err)
		}
		defer h2.Release()
		if err := refuseOnPendingDeparturesFor(v.Dir, h2); !errors.Is(err, ErrLifecyclePending) {
			t.Fatalf("a new token for the same root was exempted through the stale entry: err = %v", err)
		}
		writeFile(t, v.Dir, "Projects/other/x.md", "another committer's write\n")
		before := v.head(t)
		if _, err := commitPathsLocked(h2, "other committer", "", []string{"Projects/other/x.md"}); !errors.Is(err, ErrLifecyclePending) {
			t.Fatalf("commitPathsLocked with the new token: err = %v, want ErrLifecyclePending", err)
		}
		if v.head(t) != before {
			t.Fatal("the new token committed on top of the unfinished run")
		}
		// The new token may still finish the run (a redo clears through it),
		// and that removes the stale entry.
		if err := clearLifecycleMarker(h2, m.RunID); err != nil {
			t.Fatal(err)
		}
		if _, ok := ownedRunEntry(v.Dir); ok {
			t.Fatal("clear did not remove the ownedRuns entry")
		}
	})
	t.Run("clear, then release", func(t *testing.T) {
		v := newLCVault(t, "origin")
		h1, err := vaultlock.AcquireHeld(v.Dir, v.Dir)
		if err != nil {
			t.Fatal(err)
		}
		m := lifecycleMarker{Command: "vault copy", RunID: "run-clean", Parent: v.head(t), Rerun: "r"}
		if err := writeLifecycleMarker(h1, m); err != nil {
			t.Fatal(err)
		}
		if err := clearLifecycleMarker(h1, m.RunID); err != nil {
			t.Fatal(err)
		}
		if _, ok := ownedRunEntry(v.Dir); ok {
			t.Fatal("clear did not remove the ownedRuns entry")
		}
		if err := h1.Release(); err != nil {
			t.Fatal(err)
		}
		h2, err := vaultlock.AcquireHeld(v.Dir, v.Dir)
		if err != nil {
			t.Fatal(err)
		}
		defer h2.Release()
		if err := refuseOnPendingDeparturesFor(v.Dir, h2); err != nil {
			t.Fatalf("no marker, yet refused: %v", err)
		}
	})
}
