// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// split-purge-commits-its-own-result (U1). A purge on a vault that commits
// used to leave its whole result uncommitted, and tidy and the SessionEnd
// harvest then published half of it. These pin that the purge commits its
// own result in ONE commit, leaves nothing a publisher can take, and fails
// without losing anything.

const purgeLabel = "git@example.invalid:q/quantum-vault.git"

func pgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitenv.SafeGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// purgeVault is a git vault shaped like series A: a qa-like slug "alpha" with
// sessions, a transcript pair plus an ignored *.bak, tasks, memory, an
// untracked-and-ignored palace/<s>/.local and a realistically sized embed
// cache; a Projects-only slug "orch" (no palace/ tree, as orchestrator); a
// live project "keep"; and a change to keep that someone else has staged.
func purgeVault(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("git is required")
	}
	root := splitFixtureVault(t, "alpha", "keep")
	writeSplitFile(t, root, ".gitignore", "palace/.local/\npalace/*/.local/\n*.bak\n*.new\n.vp-locks/\n.vp-fs-probe-*\n")
	for rel, body := range map[string]string{
		"Projects/alpha/sessions/2026-09-01-aaaa-01.md":                     "session\n",
		"Projects/alpha/transcripts/2026-09-01-x.manifest.json":             "{}\n",
		"Projects/alpha/transcripts/2026-09-01-x.jsonl.zst":                 "zst",
		"Projects/alpha/transcripts/2026-09-01-x.manifest.json.d1e7238.bak": "backup\n",
		"Projects/alpha/tasks/t.md":                                         "task\n",
		"Projects/alpha/tasks/done/d.md":                                    "done\n",
		"Projects/alpha/memory/m.md":                                        "memory\n",
		"palace/alpha/.local/imported-sessions.jsonl":                       "{}\n",
		"Projects/orch/resume.md":                                           "# orch\n",
		"Projects/orch/memory/o.md":                                         "orch memory\n",
		// A tracked stamp from an older binary, as a live vault has: the
		// record write restamps it, so the purge commit carries it as M.
		"Audits/.surface": "surface = 6\n",
	} {
		writeSplitFile(t, root, rel, body)
	}
	for i := 0; i < 2000; i++ {
		writeSplitFile(t, root, fmt.Sprintf("palace/.local/embed-cache/alpha/v%04d.vec", i), "v")
	}
	writeSplitFile(t, root, "palace/.local/imports/alpha/imported-sessions.jsonl", "{}\n")
	pgit(t, root, "init", "-q", "-b", "main")
	pgit(t, root, "config", "user.name", "t")
	pgit(t, root, "config", "user.email", "t@example.invalid")
	pgit(t, root, "add", "-A")
	pgit(t, root, "commit", "-q", "-m", "seed")
	writeSplitFile(t, root, "Projects/keep/resume.md", "# someone else's edit\n")
	pgit(t, root, "add", "Projects/keep/resume.md")
	return root
}

// purgeReady runs plan, apply and verify, and returns purge's params.
func purgeReady(t *testing.T, root string) vaultSplitParams {
	t.Helper()
	dest := splitDest(t)
	p := splitPlannedParams(t, root, dest, "alpha", "orch")
	p.Action = "apply"
	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	p.Action = "purge"
	p.DepartureTo = purgeLabel
	return p
}

// vaultState is everything a failed purge must leave exactly as it was.
type vaultState struct {
	head, staged, porcelain string
	files                   map[string]string
}

func stateOf(t *testing.T, root string) vaultState {
	t.Helper()
	files := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" || rel == ".vp-locks" {
				return filepath.SkipDir
			}
			return nil
		}
		b, _ := os.ReadFile(p)
		files[rel] = string(b)
		return nil
	})
	return vaultState{
		head:      pgit(t, root, "rev-parse", "HEAD"),
		staged:    pgit(t, root, "diff", "--cached", "--name-status"),
		porcelain: pgit(t, root, "status", "--porcelain=v1", "-uall"),
		files:     files,
	}
}

