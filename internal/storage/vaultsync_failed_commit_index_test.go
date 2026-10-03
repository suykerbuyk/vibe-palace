// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rejectAllCommits installs a pre-commit hook in dir's repository that fails
// every commit.
func rejectAllCommits(t *testing.T, dir string) {
	t.Helper()
	hooks := t.TempDir()
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\necho 'hook: rejected' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "config", "core.hooksPath", hooks)
}

// A commit that fails AFTER staging — here a pre-commit hook rejects it —
// returns its error and leaves the index exactly as it was before the call:
// nothing it staged stays staged, another path's staged entry survives, and an
// operator's own pre-staged version of one of the committed paths survives as
// that version. Pinned on a born and an unborn HEAD (a fresh vault's first
// commit is the unborn case).
func TestCommitAndPushPaths_FailedCommitRestoresTheIndex(t *testing.T) {
	for _, tc := range []struct {
		name string
		repo func(*testing.T) string
	}{
		{"born HEAD", initTestRepo},
		{"unborn HEAD", initUnbornTestRepo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.repo(t)
			writeFile(t, dir, "other.md", "someone else's staged work\n")
			gitRun(t, dir, "add", "other.md")
			writeFile(t, dir, "pre.md", "operator staged this\n")
			gitRun(t, dir, "add", "pre.md")
			writeFile(t, dir, "pre.md", "then edited it again\n")
			writeFile(t, dir, "Projects/p1/.surface", "surface = 8\n")
			writeFile(t, dir, "Projects/p1/commands/README.md", "stub\n")
			rejectAllCommits(t, dir)
			before := gitRun(t, dir, "ls-files", "-s")

			_, err := CommitAndPushPaths(dir, "should fail", []string{"Projects/p1/.surface", "Projects/p1/commands/README.md", "pre.md"}, false)
			if err == nil {
				t.Fatal("the hook-rejected commit returned no error")
			}
			if after := gitRun(t, dir, "ls-files", "-s"); after != before {
				t.Errorf("index changed by a failed commit\n--- before ---\n%s\n--- after ---\n%s", before, after)
			}
			st := gitRun(t, dir, "status", "--porcelain", "-uall")
			for _, want := range []string{"?? Projects/p1/.surface", "?? Projects/p1/commands/README.md", "A  other.md", "AM pre.md"} {
				if !containsLine(st, want) {
					t.Errorf("status lacks %q:\n%s", want, st)
				}
			}
		})
	}
}

// The same, when the commit names a DIRECTORY: every entry staged beneath it
// is undone, and another path's staged entry (Projects/p10) survives. (The
// restore rewrites only entries that CHANGED, so a snapshot that also caught
// an unrelated path would still leave it alone — this pins the directory
// case, not the matcher's boundary.)
func TestCommitAndPushPaths_FailedDirectoryCommitRestoresTheIndex(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "Projects/p10/x.md", "a neighbour's staged file\n")
	gitRun(t, dir, "add", "Projects/p10/x.md")
	writeFile(t, dir, "Projects/p1/.surface", "surface = 8\n")
	writeFile(t, dir, "Projects/p1/commands/README.md", "stub\n")
	rejectAllCommits(t, dir)
	before := gitRun(t, dir, "ls-files", "-s")

	if _, err := CommitAndPushPaths(dir, "should fail", []string{"Projects/p1"}, false); err == nil {
		t.Fatal("the hook-rejected commit returned no error")
	}
	if after := gitRun(t, dir, "ls-files", "-s"); after != before {
		t.Errorf("index changed by a failed commit\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// indexEntriesFor matches a path and what lies BENEATH it as a directory, never
// a sibling that merely shares its prefix: Projects/p1 must not catch
// Projects/p10.
func TestIndexEntriesFor_DirectoryBoundary(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "Projects/p1/a.md", "a\n")
	writeFile(t, dir, "Projects/p10/y.md", "y\n")
	gitRun(t, dir, "add", "Projects/p1/a.md", "Projects/p10/y.md")

	got, err := indexEntriesFor(dir, []string{"Projects/p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("got %d keys %v, want exactly Projects/p1/a.md", len(got), got)
	}
	if _, ok := got["Projects/p1/a.md"]; !ok {
		t.Errorf("Projects/p1/a.md missing: %v", got)
	}
	for k := range got {
		if strings.HasPrefix(k, "Projects/p10/") {
			t.Errorf("a sibling sharing the prefix was matched: %s", k)
		}
	}
}
