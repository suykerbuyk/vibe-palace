// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
	"github.com/suykerbuyk/vibe-palace/internal/giterr"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// afterPushHook runs immediately after a successful push records its SHA in
// remoteSHA, before the convergence loop. Production code must leave this as a
// no-op; tests override it to inject mid-flight state changes (e.g. a
// concurrent writer mutating a bare remote between the recorded push and the
// convergence push).
var afterPushHook = func(remote string) {}

// mergeTimeout is the deadline on the reconcile's merge (mergeFetchedTip). It
// is a variable only so a test can drive a merge past it; production code must
// leave it alone.
var mergeTimeout = 120 * time.Second

// PushResult reports what happened during a CommitAndPushPaths operation.
type PushResult struct {
	CommitSHA     string           // the commit SHA that was pushed (empty if nothing to commit)
	RemoteResults map[string]error // per-remote push result (nil = success)
	// SkippedPaths lists the supplied paths that were dropped before staging
	// because they matched nothing in BOTH the worktree and the index (absent
	// file that is not tracked). Tracked-but-deleted paths are NOT skipped —
	// their deletion is staged. A populated entry here flags either a benign
	// not-yet-created path (e.g. an empty Projects/<slug>/memory/) or a
	// misspelled path the caller should notice.
	SkippedPaths []string
	// Derived is what the reconcile merges did to derived index paths on a
	// migrated vault (healed conflicts, untracked re-tracks).
	Derived DerivedMergeReport
}

// AllPushed was DELETED at 209 — see the AllPulled note in vaultpull.go for the full
// reasoning. Same shape, same fate: written, unit-tested, never wired, while
// vp_vault_sync reported `status: "ok"` on a partial push. Superseded by
// FailedRemotes + RemoteVerdict (remotes.go), which both front-ends now call.

// AnyPushed returns true if at least one remote was pushed successfully.
func (r *PushResult) AnyPushed() bool {
	for _, err := range r.RemoteResults {
		if err == nil {
			return true
		}
	}
	return false
}

// Stranded reports a commit that was created and pushed-attempted but reached
// NO remote — a local-only commit left behind despite remotes being configured.
// Distinct from a clean no-remote downgrade (RemoteResults stays nil there, so
// this returns false) and from a push=false local commit (also nil RemoteResults).
func (r *PushResult) Stranded() bool {
	return r.CommitSHA != "" && len(r.RemoteResults) > 0 && !r.AnyPushed()
}

// PlainPushResult reports a plain `git push <remote> <branch>` across remotes.
// It mirrors PullResult: per-remote outcome + captured git output, no commit of
// its own (the caller pushes an already-committed HEAD). Callers adjudicate with
// FailedRemotes / RemoteVerdict, exactly as the pull path does.
type PlainPushResult struct {
	RemoteResults map[string]error  // per-remote push result (nil = success)
	RemoteOutput  map[string]string // combined stdout+stderr git output per remote
}

// PushPlain pushes the already-committed HEAD to each configured remote with a
// plain `git push <remote> main` — the non-reconciling counterpart to Pull, mirroring
// its shape. It attempts every remote in order and records each outcome in
// RemoteResults (nil = success) alongside the captured combined stdout+stderr in
// RemoteOutput, rather than aborting internally: the two front-ends differ on
// output channel (CLI stderr vs MCP payload string) and both take their verdict
// from RemoteVerdict, so PushPlain returns data and leaves policy to the callers.
// It NEVER writes to os.Stderr.
//
// PushPlain refuses a dirty working tree: a plain push would publish a HEAD
// that leaves the uncommitted work behind. That refuse-on-dirty precheck used
// to exist twice, as a raw `git status --porcelain` in each front-end (CLI
// pushAll, MCP gitPush) — two implementations that agreed only by being kept
// in step. It lives here once now, after the git_enabled gate, and returns a
// *DirtyTreeError each front-end presents its own way. It does NOT commit, does
// NOT merge or converge (that is CommitAndPushPaths's job), and does NOT call
// RemoteVerdict. The branch is main — matching what both front-ends push today.
// The returned *PlainPushResult is always non-nil, even if a remote failed;
// the error return is reserved for a refusal or a failure to run git at all.
//
// A host config with git_enabled = false (or an unreadable one) refuses
// before any git runs, with a non-nil empty result.
func PushPlain(vaultPath string, remotes []string) (*PlainPushResult, error) {
	empty := &PlainPushResult{
		RemoteResults: map[string]error{},
		RemoteOutput:  map[string]string{},
	}
	if err := RefuseIfGitDisabled(vaultPath, "push"); err != nil {
		return empty, err
	}
	porcelain, err := porcelainStatus(vaultPath)
	if err != nil {
		return empty, fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(porcelain) != "" {
		return empty, &DirtyTreeError{Porcelain: porcelain}
	}
	return pushPlainCore(vaultPath, remotes)
}

// DirtyTreeError is PushPlain's refuse-on-dirty verdict. Porcelain is git's
// untrimmed `status --porcelain` output, so a front-end can print it verbatim
// (the CLI) or parse the paths out of it (MCP) without re-running git.
type DirtyTreeError struct {
	Porcelain string
}

func (e *DirtyTreeError) Error() string { return "vault has uncommitted changes" }

// porcelainStatus returns `git status --porcelain` for vaultPath, untrimmed:
// gitCmd trims its output, which would strip the leading status column of the
// first line (" M path") that a front-end parses.
func porcelainStatus(vaultPath string) (string, error) {
	cmd := exec.Command("git", "-C", vaultPath, "status", "--porcelain")
	cmd.Env = SafeGitEnv()
	out, err := cmd.Output()
	return string(out), err
}

// pushPlainCore is PushPlain after its git_enabled gate, for storage-internal
// composition (SyncVault).
func pushPlainCore(vaultPath string, remotes []string) (*PlainPushResult, error) {
	result := &PlainPushResult{
		RemoteResults: make(map[string]error, len(remotes)),
		RemoteOutput:  make(map[string]string, len(remotes)),
	}
	if err := RefuseIfNestedVaultGit(vaultPath, "push"); err != nil {
		return result, err
	}
	for _, remote := range remotes {
		out, err := gitCmd(vaultPath, 60*time.Second, "push", remote, "main")
		result.RemoteOutput[remote] = out
		result.RemoteResults[remote] = err
	}
	return result, nil
}

// CommitAndPushPaths stages only the supplied paths, commits with a
// machine-stamped message, and (when push=true) pushes to all configured
// remotes. Dirty paths NOT in the supplied list are left dirty in the working
// tree — this is the contamination-safe entry point used by callers that know
// which files belong to their work unit. It NEVER runs `git add -A`.
//
// Empty or nil paths returns (nil, error) — explicit caller intent is
// required.
//
// Staging uses `git add -- <paths>...` with the `--` separator so paths
// beginning with `-` are treated as paths, not flags. Long path lists are
// batched under a conservative ~64 KB argv-byte budget per invocation to stay
// clear of the Linux ~128 KB and macOS ~64 KB MAX_ARG_LEN ceilings; all
// batches must succeed before the commit step.
//
// When push=false, the function stages, commits, and returns immediately with
// PushResult.CommitSHA set and RemoteResults nil. No remote network I/O occurs
// and the merge / converge loop is skipped — useful for local-only commits
// that a later push will carry to remotes. Being ahead of a remote is NORMAL on
// this path (accumulating local commits is the whole point), so the
// already-ahead reconcile below NEVER fires when push=false.
//
// Already-ahead reconcile (push=true, ≥1 remote only): before staging the new
// commit, each remote's branch is checked — network-free — against the explicit
// remote-tracking ref via `git rev-list --count <remote>/<branch>..HEAD` (the
// code never configures upstream tracking, so @{u} would fatal; the explicit ref
// is used instead, and a never-pushed strand is ahead of even a stale ref). When
// HEAD is ahead — a prior commit that was stranded and never reached the remote —
// that remote is fetched and its tip is merged into HEAD so the new commit
// fast-forwards instead of stacking ahead-N and compounding the strand. A
// reconcile that hits a persistent content conflict aborts the merge and is
// recorded so the push to that remote is skipped (the new commit still
// commits locally — capture artifacts are never lost — and surfaces as a strand
// via per-remote RemoteResults). A merge git refuses before starting is not
// recorded (the push-rejection reconcile retries after the commit), and a vault
// with a merge, cherry-pick, revert or rebase already in progress — or one a
// killed merge left half-updated — refuses the whole call before anything is
// staged (reconcileIfAhead) — the one way this reconcile fails the call, and a
// deliberate one, since nothing may be committed into such a tree. Detection is
// fail-open: if `<remote>/<branch>` does not resolve (never fetched) or the
// fetch fails, the guard simply skips that remote — those failures make it a
// compounding optimization, never a correctness gate, and never fail the
// commit. The fetch happens ONLY in this rare already-ahead state, so the
// happy path stays network-free.
//
// CROSS-CALLER IMPACT: this function is shared by tidy, `vault commit --push`,
// and memory harvest, so the already-ahead reconcile fires for all three — and
// that is intended. An already-ahead `vault commit --push` means a prior push
// stranded and must reconcile before stacking more on top; the reconcile is
// lossless (it merges the remote tip under the prior commit, or commits locally
// and strands on a real conflict).
//
// When push=true: the happy path is a sequential fast-forward push. On a
// non-fast-forward rejection, the rejected remote is fetched and its tip is
// MERGED into the local branch (the same plain merge pullCore runs); the merge
// is then pushed to that remote.
//
// 🔴 THE RECONCILE MERGES; IT NEVER REBASES AND NEVER AUTOSTASHES. A rebase
// replays every local commit, so its cost grows with the number of commits
// ahead, and gitCmd's deadline SIGKILLs git: on 2026-10-02 a 990-commit
// `rebase --autostash` was killed at pick 590 and stranded the shared vault
// mid-rebase, with the caller's own files held in the autostash. A merge is one
// step whatever the count, keeps every local SHA, and leaves modified files it
// does not touch alone; with staged changes in the index, or when the incoming
// change touches a modified or untracked file in the way, git refuses up front
// and changes nothing.
//
// Fetch failures, a refused merge, a killed merge and a true content conflict
// (after `merge --abort`, whose own failure is reported too) surface directly
// via per-remote RemoteResults rather than masquerading as downstream errors.
// mergeFetchedTip only ever aborts a merge it started itself.
//
// After every successful merge-and-push, prior remotes whose last-pushed SHA
// differs from the new HEAD are converged with a plain `git push <remote>
// <branch>`. HEAD descends from every SHA already pushed, so that push is a
// fast-forward; a concurrent writer that moved the remote in between makes it
// a non-fast-forward, which git rejects, and the failure surfaces as
// "convergence push to <remote> failed: <err>". No push here ever forces.
//
// PushResult.CommitSHA is the commit this call made. A merge does not rewrite
// it, so it is never refreshed to the merge commit on top of it.
//
// A host config with git_enabled = false (or an unreadable one) refuses
// before any git runs; the task write and the memory harvest map that refusal
// to a skipped commit.
func CommitAndPushPaths(vaultPath, message string, paths []string, push bool) (*PushResult, error) {
	if err := RefuseIfGitDisabled(vaultPath, "commit"); err != nil {
		return nil, err
	}
	return commitAndPushPathsCore(vaultPath, message, paths, push)
}

// commitAndPushPathsCore is CommitAndPushPaths after its git_enabled gate,
// for storage-internal composition (the downgrade wrapper).
func commitAndPushPathsCore(vaultPath, message string, paths []string, push bool) (*PushResult, error) {
	result, keep, err := prepareCommitPaths(vaultPath, paths)
	if err != nil || len(keep) == 0 {
		return result, err
	}

	// Serialize the .git index critical section (reconcile + stage + commit)
	// against every other vault committer. All four committers funnel here —
	// memory.Harvest's tail, vaulttidy, the vp_vault MCP tool, and `vp vault
	// commit` (CommitAndPushPathsWithDowngrade delegates to this function) — so
	// a single repo-root advisory lock covers them all. Without it two
	// concurrent committers race `git add`/`git commit` and one hard-fails with
	// `index.lock: File exists` (exit 128). The lock is released BEFORE the
	// network push so pushes are not serialized — the index race is the only
	// correctness hazard, and holding across push would throttle every remote.
	// The key is the vault root itself, distinct from the per-path keys the
	// content writers take, so there is no self-deadlock (paths hash to
	// different sidecars).
	release, lerr := vaultlock.Acquire(vaultPath, vaultPath)
	if lerr != nil {
		return nil, fmt.Errorf("acquire vault commit lock: %w", lerr)
	}
	commitLockReleased := false
	releaseCommitLock := func() {
		if !commitLockReleased {
			commitLockReleased = true
			release()
		}
	}
	defer releaseCommitLock()

	// The commit guard, before anything is staged (commitOnlyPaths is the
	// backstop): nothing commits while a split purge is unfinished.
	if err := refuseOnPendingDepartures(vaultPath); err != nil {
		return nil, err
	}

	// Remote enumeration is required only for the network push path.
	// Local-only commits (push=false) must succeed on a vault with zero
	// remotes.
	var remotes []string
	branch := "main"
	// reconcileErrs maps a remote to a persistent reconcile conflict: such a
	// remote's push is skipped below (its RemoteResults entry is the error) so
	// the new commit lands locally and strands rather than compounding.
	var reconcileErrs map[string]error
	if push {
		var rErr error
		remotes, rErr = ListRemotes(vaultPath)
		if rErr != nil {
			return nil, fmt.Errorf("listing remotes: %w", rErr)
		}
		if len(remotes) == 0 {
			return nil, fmt.Errorf("no git remotes configured in vault %s", vaultPath)
		}
		// Resolve the branch once, up front: the already-ahead guard needs it
		// before staging and the push loop reuses it. Routed through
		// currentBranch (vaultstatus.go), which is symbolic-ref-based and
		// therefore correct even when this IS the vault's first-ever commit —
		// HEAD is a valid symbolic ref (refs/heads/<branch>) whether or not
		// that branch has any commits yet, unlike the rev-parse --abbrev-ref
		// HEAD this used to call directly, which fails on an unborn HEAD.
		branch = branchOrMain(vaultPath)
		// Fix B: heal an already-ahead branch (a prior stranded commit) BEFORE a
		// new commit stacks on top of it and compounds the strand. Gated on
		// push && len(remotes) > 0 so it never fires on the downgrade path.
		var rerr error
		if reconcileErrs, rerr = reconcileIfAhead(vaultPath, remotes, branch, &result.Derived); rerr != nil {
			return nil, rerr
		}
	}

	committed, err := stageAndCommitLocked(vaultPath, nil, defaultCommitLimits, message, "", keep)
	if err != nil {
		return nil, err
	}
	if !committed {
		return result, nil
	}

	// The index critical section is done — release before the network push so
	// pushes across committers run concurrently.
	releaseCommitLock()

	// Get commit SHA.
	sha, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--short", "HEAD")
	result.CommitSHA = sha

	// Local-only commit: skip the push loop entirely.
	if !push {
		return result, nil
	}

	// Push to all remotes. branch was resolved up front (before the
	// already-ahead guard) and is reused here.
	pushCommitted(vaultPath, remotes, branch, reconcileErrs, result)
	return result, nil
}

// prepareCommitPaths is the unlocked prologue every path committer shares:
// refuse an empty path list and a nested vault git, drop paths git cannot
// stage, and check the commit identity. An empty keep with a nil error is the
// benign no-op: every path matched nothing.
func prepareCommitPaths(vaultPath string, paths []string) (*PushResult, []string, error) {
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("no paths specified")
	}
	if err := RefuseIfNestedVaultGit(vaultPath, "commit"); err != nil {
		return nil, nil, err
	}

	result := &PushResult{}

	// Drop paths that match nothing in BOTH the worktree and the index; `git
	// add -- <path>` fatals (exit 128) on such a no-match and would abort the
	// whole commit for one absent path (e.g. a never-written memory/ dir).
	// Tracked-but-deleted paths survive the filter so their deletion is staged.
	keep, skipped := filterStageablePaths(vaultPath, paths)
	result.SkippedPaths = skipped
	if len(keep) == 0 {
		// Non-empty input filtered to nothing: benign no-op, not an error.
		// (The zero-input case errored at the guard above.)
		return result, nil, nil
	}

	if err := checkIdentity(vaultPath); err != nil {
		return nil, nil, err
	}
	return result, keep, nil
}