func assertUnchanged(t *testing.T, before, after vaultState) {
	t.Helper()
	if before.head != after.head {
		t.Errorf("HEAD moved: %s -> %s", before.head, after.head)
	}
	if before.staged != after.staged {
		t.Errorf("index changed:\nbefore %q\nafter  %q", before.staged, after.staged)
	}
	var diffs []string
	for k, v := range before.files {
		if w, ok := after.files[k]; !ok {
			diffs = append(diffs, "gone: "+k)
		} else if v != w {
			diffs = append(diffs, "changed: "+k)
		}
	}
	for k := range after.files {
		if _, ok := before.files[k]; !ok {
			diffs = append(diffs, "new: "+k)
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 0 {
		t.Errorf("working tree changed (%d): %s", len(diffs), strings.Join(firstNS(diffs, 10), "; "))
	}
}

func firstNS(ss []string, n int) []string {
	if len(ss) > n {
		return ss[:n]
	}
	return ss
}

// Row 1: the acceptance. After purge nothing of it is uncommitted — memory
// included — and exactly one commit holds every tracked deletion and both
// records.
func TestPurgeCommitsItsWholeResultInOneCommit(t *testing.T) {
	root := purgeVault(t)
	p := purgeReady(t, root)
	before := pgit(t, root, "rev-list", "--count", "HEAD")
	tracked := pgit(t, root, "ls-files", "--", "Projects/alpha", "palace/alpha", "Projects/orch")

	res, err := callSplit(t, root, p)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if got := pgit(t, root, "status", "--porcelain=v1", "-uall"); got != "M  Projects/keep/resume.md" {
		t.Errorf("after purge the only dirt must be the other party's staged change; porcelain:\n%s", got)
	}
	if after := pgit(t, root, "rev-list", "--count", "HEAD"); after != fmt.Sprint(atoi(t, before)+1) {
		t.Errorf("commit count %s -> %s, want exactly one purge commit", before, after)
	}
	// The commit is exactly: D for every tracked path under the trees, A for
	// each record.
	var want []string
	for _, rel := range strings.Split(tracked, "\n") {
		want = append(want, "D\t"+rel)
	}
	want = append(want, "A\t"+departure.RelPath("alpha"), "A\t"+departure.RelPath("orch"), "M\tAudits/.surface")
	sort.Strings(want)
	got := strings.Split(pgit(t, root, "show", "--name-status", "--format=", "HEAD"), "\n")
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("purge commit name-status:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	msg := pgit(t, root, "log", "-1", "--format=%B")
	for _, w := range []string{"vault split: purge alpha, orch", purgeLabel, p.ManifestSHA256, departure.RelPath("alpha")} {
		if !strings.Contains(msg, w) {
			t.Errorf("commit message lacks %q:\n%s", w, msg)
		}
	}
	if strings.Contains(msg, root) || strings.Contains(msg, p.Destination) {
		t.Errorf("commit message names a host path:\n%s", msg)
	}
	if pgit(t, root, "diff", "--cached", "--name-only") != "Projects/keep/resume.md" {
		t.Error("the other party's staged change must still be staged, and not in HEAD")
	}
	if sha, _ := res["commit_sha"].(string); sha == "" {
		t.Errorf("commit_sha missing: %v", res)
	}
	if left, _ := res["cleanup_left"].([]any); len(left) != 0 {
		t.Errorf("cleanup_left = %v, want nothing left", left)
	}
	for _, gone := range []string{"Projects/alpha", "palace/alpha", "Projects/orch", "palace/.local/embed-cache/alpha", "palace/.local/imports/alpha"} {
		if _, err := os.Lstat(filepath.Join(root, gone)); err == nil {
			t.Errorf("%s survived the purge", gone)
		}
	}
}

func atoi(t *testing.T, s string) int {
	var n int
	if _, err := fmt.Sscan(s, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Row 2: the publishers find nothing of the purge.
func TestPurgeLeavesNothingForTidyOrHarvest(t *testing.T) {
	root := purgeVault(t)
	p := purgeReady(t, root)
	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("purge: %v", err)
	}
	scan, err := storage.TidyScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Swept) != 0 || len(scan.Deferred) != 0 {
		t.Errorf("tidy would sweep %v, defer %v; want nothing", scan.Swept, scan.Deferred)
	}
	for _, r := range scan.Reported {
		if strings.HasPrefix(r, "Projects/alpha") || strings.HasPrefix(r, "Projects/orch") || strings.HasPrefix(r, departure.Dir) {
			t.Errorf("tidy reports purge residue %s", r)
		}
	}
	for _, s := range []string{"alpha", "orch"} {
		res, _, err := storage.CommitAndPushPathsWithDowngrade(root, "harvest", []string{"Projects/" + s + "/memory"}, false)
		if err != nil || (res != nil && res.CommitSHA != "") {
			t.Errorf("a harvest of %s's memory committed %v (err %v); want nothing to commit", s, res, err)
		}
	}
}

// Row 3: preflight refuses and changes nothing.
func TestPurgePreflightRefusesAndChangesNothing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, root string) // before plan
		late  func(t *testing.T, root string) // after apply, before purge
		want  string
	}{
		{"git-disabled", nil, func(t *testing.T, root string) { hostGitConfig(t, "git_enabled = false\n") }, "git_enabled"},
		{"no-identity", nil, func(t *testing.T, root string) {
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			pgit(t, root, "config", "--unset", "user.email")
			pgit(t, root, "config", "--unset", "user.name")
			pgit(t, root, "config", "user.useConfigOnly", "true")
		}, "no git identity"},
		{"merge-in-progress", nil, func(t *testing.T, root string) {
			writeSplitFile(t, root, ".git/MERGE_HEAD", pgit(t, root, "rev-parse", "HEAD")+"\n")
		}, "in progress"},
		{"tracked-missing", func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "Projects/alpha/tasks/t.md")); err != nil {
				t.Fatal(err)
			}
		}, nil, "missing from the working tree"},
		{"tracked-modified", func(t *testing.T, root string) {
			writeSplitFile(t, root, "Projects/alpha/tasks/t.md", "edited, uncommitted\n")
		}, nil, "differ from HEAD"},
		{"pending-record", nil, func(t *testing.T, root string) {
			writeDepartureRecord(t, root, departure.Record{Slug: "ghost", Kind: departure.MovedToVault})
		}, "did not finish"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := purgeVault(t)
			if c.setup != nil {
				c.setup(t, root)
			}
			p := purgeReady(t, root)
			if c.late != nil {
				c.late(t, root)
			}
			before := stateOf(t, root)
			_, err := callSplit(t, root, p)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("purge = %v, want a refusal containing %q", err, c.want)
			}
			assertUnchanged(t, before, stateOf(t, root))
		})
	}
}

