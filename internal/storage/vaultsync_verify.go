// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// pruneBeforeStageHook runs after the removals and before the second HEAD
// check. Production code leaves it a no-op; a test moves HEAD from it, which
// is otherwise a race no test can time.
var pruneBeforeStageHook = func() {}

// PruneVerifier is the caller's definition of "these bytes are vp's".
//
// A verified prune exists for one caller: `vp config sync` removing vault
// Templates/ mirrors and committing the removal. It used to remove the file
// first and look at git afterwards, so any git failure in between — no
// identity, a corrupt index, an unreadable repository — left a committed
// operator override deleted in the worktree with nothing telling anyone. The
// verifier is applied BEFORE anything is removed, to every copy the removal
// would touch: the worktree bytes, HEAD's committed blob, and each remote
// tip's blob.
type PruneVerifier struct {
	// Accept reports whether content at rel is vp's (a mirror the embedded
	// floor serves). content is the bytes git would check out: the worktree
	// file as it is, and a committed or remote copy with the repository's
	// clean/smudge filters applied (git-crypt, LFS, any filter driver) — so it
	// is compared like with like — and never whitespace-trimmed.
	Accept func(rel string, content []byte) bool
	// Message composes the commit message from the paths the commit carries,
	// so the message can never list a path the commit does not.
	Message func(committed []string) string
}

// PruneKept is a path left in place, with the reason.
type PruneKept struct {
	Path   string
	Reason string
}

// PruneFailure is a path whose outcome is NOT accounted for: it was removed
// from the worktree and its removal was not committed.
type PruneFailure struct {
	Path string
	Err  error
}

// PruneOutcome accounts for every path PruneMirrorsVerified was given. Each
// path lands in exactly one list.
type PruneOutcome struct {
	// Removed paths were removed by this call and the removal is final:
	// committed, or never tracked. They are what `pruned=N` counts.
	Removed []string
	// Committed paths are the ones the prune commit carries — Removed
	// tracked paths plus AlreadyGone ones whose earlier removal is committed
	// now. It is informational; a path appears here and in Removed or Gone.
	Committed []string
	// Gone paths were already absent and need nothing more: never committed,
	// committed now (see Committed), or deleted by someone else over content
	// that was not vp's. Their bookkeeping may be dropped.
	Gone []string
	// Restored paths held a vp mirror in the worktree over operator content
	// in HEAD: `git checkout HEAD --` put the operator's bytes back. Nothing
	// was removed or committed for them.
	Restored []string
	// Kept paths were left exactly as they were (a changed file, a staged
	// change, operator content at a remote tip, or a git error that made the
	// prune unverifiable).
	Kept []PruneKept
	// Failed paths were removed but their removal could not be committed.
	// Restore each with `git checkout HEAD -- <path>`.
	Failed []PruneFailure
	// Errors are the git failures behind Kept deferrals and Failed paths.
	Errors []error
}

func (o *PruneOutcome) keep(rel, reason string) {
	o.Kept = append(o.Kept, PruneKept{Path: rel, Reason: reason})
}

func (o *PruneOutcome) deferOn(rel string, err error) {
	o.keep(rel, "prune deferred: "+err.Error())
	o.Errors = append(o.Errors, fmt.Errorf("%s: %w", rel, err))
}

// err is the call's error: any git failure (a deferral the operator must fix)
// or any path left unaccounted for.
func (o *PruneOutcome) err() error {
	if len(o.Errors) == 0 && len(o.Failed) == 0 {
		return nil
	}
	return fmt.Errorf("%d git error(s) and %d path(s) removed but not committed",
		len(o.Errors), len(o.Failed))
}

