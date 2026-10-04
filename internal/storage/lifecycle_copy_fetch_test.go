// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// lazyFetchTrace is what GIT_TRACE prints when git fetches a promisor object
// on its own: `git -c fetch.negotiationAlgorithm=noop fetch <url> ...`, one
// subprocess per object it was asked to read. The copy's own explicit fetch is
// traced as `built-in: git fetch`, without the -c, so it is not counted.
const lazyFetchTrace = "fetch.negotiationAlgorithm=noop fetch"

// newBulkCopyFix is newCopyFix with n more content files under Projects/p, on
// a source remote that honours --filter and does NOT set
// uploadpack.allowAnySHA1InWant: protocol v2 allows a want for any object, and
// a real git host is configured that way. It returns the fixture and the size
// of every content file of p, by vault-relative path.
//
// The source has the HISTORY a real vault has, and both halves matter to the
// completeness check that follows the blob fetch:
//   - every one of the n files has an earlier version, in an earlier commit. A
//     check that walks history reports those n older blobs as missing after a
//     complete fetch, and would refuse every real copy;
//   - the tip commit touches nothing under p. A pathspec check with --no-walk
//     drops such a commit and lists nothing, so it would pass a snapshot that
//     holds no blob at all.
func newBulkCopyFix(t *testing.T, n int) (*copyFix, map[string]int64) {
	t.Helper()
	f := newCopyFix(t)
	gitRun(t, f.SrcBare, "config", "--unset", "uploadpack.allowAnySHA1InWant")
	for i := range n {
		writeFile(t, f.Src, fmt.Sprintf("Projects/p/sessions/bulk-%02d.md", i), fmt.Sprintf("the first version of file %d\n", i))
	}
	gitRun(t, f.Src, "add", "-A")
	gitRun(t, f.Src, "commit", "-q", "-m", "a project with many files")
	for i := range n {
		writeFile(t, f.Src, fmt.Sprintf("Projects/p/sessions/bulk-%02d.md", i), strings.Repeat("x", i+1)+"\n")
	}
	gitRun(t, f.Src, "add", "-A")
	gitRun(t, f.Src, "commit", "-q", "-m", "a second version of every file")
	writeFile(t, f.Src, "Projects/other/later.md", "a tip commit that touches nothing under p\n")
	gitRun(t, f.Src, "add", "-A")
	gitRun(t, f.Src, "commit", "-q", "-m", "an unrelated tip commit")
	gitRun(t, f.Src, "push", "-q", "origin", "main")
	if out := gitRun(t, f.Src, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD", "--", "Projects/p", "palace/p"); out != "" {
		t.Fatalf("fixture: the tip commit touches p: %s", out)
	}

	sizes := map[string]int64{}
	for _, tree := range ProjectTrees("p") {
		_ = filepath.WalkDir(filepath.Join(f.Src, tree), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(f.Src, p)
			rel = filepath.ToSlash(rel)
			if ClassifyProjectPath(rel) != ProjectContent {
				return nil
			}
			st, serr := os.Stat(p)
			if serr != nil {
				t.Fatal(serr)
			}
			sizes[rel] = st.Size()
			return nil
		})
	}
	return f, sizes
}

// missingUnder counts the objects git reports missing under tip's trees, with
// plain git and no production code: `rev-list --objects --no-walk
// --missing=print <tip>:<tree>`, which neither walks history nor fetches.
func missingUnder(t *testing.T, repo, tip string, trees ...string) int {
	t.Helper()
	args := []string{"rev-list", "--objects", "--no-walk", "--missing=print"}
	for _, tree := range trees {
		args = append(args, tip+":"+tree)
	}
	n := 0
	for line := range strings.SplitSeq(gitRun(t, repo, args...), "\n") {
		if strings.HasPrefix(line, "?") {
			n++
		}
	}
	return n
}