// Row 4: every failure in steps 1–2 rolls back losslessly, and purge can then
// simply be re-run with the same digest.
func TestPurgeRollbackIsLossless(t *testing.T) {
	t.Run("pre-commit-hook-refuses", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("needs a POSIX hook script")
		}
		root := purgeVault(t)
		p := purgeReady(t, root)
		hook := filepath.Join(root, ".git/hooks/pre-commit")
		writeSplitFile(t, root, ".git/hooks/pre-commit", "#!/bin/sh\necho refused >&2\nexit 1\n")
		if err := os.Chmod(hook, 0o755); err != nil {
			t.Fatal(err)
		}
		before := stateOf(t, root)
		if _, err := callSplit(t, root, p); err == nil || !strings.Contains(err.Error(), "rolled back") {
			t.Fatalf("purge = %v, want a rolled-back refusal", err)
		}
		assertUnchanged(t, before, stateOf(t, root))
		if err := os.Remove(hook); err != nil {
			t.Fatal(err)
		}
		if _, err := callSplit(t, root, p); err != nil {
			t.Fatalf("re-run with the same digest after a rollback: %v", err)
		}
	})
	t.Run("git-rm-refuses-a-concurrent-edit", func(t *testing.T) {
		root := purgeVault(t)
		p := purgeReady(t, root)
		var before vaultState
		splitPurgeAfterRecords = func() {
			writeSplitFile(t, root, "Projects/alpha/tasks/t.md", "a concurrent edit\n")
			before = stateOf(t, root)
		}
		t.Cleanup(func() { splitPurgeAfterRecords = nil })
		if _, err := callSplit(t, root, p); err == nil || !strings.Contains(err.Error(), "nothing was removed") {
			t.Fatalf("purge = %v, want git rm's refusal with nothing removed", err)
		}
		after := stateOf(t, root)
		if got := after.files["Projects/alpha/tasks/t.md"]; got != "a concurrent edit\n" {
			t.Errorf("the concurrent edit was lost: %q", got)
		}
		// The records are gone again and the stamp is as it was before the
		// purge; everything else is as the edit left it.
		delete(before.files, departure.RelPath("alpha"))
		delete(before.files, departure.RelPath("orch"))
		before.files["Audits/.surface"] = "surface = 6\n"
		assertUnchanged(t, before, after)
	})
	t.Run("record-write-fails", func(t *testing.T) {
		root := purgeVault(t)
		p := purgeReady(t, root)
		// orch's record path is a directory: its write fails after alpha's.
		if err := os.MkdirAll(filepath.Join(root, departure.RelPath("orch")), 0o755); err != nil {
			t.Fatal(err)
		}
		before := stateOf(t, root)
		if _, err := callSplit(t, root, p); err == nil {
			t.Fatal("purge must refuse when a record cannot be written")
		}
		assertUnchanged(t, before, stateOf(t, root))
	})
}