// stageAndCommitLocked stages keep and commits exactly those paths, stamped
// with the hostname. The caller holds the vault root commit lock and has run
// the pending-departure guard. committed is false when none of keep differs
// from HEAD: a no-op, not an error.
//
// caller is the committer's own root-lock token when it has one
// (commitPathsLocked), or nil: the backstop guard exempts a lifecycle marker
// only for the very token that wrote it.
func stageAndCommitLocked(vaultPath string, caller *vaultlock.Held, limits commitLimits, message, trailers string, keep []string) (committed bool, err error) {
	// What the index held for these paths BEFORE this call staged anything.
	// On any failure below, the index is put back to exactly this — every
	// entry this call staged is undone, and nothing it did not stage moves:
	// another path's staged content, and an operator's own pre-staged entry
	// for one of keep, both survive. A failed commit used to leave its paths
	// staged, and tidy then swept a staged-new .surface ALONE (its gate treats
	// only `??` as untracked), splitting a scaffold across two commits.
	before, err := indexEntriesFor(vaultPath, keep)
	if err != nil {
		return false, fmt.Errorf("read the index before staging: %w", err)
	}
	defer func() {
		if err != nil {
			if rerr := restoreIndexEntries(vaultPath, keep, before); rerr != nil {
				err = fmt.Errorf("%w; restoring the index also failed: %v", err, rerr)
			}
		}
	}()

	// Stage only the surviving paths. Chunk under a conservative argv byte
	// budget to stay clear of MAX_ARG_LEN ceilings.
	dropped, err := stageInBatches(vaultPath, limits.add, keep)
	if err != nil {
		return false, fmt.Errorf("git add: %w", err)
	}
	// The ignored paths the guard dropped leave the commit too: a path-scoped
	// commit would otherwise take their working-tree bytes (a re-tracked drawer
	// committed after all) or fail on a path git does not know.
	commitPaths := withoutPaths(keep, dropped)
	if len(commitPaths) == 0 {
		return false, nil
	}

	// Check if anything to commit — ASKED ABOUT OUR PATHS, not the whole index.
	//
	// 🔴 THE PREDICATE AND THE COMMIT MUST ASK THE SAME QUESTION, and they used
	// not to. This read `git diff --cached --quiet` over the entire index while
	// the commit below recorded the entire index, so the two agreed — by both
	// being wrong. Now the commit is pathspec-scoped, and a whole-index
	// predicate here would answer "yes, something is staged" on the benign case
	// where OUR paths are unchanged and somebody else's content is staged, then
	// send a scoped commit off to find nothing of its own and fail. A no-op must
	// stay a no-op.
	//
	// The `_` is CORRECT and must stay. This is a PREDICATE: `--quiet` makes the
	// exit code the entire answer, and git prints nothing to explain a
	// difference it was told not to describe. The sibling predicates
	// (`rev-parse --verify --quiet`, `ls-files --error-unmatch`,
	// `rev-parse --is-inside-work-tree`, `ls-remote --exit-code`,
	// `var GIT_AUTHOR_IDENT`) are the same shape.
	staged, derr := stagedChangesIn(vaultPath, limits.commit, commitPaths)
	if derr != nil {
		return false, derr
	}
	if !staged {
		return false, nil
	}

	// Stamp with hostname, and put any trailers AFTER the stamp: git reads
	// trailers only from the last paragraph (stampedCommitMessage).
	fullMsg := stampedCommitMessage(message, trailers)

	// Commit ONLY the paths this call was given. See commitOnlyPaths.
	// A failure here — a departure backstop that fired after staging, a
	// rejecting hook, any git error — restores the index (the deferred
	// restoreIndexEntries above).
	if err := commitOnlyPathsFor(vaultPath, caller, limits.commit, fullMsg, commitPaths); err != nil {
		return false, err
	}
	return true, nil
}