// PruneMirrorsVerified removes vault mirrors that are provably vp's and commits
// the removal, pushing it when push is true. paths are vault-relative, with
// forward slashes; the vault may be the root of its repository or nested in
// one. Nothing is removed until every check has passed, and every git failure
// before a removal leaves the file where it was.
//
// Under the vault commit lock, after the already-ahead reconcile has had its
// chance to move HEAD, each path is classified:
//
//   - present, but its worktree bytes are not vp's: kept (changed since plan);
//   - index differs from HEAD (a staged change or a staged add): kept;
//   - absent from HEAD: an untracked mirror, removed with nothing to commit —
//     this includes a vault whose HEAD does not exist yet (a freshly
//     initialized repository with no commits), detected via headExists
//     rather than by matching git's error text, and such a path still goes
//     through the same remote-tip check below as any other untracked one;
//   - HEAD blob not vp's: the worktree mirror is replaced by HEAD's copy
//     (`git checkout HEAD --`) — the operator's override comes back, and the
//     file is never removed; an already-absent path is someone else's
//     deletion of operator content and is left alone;
//   - HEAD blob vp's, but a remote tip holds other content there: kept, so the
//     prune cannot be pushed over — or later merged against — a newer
//     operator override. The remotes are fetched first, and a remote that
//     cannot be fetched defers every tracked prune;
//   - otherwise: removed, re-hashed immediately before the remove.
//
// A git error while classifying defers that path (kept). Identity is checked
// before the first tracked removal, so a host that cannot commit removes
// nothing it would have to commit. HEAD is re-read before staging; if it moved
// under us, the removed paths are checked against the new HEAD and restored
// where it is not vp's. A removal can stay uncommitted only through a stage or
// commit failure (its staging is undone) or a failed re-check after HEAD
// moved; either path is reported as Failed. Every remote must be fetched and
// its tracking ref resolved, or every tracked prune is deferred.
//
// vaultPath must be the root of its own repository; a vault nested in another
// repository uses PruneMirrorsInEnclosingRepo.
//
// The lock serialises against every vp committer. storage.Pull does not take
// it; a concurrent merge fails on git's own index guards rather than losing
// anything.
func PruneMirrorsVerified(vaultPath string, paths []string, push bool, v PruneVerifier) (*PushResult, PruneOutcome, error) {
	return pruneMirrors(vaultPath, paths, push, true, v)
}

// PruneMirrorsInEnclosingRepo is the prune for a vault nested inside another
// repository's work tree — a project or dotfiles repo that happens to hold the
// vault. That repository is not the vault's, and vp must never fetch, rebase,
// stage into, commit or push it: doing so once rebased an operator's unpushed
// commits and pushed them with a prune commit on top. Only what is read-only,
// or a working-tree restore of a vault path, is allowed:
//
//   - HEAD and index are read to classify each path, exactly as for a vault
//     that is its own repository;
//   - a mirror over operator content in HEAD is restored in place;
//   - an untracked mirror (nothing committed to protect) is removed;
//   - a TRACKED mirror is kept, with a reason naming the enclosing
//     repository: its removal could only be finished by a commit there.
//
// No remote is listed or fetched, no identity is needed, and nothing is
// staged or committed.
func PruneMirrorsInEnclosingRepo(vaultPath string, paths []string, v PruneVerifier) (PruneOutcome, error) {
	_, out, err := pruneMirrors(vaultPath, paths, false, false, v)
	return out, err
}

