// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// The pull self-heal and the post-merge untrack (task
// tidy-pull-and-audit-behaviour-keyed-on-the-migration-marker, "Plan
// revisions" R1). On a MIGRATED vault the derived index paths — drawers and
// the ingest ledger — are per-host data that git must not carry. Two merge
// outcomes would carry them anyway:
//
//   - a CONFLICT on a derived path: a lagging host's drawer edit meets the
//     migration's deletion (UD, DU). healDerivedConflicts resolves it by
//     deleting the path, and concludes the merge;
//   - a CLEAN merge that brings a derived path back, such as a lagging host's
//     commit adding a new drawer. untrackDerivedAfterMerge untracks it.
//
// Both run at the two places vp merges remote commits into the vault:
// mergeFetchedTip (behind both commit-and-push reconciles) and pullCore. Both
// run under the vault commit lock their callers hold, and take no other lock.
//
// Every git call here is correct under GIT_LITERAL_PATHSPECS=1: index edits go
// through `git update-index --stdin` (paths, not pathspecs), and listings use a
// plain directory or file pathspec filtered in Go.

// DerivedMergeReport is what the heal and the untrack did at one merge site.
type DerivedMergeReport struct {
	// Healed are the derived paths a conflicted merge deleted (index and
	// working tree) before it was concluded.
	Healed []string
	// Untracked are the derived paths a merge had (re-)tracked that were
	// untracked after it; the files stay on disk, ignored.
	Untracked []string
}

// Lines renders the report, one line per path, for both front-ends.
func (r DerivedMergeReport) Lines() []string {
	var lines []string
	for _, p := range r.Healed {
		lines = append(lines, fmt.Sprintf("[derived] deleted %s: a merge conflict on a derived index path of a migrated vault (rebuilt per host from archives)", p))
	}
	for _, p := range r.Untracked {
		lines = append(lines, fmt.Sprintf("[derived] untracked %s: a merge had tracked a derived index path of a migrated vault (the file is kept, ignored)", p))
	}
	return lines
}

func (r *DerivedMergeReport) add(other DerivedMergeReport) {
	if r == nil {
		return
	}
	r.Healed = append(r.Healed, other.Healed...)
	r.Untracked = append(r.Untracked, other.Untracked...)
}

// derivedUntrackError is a merge that succeeded but whose post-merge untrack
// failed. The merge stands; the caller must not push, so a derived path is
// never published from this host.
type derivedUntrackError struct{ err error }

func (e *derivedUntrackError) Error() string {
	return "the merge succeeded, but untracking the derived index paths it brought in failed, so nothing was pushed: " + e.err.Error()
}
func (e *derivedUntrackError) Unwrap() error { return e.err }

// isDerivedPath reports whether rel (vault-relative, slash-separated) is a
// derived index path: anything under palace/<p>/drawers/, or exactly
// palace/<p>/ingested-archives.jsonl, where <p> is a valid project slug. It is
// MigratedVaultGitignorePatterns as a predicate.
func isDerivedPath(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) < 3 || parts[0] != "palace" || slug.Validate(parts[1]) != nil {
		return false
	}
	switch {
	case parts[2] == "drawers" && len(parts) >= 4:
		return true
	case len(parts) == 3 && parts[2] == "ingested-archives.jsonl":
		return true
	}
	return false
}

// isEntitiesFile reports whether rel is a project's kg/entities.jsonl.
func isEntitiesFile(rel string) bool {
	parts := strings.Split(rel, "/")
	return len(parts) == 4 && parts[0] == "palace" && parts[2] == "kg" && parts[3] == "entities.jsonl"
}

// unmergedEntry is one path's unmerged index stages.
type unmergedEntry struct {
	path   string
	stages []string // "1", "2", "3"
}

// unmergedPathsZ lists the unmerged index entries, by `git ls-files -u -z`.
// Unlike unmergedPaths it returns git's error, and reads NUL-separated records,
// so a path git would C-quote ("palace/p/drawers/caf\303\251.md") comes back as
// itself.
func unmergedPathsZ(vaultPath string) ([]unmergedEntry, error) {
	out, _, err := gitCmdStdin(vaultPath, 30*time.Second, "", "ls-files", "-u", "-z")
	if err != nil {
		return nil, fmt.Errorf("list unmerged paths: %w", err)
	}
	var entries []unmergedEntry
	index := map[string]int{}
	for _, rec := range splitNUL([]byte(out)) {
		// "<mode> <oid> <stage>\t<path>"
		meta, path, ok := strings.Cut(rec, "\t")
		if !ok {
			return nil, fmt.Errorf("list unmerged paths: unexpected record %q", rec)
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return nil, fmt.Errorf("list unmerged paths: unexpected record %q", rec)
		}
		i, seen := index[path]
		if !seen {
			i = len(entries)
			index[path] = i
			entries = append(entries, unmergedEntry{path: path})
		}
		entries[i].stages = append(entries[i].stages, fields[2])
	}
	return entries, nil
}

