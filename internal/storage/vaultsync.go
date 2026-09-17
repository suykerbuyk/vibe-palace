// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/giterr"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// afterPushHook runs immediately after a successful push records its SHA in
// remoteSHA, before the convergence loop. Production code must leave this as a
// no-op; tests override it to inject mid-flight state changes (e.g. a
// concurrent writer mutating a bare remote between the recorded push and the
// convergence force-with-lease).
var afterPushHook = func(remote string) {}

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
	// PopConflict is set when the autostash re-apply after a rebase conflicted:
	// the capture commit DID land and push, but the operator's shelved working-tree
	// edits could not be cleanly re-applied. The edits are preserved in the git
	// stash (recoverable via `git stash list`) and PopConflictPaths names the files
	// left with conflict markers. NOT a strand — the commit reached the remote.
	PopConflict      bool
	PopConflictPaths []string
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
// plain `git push <remote> main` — the non-rebase counterpart to Pull, mirroring
// its shape. It attempts every remote in order and records each outcome in
// RemoteResults (nil = success) alongside the captured combined stdout+stderr in
// RemoteOutput, rather than aborting internally: the two front-ends differ on
// output channel (CLI stderr vs MCP payload string) and both take their verdict
// from RemoteVerdict, so PushPlain returns data and leaves policy to the callers.
// It NEVER writes to os.Stderr.
//
// PushPlain does NOT guard the working tree (the porcelain check stays in the
// front-ends), does NOT commit, does NOT converge/rebase (that is
// CommitAndPushPaths's job), and does NOT call RemoteVerdict. Pure execution +
// capture, like Pull. The branch is main — matching what both front-ends push
// today. The returned *PlainPushResult is always non-nil, even if a remote
// failed; the error return is reserved for a failure to run git at all.
func PushPlain(vaultPath string, remotes []string) (*PlainPushResult, error) {
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
// and the rebase / converge loop is skipped — useful for local-only commits
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
// that remote is fetched and HEAD is rebased onto it WITH `--autostash` so the
// new commit fast-forwards instead of stacking ahead-N and compounding the
// strand. A reconcile that hits a persistent content conflict aborts the rebase
// and is recorded so the push to that remote is skipped (the new commit still
// commits locally — capture artifacts are never lost — and surfaces as a strand
// via per-remote RemoteResults). Detection is fail-open: if `<remote>/<branch>`
// does not resolve (never fetched) or the fetch fails, the guard simply skips
// that remote — it is a compounding optimization, never a correctness gate, and
// must never hard-error the commit path. The fetch happens ONLY in this rare
// already-ahead state, so the happy path stays network-free.
//
// CROSS-CALLER IMPACT: this function is shared by tidy, `vault commit --push`,
// and memory harvest, so the already-ahead reconcile fires for all three — and
// that is intended. An already-ahead `vault commit --push` means a prior push
// stranded and must reconcile before stacking more on top; the reconcile is
// lossless (it replays the prior commit onto the remote tip, or commits locally
// and strands on a real conflict).
//
// When push=true: the happy path is a sequential fast-forward push. On a
// non-fast-forward rejection, the rejected remote is fetched and the local
// branch is rebased onto it WITH `--autostash` (so dirty tracked files that the
// caller deliberately left unswept do not abort the rebase); the rebased commit
// is then pushed to that remote. Fetch failures and true rebase content
// conflicts (after `rebase --abort`) surface directly via per-remote
// RemoteResults rather than masquerading as downstream errors. When the rebase
// itself completes but the autostash re-apply conflicts, the capture commit
// still lands and pushes — PushResult.PopConflict / PopConflictPaths name the
// files left with conflict markers and the operator's edits are preserved in the
// git stash (recoverable via `git stash list`); this is NOT a strand.
//
// After every successful rebase-and-push, prior remotes whose last-known-good
// SHA differs from the new HEAD are converged via
// `git push --force-with-lease=refs/heads/<branch>:<expected> <remote>
// <branch>`. The lease is an atomic compare-and-swap: if any concurrent writer
// has moved the prior remote's ref since we recorded it, the push is rejected
// and the failure surfaces as "convergence rejected (concurrent writer at
// <remote>): <err>" in PushResult.RemoteResults. The lease is the only path in
// this function that uses --force-with-lease; naked --force is never used.
//
// PushResult.CommitSHA is refreshed to the post-loop HEAD if any rebase
// happened, so the printed SHA always exists at the converged remotes.
func CommitAndPushPaths(vaultPath, message string, paths []string, push bool) (*PushResult, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no paths specified")
	}
	if err := RefuseIfNestedVaultGit(vaultPath, "commit"); err != nil {
		return nil, err
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
		return result, nil
	}

	if err := checkIdentity(vaultPath); err != nil {
		return nil, err
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
		branch = currentBranch(vaultPath)
		// Fix B: heal an already-ahead branch (a prior stranded commit) BEFORE a
		// new commit stacks on top of it and compounds the strand. Gated on
		// push && len(remotes) > 0 so it never fires on the downgrade path.
		reconcileErrs = reconcileIfAhead(vaultPath, remotes, branch)
	}

	// Stage only the surviving paths. Chunk under a conservative argv byte
	// budget to stay clear of MAX_ARG_LEN ceilings.
	if err := stageInBatches(vaultPath, keep); err != nil {
		return nil, fmt.Errorf("git add: %w", err)
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
	staged, derr := stagedChangesIn(vaultPath, keep)
	if derr != nil {
		return nil, derr
	}
	if !staged {
		return result, nil
	}

	// Stamp with hostname.
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	fullMsg := fmt.Sprintf("%s\n\n[%s]", message, hostname)

	// Commit ONLY the paths this call was given. See commitOnlyPaths.
	if err := commitOnlyPaths(vaultPath, fullMsg, keep); err != nil {
		return nil, err
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
func CommitRemovals(vaultPath, message string, rels []string) (*PushResult, error) {
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

	fail := func(err error) (*PushResult, error) {
		commitRemovalsBeforeUnstageHook()
		if _, rerr := gitCmd(vaultPath, 10*time.Second, append([]string{"--literal-pathspecs", "reset", "-q", "--"}, commit...)...); rerr != nil {
			err = fmt.Errorf("%w (and unstaging the removal failed: %v — run: git -C %s reset -q -- %s)",
				err, rerr, vaultPath, strings.Join(commit, " "))
		}
		return nil, err
	}
	if len(stage) > 0 {
		if err := stageInBatches(vaultPath, stage); err != nil {
			return fail(fmt.Errorf("git add: %w", err))
		}
	}
	staged, err := stagedChangesIn(vaultPath, commit)
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
func GitPathIgnored(vaultPath, rel string) (bool, error) {
	_, err := gitCmd(vaultPath, 10*time.Second, "check-ignore", "-q", "--", rel)
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
// and the local branch rebased onto it (--autostash), a TRUE rebase conflict is
// aborted and recorded, and prior remotes are converged with
// --force-with-lease. It fills result.RemoteResults (nil = success),
// PopConflict and, after any rebase, refreshes CommitSHA. A remote in
// reconcileErrs is skipped and reported with that error. Callers must have
// released the vault commit lock: pushes are deliberately not serialised.
func pushCommitted(vaultPath string, remotes []string, branch string, reconcileErrs map[string]error, result *PushResult) {
	result.RemoteResults = make(map[string]error, len(remotes))
	remoteSHA := make(map[string]string, len(remotes))
	rebasedAny := false

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

		// Rejection path (non-fast-forward or other push error). Fetch
		// failure surfaces directly — no rebase/converge.
		if _, fetchErr := gitCmd(vaultPath, 60*time.Second, "fetch", remote); fetchErr != nil {
			result.RemoteResults[remote] = fmt.Errorf("fetch %s: %w", remote, fetchErr)
			continue
		}

		// Rebase the local capture commit onto the freshly-fetched remote tip.
		// --autostash shelves any dirty tracked files (tidy deliberately leaves
		// non-swept dirt in the tree) so the rebase can start, then re-applies
		// them. The state-dir — NOT the exit code — is the source of truth for
		// whether the rebase landed: a true content conflict leaves rebase-merge/
		// rebase-apply in place (commit did NOT land), while an autostash re-apply
		// conflict completes the rebase (commit landed) yet may exit non-zero on
		// older git or exit 0 with conflict markers on modern git.
		_, rebaseErr := gitCmd(vaultPath, 60*time.Second, "rebase", "--autostash", remote+"/"+branch)
		if rebaseInProgress(vaultPath) {
			// TRUE rebase conflict: the capture commit did not land. Abort
			// (this also restores the autostashed working-tree edits) and skip
			// the push for this remote — the commit stays local (Stranded surfaces it).
			_, _ = gitCmd(vaultPath, 10*time.Second, "rebase", "--abort")
			result.RemoteResults[remote] = fmt.Errorf("rebase against %s failed: %w", remote, rebaseErr)
			continue
		}
		// Rebase completed: the capture commit is on HEAD and will push below.
		// If the autostash re-apply conflicted, the operator's edits are retained
		// in the stash and the named files carry conflict markers — surface loudly.
		if unmerged := unmergedPaths(vaultPath); len(unmerged) > 0 {
			result.PopConflict = true
			result.PopConflictPaths = unmerged
		}

		rebasedAny = true
		curSHA, _ = gitCmd(vaultPath, 10*time.Second, "rev-parse", "HEAD")

		if _, pushErr := gitCmd(vaultPath, 60*time.Second, "push", remote, branch); pushErr != nil {
			// Non-NFF push error after rebase (auth, network, etc.).
			result.RemoteResults[remote] = pushErr
			continue
		}
		remoteSHA[remote] = curSHA
		result.RemoteResults[remote] = nil
		afterPushHook(remote)

		// Converge prior remotes whose recorded SHA != current HEAD.
		for priorRemote, priorSHA := range remoteSHA {
			if priorSHA == curSHA {
				continue
			}
			if leaseErr := forceWithLease(vaultPath, priorRemote, branch, priorSHA); leaseErr != nil {
				result.RemoteResults[priorRemote] = fmt.Errorf(
					"convergence rejected (concurrent writer at %s): %w",
					priorRemote, leaseErr)
				// Leave remoteSHA[priorRemote] unchanged — caller sees
				// divergent state via per-remote error.
				continue
			}
			remoteSHA[priorRemote] = curSHA
			// result.RemoteResults[priorRemote] stays nil (still success).
			log.Printf("vault: force-converged %s from %s\n", priorRemote, short(priorSHA))
		}
	}

	// Refresh CommitSHA so callers never print a SHA that no longer exists at
	// any remote.
	if rebasedAny {
		if newSHA, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--short", "HEAD"); err == nil {
			result.CommitSHA = newSHA
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
func CommitAndPushPathsWithDowngrade(vaultPath, message string, paths []string, push bool) (res *PushResult, downgraded bool, err error) {
	effectivePush, downgraded, err := downgradePush(vaultPath, push)
	if err != nil {
		return nil, false, err
	}
	res, err = CommitAndPushPaths(vaultPath, message, paths, effectivePush)
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
// For each ahead remote it fetches then `git rebase --autostash <remote>/<branch>`
// (autostash shelves any unswept dirty tracked files so the rebase can start),
// reusing the Fix A discriminator: if rebaseInProgress afterward, a TRUE content
// conflict occurred — the rebase is aborted (restoring any autostashed dirt) and
// the remote is recorded in the returned map so the caller skips its push and
// strands the new commit. Otherwise the reconcile succeeded: HEAD now sits on the
// remote tip with the prior commit replayed, and the new commit will
// fast-forward. The returned map is nil when nothing conflicted.
func reconcileIfAhead(vaultPath string, remotes []string, branch string) map[string]error {
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
		// retains its own fetch+rebase recovery).
		if _, err := gitCmd(vaultPath, 60*time.Second, "fetch", remote); err != nil {
			continue
		}
		_, rebaseErr := gitCmd(vaultPath, 60*time.Second, "rebase", "--autostash", ref)
		if rebaseInProgress(vaultPath) {
			// TRUE conflict: the prior strand cannot replay onto the remote tip.
			// Abort (restores any autostashed dirt) and record the failure so the
			// caller skips this remote's push; the new commit stays local (strand).
			_, _ = gitCmd(vaultPath, 10*time.Second, "rebase", "--abort")
			if reconcileErrs == nil {
				reconcileErrs = make(map[string]error, 1)
			}
			reconcileErrs[remote] = fmt.Errorf("reconcile against %s failed: %w", remote, rebaseErr)
		}
		// else: reconcile succeeded — HEAD advanced onto the remote tip with the
		// prior commit replayed; the new commit fast-forwards.
	}
	return reconcileErrs
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
// Paths are literal vault-relative strings; no glob matching is performed.
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

// stageInBatches runs `git add -- <chunk>...` over paths, splitting into
// chunks whose combined argv-path bytes stay under stageBatchByteBudget.
// Always emits the `--` separator so paths beginning with `-` are treated as
// paths, not flags.
func stageInBatches(vaultPath string, paths []string) error {
	batch := make([]string, 0, len(paths))
	bytes := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		args := make([]string, 0, len(batch)+2)
		args = append(args, "add", "--")
		args = append(args, batch...)
		if _, err := gitCmd(vaultPath, 30*time.Second, args...); err != nil {
			return err
		}
		batch = batch[:0]
		bytes = 0
		return nil
	}
	for _, p := range paths {
		cost := len(p) + 1
		if len(batch) > 0 && bytes+cost > stageBatchByteBudget {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, p)
		bytes += cost
	}
	return flush()
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
func stagedChangesIn(vaultPath string, paths []string) (bool, error) {
	for _, chunk := range chunkPaths(paths) {
		args := make([]string, 0, len(chunk)+4)
		args = append(args, "diff", "--cached", "--quiet", "--")
		args = append(args, chunk...)
		if _, err := gitCmd(vaultPath, 10*time.Second, args...); err != nil {
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

	if _, err := gitCmd(vaultPath, 10*time.Second,
		"commit", "-m", message,
		"--pathspec-from-file="+name, "--pathspec-file-nul",
	); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// forceWithLease pushes branch to remote with a lease keyed to expectedSHA.
// The lease causes git to reject the push if the remote's branch ref has moved
// off expectedSHA since we last observed it — catching concurrent writers
// without resorting to naked --force.
func forceWithLease(vaultPath, remote, branch, expectedSHA string) error {
	lease := fmt.Sprintf("--force-with-lease=refs/heads/%s:%s", branch, expectedSHA)
	_, err := gitCmd(vaultPath, 60*time.Second, "push", lease, remote, branch)
	return err
}

// rebaseInProgress reports whether a rebase is mid-flight in the vault repo,
// i.e. git's rebase-merge or rebase-apply state directory exists. This is the
// reliable discriminator between a TRUE rebase conflict (state dir present,
// commit not landed) and a completed rebase whose autostash re-apply conflicted
// (state dir absent, commit landed) — the rebase exit code is NOT reliable for
// this across git versions.
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
// clean. Used after a completed rebase to detect an autostash re-apply conflict.
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
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Prevent interactive prompts. GIT_EDITOR=true short-circuits any editor
	// invocation (e.g. rebase --continue composing a merge commit message) so
	// operators with an interactive core.editor do not see the backend hang
	// waiting for stdin. All explicit commits here use `-m` already.
	//
	// SafeGitEnv, not os.Environ() directly: a process that inherits GIT_DIR /
	// GIT_WORK_TREE (spawned from a git hook, or a shell exporting them) would
	// otherwise have every vault git command answer for that other repository
	// instead of dir.
	cmd.Env = SafeGitEnv("GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")

	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
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