// Row 5: a cleanup failure after the commit is reported, never fatal, and a
// file nobody collected is never deleted — and is named a resurrection risk.
func TestPurgeCleanupReportsWhatItLeaves(t *testing.T) {
	root := purgeVault(t)
	p := purgeReady(t, root)
	splitPurgeBeforeCleanup = func() {
		writeSplitFile(t, root, "Projects/alpha/sessions/2026-09-27-late-01.md", "written during the purge\n")
	}
	t.Cleanup(func() { splitPurgeBeforeCleanup = nil })
	res, err := callSplit(t, root, p)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	late := filepath.Join(root, "Projects/alpha/sessions/2026-09-27-late-01.md")
	if _, err := os.Stat(late); err != nil {
		t.Errorf("a file written after the collect was deleted: %v", err)
	}
	left, _ := res["cleanup_left"].([]any)
	found := false
	for _, l := range left {
		m, _ := l.(map[string]any)
		if m["path"] == "Projects/alpha/sessions/2026-09-27-late-01.md" && m["class"] == "untracked" {
			found = true
		}
	}
	if !found {
		t.Errorf("cleanup_left = %v, want the late file as untracked", left)
	}
	notes, _ := res["notes"].([]any)
	if !strings.Contains(fmt.Sprint(notes), "RESURRECTION RISK") {
		t.Errorf("notes must flag the untracked leftover as a resurrection risk: %v", notes)
	}
	if sha, _ := res["commit_sha"].(string); sha == "" {
		t.Error("the purge commit must stand despite the leftover")
	}
}

// M1: a pull or merge landing between preflight and the lock moves HEAD. The
// purge refuses and nothing is lost — in particular not the merged-in file,
// which no manifest row covers.
func TestPurgeRefusesWhenHEADMovesBeforeTheLock(t *testing.T) {
	root := purgeVault(t)
	p := purgeReady(t, root)
	splitPurgeAfterRecords = func() {
		writeSplitFile(t, root, "Projects/alpha/sessions/2026-09-27-merged-01.md", "merged in\n")
		pgit(t, root, "add", "Projects/alpha/sessions/2026-09-27-merged-01.md")
		pgit(t, root, "commit", "-q", "-m", "merged from another host", "--", "Projects/alpha/sessions/2026-09-27-merged-01.md")
	}
	t.Cleanup(func() { splitPurgeAfterRecords = nil })
	_, err := callSplit(t, root, p)
	if err == nil || !strings.Contains(err.Error(), "HEAD moved") || !strings.Contains(err.Error(), "Re-run") {
		t.Fatalf("purge = %v, want the HEAD-moved refusal", err)
	}
	if got := pgit(t, root, "ls-files", "--", "Projects/alpha/sessions/2026-09-27-merged-01.md"); got == "" {
		t.Error("the merged-in file was removed")
	}
	if out := pgit(t, root, "status", "--porcelain=v1", "-uall"); out != "M  Projects/keep/resume.md" {
		t.Errorf("a refused purge left:\n%s", out)
	}
}

