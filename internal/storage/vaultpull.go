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
			// Fail-open: a checkout error (e.g. an untracked path with no HEAD
			// entry) is skipped and the path is NOT reported as healed.
			if _, err := gitCmd(vaultPath, 10*time.Second, "checkout", "HEAD", "--", p); err != nil {
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

	return result, nil
}

// dirtyTemplateCommandPaths returns the working-tree-dirty paths matching the
// glob Templates/commands/*.md (exactly that directory, .md leaf). It reuses
// tidy's whole-vault porcelain scanner/parser rather than rolling a new parser.
// Best-effort: a scan failure yields nil (the heal pass is never a hard gate).
func dirtyTemplateCommandPaths(vaultPath string) []string {
	raw, err := scanPorcelain(vaultPath)
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
