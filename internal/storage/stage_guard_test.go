// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Staging guard, migrated. A TRACKED file under an ignored directory — the
// shape where `git check-ignore -q` without --no-index answers "not ignored"
// (GitPathIgnored) — passed to stageInBatches: no error, and the index entry
// is unchanged. Mutant: no guard (git add stages it and exits 1); a guard
// built on GitPathIgnored.
func TestStageInBatches_DropsATrackedFileUnderAnIgnoredDirectory(t *testing.T) {
	dir := layMigratedVault(t)
	writeFile(t, dir, fixtureDrawer, `{"id":"re-tracked"}`+"\n")
	gitRun(t, dir, "add", "-f", "--", fixtureDrawer)
	gitRun(t, dir, "commit", "-q", "-m", "a clean merge re-tracked a drawer")
	if ignored, err := GitPathIgnored(dir, fixtureDrawer); err != nil || ignored {
		t.Fatalf("fixture: GitPathIgnored = %v, %v; the probe must say not ignored", ignored, err)
	}
	before := gitRun(t, dir, "ls-files", "-s", "--", fixtureDrawer)
	writeFile(t, dir, fixtureDrawer, `{"id":"re-tracked"}`+"\n"+`{"id":"enriched"}`+"\n")

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if _, err := stageInBatches(dir, gitAddTimeout, []string{fixtureDrawer, "README.md"}); err != nil {
		t.Fatalf("stageInBatches: %v", err)
	}
	if after := gitRun(t, dir, "ls-files", "-s", "--", fixtureDrawer); after != before {
		t.Errorf("index entry changed: %q -> %q", before, after)
	}
	if !strings.Contains(buf.String(), "dropped an ignored path") || !strings.Contains(buf.String(), fixtureDrawer) {
		t.Errorf("dropped path not logged: %q", buf.String())
	}
}

// Drawer, unmigrated: staged exactly as at HEAD. Mutant: a guard that
// matches the derived pattern directly.
func TestStageInBatches_UnmigratedDrawerIsStaged(t *testing.T) {
	dir := layUnmigratedDrawerVault(t)
	writeFile(t, dir, fixtureDrawer, `{"id":"d1"}`+"\n"+`{"id":"d2"}`+"\n")
	if _, err := stageInBatches(dir, gitAddTimeout, []string{fixtureDrawer}); err != nil {
		t.Fatalf("stageInBatches: %v", err)
	}
	if st := gitRun(t, dir, "status", "--porcelain", "--", fixtureDrawer); st != "M  "+fixtureDrawer {
		t.Errorf("status = %q, want the drawer staged", st)
	}
}

// Staging guard is batched: exactly one check-ignore per `git add` batch,
// never one per path. Mutant: one process per path.
func TestStageInBatches_OneCheckIgnorePerBatch(t *testing.T) {
	dir := initTestRepo(t)
	var paths []string
	for i := range 500 {
		p := fmt.Sprintf("Projects/p/sessions/%03d-%s.md", i, strings.Repeat("x", 150))
		writeFile(t, dir, p, "s\n")
		paths = append(paths, p)
	}
	batches := len(chunkPaths(paths))
	if batches < 2 {
		t.Fatalf("fixture: want several batches, got %d", batches)
	}
	calls := 0
	prev := checkIgnoreRun
	checkIgnoreRun = func(d string, l time.Duration, in string, env []string, args ...string) (string, int, error) {
		calls++
		return prev(d, l, in, env, args...)
	}
	t.Cleanup(func() { checkIgnoreRun = prev })
	if _, err := stageInBatches(dir, gitAddTimeout, paths); err != nil {
		t.Fatalf("stageInBatches: %v", err)
	}
	if calls != batches {
		t.Errorf("check-ignore ran %d times for %d batches", calls, batches)
	}
	if n := strings.Count(gitRun(t, dir, "diff", "--cached", "--name-only"), "\n") + 1; n != len(paths) {
		t.Errorf("%d paths staged, want %d", n, len(paths))
	}
}