// indexEntriesFor returns the index entries (`git ls-files -s` lines: "mode
// oid stage<TAB>path") at or under each of paths, keyed by path. A path may
// name a directory, so the match is the path itself or anything beneath it.
func indexEntriesFor(vaultPath string, paths []string) (map[string][]string, error) {
	cmd := exec.Command("git", "-C", vaultPath, "ls-files", "-s", "-z")
	cmd.Env = SafeGitEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files -s: %w", err)
	}
	want := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		want[strings.TrimSuffix(p, "/")] = struct{}{}
	}
	entries := map[string][]string{}
	for _, rec := range strings.Split(string(out), "\x00") {
		tab := strings.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		path := rec[tab+1:]
		// The path itself or any ancestor directory: one set lookup per
		// level, so a large commit over a large index stays linear.
		for p := path; ; {
			if _, ok := want[p]; ok {
				entries[path] = append(entries[path], rec)
				break
			}
			i := strings.LastIndexByte(p, '/')
			if i < 0 {
				break
			}
			p = p[:i]
		}
	}
	return entries, nil
}

// restoreIndexEntries puts the index entries at or under paths back to before
// (indexEntriesFor's snapshot), touching only the entries that differ: an
// entry this call added is removed, one it changed gets its old line back.
// It writes the index directly (update-index), never the work tree, and it
// needs no HEAD, so it works on an unborn branch too.
func restoreIndexEntries(vaultPath string, paths []string, before map[string][]string) error {
	after, err := indexEntriesFor(vaultPath, paths)
	if err != nil {
		return err
	}
	var remove, add []string
	for path, lines := range after {
		if !slices.Equal(lines, before[path]) {
			remove = append(remove, path)
		}
	}
	for path, lines := range before {
		if !slices.Equal(lines, after[path]) {
			add = append(add, lines...)
		}
	}
	if len(remove) > 0 {
		cmd := exec.Command("git", "-C", vaultPath, "update-index", "-z", "--force-remove", "--stdin")
		cmd.Env = SafeGitEnv()
		cmd.Stdin = strings.NewReader(strings.Join(remove, "\x00") + "\x00")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git update-index --force-remove: %s: %w", bytes.TrimSpace(out), err)
		}
	}
	if len(add) > 0 {
		cmd := exec.Command("git", "-C", vaultPath, "update-index", "-z", "--index-info")
		cmd.Env = SafeGitEnv()
		cmd.Stdin = strings.NewReader(strings.Join(add, "\x00") + "\x00")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git update-index --index-info: %s: %w", bytes.TrimSpace(out), err)
		}
	}
	return nil
}

// commitPathsLocked is commitAndPushPathsCore's local commit for a caller that
// already holds the vault root commit lock: the vault is held.Root(), and the
// lock is not taken again (vaultlock.Acquire is not reentrant). It never pushes
// and never reconciles, so nothing under it re-acquires the lock either; the
// caller publishes. Like the core, it does not read git_enabled — the caller
// gates that. A token that is not a live root lock refuses before any git runs.
// trailers, when set, is the commit's final paragraph, after the hostname
// stamp, so git parses it: a lifecycle copy's Vp-Copy-* block goes there, never
// in message. limits are the git add and git commit limits: defaultCommitLimits
// for a commit of a few files, bulkCommitLimits for a whole project.
func commitPathsLocked(held *vaultlock.Held, limits commitLimits, message, trailers string, paths []string) (*PushResult, error) {
	if err := held.RequireRoot(); err != nil {
		return nil, err
	}
	vaultPath := held.Root()
	result, keep, err := prepareCommitPaths(vaultPath, paths)
	if err != nil || len(keep) == 0 {
		return result, err
	}
	if err := refuseOnPendingDeparturesFor(vaultPath, held); err != nil {
		return nil, err
	}
	committed, err := stageAndCommitLocked(vaultPath, held, limits, message, trailers, keep)
	if err != nil {
		return nil, err
	}
	if !committed {
		return result, nil
	}
	sha, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--short", "HEAD")
	result.CommitSHA = sha
	return result, nil
}

// commitRemovalsBeforeUnstageHook runs inside CommitRemovals' failure path,
// under the vault commit lock, immediately before the removal is unstaged.
// Production leaves it a no-op; a test uses it to prove the lock is held there
// and to make the unstage itself fail.
var commitRemovalsBeforeUnstageHook = func() {}

// RemovalsLeftInHEADError is CommitRemovals' error when its commit landed but
// HEAD still holds some of the paths it was asked to remove — a race with a
// writer outside vp. The commit (SHA) carries the other paths; only Paths are
// left for the operator.
type RemovalsLeftInHEADError struct {
	SHA   string
	Paths []string
}

func (e *RemovalsLeftInHEADError) Error() string {
	return fmt.Sprintf("commit %s does not remove %s", e.SHA, strings.Join(e.Paths, ", "))
}

