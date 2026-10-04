// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"time"
)

// DerivedResidue returns the derived residue under the given projects' palace
// trees in the vault at root: every file that is UNTRACKED, IGNORED, and a
// derived index path (isDerivedPath: under palace/<slug>/drawers/, or exactly
// palace/<slug>/ingested-archives.jsonl). Split and merge leave these out of
// what they copy, and split's purge removes them as untracked rows (task
// split-and-merge-exclude-derived-palace-paths, Scope 1).
//
// All three conditions, never fewer:
//   - a TRACKED file is never residue, with or without the migration marker.
//     Before the migration a tracked drawer is the only copy of its records,
//     and leaving it out of a split's manifest would let purge delete it;
//   - a file outside the derived paths is never residue, whatever its ignore
//     status: the ignored *.manifest.json.<hash>.bak backups under
//     Projects/<p>/transcripts/ are project content and travel.
//
// Before the migration no derived path is ignored, so the set is empty. A
// TRACKED derived file on a migrated source (a drawer a merge re-tracked that
// the post-merge untrack has not cleared yet) is not residue either: split
// carries it into the destination, where it lands as an untracked file the
// destination ignores. That is harmless, so it is left that way (Chair ruling
// Q2, 2026-10-04) rather than given a second rule.
//
// It is ONE `git ls-files --others --ignored --exclude-standard -z` call for
// all slugs, with two plain pathspecs per slug (literal-mode safe; never all
// of palace/, whose .local/ holds the whole host-local index), and every line
// is re-checked with isDerivedPath. A vault that is not a git repository has
// no ignore rules, so it has no residue. A git failure is an error: callers
// copy or delete by this set, so "could not list" must not read as "none".
func DerivedResidue(root string, slugs []string) (map[string]struct{}, error) {
	set := map[string]struct{}{}
	if len(slugs) == 0 {
		return set, nil
	}
	if state, _ := InspectVaultGit(root); state == VaultNotGit {
		return set, nil
	}
	want := make(map[string]bool, len(slugs))
	args := []string{"ls-files", "--others", "--ignored", "--exclude-standard", "-z", "--"}
	for _, s := range slugs {
		want[s] = true
		args = append(args, "palace/"+s+"/drawers", "palace/"+s+"/ingested-archives.jsonl")
	}
	out, _, err := derivedResidueRun(root, 2*time.Minute, "", args...)
	if err != nil {
		return nil, fmt.Errorf("list derived residue: %w", err)
	}
	for _, rel := range splitNUL([]byte(out)) {
		if !isDerivedPath(rel) {
			continue
		}
		if slug := splitPathSegment(rel, 1); want[slug] {
			set[rel] = struct{}{}
		}
	}
	return set, nil
}

// derivedResidueRun runs DerivedResidue's one git call; a test seam counts it.
var derivedResidueRun = gitCmdStdin

// splitPathSegment returns the i-th "/"-separated segment of rel, or "".
func splitPathSegment(rel string, i int) string {
	n := 0
	start := 0
	for j := 0; j <= len(rel); j++ {
		if j == len(rel) || rel[j] == '/' {
			if n == i {
				return rel[start:j]
			}
			n++
			start = j + 1
		}
	}
	return ""
}

// RefuseIfLifecyclePending refuses when the vault at path carries a pending
// lifecycle marker (an unfinished `vp vault init`, copy or delete), with no
// exemption. A marker that cannot be read refuses; a git failure locating it
// is an error. Split uses it on a destination it is about to copy into.
func RefuseIfLifecyclePending(path string) error {
	return refuseOnLifecyclePendingStrict(path)
}

// ReadRecordedRemotes reads the vault's tracked .vibe-palace/remotes.toml, the
// remotes `vp vault init` recorded. A vault without the file records none. A
// remote whose URL carries a credential is refused, as clone refuses it.
func ReadRecordedRemotes(root string) ([]RecordedRemote, error) {
	return readRecordedRemotes(root)
}
