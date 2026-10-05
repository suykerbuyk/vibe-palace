// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// PullResult reports what happened during a Pull operation. It mirrors
// PushResult's per-remote result map and Any/All/Stranded methods, but pull has
// no commit of its own (CommitSHA is dropped) and adds the phantom-template
// self-heal accounting (HealedTemplates) plus captured git output so front-ends
// can re-print without Pull ever touching os.Stderr.
type PullResult struct {
	// RemoteResults is the per-remote pull outcome (nil = success). Pull attempts
	// every remote in the slice and records each result here, stopping early
	// only on the failures pullSweepStops names — a conflict (aborted), a tree
	// nothing may merge into, a departure — after which the remaining remotes
	// are recorded with a skip reason instead of being attempted. Best-effort
	// callers iterate the whole map; fail-fast callers stop at the first
	// non-nil entry.
	RemoteResults map[string]error
	// RemoteOutput holds the combined (stdout+stderr) git output per remote — the
	// pull's merge output, or the fetch error output when the pre-pull fetch
	// failed. The CLI re-prints it to stderr (accepting loss of live streaming)
	// and the MCP tool folds it into its returned payload string.
	RemoteOutput map[string]string
	// HealedTemplates names the Templates/commands/*.md paths whose uncommitted
	// working-tree dirt provably equalled the freshly-fetched remote ref and was
	// therefore discarded (reset to HEAD) so the merge could proceed. Nothing
	// unique is lost — the dirt matched what the merge would have produced.
	HealedTemplates []string
	// FailedHeals is HealedTemplates' counterpart: the candidate paths whose
	// heal was ATTEMPTED and FAILED, each carrying git's own explanation, and
	// which were therefore still dirty when the merge ran.
	//
	// 🔴 THIS FIELD EXISTS BECAUSE ITS ABSENCE MADE THE MERGE FAILURE
	// INEXPLICABLE. The checkout error used to be discarded at the `continue`:
	// the path was not added to HealedTemplates, so it appeared in no [heal]
	// line and in no other channel — nowhere at all. It then still obstructed
	// the merge, and the operator was shown a merge failure whose cause had
	// been measured and thrown away. Worse, the [heal] lines that DID print
	// read as a complete account of the heal pass, so the reader reasonably
	// concluded the heal had nothing to do with the failure.
	//
	// A path that fails on one remote and succeeds on a later one is NOT
	// reported here — see the reconciliation at the end of Pull. Only paths
	// that were never healed are listed, so "still an obstruction" stays true.
	FailedHeals []HealFailure
	// Derived is what the merges did to derived index paths on a migrated
	// vault: conflicted paths healed (deleted) and re-tracked paths untracked.
	Derived DerivedMergeReport
}

// HealFailure is one candidate path whose heal checkout failed, with git's own
// sentence for why. Per-path rather than a single error because one pull can
// skip several paths for several different reasons.
type HealFailure struct {
	// Path is the vault-relative Templates/commands/*.md path.
	Path string
	// Reason is git's own explanation, as wrapped by gitCmd (GitError carries
	// git's text rather than exec's bare "exit status N" — the 6343f8f
	// pattern). The comment at the checkout site names an untracked path with
	// no HEAD entry as the common, benign case; a permission error, a
	// filesystem error, an index lock held by another process, or a path that
	// changed type are not benign and were indistinguishable from it.
	Reason string
}

// AllPulled was DELETED at 209, and its deletion is the fix, not a cleanup.
//
// It was written, unit-tested, and never called by anything but its own tests —
// while `vp vault pull` exited 0 on a failing remote, which is precisely the gate it
// was written to be. Its absence WAS the bug.
//
// It is not resurrected here, because it could not have been used as written:
//
//   - It returns FALSE for an empty map, so `!AllPulled()` reads a vault with NO
//     remotes — a legitimate local-only degrade — as a failure.
//   - It cannot name the remotes that failed, and a verdict nobody can act on is
//     half a verdict.
//
// FailedRemotes + RemoteVerdict (remotes.go) answer both questions in one place, and
// BOTH front-ends now call them. Keeping a second, subtly-different predicate for the
// same concept is how two implementations of one rule silently diverge — the trap
// mdfence exists to document.

// AnyPulled returns true if at least one remote was pulled successfully.
func (r *PullResult) AnyPulled() bool {
	for _, err := range r.RemoteResults {
		if err == nil {
			return true
		}
	}
	return false
}