// CommitRemovals commits the removal of rels — vault-relative, forward-slash
// paths of tracked files an operator asked vp to remove — as one local commit
// carrying exactly those paths. It never pushes: the operator's next `vp vault
// sync` publishes it. Unlike the verified prune it does not fetch or consult a
// remote either: the operator named each file, and a newer override of one on a
// remote meets this commit at the next pull as a modify/delete conflict, which
// storage.Pull leaves for the operator.
//
// A path may be removed from the worktree only (" D") or already staged for
// deletion ("D ", e.g. by `git rm`, or by an earlier CommitRemovals whose
// unstage failed): a staged deletion holds no bytes, so it is committed as it
// stands, without the `git add` that would fatal on it.
//
// The whole stage→commit sequence runs under the vault commit lock, as every vp
// committer's does. On any stage or commit failure — a pre-commit hook that
// refuses, say — exactly its paths are unstaged before the lock is released, so
// each removal is left as an unstaged " D". Left staged, the next `vp config
// sync` would read vp's own deletion as someone else's staged change and defer
// it on every run; unstaged, it classifies the path as gone. If the unstage
// itself fails, the error says so and names the manual command.
//
// A path git no longer tracks — absent from the worktree, the index and HEAD,
// because another committer got there first — is dropped; if none is left the
// call is a no-op. After the commit each path is looked up in HEAD again, and
// any still there are returned as a *RemovalsLeftInHEADError alongside the
// result naming the commit that did land.
//
// vaultPath must be the root of its own repository. vp never commits into a
// repository that merely encloses the vault; that caller must not call this.
//
// A host config with git_enabled = false (or an unreadable one) refuses
// before any git runs. Template reset refuses up front at its own preflight,
// before removing anything; this gate is the backstop.
func CommitRemovals(vaultPath, message string, rels []string) (*PushResult, error) {
	if err := RefuseIfGitDisabled(vaultPath, "commit the template removal"); err != nil {
		return nil, err
	}
	if len(rels) == 0 {
		return nil, fmt.Errorf("no paths specified")
	}
	result := &PushResult{}
	stage, skipped := filterStageablePaths(vaultPath, rels)
	// Of the paths `git add` cannot take, a path HEAD still holds is a staged
	// deletion: it is committed without staging.
	var stagedDeletions []string
	for _, rel := range skipped {
		if _, found, err := ReadCommittedBlob(vaultPath, rel); err == nil && found {
			stagedDeletions = append(stagedDeletions, rel)
			continue
		}
		result.SkippedPaths = append(result.SkippedPaths, rel)
	}
	commit := append(append([]string(nil), stage...), stagedDeletions...)
	if len(commit) == 0 {
		return result, nil
	}
	if err := checkIdentity(vaultPath); err != nil {
		return nil, err
	}

	release, lerr := vaultlock.Acquire(vaultPath, vaultPath)
	if lerr != nil {
		return nil, fmt.Errorf("acquire vault commit lock: %w", lerr)
	}
	released := false
	unlock := func() {
		if !released {
			released = true
			release()
		}
	}
	defer unlock()

	// The commit guard, before anything is staged (commitOnlyPaths is the
	// backstop).
	if err := refuseOnPendingDepartures(vaultPath); err != nil {
		return nil, err
	}

	fail := func(err error) (*PushResult, error) {
		commitRemovalsBeforeUnstageHook()
		if _, rerr := gitCmd(vaultPath, 10*time.Second, append([]string{"--literal-pathspecs", "reset", "-q", "--"}, commit...)...); rerr != nil {
			err = fmt.Errorf("%w (and unstaging the removal failed: %v — run: git -C %s reset -q -- %s)",
				err, rerr, vaultPath, strings.Join(commit, " "))
		}
		return nil, err
	}
	if len(stage) > 0 {
		dropped, err := stageInBatches(vaultPath, gitAddTimeout, stage)
		if err != nil {
			return fail(fmt.Errorf("git add: %w", err))
		}
		// An ignored path the guard dropped is not committed either (see
		// stageInBatches).
		commit = withoutPaths(commit, dropped)
		if len(commit) == 0 {
			return result, nil
		}
	}
	staged, err := stagedChangesIn(vaultPath, gitCommitTimeout, commit)
	if err != nil {
		return fail(err)
	}
	if !staged {
		return result, nil
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	if err := commitOnlyPaths(vaultPath, fmt.Sprintf("%s\n\n[%s]", message, hostname), commit); err != nil {
		return fail(err)
	}
	unlock()

	sha, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--short", "HEAD")
	result.CommitSHA = sha
	var still []string
	for _, rel := range commit {
		if _, found, err := ReadCommittedBlob(vaultPath, rel); err != nil || found {
			still = append(still, rel)
		}
	}
	if len(still) > 0 {
		return result, &RemovalsLeftInHEADError{SHA: sha, Paths: still}
	}
	return result, nil
}

// CheckCommitIdentity returns nil when git can resolve a committer identity for
// vaultPath, and otherwise an error naming how to set one. It is the check every
// vp committer makes before it commits, exported so a caller can make it before
// it changes anything the commit would carry.
func CheckCommitIdentity(vaultPath string) error {
	return checkIdentity(vaultPath)
}

// StagedChange reports whether the index holds a change for rel that HEAD does
// not: a staged modification, addition or deletion. Any git failure is an
// error, never "not staged" — a caller about to commit rel's removal must not
// fail open on a corrupt index, because the commit would take the staged bytes
// with it.
func StagedChange(vaultPath, rel string) (bool, error) {
	headOID, inHead, err := treeEntryOID(vaultPath, "HEAD", rel)
	if err != nil {
		return false, err
	}
	indexOID, inIndex, err := indexEntryOID(vaultPath, rel)
	if err != nil {
		return false, err
	}
	return inHead != inIndex || headOID != indexOID, nil
}

// StagedDeletion reports whether the index holds a staged deletion of rel:
// HEAD has the path and the index does not. It is the one staged change that
// holds no bytes — committing it loses nothing a backup would need. Any git
// failure is an error.
func StagedDeletion(vaultPath, rel string) (bool, error) {
	_, inHead, err := treeEntryOID(vaultPath, "HEAD", rel)
	if err != nil {
		return false, err
	}
	_, inIndex, err := indexEntryOID(vaultPath, rel)
	if err != nil {
		return false, err
	}
	return inHead && !inIndex, nil
}

// GitTopLevel returns the top level of the work tree dir is in. For a vault
// nested in another repository (VaultGitNested) it names that repository.
func GitTopLevel(dir string) (string, error) {
	return gitCmd(dir, 10*time.Second, "rev-parse", "--show-toplevel")
}

// GitPathIgnored reports whether git would ignore the vault-relative path rel
// (`git check-ignore -q`). Exit status 1 is the answer "not ignored"; any other
// failure is an error.
//
// It is the one git call that runs WITHOUT literal pathspecs: check-ignore
// refuses the mode outright ("pathspec magic not supported by this command:
// 'literal'"), so it opts out with gitenv.GlobPathspecs. check-ignore reads its
// arguments as pathnames, not globs, but a leading ':' would still be magic
// (':x' misreports, ':!x' exits 128), so rel is passed as "./"+rel. Not
// ":(top)"+rel: that anchors to the top of the repository git finds, which for
// a vault nested in another repository (VaultGitNested) is the outer one, not
// the vault cmd.Dir names. An empty rel is refused: "./" alone names the vault
// root and would answer "not ignored" for a path nobody gave.
func GitPathIgnored(vaultPath, rel string) (bool, error) {
	if rel == "" {
		return false, fmt.Errorf("check whether a path is ignored: empty path")
	}
	_, err := gitCmdEnv(vaultPath, 10*time.Second, []string{gitenv.GlobPathspecs}, "check-ignore", "-q", "--", "./"+rel)
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// pushCommitted pushes the commit a caller just made to every remote, with the
// rejection recovery CommitAndPushPaths documents: a rejected remote is fetched
// and its tip merged, a refused or conflicted merge is aborted and recorded,
// and prior remotes are converged with a plain fast-forward push. It fills
// result.RemoteResults (nil = success). A remote in reconcileErrs is skipped
// and reported with that error. Callers must have
// released the vault commit lock: pushes are deliberately not serialised.
func pushCommitted(vaultPath string, remotes []string, branch string, reconcileErrs map[string]error, result *PushResult) {
	result.RemoteResults = make(map[string]error, len(remotes))
	remoteSHA := make(map[string]string, len(remotes))

	for _, remote := range remotes {
		// A persistent already-ahead reconcile conflict (Fix B) means this
		// remote diverges on content the prior strand can't replay cleanly.
		// Skip the push and surface the reconcile error so the new commit (which
		// did land locally above) reports as stranded rather than being retried.
		if err := reconcileErrs[remote]; err != nil {
			result.RemoteResults[remote] = err
			continue
		}

		curSHA, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "HEAD")

		if _, pushErr := gitCmd(vaultPath, 60*time.Second, "push", remote, branch); pushErr == nil {
			// Happy path: fast-forward push succeeded.
			remoteSHA[remote] = curSHA
			result.RemoteResults[remote] = nil
			afterPushHook(remote)
			continue
		}

		// Rejection path (non-fast-forward or other push error).
		//
		// 🔴 THE RECONCILE RUNS UNDER THE VAULT COMMIT LOCK. The merge rewrites
		// the index and the working tree, and must not run over another
		// committer's in-flight work (a split purge's staged removal). Every
		// committer holds this key across its index critical section, so taking
		// it here makes the reconcile wait for them. The network push itself
		// stays outside the lock; the fetch is inside because the guard and the
		// merge must see the tip it fetched. Callers released the lock before
		// calling pushCommitted, so this never nests.
		releaseReconcile, lerr := vaultlock.Acquire(vaultPath, vaultPath)
		if lerr != nil {
			result.RemoteResults[remote] = fmt.Errorf("acquire vault commit lock for the reconcile: %w", lerr)
			continue
		}
		reconciled := reconcileRejectedPush(vaultPath, remote, branch, result)
		releaseReconcile()
		if !reconciled {
			continue
		}
		curSHA, _ = gitCmd(vaultPath, 10*time.Second, "rev-parse", "HEAD")

		if _, pushErr := gitCmd(vaultPath, 60*time.Second, "push", remote, branch); pushErr != nil {
			// Push error after the merge (auth, network, etc.).
			result.RemoteResults[remote] = pushErr
			continue
		}
		remoteSHA[remote] = curSHA
		result.RemoteResults[remote] = nil
		afterPushHook(remote)

		// Converge prior remotes whose recorded SHA != current HEAD. HEAD
		// descends from every recorded SHA (the reconcile merged, it did not
		// rewrite), so a plain push fast-forwards; a remote a concurrent writer
		// moved in between is a non-fast-forward, and git refuses it.
		for priorRemote, priorSHA := range remoteSHA {
			if priorSHA == curSHA {
				continue
			}
			if _, convErr := gitCmd(vaultPath, 60*time.Second, "push", priorRemote, branch); convErr != nil {
				result.RemoteResults[priorRemote] = fmt.Errorf(
					"convergence push to %s failed: %w", priorRemote, convErr)
				// Leave remoteSHA[priorRemote] unchanged — caller sees
				// divergent state via per-remote error.
				continue
			}
			remoteSHA[priorRemote] = curSHA
			// result.RemoteResults[priorRemote] stays nil (still success).
			log.Printf("vault: converged %s from %s\n", priorRemote, short(priorSHA))
		}
	}
}

// CommitAndPushPathsWithDowngrade wraps CommitAndPushPaths with the remote-
// downgrade policy shared by tidy and harvest: when push is requested but the
// vault has zero configured remotes, it downgrades to a local-only commit
// (CommitAndPushPaths errors on push against a remote-less vault) and reports
// downgraded=true. When push is false it passes straight through and downgraded
// is always false. The returned *PushResult and error are CommitAndPushPaths's
// own (RemoteResults populated only when an effective push ran).
//
// Its git_enabled gate is its first statement, because downgradePush runs
// `git remote` before CommitAndPushPaths would reach its own.
func CommitAndPushPathsWithDowngrade(vaultPath, message string, paths []string, push bool) (res *PushResult, downgraded bool, err error) {
	if err := RefuseIfGitDisabled(vaultPath, "commit"); err != nil {
		return nil, false, err
	}
	return commitAndPushPathsWithDowngradeCore(vaultPath, message, paths, push)
}

// commitAndPushPathsWithDowngradeCore is CommitAndPushPathsWithDowngrade after
// its git_enabled gate, for storage-internal composition (TidyVault).
func commitAndPushPathsWithDowngradeCore(vaultPath, message string, paths []string, push bool) (res *PushResult, downgraded bool, err error) {
	effectivePush, downgraded, err := downgradePush(vaultPath, push)
	if err != nil {
		return nil, false, err
	}
	res, err = commitAndPushPathsCore(vaultPath, message, paths, effectivePush)
	if err != nil {
		return nil, downgraded, err
	}
	return res, downgraded, nil
}

// downgradePush is the remote-downgrade policy CommitAndPushPathsWithDowngrade
// and PruneMirrorsVerifiedWithDowngrade share: a push requested against a
// vault with zero remotes becomes a local-only commit, reported as downgraded.
func downgradePush(vaultPath string, push bool) (effective, downgraded bool, err error) {
	if !push {
		return false, false, nil
	}
	remotes, err := ListRemotes(vaultPath)
	if err != nil {
		return false, false, fmt.Errorf("listing remotes: %w", err)
	}
	if len(remotes) == 0 {
		return false, true, nil
	}
	return true, false, nil
}

// reconcileIfAhead heals branches that are already ahead of a remote — the
// signature of a prior commit that was stranded (never pushed) — BEFORE a new
// commit stacks on top and compounds the strand (the ahead-2 → ahead-N bug). It
// runs only on the push path with remotes present; being ahead is normal and
// intended on the push=false / no-remote downgrade path.
//
// Detection is network-free and does NOT depend on @{u}: the code never
// configures upstream tracking (pushes are explicit `git push <remote>
// <branch>`), so @{u} would fatal "no upstream configured". The explicit
// remote-tracking ref `<remote>/<branch>` is used instead. A stranded commit was
// never pushed, so it is ahead of even a STALE `<remote>/<branch>` — no fetch is
// needed to detect it.
//
// The guard is fail-open: a remote whose `<remote>/<branch>` ref does not resolve
// (never fetched) is skipped, and a fetch failure is skipped — it is a
// compounding optimization, never a correctness gate, so it must never hard-error
// the commit path. The single fetch occurs ONLY in the rare already-ahead state,
// keeping the happy path network-free.
//
// For each ahead remote it fetches then merges `<remote>/<branch>`
// (mergeFetchedTip), and sorts a failure three ways:
//
//   - A departure refusal or a true content conflict (aborted) is recorded in
//     the returned map, so the caller skips that remote's push and strands the
//     new commit.
//   - A refusal before the merge started (*mergeNotStartedError) is NOT
//     recorded. It runs before the caller's paths are staged, so the caller's
//     own uncommitted files are a common cause; the push-rejection reconcile
//     merges again after the commit, when they no longer stand in the way.
//   - A tree nothing may commit into (*vaultTreeUnsafeError: an operation
//     already in progress, a killed merge, a failed abort) is returned as the
//     error, and the caller commits nothing.
//
// Otherwise HEAD now descends from the remote tip with the prior commit
// unchanged beneath the merge, and the new commit will fast-forward. The
// returned map is nil when nothing was recorded.
func reconcileIfAhead(vaultPath string, remotes []string, branch string, rep *DerivedMergeReport) (map[string]error, error) {
	var reconcileErrs map[string]error
	for _, remote := range remotes {
		ref := remote + "/" + branch
		// Fail-open: skip remotes whose tracking ref never materialized.
		if _, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--verify", "--quiet", ref); err != nil {
			continue
		}
		// Network-free ahead check against the (possibly stale) tracking ref.
		count, err := gitCmd(vaultPath, 10*time.Second, "rev-list", "--count", ref+"..HEAD")
		if err != nil || count == "" || count == "0" {
			continue
		}
		// Already ahead: a prior commit never reached this remote. Reconcile now —
		// the only fetch on an otherwise network-free path — so the new commit
		// fast-forwards. A fetch failure is fail-open (skip; the normal push loop
		// retains its own fetch+merge recovery).
		if _, err := gitCmd(vaultPath, 60*time.Second, "fetch", remote); err != nil {
			continue
		}
		mergeErr := mergeFetchedTip(vaultPath, remote, branch, rep)
		var notStarted *mergeNotStartedError
		var unsafe *vaultTreeUnsafeError
		switch {
		case mergeErr == nil:
			// Reconciled: HEAD merged the remote tip with the prior commit
			// unchanged; the new commit fast-forwards.
		case errors.As(mergeErr, &unsafe):
			return nil, fmt.Errorf("reconcile against %s: %w", remote, mergeErr)
		case errors.As(mergeErr, &notStarted):
			// Nothing changed; the push-rejection reconcile retries after the
			// commit (see above).
		default:
			// The prior strand cannot merge with the remote tip (or the tip
			// carries a departure of its project): record it so the caller
			// skips this remote's push; the new commit stays local (strand).
			if reconcileErrs == nil {
				reconcileErrs = make(map[string]error, 1)
			}
			reconcileErrs[remote] = reconcileFailure("reconcile against "+remote+" failed", mergeErr)
		}
	}
	return reconcileErrs, nil
}