// pruneMirrors is both prunes. commit=false is the enclosing-repo form: no
// remote, no reconcile, no fetch, no identity check, no stage, no commit.
func pruneMirrors(vaultPath string, paths []string, push, commit bool, v PruneVerifier) (*PushResult, PruneOutcome, error) {
	var out PruneOutcome
	if v.Accept == nil || v.Message == nil {
		return nil, out, errors.New("verified prune: PruneVerifier needs both Accept and Message")
	}
	if len(paths) == 0 {
		return nil, out, errors.New("no paths specified")
	}
	result := &PushResult{}

	release, lerr := vaultlock.Acquire(vaultPath, vaultPath)
	if lerr != nil {
		for _, rel := range paths {
			out.keep(rel, "prune deferred: cannot take the vault commit lock")
		}
		out.Errors = append(out.Errors, fmt.Errorf("acquire vault commit lock: %w", lerr))
		return nil, out, out.err()
	}
	released := false
	unlock := func() {
		if !released {
			released = true
			release()
		}
	}
	defer unlock()

	var remotes []string
	branch := "main"
	var reconcileErrs map[string]error
	if push && commit {
		var err error
		if remotes, err = ListRemotes(vaultPath); err != nil {
			for _, rel := range paths {
				out.keep(rel, "prune deferred: cannot list the vault's remotes")
			}
			out.Errors = append(out.Errors, fmt.Errorf("listing remotes: %w", err))
			return nil, out, out.err()
		}
		// Routed through the shared currentBranch (vaultstatus.go) rather than
		// hand-rolling the same symbolic-ref call here: this and
		// CommitAndPushPaths's own branch resolution want identical
		// semantics, and a second independently-maintained copy is exactly
		// how this class of bug (see vaultstatus-and-vaultsync-abbrev-ref-branch-corruption)
		// arose in the first place.
		branch = currentBranch(vaultPath)
		if len(remotes) > 0 {
			reconcileErrs = reconcileIfAhead(vaultPath, remotes, branch)
		}
	}

	headBefore, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "HEAD")

	// A vault whose HEAD names no commit yet (a fresh `git init`, or `vp init`
	// with nothing committed) has nothing in HEAD, not a git fault: every path
	// is "absent from HEAD" without a treeEntryOID call, which would otherwise
	// report the genuine 128 exit an unborn HEAD produces as a git error.
	headBorn, hbErr := headExists(vaultPath)
	if hbErr != nil {
		for _, rel := range paths {
			out.deferOn(rel, hbErr)
		}
		return nil, out, out.err()
	}

	// Every committed or remote copy is read as git would check it out. A
	// filter driver that cannot be listed leaves nothing verifiable: every
	// path is deferred, and nothing is removed or restored.
	drivers, derr := filterDrivers(vaultPath)
	if derr != nil {
		for _, rel := range paths {
			out.deferOn(rel, derr)
		}
		return nil, out, out.err()
	}

	// Classify. Only restores write here, and only over a file that still
	// holds vp's bytes.
	type candidate struct {
		rel     string
		present bool
		tracked bool
		// verifyRemote is whether this candidate must pass the remote-tip
		// check before it can be removed: every tracked candidate, plus every
		// untracked one that is untracked ONLY because HEAD itself is unborn
		// (a remote may already hold real content nobody has pulled yet). A
		// candidate absent from HEAD because a born HEAD simply never held it
		// does not need the check — nothing has ever been committed there.
		verifyRemote bool
	}
	var cands []candidate
	for _, rel := range paths {
		abs := filepath.Join(vaultPath, filepath.FromSlash(rel))
		present := true
		if data, err := os.ReadFile(abs); err == nil {
			if !v.Accept(rel, data) {
				out.keep(rel, "changed since plan; kept")
				continue
			}
		} else if os.IsNotExist(err) {
			present = false
		} else {
			out.deferOn(rel, err)
			continue
		}
		var headOID string
		var inHead bool
		if headBorn {
			var err error
			headOID, inHead, err = treeEntryOID(vaultPath, "HEAD", rel)
			if err != nil {
				out.deferOn(rel, err)
				continue
			}
		}
		indexOID, inIndex, err := indexEntryOID(vaultPath, rel)
		if err != nil {
			out.deferOn(rel, err)
			continue
		}
		if inHead != inIndex || headOID != indexOID {
			out.keep(rel, "the index holds a staged change for it; prune deferred")
			continue
		}
		if !inHead {
			cands = append(cands, candidate{rel: rel, present: present, verifyRemote: !headBorn})
			continue
		}
		// The restore below is reached only after this read SUCCEEDED and its
		// content failed Accept. A read that fails — a filter that cannot run
		// included — defers the path: restoring through a broken filter would
		// write its cleaned bytes (ciphertext, an LFS pointer) over the file.
		blob, err := gitCheckoutContent(vaultPath, "HEAD", rel, drivers)
		if err != nil {
			out.deferOn(rel, err)
			continue
		}
		if !v.Accept(rel, blob) {
			if !present {
				// Not our removal: we never remove a file whose committed
				// copy is operator content. Someone else deleted it.
				out.Gone = append(out.Gone, rel)
				continue
			}
			if _, err := gitCmd(vaultPath, 10*time.Second, append(requiredFilterArgs(drivers), "--literal-pathspecs", "checkout", "HEAD", "--", rel)...); err != nil {
				out.deferOn(rel, fmt.Errorf("restore from HEAD: %w", err))
				continue
			}
			out.Restored = append(out.Restored, rel)
			continue
		}
		cands = append(cands, candidate{rel: rel, present: present, tracked: true, verifyRemote: true})
	}

	// The enclosing-repo form never finishes a tracked prune: that would
	// need a commit in a repository that is not the vault's.
	if !commit {
		top, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--show-toplevel")
		var rest []candidate
		for _, c := range cands {
			if c.tracked {
				out.keep(c.rel, "prune deferred: the vault is inside another repository ("+top+
					"), and vp never stages, commits or pushes a repository that is not the vault's own")
				continue
			}
			rest = append(rest, c)
		}
		cands = rest
	}

	// A remote tip holding operator content for a path means an override
	// exists that this host has not pulled. Pruning here would commit a
	// deletion that either strands behind it or, merged later, deletes it.
	// The tips are only trusted fresh: a remote that cannot be fetched, or
	// whose tracking ref does not resolve afterwards, defers every candidate
	// this check applies to (verifyRemote) — a stale ref is exactly how an
	// offline host would miss the override. This includes an untracked
	// candidate that is untracked only because HEAD is unborn: nothing is
	// committed locally, but a remote may already hold real content nobody
	// here has pulled yet.
	var needsRemoteCheck int
	for _, c := range cands {
		if c.verifyRemote {
			needsRemoteCheck++
		}
	}
	if len(remotes) > 0 && needsRemoteCheck > 0 {
		var unverified []string
		for _, remote := range remotes {
			if _, err := gitCmd(vaultPath, 60*time.Second, "fetch", "-q", remote); err != nil {
				unverified = append(unverified, remote)
				out.Errors = append(out.Errors, fmt.Errorf("fetch %s: %w", remote, err))
				continue
			}
			if _, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--verify", "--quiet", remote+"/"+branch); err != nil {
				unverified = append(unverified, remote)
				out.Errors = append(out.Errors, fmt.Errorf("%s/%s does not resolve after a fetch", remote, branch))
			}
		}
		var kept []candidate
		for _, c := range cands {
			switch {
			case !c.verifyRemote:
				kept = append(kept, c)
			case len(unverified) > 0:
				out.keep(c.rel, "prune deferred: remote not verified ("+strings.Join(unverified, ", ")+
					" could not be fetched, or has no "+branch+"), so a newer override there cannot be ruled out")
			case remoteAllows(vaultPath, remotes, branch, c.rel, drivers, v, &out):
				kept = append(kept, c)
			}
		}
		cands = kept
	}

	var needCommit bool
	for _, c := range cands {
		needCommit = needCommit || c.tracked
	}
	if needCommit {
		if err := checkIdentity(vaultPath); err != nil {
			var rest []candidate
			for _, c := range cands {
				if c.tracked {
					if c.present {
						out.deferOn(c.rel, err)
					} else {
						out.Failed = append(out.Failed, PruneFailure{Path: c.rel, Err: err})
					}
					continue
				}
				rest = append(rest, c)
			}
			cands = rest
		}
	}

	// Remove, re-hashing immediately before each remove.
	var stage []string
	for _, c := range cands {
		if !c.present {
			if c.tracked {
				stage = append(stage, c.rel)
			} else {
				out.Gone = append(out.Gone, c.rel)
			}
			continue
		}
		// Through the removal funnel, compare-and-set on the bytes just
		// accepted: vaultfs.Delete re-hashes under the path's advisory lock,
		// so an edit landing between this read and the remove is kept.
		abs := filepath.Join(vaultPath, filepath.FromSlash(c.rel))
		data, err := os.ReadFile(abs)
		if err != nil && !os.IsNotExist(err) {
			out.deferOn(c.rel, err)
			continue
		}
		if err == nil {
			if !v.Accept(c.rel, data) {
				out.keep(c.rel, "changed since plan; kept")
				continue
			}
			sum := sha256.Sum256(data)
			if _, derr := vaultfs.Delete(vaultPath, c.rel, hex.EncodeToString(sum[:])); derr != nil {
				switch {
				case errors.Is(derr, vaultfs.ErrShaConflict):
					out.keep(c.rel, "changed since plan; kept")
					continue
				case !errors.Is(derr, vaultfs.ErrFileNotFound):
					out.deferOn(c.rel, derr)
					continue
				}
			}
		}
		if c.tracked {
			stage = append(stage, c.rel)
		} else {
			out.Removed = append(out.Removed, c.rel)
		}
	}

	// Second guard: HEAD cannot move under a vp committer while we hold the
	// lock, but a non-vp git operation can. If it moved, judge the removed
	// paths against the HEAD the commit will actually land on.
	pruneBeforeStageHook()
	if len(stage) > 0 {
		if now, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "HEAD"); now != headBefore {
			var still []string
			for _, rel := range stage {
				_, found, err := treeEntryOID(vaultPath, "HEAD", rel)
				var blob []byte
				if err == nil && found {
					blob, err = gitCheckoutContent(vaultPath, "HEAD", rel, drivers)
				}
				switch {
				case err != nil:
					out.Failed = append(out.Failed, PruneFailure{Path: rel, Err: err})
				case !found:
					out.Gone = append(out.Gone, rel)
				case v.Accept(rel, blob):
					still = append(still, rel)
				default:
					if _, cerr := gitCmd(vaultPath, 10*time.Second, append(requiredFilterArgs(drivers), "--literal-pathspecs", "checkout", "HEAD", "--", rel)...); cerr != nil {
						out.Failed = append(out.Failed, PruneFailure{Path: rel, Err: fmt.Errorf("restore from a moved HEAD: %w", cerr)})
					} else {
						out.Restored = append(out.Restored, rel)
					}
				}
			}
			stage = still
		}
	}
	if len(stage) == 0 {
		return result, out, out.err()
	}

	// A stage or commit failure must not leave vp's own deletion in the
	// index: the next sync would read it as someone else's staged change and
	// keep deferring it forever. Unstage exactly the paths staged here; the
	// removal stays in the worktree (" D"), and the next sync finds it pending
	// (UncommittedRemovals) and commits it.
	fail := func(err error) (*PushResult, PruneOutcome, error) {
		if _, rerr := gitCmd(vaultPath, 10*time.Second, append([]string{"--literal-pathspecs", "reset", "-q", "--"}, stage...)...); rerr != nil {
			err = fmt.Errorf("%w (and unstaging the removal failed: %v)", err, rerr)
		}
		for _, rel := range stage {
			out.Failed = append(out.Failed, PruneFailure{Path: rel, Err: err})
		}
		out.Errors = append(out.Errors, err)
		return result, out, out.err()
	}
	if err := stageInBatches(vaultPath, stage); err != nil {
		return fail(fmt.Errorf("git add: %w", err))
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	if err := commitOnlyPaths(vaultPath, fmt.Sprintf("%s\n\n[%s]", v.Message(stage), hostname), stage); err != nil {
		return fail(err)
	}
	out.Committed = append(out.Committed, stage...)
	wasPresent := map[string]bool{}
	for _, c := range cands {
		wasPresent[c.rel] = c.present
	}
	for _, rel := range stage {
		if wasPresent[rel] {
			out.Removed = append(out.Removed, rel)
		} else {
			out.Gone = append(out.Gone, rel)
		}
	}
	unlock()

	sha, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--short", "HEAD")
	result.CommitSHA = sha
	if push && len(remotes) > 0 {
		pushCommitted(vaultPath, remotes, branch, reconcileErrs, result)
	}
	return result, out, out.err()
}

