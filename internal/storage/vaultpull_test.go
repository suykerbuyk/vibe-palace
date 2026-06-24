// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readFile reads a working-tree file relative to dir, failing the test on error.
func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// advanceRemoteMulti advances bare's main by one commit that applies an
// arbitrary multi-file mutation via a throwaway clone, so a repo tracking the
// same bare is genuinely behind on several paths at once.
func advanceRemoteMulti(t *testing.T, bare string, mutate func(clone string)) {
	t.Helper()
	other := t.TempDir()
	gitRun(t, other, "clone", "-b", "main", bare, ".")
	gitRun(t, other, "config", "user.email", "other@example.com")
	gitRun(t, other, "config", "user.name", "Other")
	mutate(other)
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-m", "remote advance")
	gitRun(t, other, "push", "origin", "main")
}

const phantomTemplate = "Templates/commands/foo.md"

// TestPull_PhantomTemplateHeal is the core Phase-1 guard: a dirty
// Templates/commands/*.md whose working-tree bytes equal the freshly-fetched
// remote ref is discarded so the merge proceeds, and the path is reported in
// HealedTemplates. Without the heal, `git pull` aborts ("local changes would be
// overwritten by merge") and the host strands.
func TestPull_PhantomTemplateHeal(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	writeFile(t, dir, phantomTemplate, "A\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "seed template A")
	gitRun(t, dir, "push", "origin", "main")

	// Remote advances the SAME template to "B".
	advanceRemote(t, bare, phantomTemplate, "B\n")

	// Phantom dirt: working tree holds the remote's "B" bytes uncommitted while
	// HEAD still has "A" — exactly the `vp commands upgrade` overwrite case.
	writeFile(t, dir, phantomTemplate, "B\n")

	res, err := Pull(dir, []string{"origin"})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !res.AllPulled() {
		t.Fatalf("expected clean pull after heal, got %#v (output=%q)", res.RemoteResults, res.RemoteOutput["origin"])
	}
	if len(res.HealedTemplates) != 1 || res.HealedTemplates[0] != phantomTemplate {
		t.Errorf("expected HealedTemplates=[%s], got %#v", phantomTemplate, res.HealedTemplates)
	}
	if got := readFile(t, dir, phantomTemplate); got != "B\n" {
		t.Errorf("merged template = %q, want %q", got, "B\n")
	}
}

// TestPull_GenuineEditNotHealed proves a real local edit (diff nonzero vs the
// remote ref) is NOT healed: it is left for the merge, which aborts, and the
// edit survives intact in the working tree.
func TestPull_GenuineEditNotHealed(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	writeFile(t, dir, phantomTemplate, "A\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "seed template A")
	gitRun(t, dir, "push", "origin", "main")

	advanceRemote(t, bare, phantomTemplate, "B\n")

	// Genuine local edit that differs from the remote "B".
	writeFile(t, dir, phantomTemplate, "LOCAL EDIT\n")

	res, err := Pull(dir, []string{"origin"})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(res.HealedTemplates) != 0 {
		t.Errorf("genuine edit must NOT be healed, got %#v", res.HealedTemplates)
	}
	if res.RemoteResults["origin"] == nil {
		t.Errorf("expected merge to abort (local changes would be overwritten), got success")
	}
	if got := readFile(t, dir, phantomTemplate); got != "LOCAL EDIT\n" {
		t.Errorf("local edit must survive, working tree = %q", got)
	}
}

// TestPull_UnreachableRemote records a fetch failure per-remote (graceful) with
// a nil top-level error, mirroring GetRemoteStatus's unreachable handling.
func TestPull_UnreachableRemote(t *testing.T) {
	dir := initTestRepo(t)
	gitRun(t, dir, "remote", "add", "origin", filepath.Join(dir, "does-not-exist.git"))

	res, err := Pull(dir, []string{"origin"})
	if err != nil {
		t.Fatalf("Pull must not hard-error on an unreachable remote: %v", err)
	}
	if res.RemoteResults["origin"] == nil {
		t.Errorf("expected a per-remote fetch error, got nil")
	}
	if !res.Stranded() {
		t.Errorf("a sole unreachable remote should strand: %#v", res.RemoteResults)
	}
}

// TestPull_MultiRemoteResultMap verifies Pull attempts EVERY remote and records
// each outcome (best-effort caller reads the whole map; fail-fast caller stops
// at the first error). One reachable, one broken: both must appear.
func TestPull_MultiRemoteResultMap(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "remote", "add", "backup", filepath.Join(dir, "does-not-exist.git"))
	gitRun(t, dir, "push", "origin", "main")

	res, err := Pull(dir, []string{"origin", "backup"})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(res.RemoteResults) != 2 {
		t.Fatalf("expected both remotes recorded, got %#v", res.RemoteResults)
	}
	if res.RemoteResults["origin"] != nil {
		t.Errorf("origin should pull cleanly (already up to date): %v", res.RemoteResults["origin"])
	}
	if res.RemoteResults["backup"] == nil {
		t.Errorf("broken backup should record an error")
	}
	if !res.AnyPulled() || res.AllPulled() {
		t.Errorf("AnyPulled=%v AllPulled=%v, want true/false", res.AnyPulled(), res.AllPulled())
	}
	if res.Stranded() {
		t.Errorf("one success means not stranded: %#v", res.RemoteResults)
	}
}

// TestPull_RestartFlow is the full /restart-style flow: a behind remote that
// advanced both the phantom template AND a disjoint file, with the host carrying
// stale phantom dirt. The heal clears the obstruction and the merge brings in
// both remote changes cleanly.
func TestPull_RestartFlow(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	writeFile(t, dir, phantomTemplate, "A\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "seed template A")
	gitRun(t, dir, "push", "origin", "main")

	// Remote advances both the phantom template and a disjoint command file in
	// one commit, so the local repo is genuinely behind on two paths.
	advanceRemoteMulti(t, bare, func(clone string) {
		writeFile(t, clone, phantomTemplate, "B\n")
		writeFile(t, clone, "Templates/commands/bar.md", "new command\n")
	})

	// Stale phantom dirt matching the incoming remote bytes.
	writeFile(t, dir, phantomTemplate, "B\n")

	res, err := Pull(dir, []string{"origin"})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !res.AllPulled() {
		t.Fatalf("restart pull should land clean, got %#v (output=%q)", res.RemoteResults, res.RemoteOutput["origin"])
	}
	if len(res.HealedTemplates) != 1 || res.HealedTemplates[0] != phantomTemplate {
		t.Errorf("expected heal of %s, got %#v", phantomTemplate, res.HealedTemplates)
	}
	if got := readFile(t, dir, phantomTemplate); got != "B\n" {
		t.Errorf("template not merged: %q", got)
	}
	if got := readFile(t, dir, "Templates/commands/bar.md"); got != "new command\n" {
		t.Errorf("disjoint remote file not merged: %q", got)
	}
	// Working tree must be clean after a successful merge.
	if dirty, _ := HasUncommittedChanges(dir, "."); dirty {
		t.Errorf("working tree should be clean after restart pull")
	}
}

// TestPull_ConflictStopsSweep proves Fix #2: once a remote's merge leaves the
// tree with unmerged (conflict) paths, the sweep stops — every later remote is
// recorded with the skip sentinel and is never fetched or merged. origin diverges
// from a divergent local commit on the same line (a real merge conflict); backup
// is a healthy remote that would otherwise merge, proving it was deliberately
// skipped rather than failing on its own.
func TestPull_ConflictStopsSweep(t *testing.T) {
	dir := initTestRepo(t)
	origin := initBareRemote(t)
	backup := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", origin)
	gitRun(t, dir, "remote", "add", "backup", backup)
	writeFile(t, dir, "conflict.md", "SEED\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "seed")
	gitRun(t, dir, "push", "origin", "main")
	gitRun(t, dir, "push", "backup", "main")

	// origin advances the same line that the local commit below changes, so the
	// merge genuinely conflicts (both diverge from the SEED ancestor).
	advanceRemote(t, origin, "conflict.md", "REMOTE\n")
	writeFile(t, dir, "conflict.md", "LOCAL\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "local divergent")

	res, err := Pull(dir, []string{"origin", "backup"})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if res.RemoteResults["origin"] == nil {
		t.Errorf("origin merge should conflict, got nil")
	}
	skip := res.RemoteResults["backup"]
	if skip == nil {
		t.Fatalf("backup should be recorded as skipped, got nil")
	}
	if !strings.Contains(skip.Error(), "skipped") {
		t.Errorf("backup should carry the skip sentinel, got %v", skip)
	}
	if out, ok := res.RemoteOutput["backup"]; ok && out != "" {
		t.Errorf("skipped backup must never be attempted (no output), got %q", out)
	}
}

// TestPull_MultiRemoteSingleScanHeal proves Fix #8: the dirty template set is
// scanned once before the loop and a path healed on the first remote is neither
// re-healed nor double-counted on a later remote. Two healthy remotes; only the
// first carries the incoming bytes, so the phantom heals exactly once and the
// second still merges cleanly with the heal already applied.
func TestPull_MultiRemoteSingleScanHeal(t *testing.T) {
	dir := initTestRepo(t)
	origin := initBareRemote(t)
	backup := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", origin)
	gitRun(t, dir, "remote", "add", "backup", backup)
	writeFile(t, dir, phantomTemplate, "A\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "seed template A")
	gitRun(t, dir, "push", "origin", "main")
	gitRun(t, dir, "push", "backup", "main")

	// Only origin advances the phantom template; backup stays at A.
	advanceRemote(t, origin, phantomTemplate, "B\n")
	// Stale phantom dirt matching origin's incoming bytes.
	writeFile(t, dir, phantomTemplate, "B\n")

	res, err := Pull(dir, []string{"origin", "backup"})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !res.AllPulled() {
		t.Fatalf("both remotes should pull clean, got %#v (origin=%q backup=%q)",
			res.RemoteResults, res.RemoteOutput["origin"], res.RemoteOutput["backup"])
	}
	if len(res.HealedTemplates) != 1 || res.HealedTemplates[0] != phantomTemplate {
		t.Errorf("phantom should heal exactly once across remotes, got %#v", res.HealedTemplates)
	}
	if got := readFile(t, dir, phantomTemplate); got != "B\n" {
		t.Errorf("merged template = %q, want %q", got, "B\n")
	}
}

// TestPull_NonMainBranch proves the heal/merge use currentBranch, not a hardcoded
// "main": the same phantom-heal path works on a differently-named branch.
func TestPull_NonMainBranch(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "trunk")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test User")
	writeFile(t, dir, phantomTemplate, "A\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "seed")
	bare := t.TempDir()
	gitRun(t, bare, "init", "--bare", "-b", "trunk")
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "origin", "trunk")

	// Remote advances the template on trunk.
	other := t.TempDir()
	gitRun(t, other, "clone", "-b", "trunk", bare, ".")
	gitRun(t, other, "config", "user.email", "other@example.com")
	gitRun(t, other, "config", "user.name", "Other")
	writeFile(t, other, phantomTemplate, "B\n")
	gitRun(t, other, "add", "-A")
	gitRun(t, other, "commit", "-m", "advance")
	gitRun(t, other, "push", "origin", "trunk")

	writeFile(t, dir, phantomTemplate, "B\n") // phantom dirt

	res, err := Pull(dir, []string{"origin"})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !res.AllPulled() {
		t.Fatalf("expected clean pull on trunk, got %#v", res.RemoteResults)
	}
	if len(res.HealedTemplates) != 1 {
		t.Errorf("expected heal on non-main branch, got %#v", res.HealedTemplates)
	}
}

// TestDirtyTemplateCommandPaths validates the glob: only Templates/commands/*.md
// at exactly one level deep, .md leaf, dirty in the working tree.
func TestDirtyTemplateCommandPaths(t *testing.T) {
	dir := initTestRepo(t)
	// Matches.
	writeFile(t, dir, "Templates/commands/match.md", "x")
	// Wrong extension, nested level, and wrong dir — all must be excluded.
	writeFile(t, dir, "Templates/commands/skip.txt", "x")
	writeFile(t, dir, "Templates/commands/sub/nested.md", "x")
	writeFile(t, dir, "Templates/skills/other.md", "x")

	got := dirtyTemplateCommandPaths(dir)
	if len(got) != 1 || got[0] != "Templates/commands/match.md" {
		t.Errorf("dirtyTemplateCommandPaths = %#v, want [Templates/commands/match.md]", got)
	}
}

// TestPullResult_Stranded covers the truth table of the PullResult methods —
// pure literal-driven, no git repo needed. Mirrors TestPushResult_Stranded.
func TestPullResult_Stranded(t *testing.T) {
	failed := fmt.Errorf("pull failed")
	cases := []struct {
		name                         string
		res                          PullResult
		wantAll, wantAny, wantStrand bool
	}{
		{
			name:       "all_remotes_fail",
			res:        PullResult{RemoteResults: map[string]error{"origin": failed, "backup": failed}},
			wantStrand: true,
		},
		{
			name:    "one_success",
			res:     PullResult{RemoteResults: map[string]error{"origin": failed, "backup": nil}},
			wantAny: true,
		},
		{
			name:    "all_success",
			res:     PullResult{RemoteResults: map[string]error{"origin": nil}},
			wantAll: true,
			wantAny: true,
		},
		{
			name: "no_remotes",
			res:  PullResult{RemoteResults: nil},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.res.AllPulled(); got != tc.wantAll {
				t.Errorf("AllPulled() = %v, want %v", got, tc.wantAll)
			}
			if got := tc.res.AnyPulled(); got != tc.wantAny {
				t.Errorf("AnyPulled() = %v, want %v", got, tc.wantAny)
			}
			if got := tc.res.Stranded(); got != tc.wantStrand {
				t.Errorf("Stranded() = %v, want %v", got, tc.wantStrand)
			}
		})
	}
}