// HasUncommittedChanges reports whether `git status --porcelain` limited to the
// given vault-relative paths produces any output. Empty/clean → false. Returns
// false (not an error) when vaultPath is not a git repo (e.g. a never-init'd
// vault), so callers can treat "no repo" the same as "nothing to commit".
//
// Paths are passed after a `--` separator so entries beginning with `-` are
// treated as paths, not flags. A directory path includes everything beneath it.
func HasUncommittedChanges(vaultPath string, relPaths ...string) (bool, error) {
	if _, err := gitCmd(vaultPath, 5*time.Second, "rev-parse", "--is-inside-work-tree"); err != nil {
		// Not a git repo (or git unavailable) → treat as clean, not an error.
		return false, nil
	}
	args := make([]string, 0, len(relPaths)+3)
	args = append(args, "status", "--porcelain", "--")
	args = append(args, relPaths...)
	out, err := gitCmd(vaultPath, 10*time.Second, args...)
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(out) != "", nil
}

// GitPathIsTracked reports whether the vault-relative path has an entry in the
// index (`git ls-files --error-unmatch -- <path>`). Returns false (not an error)
// when vaultPath is not a git repo, matching HasUncommittedChanges' shape.
//
// 🔴 IT EXISTS BECAUSE `git checkout -- a b c` IS ALL-OR-NOTHING OVER ITS
// PATHSPECS. One path git has never seen makes the whole command a pathspec error
// that restores NOTHING, while an operator who ran it reasonably believes the undo
// happened. Any caller printing a rollback command has to filter its path list
// through this first; "the file is on disk" is not the predicate, because an
// untracked file is on disk and still has no committed state to return to.
func GitPathIsTracked(vaultPath, relPath string) (bool, error) {
	if _, err := gitCmd(vaultPath, 5*time.Second, "rev-parse", "--is-inside-work-tree"); err != nil {
		return false, nil
	}
	if _, err := gitCmd(vaultPath, 10*time.Second, "ls-files", "--error-unmatch", "--", relPath); err != nil {
		// --error-unmatch exits non-zero for an untracked path. That is the
		// ANSWER, not a failure, and it is indistinguishable here from a real
		// git fault — which is why the false is returned plainly rather than
		// wrapped as an error a caller would have to decide how to treat.
		return false, nil
	}
	return true, nil
}

// filterStageablePaths partitions paths into those safe to `git add` (keep) and
// those that would make `git add -- <path>` fatal because they match nothing in
// BOTH the worktree and the index (skipped).
//
// The predicate is deletion-safe: `git add` only aborts (exit 128) when a path
// is absent from the worktree AND not tracked. A tracked file deleted from the
// worktree is NOT a no-match — `git add` correctly stages the deletion — so it
// must be kept. A naive os.Stat filter would drop it and silently fail to commit
// the removal. Keep path P when os.Lstat(P) succeeds (present in the worktree,
// including as a symlink or directory) OR `git ls-files --error-unmatch -- P`
// exits 0 (tracked, possibly deleted). Skip only when both fail.
//
// Paths are literal vault-relative strings; no glob matching is performed,
// because SafeGitEnv gives every git call literal pathspecs.
func filterStageablePaths(vaultPath string, paths []string) (keep, skipped []string) {
	for _, p := range paths {
		if _, err := os.Lstat(filepath.Join(vaultPath, p)); err == nil {
			keep = append(keep, p)
			continue
		}
		// Absent from the worktree — keep only if git tracks it (a staged
		// deletion is legitimate work; `git add` will record the removal).
		if _, err := gitCmd(vaultPath, 10*time.Second, "ls-files", "--error-unmatch", "--", p); err == nil {
			keep = append(keep, p)
			continue
		}
		skipped = append(skipped, p)
	}
	return keep, skipped
}

// stageBatchByteBudget caps the per-`git add` argv path-byte budget at ~64 KB.
// Linux MAX_ARG_LEN is ~128 KB and macOS is ~64 KB; the lower figure governs.
// Each path costs len(path)+1 bytes (NUL terminator) when measured against the
// kernel argv ceiling.
const stageBatchByteBudget = 64 * 1024

// gitAddTimeout and gitCommitTimeout are the limits on one `git add` batch and
// on the staged-diff check and `git commit` that follow it, for a commit of a
// handful of files: a wrap, a tidy, a lifecycle record.
const (
	gitAddTimeout    = 30 * time.Second
	gitCommitTimeout = 10 * time.Second
)

// commitLimits are the limits of one stage-and-commit. Almost every commit is
// small and takes defaultCommitLimits. `vp vault copy` commits a whole project
// (thousands of files, hundreds of MiB to hash into the object store) and
// takes bulkCommitLimits: a `git add` killed at 30 s leaves .git/index.lock
// behind, and the rollback then cannot reset.
type commitLimits struct {
	add    time.Duration // one `git add` batch
	commit time.Duration // the staged-diff check, and `git commit`
}

var (
	defaultCommitLimits = commitLimits{add: gitAddTimeout, commit: gitCommitTimeout}
	bulkCommitLimits    = commitLimits{add: lifecycleBulkTimeout, commit: lifecycleBulkTimeout}
)