// manifestSource names where a marker is read from.
type manifestSource int

const (
	// fromMergedIndex is stage 0 of the index: the merge's result, whichever
	// side the marker came from. MERGE_HEAD alone would miss a migrated host
	// merging an unmigrated commit (DU), where the marker is only on HEAD's side.
	fromMergedIndex manifestSource = iota
	// fromHEAD is the committed tree: after a successful merge, the merged tree.
	fromHEAD
)

// migratedInTree reports whether the vault.toml in src carries the migration
// marker. A tree without the file is unmigrated. vault.toml that is itself
// unmerged (no stage 0), a git failure, or a malformed manifest is an error:
// every caller is a writer, and a writer fails closed.
func migratedInTree(vaultPath string, src manifestSource) (bool, error) {
	var (
		out, where string
		err        error
	)
	switch src {
	case fromMergedIndex:
		where = "the merged index"
		out, _, err = gitCmdStdin(vaultPath, 10*time.Second, "", "ls-files", "-s", "-z", "--", vaultManifestRel)
	default:
		where = "HEAD"
		out, _, err = gitCmdStdin(vaultPath, 10*time.Second, "", "ls-tree", "-z", "HEAD", "--", vaultManifestRel)
	}
	if err != nil {
		return false, fmt.Errorf("read the migration marker from %s: %w", where, err)
	}
	recs := splitNUL([]byte(out))
	if len(recs) == 0 {
		return false, nil
	}
	oid := ""
	for _, rec := range recs {
		meta, _, _ := strings.Cut(rec, "\t")
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return false, fmt.Errorf("read the migration marker from %s: unexpected record %q", where, rec)
		}
		switch {
		case src == fromHEAD:
			oid = fields[2] // "<mode> blob <oid>"
		case fields[2] == "0":
			oid = fields[1] // "<mode> <oid> <stage>"
		}
	}
	if oid == "" {
		return false, fmt.Errorf("read the migration marker from %s: %s is itself unmerged, so whether the merged vault is migrated cannot be told; resolve it by hand", where, vaultManifestRel)
	}
	blob, _, err := gitCmdStdin(vaultPath, 10*time.Second, "", "cat-file", "blob", oid)
	if err != nil {
		return false, fmt.Errorf("read the migration marker from %s: %w", where, err)
	}
	m, err := surface.ParseVaultManifest([]byte(blob))
	if err != nil {
		return false, fmt.Errorf("read the migration marker from %s: %w", where, err)
	}
	return m.AuthoredOnly != "", nil
}

// dropDerivedPaths removes paths from the index, every stage, by `git
// update-index --force-remove --stdin`. With deleteFiles it then removes each
// file from the working tree (one already gone is fine). It is the one
// mechanic behind both the heal (deleteFiles: the ADR's delete, not move) and
// the untrack (keep: the file stays, ignored, and may hold enriched bytes this
// host never committed).
func dropDerivedPaths(vaultPath string, paths []string, deleteFiles bool) error {
	if len(paths) == 0 {
		return nil
	}
	in := strings.Join(paths, "\x00") + "\x00"
	if _, _, err := gitCmdStdin(vaultPath, 30*time.Second, in, "update-index", "-z", "--force-remove", "--stdin"); err != nil {
		return fmt.Errorf("remove derived paths from the index: %w", err)
	}
	if !deleteFiles {
		return nil
	}
	// Through vaultfs.Delete, the vault's removal primitive: it resolves the
	// path safely under the vault and takes the path's own advisory lock (a
	// different key from the commit lock the caller holds).
	for _, p := range paths {
		if _, err := vaultfs.Delete(vaultPath, p, ""); err != nil && !errors.Is(err, vaultfs.ErrFileNotFound) {
			return fmt.Errorf("delete derived path %s: %w", p, err)
		}
	}
	return nil
}

// healConflicts is the heal the two merge sites call; a test seam counts it.
var healConflicts = healDerivedConflicts