// The guard runs check-ignore without literal pathspecs (git refuses literal
// mode there), so it must neutralise magic itself: a file named ':!x.md' is a
// plain name, staged, not an exclude that errors or names everything else.
// Mutant: paths sent without the "./" prefix (exit 128 on ':!').
func TestStageInBatches_MagicLookingNamesAreNames(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, ".gitignore", "ign/\n")
	gitRun(t, dir, "add", "--", ".gitignore")
	gitRun(t, dir, "commit", "-q", "-m", "ignore")
	writeFile(t, dir, ":!x.md", "a\n")
	writeFile(t, dir, ":(top)y.md", "b\n")
	if _, err := stageInBatches(dir, gitAddTimeout, []string{":!x.md", ":(top)y.md"}); err != nil {
		t.Fatalf("stageInBatches: %v", err)
	}
	staged := gitRun(t, dir, "diff", "--cached", "--name-only")
	for _, want := range []string{":!x.md", ":(top)y.md"} {
		if !strings.Contains(staged, want) {
			t.Errorf("%q not staged: %q", want, staged)
		}
	}
}

// R1, probe 1: an enriched, re-tracked drawer named beside an authored file on
// a migrated vault. The guard drops it from `git add`, and the path-scoped
// commit must not take its working-tree bytes either. Mutant: the dropped set
// left in the commit's paths.
func TestCommitAndPushPaths_DoesNotCommitADroppedTrackedDrawer(t *testing.T) {
	dir := layMigratedVault(t)
	writeFile(t, dir, fixtureDrawer, `{"id":"re-tracked"}`+"\n")
	gitRun(t, dir, "add", "-f", "--", fixtureDrawer)
	gitRun(t, dir, "commit", "-q", "-m", "re-tracked")
	before := gitRun(t, dir, "rev-parse", "HEAD:"+fixtureDrawer)
	writeFile(t, dir, fixtureDrawer, `{"id":"re-tracked"}`+"\n"+`{"id":"enriched"}`+"\n")
	writeFile(t, dir, "README.md", "edited\n")
	if _, err := CommitAndPushPaths(dir, "authored", []string{fixtureDrawer, "README.md"}, false); err != nil {
		t.Fatalf("CommitAndPushPaths: %v", err)
	}
	if names := gitRun(t, dir, "show", "--name-only", "--format=", "HEAD"); names != "README.md" {
		t.Errorf("commit touched %q, want only README.md", names)
	}
	if after := gitRun(t, dir, "rev-parse", "HEAD:"+fixtureDrawer); after != before {
		t.Errorf("the drawer's committed blob changed: %s -> %s", before, after)
	}
}

// R1, probe 2: a new, untracked drawer named beside an authored file. The
// commit of the authored file succeeds instead of failing on a pathspec git
// does not know. Mutant: the dropped set left in the commit's paths.
func TestCommitAndPushPaths_AnUntrackedDroppedDrawerDoesNotFailTheCommit(t *testing.T) {
	dir := layMigratedVault(t)
	nd := strings.Replace(fixtureDrawer, ".jsonl", "-new.jsonl", 1)
	writeFile(t, dir, nd, `{"id":"new"}`+"\n")
	writeFile(t, dir, "README.md", "edited\n")
	if _, err := CommitAndPushPaths(dir, "authored", []string{nd, "README.md"}, false); err != nil {
		t.Fatalf("CommitAndPushPaths: %v", err)
	}
	if names := gitRun(t, dir, "show", "--name-only", "--format=", "HEAD"); names != "README.md" {
		t.Errorf("commit touched %q, want only README.md", names)
	}
	if tracked := gitRun(t, dir, "ls-files", "--", nd); tracked != "" {
		t.Errorf("the new drawer was tracked: %q", tracked)
	}
}