// stageInBatches runs `git add -- <chunk>...` over paths, splitting into
// chunks whose combined argv-path bytes stay under stageBatchByteBudget.
// Always emits the `--` separator so paths beginning with `-` are treated as
// paths, not flags. limit bounds each batch.
//
// THE STAGING GUARD. Before each `git add`, the batch's ignored paths are
// dropped (ignoredPaths: one `git check-ignore --no-index --stdin -z` per
// batch) and logged. `git add` of a TRACKED file under an ignored directory
// stages it AND exits 1, so a migrated vault — whose drawers are ignored —
// would otherwise fail every tidy that met a re-tracked drawer. On a vault
// without the derived ignore lines (no migration marker) nothing a capture
// writes is ignored, so the guard drops nothing that would have been staged.
//
// It returns the paths it dropped. A caller that commits must remove them from
// the paths it hands the commit (withoutPaths): `git commit --only` takes the
// WORKING-TREE bytes of every path it names, so a dropped tracked file would
// be committed anyway, and a dropped untracked one fails the whole commit
// ("pathspec ... did not match"). A directory a caller names is not a dropped
// path: `git add -- <dir>` still updates tracked files under it, ignored or
// not, so callers name files.
func stageInBatches(vaultPath string, limit time.Duration, paths []string) (dropped []string, err error) {
	batch := make([]string, 0, len(paths))
	bytes := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		ignored, err := ignoredPaths(vaultPath, limit, batch)
		if err != nil {
			return err
		}
		args := make([]string, 0, len(batch)+2)
		args = append(args, "add", "--")
		for _, p := range batch {
			if _, skip := ignored[p]; skip {
				slog.Info("stage: dropped an ignored path", "vault", vaultPath, "path", p)
				dropped = append(dropped, p)
				continue
			}
			args = append(args, p)
		}
		if len(args) > 2 {
			if _, err := gitCmd(vaultPath, limit, args...); err != nil {
				return err
			}
		}
		batch = batch[:0]
		bytes = 0
		return nil
	}
	for _, p := range paths {
		cost := len(p) + 1
		if len(batch) > 0 && bytes+cost > stageBatchByteBudget {
			if err := flush(); err != nil {
				return dropped, err
			}
		}
		batch = append(batch, p)
		bytes += cost
	}
	if err := flush(); err != nil {
		return dropped, err
	}
	return dropped, nil
}

// withoutPaths returns paths minus every member of drop, in order.
func withoutPaths(paths, drop []string) []string {
	if len(drop) == 0 {
		return paths
	}
	gone := make(map[string]struct{}, len(drop))
	for _, p := range drop {
		gone[p] = struct{}{}
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, ok := gone[p]; !ok {
			out = append(out, p)
		}
	}
	return out
}

// checkIgnoreRun runs one `git check-ignore` over a batch; a test seam counts
// the calls.
var checkIgnoreRun = gitCmdStdinEnv

// ignoredPaths returns the members of paths that git's ignore rules match, by
// path alone: `git check-ignore --no-index --stdin -z`, ONE process for the
// whole batch.
//
// --no-index is the point. Without it check-ignore answers "not ignored" for a
// tracked file, even one under an ignored directory — which is exactly the
// file `git add` stages and then fails on (GitPathIgnored omits it, and is not
// used here for that reason). Exit 1 means "none ignored"; any other failure
// is an error.
//
// Like GitPathIgnored it runs WITHOUT literal pathspecs, the second sanctioned
// user of gitenv.GlobPathspecs: check-ignore refuses literal mode outright
// (exit 128, "pathspec magic not supported by this command: 'literal'"). Its
// stdin lines are read as pathspecs, so a leading ':' would be magic (':!x'
// exits 128); each path is therefore sent as "./"+path, which git reads as a
// plain name and echoes back with the prefix, stripped here.
func ignoredPaths(vaultPath string, limit time.Duration, paths []string) (map[string]struct{}, error) {
	var in strings.Builder
	for _, p := range paths {
		in.WriteString("./" + p + "\x00")
	}
	out, code, err := checkIgnoreRun(vaultPath, limit, in.String(), []string{gitenv.GlobPathspecs},
		"check-ignore", "--no-index", "--stdin", "-z")
	if code == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("git check-ignore: %w", err)
	}
	ignored := map[string]struct{}{}
	for p := range strings.SplitSeq(out, "\x00") {
		if p != "" {
			ignored[strings.TrimPrefix(p, "./")] = struct{}{}
		}
	}
	return ignored, nil
}

// gitCmdStdin is gitCmd with a standard input, raw (untrimmed) output for
// NUL-delimited records, and the exit code (-1 when git did not exit).
func gitCmdStdin(dir string, timeout time.Duration, stdin string, args ...string) (string, int, error) {
	return gitCmdStdinEnv(dir, timeout, stdin, nil, args...)
}

// gitCmdStdinEnv is gitCmdStdin with extraEnv appended after SafeGitEnv's own
// settings, so it wins (gitenv.GlobPathspecs, for check-ignore).
func gitCmdStdinEnv(dir string, timeout time.Duration, stdin string, extraEnv []string, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = SafeGitEnv(append([]string{"GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true"}, extraEnv...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return string(out), 0, nil
	}
	code := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	}
	if ctx.Err() != nil {
		return string(out), code, &GitError{Err: fmt.Errorf("timed out after %s: %w: %w", timeout, ctx.Err(), err)}
	}
	return string(out), code, &GitError{Detail: gitDetailLine(strings.TrimSpace(stderr.String())), Err: err}
}

// chunkPaths splits paths into groups whose combined argv-path bytes stay under
// stageBatchByteBudget. Extracted so the staging pass and the "is anything of
// ours staged?" predicate chunk identically — two different splits over one path
// list is a way for the two to disagree about the same set.
func chunkPaths(paths []string) [][]string {
	var out [][]string
	batch := make([]string, 0, len(paths))
	bytes := 0
	for _, p := range paths {
		cost := len(p) + 1
		if len(batch) > 0 && bytes+cost > stageBatchByteBudget {
			out = append(out, batch)
			batch = make([]string, 0, len(paths))
			bytes = 0
		}
		batch = append(batch, p)
		bytes += cost
	}
	if len(batch) > 0 {
		out = append(out, batch)
	}
	return out
}

// stagedChangesIn reports whether any of the given paths differs between the
// index and HEAD — the scoped form of the "anything to commit?" question.
//
// Chunked for the same argv reason stageInBatches is, and `git diff` is the one
// command in this file that CANNOT take --pathspec-from-file (it rejects the
// flag with a usage error, exit 129), so the path list has to ride in argv here.
// Any chunk reporting a difference is enough: the question is existential.
func stagedChangesIn(vaultPath string, limit time.Duration, paths []string) (bool, error) {
	for _, chunk := range chunkPaths(paths) {
		args := make([]string, 0, len(chunk)+4)
		args = append(args, "diff", "--cached", "--quiet", "--")
		args = append(args, chunk...)
		if _, err := gitCmd(vaultPath, limit, args...); err != nil {
			// Non-zero exit from --quiet means "there IS a difference". It is
			// also what a genuine failure looks like, which is acceptable here:
			// the false positive costs one commit attempt that then reports its
			// own error, whereas treating a failure as "nothing to commit" would
			// silently skip a write the caller believes landed.
			return true, nil
		}
	}
	return false, nil
}

// commitOnlyPaths runs `git commit` restricted to the given paths.
//
// 🔴 THIS IS THE HALF THAT MAKES THE FUNCTION'S NAME TRUE. Staging was always
// scoped — `git add -- <paths>`, never `git add -A`, and that invariant is
// asserted in several places. The commit was not: `git commit -m <msg>` with no
// pathspec records EVERYTHING IN THE INDEX. So content a human had already
// staged, or that another tool left staged, rode out under whichever
// machine-authored message this caller supplied — tidy's "sweep N capture
// artifacts", the memory harvest's, or a task writer's "vault: amend task p/s".
// A commit message asserting machine provenance over unreviewed human content is
// exactly the outcome the tidy carve-out was rejected for producing on purpose;
// it was reachable by accident through every caller that had scoped its paths
// correctly.
//
// 🔴 THE PATHSPEC RIDES IN A FILE, NOT IN ARGV. A commit cannot be split into
// batches the way staging can — it is one atomic operation over the whole set —
// so the chunking that keeps `git add` under MAX_ARG_LEN has no equivalent here,
// and a large sweep (tidy over a busy vault) would push a multi-thousand-path
// argv at the kernel. --pathspec-from-file with --pathspec-file-nul removes the
// ceiling entirely and is byte-exact for any path.
//
// The file is written OUTSIDE the vault. A scratch file inside it would be
// untracked dirt for the duration of the commit — which is dirt this very
// subsystem classifies, reports, and refuses to sync on.
//
// Paths left staged that are NOT in this list stay staged. That is deliberate:
// they are someone else's in-flight work, and quietly un-staging them would be a
// different way of taking it away from them.
//
// # Two measured behaviour changes, both accepted
//
// 🔴 A MERGE OR CHERRY-PICK IN PROGRESS NOW REFUSES, WHERE THE UNSCOPED FORM
// COMMITTED. Measured on git 2.55.0: with MERGE_HEAD present, git rejects a
// pathspec commit with `fatal: cannot do a partial commit during a merge.` (exit
// 128) unconditionally — even with zero conflicts left and even when the
// pathspec names a file the merge never touched. CHERRY_PICK_HEAD behaves the
// same; rebase and revert are not guarded by git.
//
// Refusing is the CORRECT outcome and must not be "fixed" by falling back to the
// unscoped commit. A merge in progress is the state in which the index is MOST
// likely to hold content this caller never chose, so the fallback would
// reintroduce the exact defect this function exists to close, precisely where it
// does the most damage: a human's half-finished merge, committed as a merge
// commit, under a message reading "vault tidy: sweep N capture artifacts".
//
// git's own sentence is left to surface rather than wrapped in a hand-written
// one. It is a single accurate line that names what to finish, and
// TestCommitPathSurfacesGitsOwnDiagnosis is the standing gate that git's
// diagnosis reaches the caller instead of a bare exit code.
//
// 🔴 IT COMMITS WORKTREE CONTENT FOR THE NAMED PATHS, NOT INDEX CONTENT. That is
// what a pathspec commit means (`-- <paths>` is byte-identical to `--only --
// <paths>`; verified, same commit hash). In this function's sequence the
// distinction is inert, because `git add -- <paths>` runs immediately above and
// makes index and worktree identical for exactly these paths. What it leaves is
// a narrow window: an external writer touching one of our paths between the add
// and the commit has its edit committed rather than ignored. The window is
// milliseconds, inside the vault commit lock that serialises every vp committer,
// and it replaces a strictly worse hazard — committing the ENTIRE index, every
// time, with no window at all because it was unconditional.
//
// The scoped predicate above and this commit can therefore only disagree if the
// tree changes underneath them, which is a race the caller should see as an
// error rather than as silence. No "nothing to commit" special case is written
// here for that reason: git reports it with no fatal:/error: prefix, so
// tolerating it would mean matching on prose, and turning a genuine race into a
// silent no-op is the wrong direction for a function whose whole job is making a
// write durable.
func commitOnlyPaths(vaultPath, message string, paths []string) error {
	return commitOnlyPathsFor(vaultPath, nil, gitCommitTimeout, message, paths)
}