// Stranded reports that remotes were configured and pull-attempted but NONE
// pulled successfully — the host is left behind with no merge applied. A
// remote-less vault (RemoteResults empty) returns false.
func (r *PullResult) Stranded() bool {
	return len(r.RemoteResults) > 0 && !r.AnyPulled()
}

// Pull fetches each configured remote and merges its freshly-fetched
// remote-tracking ref (`git fetch <remote> <branch>` then `git merge
// <remote>/<branch>` — a single network round-trip, not `git pull`'s redundant
// re-fetch). It attempts every remote in the slice and records each outcome in
// RemoteResults rather than aborting internally — the two front-ends differ on
// error semantics (best-effort vs fail-fast), output channel, and a CLI-only
// dry-run, so Pull returns data and leaves those policy choices to the callers.
// Early exits are the failures pullSweepStops names; the remaining remotes are
// then recorded as skipped (see below). It NEVER writes to os.Stderr.
//
// Pull merges through mergeFetchedTip, the same merge the push path's
// reconcile runs, and so follows its rule: a conflicted merge the pull started
// is ABORTED (the vault is left at its pre-pull HEAD, the conflicting paths
// named, the remedy given), and a merge, cherry-pick, revert or rebase it did
// not start is never touched. It does not run the push path's converge loop.
//
// Pre-flight exceptions to "returns data, not errors": before anything else
// runs — before the phantom-template scan, before any remote is touched — Pull
// refuses outright if the vault is nested inside another repository's work
// tree (see RefuseIfNestedVaultGit), if a lifecycle command's commit is
// pending, or if a git operation is already in progress in the vault. Each
// refusal IS the top-level error return and is the verdict on its own; every
// caller checks the returned err before touching RemoteResults.
//
// Phantom-template self-heal: before the merge, each working-tree-dirty
// Templates/commands/*.md path whose content provably equals the freshly-fetched
// remote ref (`git diff --quiet <remote>/<branch> -- <path>` exits 0) has its
// uncommitted dirt discarded (`git checkout HEAD -- <path>`) so it cannot trip
// the "local changes would be overwritten by merge" abort. This is the common
// failure mode where a v4 or older `vp commands upgrade` wrote older template
// bytes over a newer committed copy (from v5 the upgrade commands write nothing
// under Templates/, but older binaries on lagging hosts still can, and their
// dirt still arrives here): the stale local bytes match the remote the merge is
// about to bring in, so dropping them loses nothing. The heal pass is fail-open
// — any error skips the path, never fatal — and a genuinely-edited template
// (diff nonzero) is left untouched for the merge to handle.
//
// A host config with git_enabled = false (or an unreadable one) refuses
// before any git runs: the returned *PullResult is non-nil and empty, and the
// error wraps ErrGitDisabled (or ErrGitConfigUnreadable).
func Pull(vaultPath string, remotes []string) (*PullResult, error) {
	if err := RefuseIfGitDisabled(vaultPath, "pull"); err != nil {
		return &PullResult{
			RemoteResults: map[string]error{},
			RemoteOutput:  map[string]string{},
		}, err
	}
	res, err := pullCore(vaultPath, remotes)
	// A pull is how a host learns that a project moved to another vault, so it
	// is where that project's host-local embed cache goes. It never fails the
	// pull, runs no embedder, and holds no vault lock.
	sweepDepartedAfterPull(vaultPath)
	return res, err
}

