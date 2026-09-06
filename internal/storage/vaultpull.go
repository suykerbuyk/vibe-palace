// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"strings"
	"time"
)

// PullResult reports what happened during a Pull operation. It mirrors
// PushResult's per-remote result map and Any/All/Stranded methods, but pull has
// no commit of its own (CommitSHA is dropped) and adds the phantom-template
// self-heal accounting (HealedTemplates) plus captured git output so front-ends
// can re-print without Pull ever touching os.Stderr.
type PullResult struct {
	// RemoteResults is the per-remote pull outcome (nil = success). Pull attempts
	// every remote in the slice and records each result here, stopping early only
	// when a merge leaves the tree with unmerged (conflict) paths: no later remote
	// can merge onto a conflicted tree, so the remaining remotes are recorded with
	// a skip sentinel instead of being attempted. Best-effort callers iterate the
	// whole map; fail-fast callers stop at the first non-nil entry.
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
// The one early exit is a merge that leaves unmerged paths: the conflicted tree
// is unmergeable, so the remaining remotes are recorded as skipped (see below).
// It NEVER writes to os.Stderr.
//
// Pull keeps plain MERGE semantics; it deliberately does NOT replicate the push
// path's rebase/force-with-lease converge loop.
//
// Phantom-template self-heal: before the merge, each working-tree-dirty
// Templates/commands/*.md path whose content provably equals the freshly-fetched
// remote ref (`git diff --quiet <remote>/<branch> -- <path>` exits 0) has its
// uncommitted dirt discarded (`git checkout HEAD -- <path>`) so it cannot trip
// the "local changes would be overwritten by merge" abort. This is the common
// failure mode where `vp commands upgrade` wrote older template bytes over a
// newer committed copy: the stale local bytes match the remote the merge is
// about to bring in, so dropping them loses nothing. The heal pass is fail-open
// — any error skips the path, never fatal — and a genuinely-edited template
// (diff nonzero) is left untouched for the merge to handle.
func Pull(vaultPath string, remotes []string) (*PullResult, error) {
	result := &PullResult{
		RemoteResults: make(map[string]error, len(remotes)),
		RemoteOutput:  make(map[string]string, len(remotes)),
	}
	branch := currentBranch(vaultPath)

	// Scan the dirty Templates/commands/*.md set ONCE, before the loop. The set
	// never grows across remotes: a clean merge leaves the touched templates
	// committed (so clean, and absent from a re-scan), and a conflict trips the
	// break below before any later remote runs. The per-remote diff against the
	// (per-remote) ref still happens inside the loop; only the candidate-path scan
	// is hoisted. `healed` records paths already discarded so a later remote does
	// not re-probe them.
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

		// Merge the already-fetched remote-tracking ref. `git fetch` above updated
		// <remote>/<branch>, so `git merge <remote>/<branch>` reuses it — same
		// plain-merge semantics as `git pull <remote> <branch>` (no rebase) but
		// without the second fetch that `git pull` would perform.
		out, err := gitCmd(vaultPath, 120*time.Second, "merge", ref)
		result.RemoteOutput[remote] = out
		result.RemoteResults[remote] = err

		// A merge that left unmerged paths makes the tree unmergeable: no later
		// remote can merge onto a conflicted tree, and continuing would only run
		// doomed heal+merge attempts. Record the remaining (not-yet-attempted)
		// remotes with a skip sentinel and abandon the sweep. A plain fetch/network
		// failure (handled above with `continue`) carries no unmerged paths and so
		// still falls through to the next mirror — best-effort preserved.
		if err != nil && len(unmergedPaths(vaultPath)) > 0 {
			for _, skipped := range remotes[i+1:] {
				result.RemoteResults[skipped] = fmt.Errorf("skipped: prior remote left an unresolved merge conflict")
			}
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