// D2: a TRACKED authored file that an ignore line matches (here a user's own
// pattern) is not committed, and the result says so: it is in SkippedPaths
// with SkipIgnored as its reason. Before the guard, `git add` failed loudly;
// silence would hide it. Mutant: the dropped paths not reported.
func TestCommitAndPushPaths_ReportsAnIgnoredPathAsSkipped(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "notes/keep.md", "v1\n")
	gitRun(t, dir, "add", "--", "notes/keep.md")
	gitRun(t, dir, "commit", "-q", "-m", "notes")
	writeFile(t, dir, ".gitignore", "notes/\n")
	gitRun(t, dir, "add", "--", ".gitignore")
	gitRun(t, dir, "commit", "-q", "-m", "a user's own ignore line")
	writeFile(t, dir, "notes/keep.md", "v2\n")
	writeFile(t, dir, "README.md", "edited\n")
	res, err := CommitAndPushPaths(dir, "authored", []string{"notes/keep.md", "README.md"}, false)
	if err != nil {
		t.Fatalf("CommitAndPushPaths: %v", err)
	}
	if !slices.Contains(res.SkippedPaths, "notes/keep.md") || res.SkipReasons["notes/keep.md"] != SkipIgnored {
		t.Errorf("SkippedPaths %q, SkipReasons %q; want notes/keep.md skipped as %q", res.SkippedPaths, res.SkipReasons, SkipIgnored)
	}
	if names := gitRun(t, dir, "show", "--name-only", "--format=", "HEAD"); names != "README.md" {
		t.Errorf("commit touched %q, want only README.md", names)
	}
}

// D1: CommitRemovals leaves a removal the vault ignores out of its commit and
// reports it, rather than committing it through the path-scoped commit. The
// path is a Templates/ mirror under a custom ignore line. Mutant: the dropped
// set left in the commit.
func TestCommitRemovals_DropsAnIgnoredRemoval(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "Templates/commands/a.md", "a\n")
	writeFile(t, dir, "Templates/commands/b.md", "b\n")
	gitRun(t, dir, "add", "--", "Templates")
	gitRun(t, dir, "commit", "-q", "-m", "mirrors")
	writeFile(t, dir, ".gitignore", "Templates/commands/b.md\n")
	gitRun(t, dir, "add", "--", ".gitignore")
	gitRun(t, dir, "commit", "-q", "-m", "ignore b")
	for _, f := range []string{"a.md", "b.md"} {
		if err := os.Remove(filepath.Join(dir, "Templates/commands", f)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := CommitRemovals(dir, "remove mirrors", []string{"Templates/commands/a.md", "Templates/commands/b.md"})
	if err != nil {
		t.Fatalf("CommitRemovals: %v", err)
	}
	if names := gitRun(t, dir, "show", "--name-only", "--format=", "HEAD"); names != "Templates/commands/a.md" {
		t.Errorf("commit touched %q, want only Templates/commands/a.md", names)
	}
	if res.SkipReasons["Templates/commands/b.md"] != SkipIgnored {
		t.Errorf("SkipReasons %q, want b.md skipped as ignored", res.SkipReasons)
	}
}

// D1: the mirror prune keeps a mirror the vault ignores (reported, not pruned)
// rather than committing its removal through the path-scoped commit. Mutant:
// the dropped set left in the commit.
func TestPruneMirrors_KeepsAnIgnoredMirror(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "T/a.md", "mirror\n")
	writeFile(t, dir, "T/b.md", "mirror\n")
	gitRun(t, dir, "add", "--", "T")
	gitRun(t, dir, "commit", "-q", "-m", "mirrors")
	writeFile(t, dir, ".gitignore", "T/b.md\n")
	gitRun(t, dir, "add", "--", ".gitignore")
	gitRun(t, dir, "commit", "-q", "-m", "ignore b")
	_, out, err := pruneMirrors(dir, []string{"T/a.md", "T/b.md"}, false, true, acceptOnly(nil, "mirror\n"))
	if err != nil {
		t.Fatalf("prune: %v (%+v)", err, out)
	}
	if names := gitRun(t, dir, "show", "--name-only", "--format=", "HEAD"); names != "T/a.md" {
		t.Errorf("commit touched %q, want only T/a.md", names)
	}
	if !slices.Contains(out.Committed, "T/a.md") || slices.Contains(out.Committed, "T/b.md") {
		t.Errorf("Committed = %q", out.Committed)
	}
	if st := gitRun(t, dir, "status", "--porcelain", "--", "T"); st != "" {
		t.Errorf("the kept mirror was left deleted: %q", st)
	}
}