// pullCore is Pull after its git_enabled gate. Storage-internal composition
// (SyncVault) calls it directly, so one outermost operation reads the host
// config once.
func pullCore(vaultPath string, remotes []string) (*PullResult, error) {
	result := &PullResult{
		RemoteResults: make(map[string]error, len(remotes)),
		RemoteOutput:  make(map[string]string, len(remotes)),
	}
	if err := RefuseIfNestedVaultGit(vaultPath, "pull"); err != nil {
		return result, err
	}
	// A lifecycle command's unfinished commit must never be merged onto: the
	// merge would carry unseen writes into it, which its own publish then
	// could not see (lifecycle_marker.go). Pull and SyncVault both come here.
	if err := refuseOnLifecyclePendingStrict(vaultPath); err != nil {
		return result, err
	}
	// 🔴 BEFORE THE TEMPLATE SCAN AND HEAL. A merge, cherry-pick, revert or
	// rebase in progress is someone else's — vp aborts its own conflicted
	// merges — and the heal below would discard their staged resolution of a
	// template (`checkout HEAD --` of a path whose resolved bytes equal the
	// remote's). mergeFetchedTip refuses the same state again under the lock;
	// that check alone came too late for the heal.
	if err := refuseOperationInProgress(vaultPath, "pull"); err != nil {
		return result, err
	}
	branch := branchOrMain(vaultPath)

	// Scan the dirty Templates/commands/*.md set ONCE, before the loop. The set
	// never grows across remotes: a clean merge leaves the touched templates
	// committed (so clean, and absent from a re-scan), and an aborted conflict
	// restores the tree and stops the sweep before any later remote runs. The
	// per-remote diff against the (per-remote) ref still happens inside the
	// loop; only the candidate-path scan is hoisted. `healed` records paths
	// already discarded so a later remote does not re-probe them.
	dirty := dirtyTemplateCommandPaths(vaultPath)
	healed := map[string]bool{}
	// Latest failure reason per candidate path. Reconciled against `healed`
	// after the remote sweep so a path that failed on one remote and healed on
	// a later one is not reported as an obstruction it no longer is.
	healFailed := map[string]string{}

	for i, remote := range remotes {
		ref := remote + "/" + branch

		// Fetch first so both the heal pass and the merge see the current remote
		// tip. A fetch failure (unreachable remote) is recorded per-remote and we
		// move on — mirrors GetRemoteStatus's clean handling of an unreachable
		// remote rather than crashing the whole sweep.
		if out, err := gitCmd(vaultPath, 60*time.Second, "fetch", remote, branch); err != nil {
			result.RemoteResults[remote] = fmt.Errorf("fetch %s: %w", remote, err)
			result.RemoteOutput[remote] = out
			continue
		}

		// 🔴 THE GUARD, HEAL AND MERGE RUN UNDER THE VAULT COMMIT LOCK. A merge
		// rewrites the index and working tree under every other committer, and
		// a split purge's staged removal is exactly the in-flight state it must
		// not merge over; every committer holds this key across its index
		// critical section. The fetch above stays outside it (network). Nothing
		// that calls pullCore holds the key.
		release, lerr := vaultlock.Acquire(vaultPath, vaultPath)
		if lerr != nil {
			result.RemoteResults[remote] = fmt.Errorf("acquire vault commit lock for the merge: %w", lerr)
			continue
		}
		stop := func() bool {
			// A departure incoming onto work under the departed slug is refused
			// BEFORE the heal pass or the merge touch anything: HEAD and the working
			// tree stay exactly as they were. Later remotes are skipped for the same
			// reason a conflict skips them — the host has to carry that work across
			// first (see guardIncomingDepartures).
			if err := guardIncomingDepartures(vaultPath, remote, branch); err != nil {
				result.RemoteResults[remote] = err
				why, _ := pullSweepStops(remote, err)
				for _, skipped := range remotes[i+1:] {
					result.RemoteResults[skipped] = fmt.Errorf("skipped: %s", why)
				}
				return true
			}

			// Heal pass over the single dirty scan. The diff is per-remote (ref
			// differs), but the candidate set is fixed; skip any path already healed
			// on an earlier remote.
			for _, p := range dirty {
				if healed[p] {
					continue
				}
				// Working-tree content == remote ref content? Exit 0 means no diff →
				// the dirt is the remote's own bytes and is safe to discard. A nonzero
				// exit (genuine local edit) or any probe error skips the path.
				if _, err := gitCmd(vaultPath, 10*time.Second, "diff", "--quiet", ref, "--", p); err != nil {
					continue
				}
				// Discard the uncommitted obstruction so the merge can proceed.
				//
				// 🔴 FAIL-OPEN IS THE RULING, NOT AN OVERSIGHT — DO NOT MAKE THIS
				// ABORT THE PULL. A vault pull is the route by which a host
				// RECEIVES a newer binary's fixes, including the fix that would
				// repair whatever is wrong with its own template mirror. Aborting
				// on one mirror's checkout failure strands exactly the host that
				// most needs the pull to succeed — the same self-lockout hazard
				// already ruled against when `vault sync` was unwrapped from
				// mutates(). The heal pass is a best-effort convenience over a
				// narrow, guarded, fully-recoverable path set and stays one.
				//
				// What changed: the error is no longer DISCARDED. The defect was
				// silence, not permissiveness. The path is still skipped and still
				// not reported as healed, but git's reason is now recorded and
				// rendered beside the [heal] lines, so the merge failure this path
				// goes on to cause has a stated cause.
				if _, err := gitCmd(vaultPath, 10*time.Second, "checkout", "HEAD", "--", p); err != nil {
					healFailed[p] = err.Error()
					continue
				}
				healed[p] = true
				result.HealedTemplates = append(result.HealedTemplates, p)
			}

			// Merge the already-fetched remote-tracking ref through the one
			// guarded merge (mergeFetchedTip): plain-merge semantics, as `git
			// pull <remote> <branch>` without its second fetch. On a migrated
			// vault a conflict on derived paths alone is healed and the merge
			// concluded; any other conflict is ABORTED and named.
			out, err := mergeFetchedTip(vaultPath, remote, branch, &result.Derived)
			result.RemoteOutput[remote] = out
			if err == nil {
				result.RemoteResults[remote] = nil
				return false
			}
			var conflict *mergeConflictError
			if errors.As(err, &conflict) {
				slog.Warn("vault pull: merge conflicted; vp aborted it, nothing was merged",
					"remote", remote, "ref", conflict.ref, "tip", conflict.tip,
					"paths", conflict.paths, "abort_failed", conflict.abortErr != nil)
				if conflict.abortErr == nil {
					err = fmt.Errorf("%w — the pull from %s was NOT applied; to take its commits, merge by hand (git -C %s merge %s), resolve, commit, then run vp vault sync", err, remote, vaultPath, ref)
				}
			}
			result.RemoteResults[remote] = err
			why, stop := pullSweepStops(remote, err)
			if !stop {
				return false
			}
			for _, skipped := range remotes[i+1:] {
				result.RemoteResults[skipped] = fmt.Errorf("skipped: %s", why)
			}
			return true
		}()
		release()
		if stop {
			break
		}
	}

	// Reconcile: report only the paths that were never healed on ANY remote,
	// because those are the ones still obstructing. Iterating `dirty` rather
	// than ranging the map keeps the order deterministic for callers and tests.
	for _, p := range dirty {
		if reason, failed := healFailed[p]; failed && !healed[p] {
			result.FailedHeals = append(result.FailedHeals, HealFailure{Path: p, Reason: reason})
		}
	}

	return result, nil
}