// remoteAllows reports whether every remote tip either lacks rel or holds vp's
// bytes there. A tip holding anything else keeps the path, and a git error
// reading it defers the path; both are recorded in out. A remote whose
// tracking ref does not resolve (never fetched) says nothing either way.
//
// A tip is read as git would check it out (gitCheckoutContent), with the
// attributes of the WORKTREE's .gitattributes: `cat-file --filters` never
// reads a revision's own. A tip whose attributes differ is therefore read with
// the local ones, which can only make its bytes fail Accept — toward keep.
func remoteAllows(vaultPath string, remotes []string, branch, rel string, drivers []string, v PruneVerifier, out *PruneOutcome) bool {
	for _, remote := range remotes {
		ref := remote + "/" + branch
		if _, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--verify", "--quiet", ref); err != nil {
			continue
		}
		_, found, err := treeEntryOID(vaultPath, ref, rel)
		if err != nil {
			out.deferOn(rel, err)
			return false
		}
		if !found {
			continue
		}
		blob, err := gitCheckoutContent(vaultPath, ref, rel, drivers)
		if err != nil {
			out.deferOn(rel, err)
			return false
		}
		if !v.Accept(rel, blob) {
			out.keep(rel, ref+" holds operator content for it; prune deferred — pull first, and keep that copy")
			return false
		}
	}
	return true
}

