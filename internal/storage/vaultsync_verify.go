// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
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
	// floor serves). content is exact bytes — never whitespace-trimmed.
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
//   - absent from HEAD: an untracked mirror, removed with nothing to commit;
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
		if b, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--abbrev-ref", "HEAD"); b != "" {
			branch = b
		}
		if len(remotes) > 0 {
			reconcileErrs = reconcileIfAhead(vaultPath, remotes, branch)
		}
	}

	headBefore, _ := gitCmd(vaultPath, 10*time.Second, "rev-parse", "HEAD")

	// Classify. Only restores write here, and only over a file that still
	// holds vp's bytes.
	type candidate struct {
		rel     string
		present bool
		tracked bool
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
		headOID, inHead, err := treeEntryOID(vaultPath, "HEAD", rel)
		if err != nil {
			out.deferOn(rel, err)
			continue
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
			cands = append(cands, candidate{rel: rel, present: present})
			continue
		}
		blob, err := gitBlob(vaultPath, headOID)
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
			if _, err := gitCmd(vaultPath, 10*time.Second, "--literal-pathspecs", "checkout", "HEAD", "--", rel); err != nil {
				out.deferOn(rel, fmt.Errorf("restore from HEAD: %w", err))
				continue
			}
			out.Restored = append(out.Restored, rel)
			continue
		}
		cands = append(cands, candidate{rel: rel, present: present, tracked: true})
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
	// whose tracking ref does not resolve afterwards, defers every tracked
	// prune — a stale ref is exactly how an offline host would miss the
	// override.
	var tracked int
	for _, c := range cands {
		if c.tracked {
			tracked++
		}
	}
	if len(remotes) > 0 && tracked > 0 {
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
			case !c.tracked:
				kept = append(kept, c)
			case len(unverified) > 0:
				out.keep(c.rel, "prune deferred: remote not verified ("+strings.Join(unverified, ", ")+
					" could not be fetched, or has no "+branch+"), so a newer override there cannot be ruled out")
			case remoteAllows(vaultPath, remotes, branch, c.rel, v, &out):
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
				oid, found, err := treeEntryOID(vaultPath, "HEAD", rel)
				var blob []byte
				if err == nil && found {
					blob, err = gitBlob(vaultPath, oid)
				}
				switch {
				case err != nil:
					out.Failed = append(out.Failed, PruneFailure{Path: rel, Err: err})
				case !found:
					out.Gone = append(out.Gone, rel)
				case v.Accept(rel, blob):
					still = append(still, rel)
				default:
					if _, cerr := gitCmd(vaultPath, 10*time.Second, "--literal-pathspecs", "checkout", "HEAD", "--", rel); cerr != nil {
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
	// removal stays in the worktree, the lock entry stays, and the next sync
	// commits it (case 1b).
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
func remoteAllows(vaultPath string, remotes []string, branch, rel string, v PruneVerifier, out *PruneOutcome) bool {
	for _, remote := range remotes {
		ref := remote + "/" + branch
		if _, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--verify", "--quiet", ref); err != nil {
			continue
		}
		oid, found, err := treeEntryOID(vaultPath, ref, rel)
		if err != nil {
			out.deferOn(rel, err)
			return false
		}
		if !found {
			continue
		}
		blob, err := gitBlob(vaultPath, oid)
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

// ReadCommittedBlob returns HEAD's committed bytes for a vault-relative path,
// read-only: no index write, no identity, no lock. found is false when HEAD
// does not hold the path. Any git failure is an error, never "absent".
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
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
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