// healDerivedConflicts resolves a conflicted merge whose every unmerged path is
// derived, on a vault the MERGED index says is migrated: it deletes those
// paths (index and working tree) and concludes the merge with
// `git commit --no-edit`. entries are the merge's unmerged paths
// (unmergedPathsZ); a caller with none — git refused the merge before it
// started — does not call it.
//
// handled is false, with nothing touched, when any unmerged path is not
// derived (the heal is all-or-nothing; the caller keeps its own handling), or
// when the merged vault is not migrated. A marker that cannot be read is err, and
// nothing is touched. named lists a kg/entities.jsonl both sides changed (UU),
// which only the migration's operator attestation prevents; it is never
// resolved here, and the caller names it in its error.
func healDerivedConflicts(vaultPath string, entries []unmergedEntry) (healed, named []string, handled bool, err error) {
	var derived []string
	other := false
	for _, e := range entries {
		switch {
		case isDerivedPath(e.path):
			derived = append(derived, e.path)
		default:
			other = true
			if isEntitiesFile(e.path) && slices.Contains(e.stages, "2") && slices.Contains(e.stages, "3") {
				named = append(named, e.path)
			}
		}
	}
	if other || len(derived) == 0 {
		return nil, named, false, nil
	}
	migrated, err := migratedInTree(vaultPath, fromMergedIndex)
	if err != nil || !migrated {
		return nil, nil, false, err
	}
	if err := dropDerivedPaths(vaultPath, derived, true); err != nil {
		return nil, nil, false, err
	}
	if _, err := gitCmd(vaultPath, gitCommitTimeout, "commit", "--no-edit"); err != nil {
		return nil, nil, false, fmt.Errorf("conclude the merge after deleting %d derived path(s): %w", len(derived), err)
	}
	for _, p := range derived {
		slog.Info("merge: deleted a conflicted derived index path", "vault", vaultPath, "path", p)
	}
	return derived, nil, true, nil
}

// untrackBeforeCommit runs between the untrack's index edit and its commit; a
// test seam rewrites a file or fails the commit there.
var untrackBeforeCommit = func() error { return nil }

// untrackDerivedAfterMerge untracks every derived path the merged tree tracks,
// on a vault whose HEAD is migrated, and commits that FROM THE INDEX. It runs
// after every successful merge — clean, fast-forward, already up to date, or
// concluded by the heal — with no "HEAD did not move" shortcut: a run whose
// untrack failed must be retried by the next, up-to-date one, or that one
// would push the tracked drawer.
//
// The commit is a plain `git commit`, not a path-scoped one: under the commit
// lock, after a merge (which needs a clean index), the index holds only this
// edit, and that is checked first (`git diff --cached` lists exactly the
// dropped set). A path-scoped commit would re-read the working tree and fail
// with "nothing to commit" if the file were rewritten in between, leaving a
// staged deletion that blocks the next merge. On any failure after the edit
// the index entries are restored, so nothing is left staged.
//
// Extracted KG records a merge brings in are NOT untracked here; they stay
// tracked, and the kg-tracked-extracted audit reports them.
func untrackDerivedAfterMerge(vaultPath string) ([]string, error) {
	out, _, err := gitCmdStdin(vaultPath, 30*time.Second, "", "ls-files", "-z", "--", "palace")
	if err != nil {
		return nil, fmt.Errorf("list tracked palace paths: %w", err)
	}
	var derived []string
	for _, p := range splitNUL([]byte(out)) {
		if isDerivedPath(p) {
			derived = append(derived, p)
		}
	}
	if len(derived) == 0 {
		return nil, nil
	}
	migrated, err := migratedInTree(vaultPath, fromHEAD)
	if err != nil || !migrated {
		return nil, err
	}
	before, err := indexEntriesFor(vaultPath, derived)
	if err != nil {
		return nil, err
	}
	if err := dropDerivedPaths(vaultPath, derived, false); err != nil {
		return nil, err
	}
	restore := func(cause error) error {
		if rerr := restoreIndexEntries(vaultPath, derived, before); rerr != nil {
			return fmt.Errorf("%w; and restoring the index failed: %w", cause, rerr)
		}
		return cause
	}
	if err := untrackBeforeCommit(); err != nil {
		return nil, restore(err)
	}
	staged, _, err := gitCmdStdin(vaultPath, 30*time.Second, "", "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return nil, restore(fmt.Errorf("check the staged untrack: %w", err))
	}
	got := splitNUL([]byte(staged))
	want := slices.Clone(derived)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		return nil, restore(fmt.Errorf("refusing to commit the untrack: the index holds %d staged path(s), not exactly the %d derived path(s) dropped", len(got), len(want)))
	}
	msg := fmt.Sprintf("Untrack derived index paths a merge re-tracked on a migrated vault\n\n%d path(s), kept on disk and ignored:\n  %s",
		len(derived), strings.Join(derived, "\n  "))
	if _, err := gitCmd(vaultPath, gitCommitTimeout, "commit", "-q", "-m", msg); err != nil {
		return nil, restore(fmt.Errorf("commit the untrack: %w", err))
	}
	for _, p := range derived {
		slog.Info("merge: untracked a derived index path", "vault", vaultPath, "path", p)
	}
	return derived, nil
}