// PruneMirrorsVerifiedWithDowngrade is PruneMirrorsVerified under
// CommitAndPushPathsWithDowngrade's remote policy: a push requested against a
// vault with no remotes becomes a local-only commit, reported as downgraded.
// A failure to list the remotes defers every path rather than failing open.
func PruneMirrorsVerifiedWithDowngrade(vaultPath string, paths []string, push bool, v PruneVerifier) (res *PushResult, out PruneOutcome, downgraded bool, err error) {
	effectivePush, downgraded, err := downgradePush(vaultPath, push)
	if err != nil {
		for _, rel := range paths {
			out.keep(rel, "prune deferred: cannot list the vault's remotes")
		}
		out.Errors = append(out.Errors, err)
		return nil, out, false, out.err()
	}
	res, out, err = PruneMirrorsVerified(vaultPath, paths, effectivePush, v)
	return res, out, downgraded, err
}

// ReadCommittedContent returns HEAD's copy of a vault-relative path as git
// would check it out — the repository's clean/smudge filters applied, exactly
// as the verified prune compares it — read-only: no index write, no identity,
// no lock. found is false when HEAD does not hold the path. Any git failure,
// including a filter that cannot run, is an error, never "absent".
func ReadCommittedContent(vaultPath, rel string) (content []byte, found bool, err error) {
	if _, found, err = treeEntryOID(vaultPath, "HEAD", rel); err != nil || !found {
		return nil, found, err
	}
	drivers, err := filterDrivers(vaultPath)
	if err != nil {
		return nil, false, err
	}
	content, err = gitCheckoutContent(vaultPath, "HEAD", rel, drivers)
	if err != nil {
		return nil, false, err
	}
	return content, true, nil
}

