// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// commitRenameLocked is the one-commit committer for `vp vault rename` (U9). It
// is modelled on CommitSplitPurgeLocked (split_purge.go), and differs in the
// one way the plan's round-2 review (S1/SF3) found the purge committer could
// not serve a rename: the purge committer only ever removes trees and stages
// the departure record (it has NO `git add` path for ADDED files), whereas a
// rename, by the time it commits, has ALREADY moved Projects/<old>/ ->
// Projects/<new>/ (and palace/<old>/ -> palace/<new>/) on disk and rewritten
// the identifiers in place. So this committer stages, in one commit under the
// vault root lock:
//
//   - the removal of the old trees (git add -A over them stages the deletions
//     of the files the move left the working tree without);
//   - the addition of the new trees (the moved, rewritten files), the W8
//     baseline rewrite and the renamed departure record;
//   - Audits/.surface when the rewrite's atomicfile stamps dirtied it.
//
// SF3 (empty-pathspec guard): `git add` and a commit pathspec are both fatal on
// a pathspec that matches nothing — a Projects-only slug has no palace/ tree on
// either side. So only an old tree that still has index entries, and only a new
// tree that exists on disk, is named; the same guard CommitSplitPurgeLocked
// carries for its `git rm`.
//
// It carries the HEAD-moved refusal (a pull or merge landing between the
// preflight and the lock): ExpectHead is the HEAD requireHeadAtEveryRemote
// bound, and if HEAD has moved nothing is committed.
//
// NOTE (plan vs. real code): S1/SF3 described `git rm -r` without -f for the
// old trees, for its compare-and-set refusal on tracked drift. After the move
// the old-tree files are already gone from the working tree, so a `git rm`
// compare-and-set there is moot; drift is refused earlier, by the pre-move
// footprint-clean check and requireHeadAtEveryRemote, where it must be caught
// anyway (the files are moved before this committer runs). Staging the
// deletions with `git add -A` is equivalent and also stages the new trees in
// one pass.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// RenameCommit is the one rename commit.
type RenameCommit struct {
	From, To   string // the old and new slugs
	Record     string // the renamed departure record, vault-relative
	ExpectHead string // the HEAD the rename was planned against
	Message    string // the commit subject and body; the hostname line is appended
	Trailers   string // the message's final paragraph (Vp-Rename-* and Vp-Run)
}

// RenameCommitResult reports the commit.
type RenameCommitResult struct {
	CommitSHA string
}

// RenameHeadMovedError is commitRenameLocked's refusal when HEAD moved between
// the preflight and the lock. Nothing was committed.
type RenameHeadMovedError struct{ From, To string }

func (e *RenameHeadMovedError) Error() string {
	return fmt.Sprintf("refusing to rename: the vault's HEAD moved from %s to %s after the rename was checked (a pull or merge landed); nothing was committed. Re-run the dry run", short(e.From), short(e.To))
}

// commitRenameLocked commits the move, the rewrites and the renamed record in
// one local commit, under a caller that already holds the vault root lock. It
// never pushes.
func commitRenameLocked(held *vaultlock.Held, c RenameCommit) (*RenameCommitResult, error) {
	if err := held.RequireRoot(); err != nil {
		return nil, err
	}
	vaultPath := held.Root()

	head, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("read HEAD: %w", err)
	}
	if head != c.ExpectHead {
		return nil, &RenameHeadMovedError{From: c.ExpectHead, To: head}
	}

	// SF3: only old trees with index entries, only new trees present on disk.
	var pathspecs []string
	for _, tree := range ProjectTrees(c.From) {
		out, err := gitCmd(vaultPath, 30*time.Second, "ls-files", "-z", "--", tree)
		if err != nil {
			return nil, fmt.Errorf("list tracked files under %s: %w", tree, err)
		}
		if len(splitZ([]byte(out))) > 0 {
			pathspecs = append(pathspecs, tree)
		}
	}
	for _, tree := range ProjectTrees(c.To) {
		if _, err := os.Lstat(filepath.Join(vaultPath, filepath.FromSlash(tree))); err == nil {
			pathspecs = append(pathspecs, tree)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat %s: %w", tree, err)
		}
	}
	// Audits/baseline.json (W8) when the rewrite changed it.
	const baselineRel = "Audits/baseline.json"
	if _, err := os.Lstat(filepath.Join(vaultPath, filepath.FromSlash(baselineRel))); err == nil {
		if dirty, derr := HasUncommittedChanges(vaultPath, baselineRel); derr != nil {
			return nil, fmt.Errorf("check %s: %w", baselineRel, derr)
		} else if dirty {
			pathspecs = append(pathspecs, baselineRel)
		}
	}
	// The renamed departure record, always.
	if c.Record != "" {
		pathspecs = append(pathspecs, c.Record)
	}
	// Audits/.surface, stamped by the atomicfile rewrites, when it is dirty.
	const surfaceRel = "Audits/.surface"
	if _, err := os.Lstat(filepath.Join(vaultPath, filepath.FromSlash(surfaceRel))); err == nil {
		if dirty, derr := HasUncommittedChanges(vaultPath, surfaceRel); derr != nil {
			return nil, fmt.Errorf("check %s: %w", surfaceRel, derr)
		} else if dirty {
			pathspecs = append(pathspecs, surfaceRel)
		}
	}

	// Stage deletions (old trees) and additions (new trees, baseline, record,
	// .surface) in one pass. `git add -A` over a pathspec stages a removal when
	// the working-tree file is gone and an addition when it is new. A record the
	// vault's .gitignore would drop is refused: a rename without its record is a
	// slug left unprotected.
	if _, err := gitCmd(vaultPath, gitAddTimeout, append([]string{"add", "-A", "--"}, pathspecs...)...); err != nil {
		return nil, fmt.Errorf("stage the rename: %w", err)
	}
	if c.Record != "" {
		if out, err := gitCmd(vaultPath, 10*time.Second, "ls-files", "--", c.Record); err != nil || out == "" {
			return nil, fmt.Errorf("stage the rename: the departure record %s is not staged (ignored by the vault's .gitignore?)", c.Record)
		}
	}

	if err := commitPathspec(vaultPath, gitCommitTimeout, stampedCommitMessage(c.Message, c.Trailers), pathspecs); err != nil {
		return nil, fmt.Errorf("commit the rename: %w", err)
	}
	sha, err := gitCmd(vaultPath, 10*time.Second, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, fmt.Errorf("read the rename commit: %w", err)
	}
	return &RenameCommitResult{CommitSHA: sha}, nil
}