// M2: a slug that was re-created and departs again overwrites its committed
// record in the purge commit, and a rollback restores the old record.
func TestPurgeReDepartureOverwritesACommittedRecord(t *testing.T) {
	seed := func(t *testing.T) (string, string) {
		root := purgeVault(t)
		writeDepartureRecord(t, root, departure.Record{Slug: "alpha", Kind: departure.Renamed, To: "alpha-old", Date: "2026-01-01"})
		pgit(t, root, "add", departure.RelPath("alpha"))
		pgit(t, root, "commit", "-q", "-m", "an earlier departure", "--", departure.RelPath("alpha"))
		b, _ := os.ReadFile(filepath.Join(root, departure.RelPath("alpha")))
		return root, string(b)
	}
	t.Run("overwritten-in-the-purge-commit", func(t *testing.T) {
		root, _ := seed(t)
		p := purgeReady(t, root)
		if _, err := callSplit(t, root, p); err != nil {
			t.Fatalf("a re-departure must purge: %v", err)
		}
		if st := pgit(t, root, "show", "--name-status", "--format=", "HEAD", "--", departure.RelPath("alpha")); st != "M\t"+departure.RelPath("alpha") {
			t.Errorf("the record must be modified in the purge commit, got %q", st)
		}
		if rec, ok := departure.Read(root, "alpha"); !ok || rec.Kind != departure.MovedToVault {
			t.Errorf("record = %+v, want moved-to-vault", rec)
		}
	})
	t.Run("rollback-restores-the-old-record", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("needs a POSIX hook script")
		}
		root, old := seed(t)
		p := purgeReady(t, root)
		writeSplitFile(t, root, ".git/hooks/pre-commit", "#!/bin/sh\nexit 1\n")
		if err := os.Chmod(filepath.Join(root, ".git/hooks/pre-commit"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := callSplit(t, root, p); err == nil {
			t.Fatal("test premise: the hook refuses the commit")
		}
		b, err := os.ReadFile(filepath.Join(root, departure.RelPath("alpha")))
		if err != nil || string(b) != old {
			t.Errorf("the committed record must be restored byte-for-byte: %q (%v)", b, err)
		}
		if out := pgit(t, root, "status", "--porcelain=v1", "-uall"); out != "M  Projects/keep/resume.md" {
			t.Errorf("a rolled-back re-departure left:\n%s", out)
		}
	})
}

// Row 7e: a crash between the records and the commit is guarded. A copy of the
// vault taken at that moment — what a killed purge leaves — refuses the
// harvest-shaped commit that would otherwise publish the memory deletions.
func TestPurgeInterruptedAfterTheRecordsIsGuarded(t *testing.T) {
	root := purgeVault(t)
	p := purgeReady(t, root)
	crash := filepath.Join(t.TempDir(), "crashed")
	splitPurgeAfterRecords = func() {
		if out, err := exec.Command("cp", "-a", root, crash).CombinedOutput(); err != nil {
			t.Fatalf("snapshot: %v %s", err, out)
		}
	}
	t.Cleanup(func() { splitPurgeAfterRecords = nil })
	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(crash, "Projects/alpha/memory")); err != nil {
		t.Fatal(err)
	}
	_, _, err := storage.CommitAndPushPathsWithDowngrade(crash, "harvest", []string{"Projects/alpha/memory"}, false)
	if !errors.Is(err, storage.ErrPendingDeparture) {
		t.Fatalf("a harvest after a crashed purge = %v, want the pending-departure refusal", err)
	}
}

// Row 8: a vault that is not its own git repository keeps the plain removal.
func TestPurgeOnANonGitVaultCommitsNothing(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := splitDest(t)
	p := splitPlannedParams(t, root, dest, "alpha")
	p.Action = "apply"
	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	p.Action = "purge"
	res, err := callSplit(t, root, p)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if sha, _ := res["commit_sha"].(string); sha != "" {
		t.Errorf("commit_sha = %q on a non-git vault", sha)
	}
	if notes := fmt.Sprint(res["notes"]); !strings.Contains(notes, "Nothing was committed") {
		t.Errorf("notes must say nothing was committed: %s", notes)
	}
	if _, err := os.Lstat(filepath.Join(root, "Projects/alpha")); err == nil {
		t.Error("Projects/alpha survived")
	}
}

// D1: an untracked, non-ignored row under a purged tree (content, so a
// manifest row, copied) is still on disk when the commit lands. The
// post-commit check must look at TRACKED state only; cleanup then removes the
// row and tidy finds nothing to sweep back.
func TestPurgeWithAnUntrackedContentRowSucceedsAndCleansIt(t *testing.T) {
	root := purgeVault(t)
	uncommitted := "Projects/alpha/sessions/2026-09-27-uncommitted-01.md"
	writeSplitFile(t, root, uncommitted, "captured, never committed\n")
	p := purgeReady(t, root)
	res, err := callSplit(t, root, p)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if left, _ := res["cleanup_left"].([]any); len(left) != 0 {
		t.Errorf("cleanup_left = %v, want the row removed", left)
	}
	if _, err := os.Stat(filepath.Join(root, uncommitted)); err == nil {
		t.Error("cleanup must remove the untracked content row it collected")
	}
	scan, err := storage.TidyScan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Swept) != 0 {
		t.Errorf("tidy would sweep %v back in", scan.Swept)
	}
	if got := pgit(t, root, "status", "--porcelain=v1", "-uall"); got != "M  Projects/keep/resume.md" {
		t.Errorf("porcelain after purge:\n%s", got)
	}
}