// ReadCommittedBlob returns HEAD's committed bytes for a vault-relative path,
// raw — the blob as stored, no filter applied — read-only: no index write, no
// identity, no lock. found is false when HEAD does not hold the path. Any git
// failure is an error, never "absent". A caller comparing content with what
// vp serves wants ReadCommittedContent; this one answers "is it tracked?".
func ReadCommittedBlob(vaultPath, rel string) (blob []byte, found bool, err error) {
	oid, found, err := treeEntryOID(vaultPath, "HEAD", rel)
	if err != nil || !found {
		return nil, found, err
	}
	blob, err = gitBlob(vaultPath, oid)
	return blob, err == nil, err
}

// treeEntryOID looks rel up in the tree of rev. An empty listing means the
// path is absent there — which `git cat-file blob <rev>:<rel>` cannot
// distinguish from a git fault (both exit 128), and conflating the two would
// fail open. Paths are relative to vaultPath, which may sit below the
// repository root.
func treeEntryOID(vaultPath, rev, rel string) (oid string, found bool, err error) {
	out, err := gitCmd(vaultPath, 10*time.Second, "--literal-pathspecs", "ls-tree", "-z", rev, "--", rel)
	if err != nil {
		return "", false, fmt.Errorf("git ls-tree %s -- %s: %w", rev, rel, err)
	}
	// "<mode> SP <type> SP <oid> TAB <path> NUL"
	meta, _, ok := strings.Cut(strings.TrimRight(out, "\x00"), "\t")
	if !ok {
		return "", false, nil
	}
	f := strings.Fields(meta)
	if len(f) != 3 {
		return "", false, fmt.Errorf("git ls-tree %s -- %s: unexpected entry %q", rev, rel, meta)
	}
	if f[1] != "blob" {
		return "", false, fmt.Errorf("%s holds a %s at %s, not a file", rev, f[1], rel)
	}
	return f[2], true, nil
}

// indexEntryOID looks rel up in the index. A path with unmerged (conflict)
// entries is reported as an error: it is not ours to settle. A corrupt or
// unreadable index is an error too — never "not staged".
func indexEntryOID(vaultPath, rel string) (oid string, found bool, err error) {
	out, err := gitCmd(vaultPath, 10*time.Second, "--literal-pathspecs", "ls-files", "-s", "-z", "--", rel)
	if err != nil {
		return "", false, fmt.Errorf("git ls-files -s -- %s: %w", rel, err)
	}
	entries := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	var hit string
	for _, e := range entries {
		// "<mode> SP <oid> SP <stage> TAB <path>"
		meta, path, ok := strings.Cut(e, "\t")
		if !ok || path != rel {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 3 {
			return "", false, fmt.Errorf("git ls-files -s -- %s: unexpected entry %q", rel, meta)
		}
		if f[2] != "0" {
			return "", false, fmt.Errorf("%s has unmerged entries in the index", rel)
		}
		hit = f[1]
	}
	return hit, hit != "", nil
}

// gitBlob returns a blob's exact bytes. gitCmd cannot: it trims whitespace from
// its output, and a trailing newline is part of what the verifier hashes.
func gitBlob(vaultPath, oid string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "cat-file", "blob", oid)
	cmd.Dir = vaultPath
	cmd.Env = SafeGitEnv("GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		var detail string
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			detail = gitDetailLine(strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git cat-file blob %s: %w", oid, &GitError{Detail: detail, Err: err})
	}
	return out, nil
}

// filterDrivers lists the filter drivers configured for vaultPath's repository
// that transform content on checkout: every driver with a
// filter.<driver>.smudge or filter.<driver>.process key, in any config file
// git reads there. A driver name may itself contain dots; only the "filter."
// prefix and the final ".smudge"/".process" are stripped. No such key is an
// empty list; any other git failure is an error.
func filterDrivers(vaultPath string) ([]string, error) {
	stdout, stderr, err := gitExact(vaultPath, 10*time.Second,
		"config", "--name-only", "--get-regexp", `^filter\..+\.(smudge|process)$`)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 && len(bytes.TrimSpace(stderr)) == 0 {
			return nil, nil // exit 1 with nothing said: no such key
		}
		return nil, fmt.Errorf("list filter drivers: %w", &GitError{Detail: gitDetailLine(strings.TrimSpace(string(stderr))), Err: err})
	}
	seen := map[string]bool{}
	var drivers []string
	for line := range strings.SplitSeq(string(stdout), "\n") {
		name := strings.TrimPrefix(strings.TrimSpace(line), "filter.")
		i := strings.LastIndex(name, ".")
		if i <= 0 {
			continue
		}
		d := name[:i]
		if strings.Contains(d, "=") {
			// `git -c filter.<d>.required=true` splits at the first '=', so
			// such a driver cannot be forced to fail loudly. Nothing read
			// through it could be trusted.
			return nil, fmt.Errorf("filter driver %q: a name containing '=' cannot be required with git -c, so its output cannot be verified", d)
		}
		if !seen[d] {
			seen[d] = true
			drivers = append(drivers, d)
		}
	}
	return drivers, nil
}