// commitOnlyPathsFor is commitOnlyPaths for a committer holding the root lock
// as caller (see refuseOnPendingDeparturesFor).
func commitOnlyPathsFor(vaultPath string, caller *vaultlock.Held, limit time.Duration, message string, paths []string) error {
	// The commit guard's backstop: every caller also runs it before staging.
	if err := refuseOnPendingDeparturesFor(vaultPath, caller); err != nil {
		return err
	}
	// The departed-project backstop: a file that reached a departed project's
	// tree some other way than vaultfs (a raw editor write, a copy by hand) is
	// not committed either. Deletions pass: removing a leftover mutates no
	// project, and the delete's own commit does not come through here.
	if err := refuseDepartedInCommit(vaultPath, paths); err != nil {
		return err
	}
	return commitPathspec(vaultPath, limit, message, paths)
}

// refuseDepartedInCommit refuses a commit of paths that would add or modify a
// file under a project tree whose project has a departure record
// (vaultfs.RefuseDepartedWrite). It asks git exactly what `git commit <paths>`
// would take — every change against HEAD under the pathspec, deletions excepted
// — so a directory pathspec covering a departed tree is caught too.
func refuseDepartedInCommit(vaultPath string, paths []string) error {
	base := []string{"-c", "core.quotepath=off", "diff", "HEAD", "--name-only", "-z", "--diff-filter=d"}
	if _, herr := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--verify", "-q", "HEAD^{commit}"); herr != nil {
		// Unborn: the first commit takes what is staged.
		base = []string{"-c", "core.quotepath=off", "diff", "--cached", "--name-only", "-z", "--diff-filter=d"}
	}
	// git diff takes no --pathspec-from-file, so the pathspecs go after "--",
	// in batches that keep argv well under the platform limit.
	const batch = 256
	for start := 0; start < len(paths); start += batch {
		end := min(start+batch, len(paths))
		args := append(append(append([]string{}, base...), "--"), paths[start:end]...)
		cmd := exec.Command("git", args...)
		cmd.Dir = vaultPath
		cmd.Env = SafeGitEnv("GIT_TERMINAL_PROMPT=0")
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("check departed trees: git diff: %w", err)
		}
		for _, rel := range strings.Split(string(out), "\x00") {
			if rel == "" {
				continue
			}
			if err := vaultfs.RefuseDepartedWrite(vaultPath, rel); err != nil {
				return err
			}
		}
	}
	return nil
}

