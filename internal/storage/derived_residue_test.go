// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

func residueKeys(set map[string]struct{}) []string {
	return slices.Sorted(maps.Keys(set))
}

// A tracked drawer is never residue: not on an unmigrated vault, and not on a
// migrated one where the same file is still tracked under an ignored path.
// Mutant: a filter that drops the only copy of a tracked drawer (11-B1).
func TestDerivedResidue_TrackedDrawerIsNever(t *testing.T) {
	un := layUnmigratedDrawerVault(t)
	if set, err := DerivedResidue(un, []string{"p"}); err != nil || len(set) != 0 {
		t.Fatalf("unmigrated: %v, %v; want empty", residueKeys(set), err)
	}
	mig := layMigratedVault(t)
	writeFile(t, mig, fixtureDrawer, "{}\n")
	gitRun(t, mig, "add", "-f", "--", fixtureDrawer)
	gitRun(t, mig, "commit", "-q", "-m", "re-tracked")
	if set, err := DerivedResidue(mig, []string{"p"}); err != nil || len(set) != 0 {
		t.Fatalf("migrated, tracked drawer: %v, %v; want empty", residueKeys(set), err)
	}
}

// Untracked, ignored derived files are residue; untracked ignored files
// outside the derived paths (the *.bak manifests) and untracked files that are
// not ignored are not. Mutant: "untracked and ignored" without the path
// condition (split-B1); over-broad filtering.
func TestDerivedResidue_PathAndUntrackedAndIgnored(t *testing.T) {
	dir := layMigratedVault(t)
	writeFile(t, dir, "palace/p/drawers/w/r/drawers.jsonl", "{}\n")
	writeFile(t, dir, "palace/p/ingested-archives.jsonl", "{}\n")
	writeFile(t, dir, "Projects/p/transcripts/x.manifest.json.abc.bak", "{}\n")
	writeFile(t, dir, "palace/p/kg/triples/s/a.json", "{}\n")       // untracked, NOT ignored
	writeFile(t, dir, "palace/q/drawers/w/r/drawers.jsonl", "{}\n") // another slug
	set, err := DerivedResidue(dir, []string{"p"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"palace/p/drawers/w/r/drawers.jsonl", "palace/p/ingested-archives.jsonl"}
	if got := residueKeys(set); !slices.Equal(got, want) {
		t.Errorf("residue = %q, want %q", got, want)
	}
}

// One git call for all slugs. Mutant: one call per slug or per path.
func TestDerivedResidue_OneGitCall(t *testing.T) {
	dir := layMigratedVault(t)
	calls := 0
	prev := derivedResidueRun
	derivedResidueRun = func(d string, l time.Duration, in string, args ...string) (string, int, error) {
		calls++
		if !slices.Contains(args, "--ignored") || !slices.Contains(args, "--others") {
			t.Errorf("unexpected git call %q", args)
		}
		return prev(d, l, in, args...)
	}
	t.Cleanup(func() { derivedResidueRun = prev })
	if _, err := DerivedResidue(dir, []string{"a", "b", "p"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("%d git calls for three slugs, want 1", calls)
	}
}

// A git failure is an error, never an empty set. Mutant: the error swallowed.
func TestDerivedResidue_GitFailureIsAnError(t *testing.T) {
	dir := layMigratedVault(t)
	prev := derivedResidueRun
	derivedResidueRun = func(string, time.Duration, string, ...string) (string, int, error) {
		return "", 128, errors.New("injected")
	}
	t.Cleanup(func() { derivedResidueRun = prev })
	if _, err := DerivedResidue(dir, []string{"p"}); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("err = %v, want the git failure", err)
	}
}

// A vault that is not a git repository has no ignore rules, so no residue.
func TestDerivedResidue_NotAGitVault(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "palace/p/drawers/w/r/drawers.jsonl", "{}\n")
	if set, err := DerivedResidue(dir, []string{"p"}); err != nil || len(set) != 0 {
		t.Fatalf("%v, %v; want empty", residueKeys(set), err)
	}
}