// requiredFilterArgs is `-c filter.<d>.required=true` for every driver. A
// driver that is not required fails OPEN: git 2.55, measured, exits 0 from
// `cat-file --filters` (and checkout writes) the CLEANED bytes — ciphertext, an
// LFS pointer, rot13 — when its smudge command is missing or exits non-zero.
// Forced required, the same failure is a non-zero exit. git-crypt and
// `git lfs install` set the flag themselves; custom filters usually do not.
func requiredFilterArgs(drivers []string) []string {
	args := make([]string, 0, 2*len(drivers))
	for _, d := range drivers {
		args = append(args, "-c", "filter."+d+".required=true")
	}
	return args
}

// gitCheckoutContent returns rev's copy of the vault-relative path rel as git
// would check it out: `git cat-file --filters <rev>:./<rel>`, run in the vault,
// with every driver in drivers forced required. The "./" form resolves rel
// against the vault even when the vault is nested below its repository's root;
// `<rev>:<rel>` would name the wrong path there, and --path= silently returns
// the cleaned bytes. The bytes are exact, never trimmed.
//
// Any non-zero exit, and any output on stderr, is an error: a filter that
// complains and exits 0 anyway has not produced bytes anyone should compare.
// The caller must have checked that rev holds rel (treeEntryOID).
func gitCheckoutContent(vaultPath, rev, rel string, drivers []string) ([]byte, error) {
	spec := rev + ":./" + rel
	args := append(requiredFilterArgs(drivers), "cat-file", "--filters", spec)
	stdout, stderr, err := gitExact(vaultPath, 60*time.Second, args...)
	detail := gitDetailLine(strings.TrimSpace(string(stderr)))
	if err != nil {
		return nil, fmt.Errorf("git cat-file --filters %s: %w", spec, &GitError{Detail: detail, Err: err})
	}
	if detail != "" {
		return nil, fmt.Errorf("git cat-file --filters %s: %w", spec,
			&GitError{Detail: detail, Err: errors.New("git reported a problem on stderr")})
	}
	return stdout, nil
}

// gitExact runs git in dir and returns its stdout exactly as written and its
// stderr separately — never combined, never trimmed. gitCmd combines the two,
// which is right for a message and wrong for content: a filter's warning would
// otherwise be read as part of the file.
func gitExact(dir string, timeout time.Duration, args ...string) (stdout, stderr []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = SafeGitEnv("GIT_TERMINAL_PROMPT=0")
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	stdout, err = cmd.Output()
	return stdout, errBuf.Bytes(), err
}