// pullSweepStops is the pull's rule for the remotes after a failed merge:
// stop, recording each later remote as skipped for why, or go on to the next.
//
//   - A tree nothing may merge into (*vaultTreeUnsafeError: an operation in
//     progress, a killed merge, a failed abort). STOP. Checked first: a failed
//     abort wraps the conflict.
//   - A conflict (the merge was aborted): every mirror carries the same
//     commits, so each would conflict the same way. STOP.
//   - A departure onto work this host still has under the departed project
//     (*DepartedWorkError): the work has to be carried across first. STOP.
//   - A merge git refused before changing anything (*mergeNotStartedError): the
//     tree is as it was, and a mirror may still merge. GO ON.
//   - A merge that stands but whose derived-path untrack failed
//     (*derivedUntrackError): the tree is a clean merge result. GO ON.
//   - Anything else: STOP — an unrecognised failure is never merged past.
func pullSweepStops(remote string, err error) (why string, stop bool) {
	var (
		unsafe     *vaultTreeUnsafeError
		conflict   *mergeConflictError
		departed   *DepartedWorkError
		notStarted *mergeNotStartedError
		untrack    *derivedUntrackError
	)
	switch {
	case errors.As(err, &unsafe):
		return fmt.Sprintf("%s left the vault in a state nothing may merge into (see its error)", remote), true
	case errors.As(err, &conflict):
		return fmt.Sprintf("%s conflicted and its merge was aborted, so nothing was merged; a mirror carries the same commits and would conflict the same way", remote), true
	case errors.As(err, &departed):
		return fmt.Sprintf("%s carries a departure this host still has work under; carry that work across first", remote), true
	case errors.As(err, &notStarted), errors.As(err, &untrack):
		return "", false
	}
	return fmt.Sprintf("%s failed in a way the pull does not merge past (see its error)", remote), true
}

// dirtyTemplateCommandPaths returns the working-tree-dirty paths matching the
// glob Templates/commands/*.md (exactly that directory, .md leaf). It reuses
// tidy's whole-vault porcelain scanner/parser rather than rolling a new parser.
// Best-effort: a scan failure yields nil (the heal pass is never a hard gate).
func dirtyTemplateCommandPaths(vaultPath string) []string {
	raw, err := scanPorcelain(vaultPath, DefaultTidyScanTimeout)
	if err != nil {
		return nil
	}
	var paths []string
	for _, e := range parsePorcelainZ(raw) {
		parts := strings.Split(e.Path, "/")
		if len(parts) == 3 && parts[0] == "Templates" && parts[1] == "commands" &&
			strings.HasSuffix(parts[2], ".md") {
			paths = append(paths, e.Path)
		}
	}
	return paths
}
