// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"fmt"
	"log/slog"
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

	if err := stageInBatches(dir, gitAddTimeout, []string{fixtureDrawer, "README.md"}); err != nil {
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
	if err := stageInBatches(dir, gitAddTimeout, []string{fixtureDrawer}); err != nil {
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
	if err := stageInBatches(dir, gitAddTimeout, paths); err != nil {
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
	if err := stageInBatches(dir, gitAddTimeout, []string{":!x.md", ":(top)y.md"}); err != nil {
		t.Fatalf("stageInBatches: %v", err)
	}
	staged := gitRun(t, dir, "diff", "--cached", "--name-only")
	for _, want := range []string{":!x.md", ":(top)y.md"} {
		if !strings.Contains(staged, want) {
			t.Errorf("%q not staged: %q", want, staged)
		}
	}
}