// UncommittedRemovals lists the tracked files under the vault-relative
// directory dir that were removed from the worktree and whose removal is not
// committed — ` D` in `git status` — each with HEAD's copy as git would check it
// out (ReadCommittedContent). It is read-only.
//
// It is how `vp config sync` finishes a prune whose commit did not land (a
// stage or commit failure, a reset whose commit failed, a deletion by hand)
// without host-local state: the caller classifies each HEAD copy and plans the
// commit only for vp-shipped bytes. want, when non-nil, restricts the list to
// the paths it accepts, BEFORE anything is read — so a broken filter on a file
// the caller would never act on cannot fail the whole list.
//
// Listed only when all of these hold, so the list can never turn into a
// mass-deletion commit:
//
//   - `git ls-files -v --deleted` lists it (out-of-cone sparse entries and
//     staged deletions are never listed);
//   - it is not assume-unchanged (a lowercase tag) or skip-worktree (S) —
//     `git status` hides those, and staging one may be a no-op;
//   - HEAD holds it (an added-then-deleted index entry has nothing to commit);
//   - the index holds exactly HEAD's blob (a staged change followed by a
//     worktree deletion is someone's work in progress, and the prune would
//     defer it on every sync anyway).
//
// stdout and stderr are read separately: a hint on stderr is never a path.
// Keys are vault-relative with forward slashes. Any git failure is an error.
func UncommittedRemovals(vaultPath, dir string, want func(rel string) bool) (map[string][]byte, error) {
	stdout, stderr, err := gitExact(vaultPath, 30*time.Second,
		"--literal-pathspecs", "ls-files", "-v", "--deleted", "-z", "--", dir)
	if err != nil {
		return nil, fmt.Errorf("list uncommitted removals under %s: %w", dir,
			&GitError{Detail: gitDetailLine(strings.TrimSpace(string(stderr))), Err: err})
	}
	var rels []string
	seen := map[string]bool{}
	for entry := range strings.SplitSeq(string(stdout), "\x00") {
		tag, rel, ok := strings.Cut(entry, " ")
		if !ok || rel == "" || len(tag) != 1 {
			continue
		}
		if tag == "S" || strings.ToLower(tag) == tag {
			continue // skip-worktree, or assume-unchanged
		}
		if want != nil && !want(rel) {
			continue
		}
		if !seen[rel] {
			seen[rel] = true
			rels = append(rels, rel)
		}
	}
	out := map[string][]byte{}
	if len(rels) == 0 {
		return out, nil
	}
	if born, err := headExists(vaultPath); err != nil || !born {
		return out, err // no HEAD: nothing is committed to remove
	}
	for _, rel := range rels {
		headOID, inHead, err := treeEntryOID(vaultPath, "HEAD", rel)
		if err != nil {
			return nil, err
		}
		if !inHead {
			continue
		}
		indexOID, inIndex, err := indexEntryOID(vaultPath, rel)
		if err != nil {
			return nil, err
		}
		if !inIndex || indexOID != headOID {
			continue
		}
		content, found, err := ReadCommittedContent(vaultPath, rel)
		if err != nil {
			return nil, err
		}
		if found {
			out[rel] = content
		}
	}
	return out, nil
}

// RetiredTemplatesLockRel is where vp binaries before
// template-provenance-manifest-retires-the-host-local-lock kept the
// host-local templates.lock. No vp from this release reads or writes it.
const RetiredTemplatesLockRel = ".vibe-palace/templates.lock"

// RetiredTemplatesLock reports whether the vault holds the retired
// .vibe-palace/templates.lock and whether `vp config sync` may remove it:
// only when it is a regular file reached directly, UNTRACKED — in neither the
// index nor HEAD, so a `git rm --cached` lock HEAD still holds is tracked — and
// NOT ignored. Such a lock is host-local dirt that no vp from this release
// reads, and on a canonically configured git vault it makes `vp vault sync`
// refuse. A tracked lock is shared state and an ignored one is the operator's
// choice; both are left. content is the bytes read, for a compare-and-set
// removal. Any git failure is an error, never "removable".
func RetiredTemplatesLock(vaultPath string) (content []byte, removable bool, err error) {
	rel := RetiredTemplatesLockRel
	if derr := vaultfs.CheckDirectPath(vaultPath, rel); derr != nil {
		if errors.Is(derr, vaultfs.ErrIndirectPath) {
			return nil, false, nil
		}
		return nil, false, derr
	}
	content, err = os.ReadFile(filepath.Join(vaultPath, filepath.FromSlash(rel)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if _, inIndex, err := indexEntryOID(vaultPath, rel); err != nil || inIndex {
		return content, false, err
	}
	if born, err := headExists(vaultPath); err != nil {
		return content, false, err
	} else if born {
		if _, inHead, err := treeEntryOID(vaultPath, "HEAD", rel); err != nil || inHead {
			return content, false, err
		}
	}
	ignored, err := GitPathIgnored(vaultPath, rel)
	if err != nil || ignored {
		return content, false, err
	}
	return content, true, nil
}

// headExists reports whether HEAD names a commit. An unborn branch (a fresh
// `git init`) is false, not an error; any other failure is an error.
func headExists(vaultPath string) (bool, error) {
	_, stderr, err := gitExact(vaultPath, 10*time.Second, "rev-parse", "-q", "--verify", "HEAD^{commit}")
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 && len(bytes.TrimSpace(stderr)) == 0 {
		return false, nil
	}
	return false, &GitError{Detail: gitDetailLine(strings.TrimSpace(string(stderr))), Err: err}
}