// The copy of a project with many files makes no per-file fetch: a blobless
// snapshot holds no file contents, and every git call that reads one (the -l
// listing, the checkout) would otherwise fetch it in a subprocess of its own.
func TestCopy_FetchesTheFootprintInOneRequest(t *testing.T) {
	const n = 40
	f, sizes := newBulkCopyFix(t, n)

	// The fixture must really be blobless, or the test passes on a remote that
	// ignores the filter and proves nothing.
	probe, err := newRemoteSnapshot(fileURL(f.SrcBare), "main")
	if err != nil {
		t.Fatal(err)
	}
	missing := missingUnder(t, probe.Dir, probe.Tip, ProjectTrees("p")...)
	_ = probe.Close()
	if missing < n {
		t.Fatalf("fixture: a fresh snapshot is missing %d objects under p, want at least %d: the source remote did not honour --filter=blob:none", missing, n)
	}

	trace := filepath.Join(t.TempDir(), "git.trace")
	packets := filepath.Join(t.TempDir(), "git.packets")
	t.Setenv("GIT_TRACE", trace)
	t.Setenv("GIT_TRACE_PACKET", packets)
	plan, err := PlanCopy(f.req("p"))
	if err != nil {
		t.Fatalf("PlanCopy: %v", err)
	}
	if len(plan.Files) != len(sizes) {
		t.Fatalf("the plan lists %d files, want %d", len(plan.Files), len(sizes))
	}
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("GIT_TRACE wrote no file, so a count of zero would mean nothing: %v", err)
	}
	if !strings.Contains(string(raw), "built-in: git ls-tree -r -l") {
		t.Fatalf("the trace does not hold the footprint listing, so a count of zero would mean nothing:\n%s", firstLines(string(raw), 20))
	}
	if got := strings.Count(string(raw), lazyFetchTrace); got != 0 {
		t.Errorf("the copy's plan made git fetch objects lazily: %d trace line(s) of `git -c %s`, want 0", got, lazyFetchTrace)
	}

	// The explicit fetch is the request git makes for a lazy fetch: wants only.
	// Without -c fetch.negotiationAlgorithm=noop git also sends `have` lines,
	// naming commits whose trees reference every wanted blob.
	wire, err := os.ReadFile(packets)
	if err != nil {
		t.Fatalf("GIT_TRACE_PACKET wrote no file: %v", err)
	}
	if wants := strings.Count(string(wire), "fetch> want "); wants < n {
		t.Fatalf("the packet trace holds %d `want` line(s), want at least %d: the blob fetch is not in it", wants, n)
	}
	if haves := strings.Count(string(wire), "fetch> have "); haves != 0 {
		t.Errorf("the copy's fetches sent %d `have` line(s), want 0", haves)
	}

	var total int64
	for _, cf := range plan.Files {
		want, ok := sizes[cf.Path]
		if !ok || cf.Size != want {
			t.Errorf("%s: size %d in the plan, %d in the source (known: %v)", cf.Path, cf.Size, want, ok)
		}
		total += want
	}
	if plan.Bytes != total {
		t.Errorf("plan.Bytes = %d, want %d", plan.Bytes, total)
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// bloblessSnapshot is a fresh snapshot of f's source that has NOT fetched any
// blob, removed when the test ends.
func bloblessSnapshot(t *testing.T, f *copyFix) *remoteSnapshot {
	t.Helper()
	s, err := newRemoteSnapshot(fileURL(f.SrcBare), "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// The completeness check that follows the blob fetch: it counts what the tip's
// trees lack and nothing else. The two forms it must not be are both pinned
// here. A pathspec form walks history and reports the older version of every
// file, which nobody fetched; a pathspec form with --no-walk lists nothing at
// all when the tip commit does not touch the project. The fixture's history
// (newBulkCopyFix) turns each of them red.
func TestRemoteSnapshot_CompletenessCheckCountsTheTipOnly(t *testing.T) {
	const n = 40
	f, _ := newBulkCopyFix(t, n)
	// The check passes the manifest as a blob argument, and git reports a
	// missing blob given that way only from 2.45.0, the release the variable
	// arrived in: on an older git the exact count below is not known.
	requireNoLazyFetchSupport(t, f)
	paths := append(ProjectTrees("p"), vaultManifestRel, "Audits/departures/p.json")

	s := bloblessSnapshot(t, f)
	before, err := s.missingObjects(s.Tip, paths)
	if err != nil {
		t.Fatal(err)
	}
	// 44 content files and .surface under the two trees, and the manifest.
	if want := n + 6; before != want {
		t.Fatalf("before the fetch: %d object(s) missing, want %d", before, want)
	}
	err = s.requireComplete(s.Tip, paths)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("lacks %d object(s)", before)) {
		t.Fatalf("an incomplete snapshot must be refused with the count %d; got %v", before, err)
	}

	if err := s.fetchBlobs(s.Tip, paths); err != nil {
		t.Fatalf("fetchBlobs: %v", err)
	}
	after, err := s.missingObjects(s.Tip, paths)
	if err != nil || after != 0 {
		t.Fatalf("after the fetch: %d object(s) missing (err %v), want 0: the older versions in history are not the tip's", after, err)
	}
	// The fetch took the tip's blobs only: the first versions are still absent.
	old := gitRun(t, f.Src, "rev-parse", "HEAD~2:Projects/p/sessions/bulk-00.md")
	if old == gitRun(t, f.Src, "rev-parse", "HEAD:Projects/p/sessions/bulk-00.md") {
		t.Fatal("fixture: the file has no earlier version")
	}
	if out := gitRun(t, s.Dir, "rev-list", "--objects", "--no-walk", "--missing=print", old); out != "?"+old {
		t.Errorf("the first version of a file should still be missing from the snapshot; rev-list printed %q", out)
	}
}

// requireNoLazyFetchSupport skips the test on a git that ignores
// GIT_NO_LAZY_FETCH, which is a git older than 2.45.0 (or an unpatched older
// series): there a read of a missing blob fetches it and succeeds, so the
// guard under test does not exist, and `rev-list --missing=print` does not yet
// report a missing blob given as a starting point.
func requireNoLazyFetchSupport(t *testing.T, f *copyFix) {
	t.Helper()
	probe := bloblessSnapshot(t, f)
	blob := gitRun(t, f.Src, "rev-parse", "HEAD:Projects/p/resume.md")
	cmd := exec.Command("git", "-C", probe.Dir, "cat-file", "-e", blob)
	// The variable is spelled out, not taken from the code under test: the
	// probe asks what git does, and must not follow a change to the constant.
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	if cmd.Run() == nil {
		t.Skip("this git ignores GIT_NO_LAZY_FETCH")
	}
}

// A blob the snapshot lacks is an error that names the path, never a fetch per
// file: the listing and the checkout are called directly on a snapshot that
// skipped the blob fetch.
func TestRemoteSnapshot_MissedBlobIsAnErrorNotALazyFetch(t *testing.T) {
	f, _ := newBulkCopyFix(t, 5)
	requireNoLazyFetchSupport(t, f)
	s := bloblessSnapshot(t, f)
	trace := filepath.Join(t.TempDir(), "git.trace")
	t.Setenv("GIT_TRACE", trace)

	if _, _, err := footprintFiles(s, s.Tip, "p"); err == nil || !strings.Contains(err.Error(), "Projects/p/") || !strings.Contains(err.Error(), "is not in the snapshot") {
		t.Errorf("footprintFiles on a blobless snapshot must name a path that is not in the snapshot; got %v", err)
	}
	if _, err := s.checkoutFootprint(s.Tip, []string{"p"}); err == nil || !strings.Contains(err.Error(), "Projects/p/") {
		t.Errorf("checkoutFootprint on a blobless snapshot must fail naming a path; got %v", err)
	}
	// The readers' `show` too, with the remote still reachable: without the
	// variable git would fetch the manifest and the read would succeed.
	if _, found, err := s.readFile(s.Tip, vaultManifestRel); !found || err == nil || !strings.Contains(err.Error(), vaultManifestRel) {
		t.Errorf("readFile of a manifest the snapshot lacks must be an error naming it; found=%v err=%v", found, err)
	}

	raw, err := os.ReadFile(trace)
	if err != nil || !strings.Contains(string(raw), "built-in: git ls-tree -r -l") {
		t.Fatalf("the trace does not hold the listing (err %v), so a count of zero would mean nothing", err)
	}
	if got := strings.Count(string(raw), lazyFetchTrace); got != 0 {
		t.Errorf("%d lazy-fetch trace line(s), want 0", got)
	}
}

// snapshotFormat tells a source with no manifest from one whose manifest
// cannot be read. The first is refused as not a vault; the second is an error
// that names the file, and is never that refusal.
func TestSnapshotFormat_AbsentIsNotAVaultUnreadableIsAnError(t *testing.T) {
	f, _ := newBulkCopyFix(t, 1)
	s := bloblessSnapshot(t, f)
	loseTheRemote(t, f)

	// The tree lists the manifest, its blob was never fetched, and the remote
	// is gone: the read fails on any git, with or without GIT_NO_LAZY_FETCH.
	_, _, err := snapshotFormat(s, s.Tip)
	if err == nil || errors.Is(err, ErrCopyRefused) || !strings.Contains(err.Error(), vaultManifestRel) || strings.Contains(err.Error(), "not a vault") {
		t.Errorf("an unreadable manifest must be an error naming %s, not a refusal; got %v", vaultManifestRel, err)
	}

	// A source that is not a vault: the first commit of the fixture's history
	// has no manifest at all.
	root := gitRun(t, f.Src, "rev-list", "--max-parents=0", "HEAD")
	if gitRun(t, s.Dir, "ls-tree", root, "--", vaultManifestRel) != "" {
		t.Fatalf("fixture: the root commit %s holds a manifest", root)
	}
	if _, _, err := snapshotFormat(s, root); !errors.Is(err, ErrCopyRefused) || !strings.Contains(err.Error(), "not a vault") {
		t.Errorf("a source with no manifest must be refused as not a vault; got %v", err)
	}
}

// The departure-record read tells "no record" from a record that cannot be
// read: the second must never pass for the first, or the copy would skip its
// "project departed the source" refusal.
func TestCopySourceRefusals_UnreadableDepartureRecordIsAnError(t *testing.T) {
	f, _ := newBulkCopyFix(t, 1)
	f.srcPush(t, "Audits/departures/orch.json", `{"format":"vp-departure/1","slug":"orch","kind":"moved-to-vault","to":"git@example.com:x/y.git","date":"2026-09-30"}`+"\n")
	s := bloblessSnapshot(t, f)
	// The listing in copySourceRefusals needs orch's own blobs; the record's
	// blob is left out, and then the remote is gone, so the record cannot be
	// read on any git, with or without GIT_NO_LAZY_FETCH.
	if err := s.fetchBlobs(s.Tip, ProjectTrees("orch")); err != nil {
		t.Fatal(err)
	}
	loseTheRemote(t, f)

	// No record for p in the tree: not found, and no error.
	if _, found, err := s.readFile(s.Tip, "Audits/departures/p.json"); found || err != nil {
		t.Errorf("no record in the tree: found=%v err=%v, want false and nil", found, err)
	}
	// A record for orch in the tree, its blob never fetched.
	_, found, err := s.readFile(s.Tip, "Audits/departures/orch.json")
	if !found || err == nil || !strings.Contains(err.Error(), "Audits/departures/orch.json") {
		t.Errorf("an unreadable record: found=%v err=%v, want found and an error naming it", found, err)
	}
	// Through the caller.
	fp, n, err := footprintHash(s.Dir, s.Tip, "orch")
	if err != nil {
		t.Fatal(err)
	}
	_, _, refusals, err := copySourceRefusals(s, s.Tip, []string{"orch"}, map[string]string{"orch": fp}, map[string]int{"orch": n})
	if err == nil || !strings.Contains(err.Error(), "Audits/departures/orch.json") {
		t.Errorf("copySourceRefusals must fail on a record it cannot read; got refusals %v, err %v", refusals, err)
	}
}

// loseTheRemote moves f's source remote away, as a network failure would: a
// snapshot made earlier keeps its commits and trees and can fetch nothing.
func loseTheRemote(t *testing.T, f *copyFix) {
	t.Helper()
	if err := os.Rename(f.SrcBare, f.SrcBare+".away"); err != nil {
		t.Fatal(err)
	}
}

// Absent means the tree listing succeeded and printed nothing. A listing that
// fails is an error, never "absent".
func TestRemoteSnapshot_ReadFileReturnsAFailedListingAsAnError(t *testing.T) {
	f, _ := newBulkCopyFix(t, 1)
	s := bloblessSnapshot(t, f)
	if _, found, err := s.readFile(s.Tip, "no/such/file.toml"); found || err != nil {
		t.Errorf("an absent path: found=%v err=%v, want false and nil", found, err)
	}
	const noSuchCommit = "0123456789abcdef0123456789abcdef01234567"
	if _, found, err := s.readFile(noSuchCommit, vaultManifestRel); err == nil {
		t.Errorf("a listing that fails must be an error, not absent; found=%v", found)
	}
	if _, _, err := snapshotFormat(s, noSuchCommit); err == nil || errors.Is(err, ErrCopyRefused) {
		t.Errorf("snapshotFormat over a failed listing must be an error, not the not-a-vault refusal; got %v", err)
	}
}

// recordPushLimits makes every publish push a real one and records the limit
// it was given, per marker command's push.
func recordPushLimits(t *testing.T) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	old := lifecyclePush
	lifecyclePush = func(vaultPath, remote, sha, branch string, limit time.Duration) error {
		got = append(got, limit)
		return old(vaultPath, remote, sha, branch, limit)
	}
	t.Cleanup(func() { lifecyclePush = old })
	return &got
}

// Who gets which limit on the publish push. A copy's commit carries a whole
// project and gets the bulk limit, on its first run and on the redo that
// finishes an interrupted one. A delete pushes deletions and records, and an
// init three small files: they keep the short limit, and the delete holds the
// lock of the vault every session on the host writes.
func TestLifecyclePush_TheCopyGetsTheBulkLimitDeleteAndInitTheShortOne(t *testing.T) {
	if lifecycleBulkTimeout <= lifecycleNetTimeout {
		t.Fatalf("lifecycleBulkTimeout (%s) is not above lifecycleNetTimeout (%s)", lifecycleBulkTimeout, lifecycleNetTimeout)
	}
	f := newCopyFix(t)
	got := recordPushLimits(t)
	only := func(what string, want time.Duration) {
		t.Helper()
		if len(*got) == 0 {
			t.Fatalf("%s: no push was made", what)
		}
		for _, l := range *got {
			if l != want {
				t.Errorf("%s: a push was given %s, want %s", what, l, want)
			}
		}
		*got = nil
	}

	// A copy that dies between its commit and its publish pushes nothing.
	withCopyHook(t, func(stage string, _ int, _ string) error {
		if stage == "publish" {
			return errCopySimulatedKill
		}
		return nil
	})
	if _, err := ApplyCopy(f.req("orch")); !errors.Is(err, errCopySimulatedKill) {
		t.Fatalf("err = %v", err)
	}
	if len(*got) != 0 {
		t.Fatalf("the killed copy pushed: %v", *got)
	}
	copyTestHook = nil
	// Its redo publishes, under the copy's limit.
	if res, err := ApplyCopy(f.req("orch")); err != nil || res.Redo != RedoPublished {
		t.Fatalf("redo: %+v, %v", res, err)
	}
	only("the copy's redo", lifecycleBulkTimeout)

	// A copy that runs straight through.
	if _, err := ApplyCopy(f.req("p")); err != nil {
		t.Fatal(err)
	}
	only("the copy", lifecycleBulkTimeout)

	// The delete of the copied project from the source vault.
	if _, err := ApplyDelete(f.Src, DeleteRequest{Projects: []string{"p"}, MovedTo: fileURL(f.VBare)}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	only("the delete", lifecycleNetTimeout)

	// An init of a new vault.
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	if _, err := InitVault(t.Context(), initReq(filepath.Join(t.TempDir(), "new-vault"), origin, github)); err != nil {
		t.Fatalf("init: %v", err)
	}
	only("the init", lifecycleNetTimeout)
}

// gitStandIn puts a `git` first on PATH that behaves as the real one except in
// the ways a test switches on, and returns the switch. Modes:
//
//	"drop-one":      the blob fetch (`fetch ... --stdin`) loses the first id on
//	                 its stdin, as a server that sends less than it was asked for
//	"fail-rev-list": every `rev-list` exits 1 and prints nothing
//
// An empty mode leaves git alone.
func gitStandIn(t *testing.T) (setMode func(string)) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the git stand-in is a shell script")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	mode := filepath.Join(dir, "mode")
	script := `#!/bin/sh
mode=$(cat "` + mode + `" 2>/dev/null)
fetch=0; stdin=0; revlist=0
for a in "$@"; do
  case "$a" in fetch) fetch=1 ;; --stdin) stdin=1 ;; rev-list) revlist=1 ;; esac
done
if [ "$mode" = drop-one ] && [ $fetch = 1 ] && [ $stdin = 1 ]; then
  sed 1d | "` + real + `" "$@"
  exit $?
fi
if [ "$mode" = fail-rev-list ] && [ $revlist = 1 ]; then
  exit 1
fi
exec "` + real + `" "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func(m string) {
		t.Helper()
		if err := os.WriteFile(mode, []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A remote that sends less than it was asked for is refused by the blob fetch
// itself, with the count, and one missing object is enough. The copy's plan
// stops on that refusal: it does not go on to read the snapshot.
func TestRemoteSnapshot_UnderDeliveredBlobFetchIsRefusedWithTheCount(t *testing.T) {
	f, _ := newBulkCopyFix(t, 5)
	setMode := gitStandIn(t)
	paths := append(ProjectTrees("p"), vaultManifestRel)

	s := bloblessSnapshot(t, f)
	setMode("drop-one")
	err := s.fetchBlobs(s.Tip, paths)
	if err == nil || !strings.Contains(err.Error(), "lacks 1 object(s)") {
		t.Fatalf("a blob fetch that was sent one object short must be refused with \"lacks 1 object(s)\"; got %v", err)
	}
	// The stand-in withheld one id and nothing else: a second, whole fetch
	// completes the same snapshot.
	setMode("")
	if err := s.fetchBlobs(s.Tip, paths); err != nil {
		t.Fatalf("the same fetch through the real git: %v", err)
	}

	setMode("drop-one")
	if _, err := PlanCopy(f.req("p")); err == nil || !errors.Is(err, ErrCopyRefused) || !strings.Contains(err.Error(), "lacks 1 object(s)") {
		t.Fatalf("the copy's plan must be refused by the incomplete fetch, with the count; got %v", err)
	}
}

// The completeness check fails closed on its own failure: a listing or a
// rev-list that errors is an error, never "nothing is missing".
func TestRemoteSnapshot_CompletenessCheckFailsClosed(t *testing.T) {
	f, _ := newBulkCopyFix(t, 1)
	s := bloblessSnapshot(t, f)
	paths := append(ProjectTrees("p"), vaultManifestRel)

	const noSuchCommit = "0123456789abcdef0123456789abcdef01234567"
	if n, err := s.missingObjects(noSuchCommit, paths); err == nil {
		t.Errorf("missingObjects on a commit the snapshot lacks returned %d and no error", n)
	}

	setMode := gitStandIn(t)
	setMode("fail-rev-list")
	if n, err := s.missingObjects(s.Tip, paths); err == nil {
		t.Errorf("missingObjects returned %d and no error although rev-list failed", n)
	}
	if err := s.requireComplete(s.Tip, paths); err == nil {
		t.Error("requireComplete passed a snapshot it could not check")
	}
}