// commitPathspec is commitOnlyPaths without the commit guard. Its only other
// caller is CommitSplitPurge, whose commit is the one that finishes the
// pending departure records the guard refuses on.
func commitPathspec(vaultPath string, limit time.Duration, message string, paths []string) error {
	f, err := os.CreateTemp("", "vp-commit-pathspec-*")
	if err != nil {
		return fmt.Errorf("git commit: create pathspec file: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)

	var buf strings.Builder
	for _, p := range paths {
		buf.WriteString(p)
		buf.WriteByte(0)
	}
	if _, err := f.WriteString(buf.String()); err != nil {
		f.Close()
		return fmt.Errorf("git commit: write pathspec file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("git commit: close pathspec file: %w", err)
	}

	if _, err := gitCmd(vaultPath, limit,
		"commit", "-m", message,
		"--pathspec-from-file="+name, "--pathspec-file-nul",
	); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// mergeFetchedTip merges the already-fetched <remote>/<branch> into HEAD — the
// plain merge pullCore runs (vaultpull.go), and the commit-and-push reconcile's
// only way of taking in a remote's commits. nil means HEAD now descends from
// the remote tip.
//
// It only ever cleans up after a merge THIS call started. In order:
//
//  1. Any merge, cherry-pick, revert or rebase already in progress refuses,
//     touching nothing (*vaultTreeUnsafeError). pullCore deliberately leaves a
//     conflicted merge for the operator; merging over it fails and aborting
//     it would destroy their hand resolution. A probe that cannot tell refuses
//     the same way.
//  2. The departure guard: a remote tip that takes away a project this host
//     still has work under is refused with the *DepartedWorkError, before
//     anything changes. It runs here so no caller can merge without it.
//  3. The merge: one step whatever the number of commits ahead, rewriting no
//     local commit and stashing nothing.
//  4. A merge killed at its deadline (gitCmd SIGKILLs git), or by any other
//     signal, is NOT aborted and nothing is removed: it can leave index.lock (which another process may
//     own) and a half-updated tree with no MERGE_HEAD, so it is reported as
//     such (*vaultTreeUnsafeError, killedMergeError).
//  5. A merge this call left in progress (MERGE_HEAD, or unmerged paths) is
//     aborted with `merge --abort`. A failed abort is joined into the error and
//     is *vaultTreeUnsafeError.
//  6. Anything else is git refusing before it changed anything — staged
//     changes in the index, or the incoming change touching a modified or
//     untracked file in the way — returned as *mergeNotStartedError.
//
// On a migrated vault two more steps run (derived_merge.go): a conflict whose
// every unmerged path is derived is HEALED (the paths deleted, the merge
// concluded) instead of aborted, and after every successful merge the derived
// paths the merged tree tracks are UNTRACKED and committed. A failed untrack
// is *derivedUntrackError: the merge stands, and the caller does not push.
// rep, when non-nil, collects what both did.
func mergeFetchedTip(vaultPath, remote, branch string, rep *DerivedMergeReport) error {
	ref := remote + "/" + branch
	if op, err := operationInProgress(vaultPath); err != nil {
		return &vaultTreeUnsafeError{fmt.Errorf("refusing to merge %s: cannot tell whether a git operation is in progress, so nothing was touched: %w", ref, err)}
	} else if op != "" {
		return &vaultTreeUnsafeError{fmt.Errorf("refusing to merge %s: %s is in progress in the vault and vp did not start it; nothing was touched — conclude or abort it by hand first", ref, describeOperation(op))}
	}
	if derr := guardIncomingDepartures(vaultPath, remote, branch); derr != nil {
		return derr
	}
	_, mergeErr := gitCmd(vaultPath, mergeTimeout, "merge", ref)
	if mergeErr == nil {
		return untrackAfterMerge(vaultPath, rep)
	}
	if killed := killedMergeError(vaultPath, ref, mergeErr); killed != nil {
		return &vaultTreeUnsafeError{killed}
	}
	op, err := operationInProgress(vaultPath)
	if err != nil {
		return &vaultTreeUnsafeError{fmt.Errorf("%w; and whether it left a merge in progress cannot be told: %w", mergeErr, err)}
	}
	if op != "MERGE_HEAD" && len(unmergedPaths(vaultPath)) == 0 {
		return &mergeNotStartedError{mergeErr}
	}
	var (
		healed, named []string
		handled       bool
	)
	entries, healErr := unmergedPathsZ(vaultPath)
	if healErr == nil && len(entries) > 0 {
		healed, named, handled, healErr = healConflicts(vaultPath, entries)
	}
	if healErr == nil && handled {
		rep.add(DerivedMergeReport{Healed: healed})
		return untrackAfterMerge(vaultPath, rep)
	}
	mergeErr = joinHealOutcome(mergeErr, named, healErr)
	if _, abortErr := gitCmd(vaultPath, 10*time.Second, "merge", "--abort"); abortErr != nil {
		return &vaultTreeUnsafeError{fmt.Errorf("%w; abort failed: %w", mergeErr, abortErr)}
	}
	return mergeErr
}

// untrackAfterMerge runs the post-merge untrack and reports it, wrapping a
// failure as *derivedUntrackError.
func untrackAfterMerge(vaultPath string, rep *DerivedMergeReport) error {
	untracked, err := untrackDerivedAfterMerge(vaultPath)
	if err != nil {
		return &derivedUntrackError{err}
	}
	rep.add(DerivedMergeReport{Untracked: untracked})
	return nil
}

// joinHealOutcome adds to a conflicted merge's error what the heal found: a
// kg/entities.jsonl both sides changed, named, and a heal that could not run
// (a marker that cannot be read, for one).
func joinHealOutcome(mergeErr error, named []string, healErr error) error {
	for _, p := range named {
		mergeErr = fmt.Errorf("%w; unresolved conflict on %s: both sides changed it, which the migration's \"every writer host synced, pushed and clean\" attestation exists to prevent — resolve it by hand", mergeErr, p)
	}
	if healErr != nil {
		mergeErr = fmt.Errorf("%w; derived-path heal not run: %w", mergeErr, healErr)
	}
	return mergeErr
}

// describeOperation names an operationInProgress result for a person: the
// state file alone ("MERGE_HEAD") does not say what kind of operation it is.
func describeOperation(op string) string {
	switch op {
	case "MERGE_HEAD":
		return "a merge (MERGE_HEAD)"
	case "CHERRY_PICK_HEAD":
		return "a cherry-pick (CHERRY_PICK_HEAD)"
	case "REVERT_HEAD":
		return "a revert (REVERT_HEAD)"
	}
	return op
}

// killedMergeError describes a merge whose git process did not exit on its
// own — killed by gitCmd at its deadline, or by any signal — or returns nil
// when it exited with a status. Such a merge can stop mid-write. It removes
// nothing: index.lock may belong to another process, and only a human can
// tell.
func killedMergeError(vaultPath, ref string, mergeErr error) error {
	var how string
	var exitErr *exec.ExitError
	switch {
	case errors.Is(mergeErr, context.DeadlineExceeded):
		how = "killed at its deadline"
	case errors.As(mergeErr, &exitErr) && exitErr.ExitCode() == -1:
		// ExitCode is -1 when the process was terminated by a signal.
		how = "killed by a signal"
	default:
		return nil
	}
	msg := fmt.Sprintf("the merge of %s was %s, so the index and working tree may be half-updated", ref, how)
	if lock, err := gitCmd(vaultPath, 5*time.Second, "rev-parse", "--git-path", "index.lock"); err == nil {
		if !filepath.IsAbs(lock) {
			lock = filepath.Join(vaultPath, lock)
		}
		if _, statErr := os.Lstat(lock); statErr == nil {
			msg += fmt.Sprintf("; %s was left behind, and no vault commit can run until it is removed — first confirm no git process holds it", lock)
		}
	}
	return fmt.Errorf("%s: %w", msg, mergeErr)
}

// mergeNotStartedError is a merge git refused before changing anything.
type mergeNotStartedError struct{ err error }

func (e *mergeNotStartedError) Error() string { return e.err.Error() }
func (e *mergeNotStartedError) Unwrap() error { return e.err }

// vaultTreeUnsafeError is a merge that could not run, or did not finish, in a
// state nothing may commit into: an operation already in progress, a merge
// killed by its deadline or a signal, or a failed abort.
type vaultTreeUnsafeError struct{ err error }

func (e *vaultTreeUnsafeError) Error() string { return e.err.Error() }
func (e *vaultTreeUnsafeError) Unwrap() error { return e.err }

// reconcileFailure prefixes a failed reconcile's merge error with what was
// being reconciled. A departure refusal is returned as it is: its text is the
// operator's remedy, and it is the same refusal pullCore reports.
func reconcileFailure(what string, err error) error {
	var departed *DepartedWorkError
	if errors.As(err, &departed) {
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}

// reconcileRejectedPush is pushCommitted's rejection recovery for one remote:
// fetch, then the guarded merge of the fetched tip (mergeFetchedTip). The
// caller holds the vault commit lock across it. reconciled is false when the
// push to this remote must be skipped (its RemoteResults entry is set).
func reconcileRejectedPush(vaultPath, remote, branch string, result *PushResult) (reconciled bool) {
	// Fetch failure surfaces directly — no merge/converge.
	if _, fetchErr := gitCmd(vaultPath, 60*time.Second, "fetch", remote); fetchErr != nil {
		result.RemoteResults[remote] = fmt.Errorf("fetch %s: %w", remote, fetchErr)
		return false
	}

	// Merge the freshly-fetched remote tip under the capture commit. Any
	// failure (a departure refusal, an aborted conflict, a refusal before the
	// merge started, a killed merge) skips the push for this remote — the
	// commit stays local (Stranded surfaces it).
	if mergeErr := mergeFetchedTip(vaultPath, remote, branch, &result.Derived); mergeErr != nil {
		result.RemoteResults[remote] = reconcileFailure("merge of "+remote+"/"+branch+" failed", mergeErr)
		return false
	}
	return true
}

// rebaseInProgress reports whether a rebase is mid-flight in the vault repo,
// i.e. git's rebase-merge or rebase-apply state directory exists. vp itself
// never rebases the vault; this reads a rebase some other process left behind
// (operationInProgress).
func rebaseInProgress(vaultPath string) bool {
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		p, err := gitCmd(vaultPath, 5*time.Second, "rev-parse", "--git-path", name)
		if err != nil || p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(vaultPath, p)
		}
		if fi, statErr := os.Stat(p); statErr == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// unmergedPaths returns the de-duplicated set of working-tree paths with
// unmerged (conflict) entries, via `git ls-files -u`. Empty when the tree is
// clean. Used to tell a conflicted merge from a refused one.
func unmergedPaths(vaultPath string) []string {
	out, err := gitCmd(vaultPath, 10*time.Second, "ls-files", "-u")
	if err != nil || out == "" {
		return nil
	}
	seen := map[string]bool{}
	var paths []string
	for line := range strings.SplitSeq(out, "\n") {
		// format: "<mode> <sha> <stage>\t<path>"
		_, after, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		p := after
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths
}

// short returns the first 7 characters of a SHA for log breadcrumbs, or the
// original string if shorter.
func short(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// ListRemotes discovers all configured git remotes for the vault repo.
func ListRemotes(vaultPath string) ([]string, error) {
	out, err := gitCmd(vaultPath, 10*time.Second, "remote")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var remotes []string
	for r := range strings.SplitSeq(out, "\n") {
		if r = strings.TrimSpace(r); r != "" {
			remotes = append(remotes, r)
		}
	}
	return remotes, nil
}

// VaultRemoteURLs returns every remote URL configured in the vault repo's OWN
// config (`--local`, so a user- or system-level remote.*.url can never count).
// A repository with no remotes is (nil, nil). Any other failure — git missing,
// a failed or timed-out exec, a directory that is not a repository — is an
// error: `vp config bind` compares these URLs with a departure record's label,
// and must not read "could not ask git" as "no remotes".
func VaultRemoteURLs(vaultPath string) ([]string, error) {
	out, err := gitCmd(vaultPath, 5*time.Second, "config", "--local", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 && out == "" {
			return nil, nil // --get-regexp matched nothing
		}
		return nil, fmt.Errorf("read the remotes of %s: %w", vaultPath, err)
	}
	var urls []string
	for line := range strings.SplitSeq(out, "\n") {
		if _, url, ok := strings.Cut(strings.TrimSpace(line), " "); ok && url != "" {
			urls = append(urls, strings.TrimSpace(url))
		}
	}
	return urls, nil
}

// checkIdentity returns nil if git can resolve a committer identity from any
// source (.git/config, ~/.gitconfig, system gitconfig, or GIT_AUTHOR_*/
// GIT_COMMITTER_* env vars). Returns an actionable error otherwise. Uses
// `git var GIT_AUTHOR_IDENT` — git's own identity-resolution check, mirroring
// what `git commit` does internally.
func checkIdentity(vaultPath string) error {
	if _, err := gitCmd(vaultPath, 5*time.Second, "var", "GIT_AUTHOR_IDENT"); err != nil {
		return fmt.Errorf(
			"no git identity configured for vault commits (HOME=%s). "+
				"Set with: git config --global user.email <addr> && "+
				"git config --global user.name <name>",
			os.Getenv("HOME"),
		)
	}
	return nil
}

// gitCmd runs a git command in the vault directory with a timeout. Output is
// whitespace-trimmed — convenient for SHA / branch-name parsing.
func gitCmd(dir string, timeout time.Duration, args ...string) (string, error) {
	return gitCmdEnv(dir, timeout, nil, args...)
}

// gitCmdEnv is gitCmd with extraEnv appended after its own environment, so an
// entry there overrides SafeGitEnv's defaults for this one command.
func gitCmdEnv(dir string, timeout time.Duration, extraEnv []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Prevent interactive prompts. GIT_EDITOR=true short-circuits any editor
	// invocation (e.g. the reconcile's merge composing its commit message) so
	// operators with an interactive core.editor do not see the backend hang
	// waiting for stdin. All explicit commits here use `-m` already.
	//
	// SafeGitEnv, not os.Environ() directly: a process that inherits GIT_DIR /
	// GIT_WORK_TREE (spawned from a git hook, or a shell exporting them) would
	// otherwise have every vault git command answer for that other repository
	// instead of dir.
	cmd.Env = append(SafeGitEnv("GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true"), extraEnv...)

	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		// A deadline, said as one. exec reports the kill as "signal: killed",
		// and the detail line would be whatever git happened to have printed
		// before it died (the first file of a long listing, say), which reads
		// as if that line were the fault. Both causes stay in the chain: the
		// deadline for errors.Is, the exec error for callers that inspect it.
		// This names the limit; it does not make the call return at it. The
		// kill reaches git only, so a child that holds the output pipe (ssh, a
		// lazy fetch) is waited for.
		if ctx.Err() != nil {
			return trimmed, &GitError{Err: fmt.Errorf("timed out after %s: %w: %w", timeout, ctx.Err(), err)}
		}
		// Wrap HERE, not at the call sites. exec's *ExitError renders as exactly
		// "exit status 128" while git's own explanation — already captured, one
		// line above — was being dropped. Attaching it at the single point where
		// both are in hand means every existing `fmt.Errorf("...: %w", err)` site
		// gains the diagnosis without being rewritten, and every future one is
		// born with it. Patching the human-facing call sites individually would
		// have left the next one to rot.
		return trimmed, &GitError{Detail: gitDetailLine(trimmed), Err: err}
	}
	return trimmed, nil
}

// GitError pairs git's own message with the exit status exec reports.
//
// This ALIASES internal/giterr, which holds the type and the detail-line
// picker. It moved out of this package because internal/storage imports
// internal/project and internal/wrapstate — so either of those importing
// storage back for this one helper would be an import cycle, and
// internal/wrapstate runs its own git subprocesses and needs it. giterr has no
// internal dependencies, so every package that spawns git can reach it. Same
// move, same reason, as SafeGitEnv -> internal/gitenv (see git.go).
//
// An ALIAS, not a new named type: every existing &GitError{...} literal,
// errors.As(err, &ge) with a *GitError target, and type switch in this package
// and its tests keeps working unchanged, which is what makes the relocation
// invisible to callers.
type GitError = giterr.GitError

// gitDetailLine forwards to giterr.DetailLine. Kept as a package-local name so
// the seven call sites in this package read as they did before the move.
func gitDetailLine(out string) string { return giterr.DetailLine(out) }
