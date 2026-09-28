// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// delFixture is a source vault V (one or more file:// remotes, in sync) holding
// project p with tracked content in both trees and three ignored leftovers,
// and a destination vault B whose published branch holds a copy of p.
type delFixture struct {
	*lcVault
	B, BBare string
}

var delLeftovers = []string{
	"Projects/p/resume.md.bak",
	"palace/p/.local/imported-sessions.jsonl",
	"palace/.local/embed-cache/p/0001.vec",
}

func newDelFixture(t *testing.T, remotes ...string) *delFixture {
	t.Helper()
	v := newLCVault(t, remotes...)
	writeFile(t, v.Dir, ".gitignore", "*.bak\npalace/.local/\npalace/*/.local/\n.vp-locks/\n")
	writeFile(t, v.Dir, "Projects/p/sessions/s1.md", "session\n")
	writeFile(t, v.Dir, "palace/p/kg/entities.jsonl", "{}\n")
	gitRun(t, v.Dir, "add", "-A")
	gitRun(t, v.Dir, "commit", "-q", "-m", "more of p")
	for _, r := range remotes {
		gitRun(t, v.Dir, "push", "-q", r, "main")
	}
	for _, rel := range delLeftovers {
		writeFile(t, v.Dir, rel, "leftover "+rel+"\n")
	}
	f := &delFixture{lcVault: v, BBare: initBareRemote(t)}
	f.B = t.TempDir()
	gitRun(t, f.B, "init", "-q", "-b", "main")
	gitRun(t, f.B, "config", "user.email", "b@example.com")
	gitRun(t, f.B, "config", "user.name", "B")
	writeFile(t, f.B, "README.md", "vault B\n")
	gitRun(t, f.B, "add", "-A")
	gitRun(t, f.B, "commit", "-q", "-m", "B seed")
	gitRun(t, f.B, "remote", "add", "origin", fileURL(f.BBare))
	gitRun(t, f.B, "push", "-q", "origin", "main")
	return f
}

// copyToB writes p's tracked files at V's HEAD into B and commits them as a
// copy commit carrying the § Copy trailers; it does not push.
func (f *delFixture) copyToB(t *testing.T) string {
	t.Helper()
	fp, _, err := footprintHash(f.Dir, "HEAD", "p")
	if err != nil {
		t.Fatal(err)
	}
	return f.copyToBWith(t, fileURL(f.Bares[f.Remotes[0]]), "p "+fp)
}

// copyToBWith is copyToB with a given Vp-Copy-Source and Vp-Copy-Footprint
// value ("" leaves the footprint trailer out).
func (f *delFixture) copyToBWith(t *testing.T, source, footprint string) string {
	t.Helper()
	f.writePToB(t)
	trailers := "Vp-Copy-Project: p\nVp-Copy-Source: " + source
	if footprint != "" {
		trailers += "\nVp-Copy-Footprint: " + footprint
	}
	gitRun(t, f.B, "add", "-A")
	gitRun(t, f.B, "commit", "-q", "-m", "vault copy: p\n\n"+trailers)
	return gitRun(t, f.B, "rev-parse", "HEAD")
}

// writePToB writes p's tracked files at V's HEAD into B's working tree.
func (f *delFixture) writePToB(t *testing.T) {
	t.Helper()
	for _, rel := range strings.Split(gitRun(t, f.Dir, "ls-tree", "-r", "--name-only", "HEAD", "--", "Projects/p", "palace/p"), "\n") {
		writeFile(t, f.B, rel, gitRun(t, f.Dir, "show", "HEAD:"+rel)+"\n")
	}
}

func (f *delFixture) pushB(t *testing.T) { gitRun(t, f.B, "push", "-q", "origin", "main") }

func (f *delFixture) movedReq() DeleteRequest {
	return DeleteRequest{Projects: []string{"p"}, MovedTo: fileURL(f.BBare)}
}

func (f *delFixture) leftoversPresent(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, rel := range delLeftovers {
		if _, err := os.Lstat(filepath.Join(f.Dir, rel)); err == nil {
			out = append(out, rel)
		}
	}
	return out
}

// seam sets a test seam for the test's duration.
func seam[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

var errKilled = errors.New("killed (test)")

// Q2/P5/P6 and Q4: one commit holding exactly the tracked footprint's removal,
// the record and Audits/.surface; the record carries every Q4 field; the
// commit is on every remote; the leftovers go afterwards.
func TestDeleteMovedCommitsFootprintAndRecordAndPublishes(t *testing.T) {
	f := newDelFixture(t, "origin", "mirror")
	copyCommit := f.copyToB(t)
	f.pushB(t)
	parent := f.head(t)
	fp, _, _ := footprintHash(f.Dir, "HEAD", "p")

	res, err := ApplyDelete(f.Dir, f.movedReq())
	if err != nil {
		t.Fatal(err)
	}
	commit := f.head(t)
	if res.Commit != commit || gitRun(t, f.Dir, "rev-parse", "HEAD~1") != parent {
		t.Fatalf("result commit %s, HEAD %s; want one commit on %s", res.Commit, commit, parent)
	}
	for _, r := range f.Remotes {
		if got := gitRun(t, f.Bares[r], "rev-parse", "main"); got != commit {
			t.Errorf("remote %s is at %s, want %s", r, got, commit)
		}
	}
	changed := strings.Split(gitRun(t, f.Dir, "show", "--name-only", "--format=", "HEAD"), "\n")
	sort.Strings(changed)
	want := []string{"Audits/.surface", "Audits/departures/p.json", "Projects/p/resume.md", "Projects/p/sessions/s1.md", "palace/p/kg/entities.jsonl"}
	if strings.Join(changed, ",") != strings.Join(want, ",") {
		t.Fatalf("the commit touches %v, want %v", changed, want)
	}
	if out := gitRun(t, f.Dir, "ls-files", "--", "Projects/p", "palace/p"); out != "" {
		t.Fatalf("still tracked: %s", out)
	}
	blob, found, err := ReadCommittedBlob(f.Dir, departure.RelPath("p"))
	if err != nil || !found {
		t.Fatalf("record not committed: %v", err)
	}
	rec := departure.Parse("p", blob)
	if rec.Malformed != "" || rec.Kind != departure.MovedToVault || rec.To != fileURL(f.BBare) ||
		rec.CopyCommit != copyCommit || rec.Footprint != fp || rec.Generation != 1 {
		t.Fatalf("record = %+v", rec)
	}
	tr, err := commitsWithTrailer(f.Dir, "HEAD", trailerDeleteProject)
	if err != nil || len(tr) != 1 || footprintTrailer(tr[0].Trailers[trailerDeleteFP], "p") != fp ||
		tr[0].Trailers[trailerDeleteKind][0] != "moved-to-vault" || len(tr[0].Trailers[lifecycleRunTrailer]) != 1 {
		t.Fatalf("delete trailers = %+v, %v", tr, err)
	}
	if left := f.leftoversPresent(t); len(left) != 0 {
		t.Fatalf("leftovers not removed: %v", left)
	}
	if markerFound(t, f.Dir) {
		t.Fatal("the marker was not cleared")
	}
	if len(res.Undo) != 3 || !strings.Contains(res.Undo[0], "revert --no-edit "+commit) {
		t.Fatalf("undo lines = %q", res.Undo)
	}
}

// The dry run lists the leftovers as not restorable (X6 among them), prints
// the real-run line with --expect, and writes nothing.
func TestDeleteDryRunListsLeftoversAndWritesNothing(t *testing.T) {
	f := newDelFixture(t, "origin")
	f.copyToB(t)
	f.pushB(t)
	_ = lockDir(t, f.Dir).Release() // the host-local .vp-locks/ sidecar every lock creates
	head, status := f.head(t), gitRun(t, f.Dir, "status", "--porcelain", "--ignored")

	plan, err := PlanDelete(f.Dir, f.movedReq())
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, l := range plan.Leftovers {
		paths = append(paths, l.Path+":"+l.Class)
	}
	sort.Strings(paths)
	want := []string{"Projects/p/resume.md.bak:ignored", "palace/.local/embed-cache/p/0001.vec:machine-local", "palace/p/.local/imported-sessions.jsonl:machine-local"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("leftovers = %v, want %v", paths, want)
	}
	if !strings.Contains(plan.Command, "--expect "+plan.Digest) || !strings.HasPrefix(plan.Digest, "v1:") {
		t.Fatalf("command = %q", plan.Command)
	}
	if f.head(t) != head || gitRun(t, f.Dir, "status", "--porcelain", "--ignored") != status || markerFound(t, f.Dir) {
		t.Fatalf("the dry run wrote something:\n%s\n---\n%s", status, gitRun(t, f.Dir, "status", "--porcelain", "--ignored"))
	}
	if _, err := ApplyDelete(f.Dir, DeleteRequest{Projects: []string{"p"}, MovedTo: fileURL(f.BBare), Expect: "v1:00"}); err == nil ||
		!strings.Contains(err.Error(), "--expect") {
		t.Fatalf("a wrong --expect must refuse: %v", err)
	}
	if _, err := ApplyDelete(f.Dir, DeleteRequest{Projects: []string{"p"}, MovedTo: fileURL(f.BBare), Expect: plan.Digest}); err != nil {
		t.Fatalf("the dry run's --expect must pass: %v", err)
	}
}

// write after copy: a commit to p in A after the copy refuses, though the
// copy commit's own F still equals its trailer.
func TestDeleteRefusesAWriteAfterTheCopy(t *testing.T) {
	f := newDelFixture(t, "origin")
	f.copyToB(t)
	f.pushB(t)
	writeFile(t, f.Dir, "Projects/p/sessions/s2.md", "written after the copy\n")
	gitRun(t, f.Dir, "add", "-A")
	gitRun(t, f.Dir, "commit", "-q", "-m", "a session after the copy")
	gitRun(t, f.Dir, "push", "-q", "origin", "main")
	head := f.head(t)

	_, err := ApplyDelete(f.Dir, f.movedReq())
	if err == nil || !strings.Contains(err.Error(), "no copy of") {
		t.Fatalf("err = %v", err)
	}
	if f.head(t) != head || markerFound(t, f.Dir) || len(f.leftoversPresent(t)) != 3 {
		t.Fatal("a refused delete changed V")
	}
}

// partly reverted copy: B drops palace/p after the copy → refuses on F(B tip).
func TestDeleteRefusesAPartlyRevertedCopy(t *testing.T) {
	f := newDelFixture(t, "origin")
	f.copyToB(t)
	gitRun(t, f.B, "rm", "-q", "-r", "palace/p")
	gitRun(t, f.B, "commit", "-q", "-m", "drop palace/p in B")
	f.pushB(t)

	_, err := ApplyDelete(f.Dir, f.movedReq())
	if err == nil || !strings.Contains(err.Error(), "no copy of") {
		t.Fatalf("err = %v", err)
	}
}

// copy behind a merge: the copy commit sits on a merge's second parent, with
// an unrelated commit after it, and is still found.
func TestDeleteFindsTheCopyBehindAMerge(t *testing.T) {
	f := newDelFixture(t, "origin")
	gitRun(t, f.B, "checkout", "-q", "-b", "copy")
	f.copyToB(t)
	gitRun(t, f.B, "checkout", "-q", "main")
	writeFile(t, f.B, "Projects/q/resume.md", "another project in B\n")
	gitRun(t, f.B, "add", "-A")
	gitRun(t, f.B, "commit", "-q", "-m", "B's own work")
	gitRun(t, f.B, "merge", "-q", "--no-ff", "--no-edit", "copy")
	writeFile(t, f.B, "Projects/q/later.md", "later\n")
	gitRun(t, f.B, "add", "-A")
	gitRun(t, f.B, "commit", "-q", "-m", "an unrelated commit after the merge")
	f.pushB(t)

	if _, err := ApplyDelete(f.Dir, f.movedReq()); err != nil {
		t.Fatalf("the copy behind a merge must be found: %v", err)
	}
}

// changed leftover: a .bak rewritten after the plan is kept and reported.
func TestDeleteKeepsALeftoverChangedAfterThePlan(t *testing.T) {
	f := newDelFixture(t, "origin")
	f.copyToB(t)
	f.pushB(t)
	seam(t, &deleteAfterPlan, func() { writeFile(t, f.Dir, "Projects/p/resume.md.bak", "rewritten after the plan\n") })

	res, err := ApplyDelete(f.Dir, f.movedReq())
	if err != nil {
		t.Fatal(err)
	}
	if left := f.leftoversPresent(t); len(left) != 1 || left[0] != "Projects/p/resume.md.bak" {
		t.Fatalf("leftovers present = %v, want only the rewritten .bak", left)
	}
	if len(res.Kept) != 1 || res.Kept[0].Path != "Projects/p/resume.md.bak" || res.Kept[0].Reason == "" {
		t.Fatalf("kept = %+v", res.Kept)
	}
}

// Q9b: a second delete refuses "not present" and changes nothing.
func TestDeleteReRunRefusesNotPresent(t *testing.T) {
	f := newDelFixture(t, "origin")
	f.copyToB(t)
	f.pushB(t)
	if _, err := ApplyDelete(f.Dir, f.movedReq()); err != nil {
		t.Fatal(err)
	}
	head, status := f.head(t), gitRun(t, f.Dir, "status", "--porcelain", "--ignored")
	_, err := ApplyDelete(f.Dir, f.movedReq())
	if err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("err = %v", err)
	}
	if f.head(t) != head || gitRun(t, f.Dir, "status", "--porcelain", "--ignored") != status {
		t.Fatal("the refused re-run changed V")
	}
}

// crash after publish: no marker, every remote holds the delete → a re-run
// removes only the leftovers and makes no commit.
func TestDeleteReRunAfterPublishRemovesOnlyTheLeftovers(t *testing.T) {
	f := newDelFixture(t, "origin", "mirror")
	f.copyToB(t)
	f.pushB(t)
	seam(t, &deleteBeforeLeftovers, func() error { return errKilled })
	if _, err := ApplyDelete(f.Dir, f.movedReq()); !errors.Is(err, errKilled) {
		t.Fatalf("err = %v", err)
	}
	if markerFound(t, f.Dir) || len(f.leftoversPresent(t)) != 3 {
		t.Fatal("want: published (no marker) with every leftover still present")
	}
	head := f.head(t)
	deleteBeforeLeftovers = func() error { return nil }

	res, err := ApplyDelete(f.Dir, f.movedReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan.Mode != DeleteLeftovers || res.Commit != "" || f.head(t) != head {
		t.Fatalf("mode %s, commit %q, HEAD moved: %v", res.Plan.Mode, res.Commit, f.head(t) != head)
	}
	if left := f.leftoversPresent(t); len(left) != 0 {
		t.Fatalf("leftovers not removed: %v", left)
	}
}

// unpublished delete: killed before its push. With no marker (it was lost),
// the local delete is not on the live remotes, so the re-run never deletes
// the leftovers.
func TestDeleteNeverRemovesLeftoversAgainstAnUnpublishedDelete(t *testing.T) {
	f := newDelFixture(t, "origin")
	f.copyToB(t)
	f.pushB(t)
	seam(t, &deleteBeforePublish, func() error { return errKilled })
	if _, err := ApplyDelete(f.Dir, f.movedReq()); !errors.Is(err, errKilled) {
		t.Fatalf("err = %v", err)
	}
	p, err := lifecycleMarkerPath(f.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	deleteBeforePublish = func() error { return nil }

	_, err = ApplyDelete(f.Dir, f.movedReq())
	if err == nil || !strings.Contains(err.Error(), "not published") {
		t.Fatalf("err = %v", err)
	}
	if len(f.leftoversPresent(t)) != 3 {
		t.Fatal("leftovers were removed against an unpublished delete")
	}
}

// unpublished delete, marker kept: the re-run redoes the publish (the same
// commit) and then removes the leftovers.
func TestDeleteReRunWithAMarkerRedoesThePublish(t *testing.T) {
	f := newDelFixture(t, "origin")
	f.copyToB(t)
	f.pushB(t)
	seam(t, &deleteBeforePublish, func() error { return errKilled })
	if _, err := ApplyDelete(f.Dir, f.movedReq()); !errors.Is(err, errKilled) {
		t.Fatalf("err = %v", err)
	}
	commit := f.head(t)
	if !markerFound(t, f.Dir) || len(f.leftoversPresent(t)) != 3 || gitRun(t, f.Bares["origin"], "rev-parse", "main") == commit {
		t.Fatal("want: marker kept, leftovers present, nothing pushed")
	}
	deleteBeforePublish = func() error { return nil }

	res, err := ApplyDelete(f.Dir, f.movedReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.Redo != RedoPublished || gitRun(t, f.Bares["origin"], "rev-parse", "main") != commit || f.head(t) != commit {
		t.Fatalf("redo %s; remote %s; want the same commit %s published", res.Redo, gitRun(t, f.Bares["origin"], "rev-parse", "main"), commit)
	}
	if markerFound(t, f.Dir) || len(f.leftoversPresent(t)) != 0 {
		t.Fatal("want: marker cleared and leftovers removed")
	}
}

// unpublished delete, remote moved: the re-run resets (never rebases), keeps
// the leftovers, restores p, and asks for a fresh dry run.
func TestDeleteReRunAfterTheRemoteMovedResetsAndKeepsLeftovers(t *testing.T) {
	f := newDelFixture(t, "origin")
	f.copyToB(t)
	f.pushB(t)
	parent := f.head(t)
	seam(t, &deleteBeforePublish, func() error { return errKilled })
	if _, err := ApplyDelete(f.Dir, f.movedReq()); !errors.Is(err, errKilled) {
		t.Fatalf("err = %v", err)
	}
	pushFromOtherHost(t, f.Bares["origin"], "Projects/other/elsewhere.md", "another host\n")
	deleteBeforePublish = func() error { return nil }

	_, err := ApplyDelete(f.Dir, f.movedReq())
	if !errors.Is(err, ErrRedoRemoteMoved) {
		t.Fatalf("err = %v", err)
	}
	if f.head(t) != parent || markerFound(t, f.Dir) || len(f.leftoversPresent(t)) != 3 {
		t.Fatal("want: reset to the parent, marker cleared, leftovers kept")
	}
	if _, err := os.Stat(filepath.Join(f.Dir, "Projects/p/resume.md")); err != nil {
		t.Fatalf("p's tracked files must be back: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.Dir, filepath.FromSlash(departure.RelPath("p")))); !os.IsNotExist(err) {
		t.Fatalf("the record must be gone: %v", err)
	}
}

// scoped clean: another project's uncommitted edit in A neither refuses nor
// resets the delete, and survives it.
func TestDeleteIgnoresOtherProjectsDirt(t *testing.T) {
	f := newDelFixture(t, "origin")
	f.copyToB(t)
	f.pushB(t)
	writeFile(t, f.Dir, "Projects/other/resume.md", "another session's uncommitted edit\n")

	if _, err := ApplyDelete(f.Dir, f.movedReq()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(f.Dir, "Projects/other/resume.md"))
	if string(b) != "another session's uncommitted edit\n" {
		t.Fatalf("the other project's edit was lost: %q", b)
	}
}

// --discard: kind deleted, no destination read, no destination named.
func TestDeleteDiscard(t *testing.T) {
	f := newDelFixture(t, "origin")
	if _, err := ApplyDelete(f.Dir, DeleteRequest{Projects: []string{"p"}}); err == nil || !strings.Contains(err.Error(), "--discard") {
		t.Fatalf("neither flag must refuse: %v", err)
	}
	if _, err := ApplyDelete(f.Dir, DeleteRequest{Projects: []string{"p"}, Discard: true}); err != nil {
		t.Fatal(err)
	}
	blob, _, _ := ReadCommittedBlob(f.Dir, departure.RelPath("p"))
	if rec := departure.Parse("p", blob); rec.Kind != departure.Deleted || rec.To != "" || rec.CopyCommit != "" || rec.Footprint == "" {
		t.Fatalf("record = %+v", rec)
	}
}

// unpublished state: V ahead of a remote refuses before anything is written.
func TestDeleteRefusesAVaultAheadOfARemote(t *testing.T) {
	f := newDelFixture(t, "origin")
	writeFile(t, f.Dir, "Projects/other/unpushed.md", "unpushed\n")
	gitRun(t, f.Dir, "add", "-A")
	gitRun(t, f.Dir, "commit", "-q", "-m", "unpushed")
	head := f.head(t)
	_, err := ApplyDelete(f.Dir, DeleteRequest{Projects: []string{"p"}, Discard: true})
	if !errors.Is(err, ErrRemoteNotAtHead) {
		t.Fatalf("err = %v", err)
	}
	if f.head(t) != head || markerFound(t, f.Dir) {
		t.Fatal("a refused delete wrote something")
	}
	if _, err := os.Stat(filepath.Join(f.Dir, filepath.FromSlash(departure.RelPath("p")))); !os.IsNotExist(err) {
		t.Fatal("a record was written")
	}
}

// lock: the whole run completes under one root lock, never re-acquiring it.
func TestDeleteCompletesUnderOneRootLock(t *testing.T) {
	f := newDelFixture(t, "origin")
	var err error
	withinDeadline(t, "ApplyDelete", func() {
		_, err = ApplyDelete(f.Dir, DeleteRequest{Projects: []string{"p"}, Discard: true})
	})
	if err != nil {
		t.Fatal(err)
	}
}

// killed after the record, before the commit: the marker was written before
// the first write, so the re-run rolls back the untracked record, clears the
// marker and completes afresh.
func TestDeleteKilledBeforeItsCommitIsRolledBackAndRedone(t *testing.T) {
	f := newDelFixture(t, "origin")
	parent := f.head(t)
	seam(t, &deleteAfterRecords, func() error { return errKilled })
	req := DeleteRequest{Projects: []string{"p"}, Discard: true}
	if _, err := ApplyDelete(f.Dir, req); !errors.Is(err, errKilled) {
		t.Fatalf("err = %v", err)
	}
	if f.head(t) != parent || !markerFound(t, f.Dir) {
		t.Fatal("want: no commit, marker standing")
	}
	deleteAfterRecords = func() error { return nil }
	res, err := ApplyDelete(f.Dir, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Redo != RedoRolledBack || res.Commit == "" || gitRun(t, f.Dir, "rev-parse", "HEAD~1") != parent {
		t.Fatalf("redo %s, commit %q", res.Redo, res.Commit)
	}
}

// --moved-to naming this vault's own remote refuses.
func TestDeleteRefusesItsOwnRemoteAsTheDestination(t *testing.T) {
	f := newDelFixture(t, "origin")
	_, err := ApplyDelete(f.Dir, DeleteRequest{Projects: []string{"p"}, MovedTo: fileURL(f.Bares["origin"])})
	if err == nil || !strings.Contains(err.Error(), "remote of this vault itself") {
		t.Fatalf("err = %v", err)
	}
}

// Untracked content git does not ignore under the footprint refuses: the
// commit would not carry it, and tidy would commit it back.
func TestDeleteRefusesUntrackedContent(t *testing.T) {
	f := newDelFixture(t, "origin")
	writeFile(t, f.Dir, "Projects/p/sessions/uncommitted.md", "never committed\n")
	_, err := ApplyDelete(f.Dir, DeleteRequest{Projects: []string{"p"}, Discard: true})
	if err == nil || !strings.Contains(err.Error(), "untracked file") {
		t.Fatalf("err = %v", err)
	}
	if markerFound(t, f.Dir) {
		t.Fatal("a refused delete left a marker")
	}
}

// One present and one departed project in one run refuses.
func TestDeleteRefusesMixedModes(t *testing.T) {
	f := newDelFixture(t, "origin")
	if _, err := ApplyDelete(f.Dir, DeleteRequest{Projects: []string{"other"}, Discard: true}); err != nil {
		t.Fatal(err)
	}
	_, err := ApplyDelete(f.Dir, DeleteRequest{Projects: []string{"p", "other"}, Discard: true})
	if err == nil || !strings.Contains(err.Error(), "separate runs") {
		t.Fatalf("err = %v", err)
	}
}

// The delete commit's trailers parse as trailers for git itself, not only for
// commitsWithTrailer: the hostname stamp sits before the final trailer block.
func TestDeleteCommitTrailersParseWithInterpretTrailers(t *testing.T) {
	f := newDelFixture(t, "origin")
	if _, err := ApplyDelete(f.Dir, DeleteRequest{Projects: []string{"p"}, Discard: true}); err != nil {
		t.Fatal(err)
	}
	msg := gitRun(t, f.Dir, "log", "-1", "--format=%B")
	cmd := exec.Command("git", "interpret-trailers", "--parse")
	cmd.Stdin = strings.NewReader(msg + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	fp, _, _ := footprintHash(f.Dir, "HEAD~1", "p")
	for _, want := range []string{"Vp-Delete-Project: p", "Vp-Delete-Kind: deleted", "Vp-Delete-Footprint: p " + fp, "Vp-Run: delete-"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("interpret-trailers --parse lacks %q:\n%s\nmessage:\n%s", want, out, msg)
		}
	}
	if !strings.Contains(msg, "\n\n[") {
		t.Errorf("the hostname stamp is missing:\n%s", msg)
	}
}

// A delete commit carries the Vp-Run trailer in exactly U3's format: killed
// after its commit but before the marker recorded it, the redo adopts the
// commit by that trailer and publishes it.
func TestDeleteCommitIsAdoptedByItsRunTrailer(t *testing.T) {
	f := newDelFixture(t, "origin")
	seam(t, &deleteBeforePublish, func() error { return errKilled })
	req := DeleteRequest{Projects: []string{"p"}, Discard: true}
	if _, err := ApplyDelete(f.Dir, req); !errors.Is(err, errKilled) {
		t.Fatalf("err = %v", err)
	}
	commit := f.head(t)
	m, found, err := readLifecycleMarker(f.Dir)
	if err != nil || !found || m.Commit != commit {
		t.Fatalf("marker = %+v, %v", m, err)
	}
	if got, err := commitRunTrailer(f.Dir, commit); err != nil || got != m.RunID {
		t.Fatalf("Vp-Run trailer = %q (%v), want %q", got, err, m.RunID)
	}
	m.Commit = ""
	p, _ := lifecycleMarkerPath(f.Dir)
	b, _ := json.Marshal(m)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	deleteBeforePublish = func() error { return nil }
	res, err := ApplyDelete(f.Dir, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Redo != RedoPublished || gitRun(t, f.Bares["origin"], "rev-parse", "main") != commit {
		t.Fatalf("redo %s: the commit was not adopted and published", res.Redo)
	}
}

// R1 (review): a redo that publishes the delete, with no leftover to remove,
// is success, not "not present".
func TestDeleteRedoThatPublishesWithNoLeftoversSucceeds(t *testing.T) {
	f := newDelFixture(t, "origin")
	for _, rel := range delLeftovers {
		if err := os.Remove(filepath.Join(f.Dir, rel)); err != nil {
			t.Fatal(err)
		}
	}
	f.copyToB(t)
	f.pushB(t)
	seam(t, &deleteBeforePublish, func() error { return errKilled })
	if _, err := ApplyDelete(f.Dir, f.movedReq()); !errors.Is(err, errKilled) {
		t.Fatalf("err = %v", err)
	}
	commit := f.head(t)
	deleteBeforePublish = func() error { return nil }
	res, err := ApplyDelete(f.Dir, f.movedReq())
	if err != nil {
		t.Fatalf("the redo published the delete, yet the run reports failure: %v", err)
	}
	if res.Redo != RedoPublished || res.Commit != commit || gitRun(t, f.Bares["origin"], "rev-parse", "main") != commit {
		t.Fatalf("res = %+v", res)
	}
	// A plain re-run, with no marker, still refuses "not present".
	if _, err := ApplyDelete(f.Dir, f.movedReq()); err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("a plain re-run: err = %v", err)
	}
}

// R2 (review): a copy commit made the way U5 makes it — commitPathsLocked,
// the § Copy trailers passed as trailers — parses, and findVerifiedCopy finds
// it.
func TestDeleteFindsACopyCommitMadeThroughCommitPathsLocked(t *testing.T) {
	f := newDelFixture(t, "origin")
	f.writePToB(t)
	fp, _, _ := footprintHash(f.Dir, "HEAD", "p")
	h, err := vaultlock.AcquireHeld(f.B, f.B)
	if err != nil {
		t.Fatal(err)
	}
	trailers := "Vp-Copy-Project: p\nVp-Copy-Source: " + fileURL(f.Bares["origin"]) + "\nVp-Copy-Footprint: p " + fp
	if _, err := commitPathsLocked(h, "vault copy: p", trailers, []string{"Projects/p", "palace/p"}); err != nil {
		t.Fatal(err)
	}
	_ = h.Release()
	f.pushB(t)
	if _, err := PlanDelete(f.Dir, f.movedReq()); err != nil {
		t.Fatalf("a copy commit made through commitPathsLocked is not found: %v\nits message:\n%s", err, gitRun(t, f.B, "log", "-1", "--format=%B"))
	}
}

// G2: a copy whose Vp-Copy-Source is not one of this vault's remotes is a
// copy of some other vault, and refuses.
func TestDeleteRefusesACopyFromAnotherSource(t *testing.T) {
	f := newDelFixture(t, "origin")
	fp, _, _ := footprintHash(f.Dir, "HEAD", "p")
	f.copyToBWith(t, "git@example.invalid:someone/else.git", "p "+fp)
	f.pushB(t)
	if _, err := PlanDelete(f.Dir, f.movedReq()); err == nil || !strings.Contains(err.Error(), "no commit on") {
		t.Fatalf("err = %v", err)
	}
}

// G8: a copy commit whose Vp-Copy-Footprint is wrong, or missing, refuses
// even when the bytes match.
func TestDeleteRefusesACopyWithAWrongOrMissingFootprintTrailer(t *testing.T) {
	for name, fpTrailer := range map[string]string{"wrong": "p v1:" + strings.Repeat("0", 64), "missing": ""} {
		f := newDelFixture(t, "origin")
		f.copyToBWith(t, fileURL(f.Bares["origin"]), fpTrailer)
		f.pushB(t)
		if _, err := PlanDelete(f.Dir, f.movedReq()); err == nil || !strings.Contains(err.Error(), "no copy of") {
			t.Fatalf("%s footprint trailer: err = %v", name, err)
		}
	}
}

// G5 (review probe P4): the destination's own record generation is read from
// its snapshot and floors the new one.
func TestDeleteGenerationIsAboveTheDestinationRecord(t *testing.T) {
	f := newDelFixture(t, "origin")
	f.copyToB(t)
	writeFile(t, f.B, departure.RelPath("p"), `{"format":"vp-departure/1","slug":"p","kind":"moved-to-vault","to":"x","date":"2026-01-01","generation":4}`+"\n")
	gitRun(t, f.B, "add", "-A")
	gitRun(t, f.B, "commit", "-q", "-m", "B once recorded p")
	f.pushB(t)
	plan, err := PlanDelete(f.Dir, f.movedReq())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Projects[0].DestinationGeneration != 4 || plan.Projects[0].Generation != 5 {
		t.Fatalf("destination gen %d, gen %d; want 4 and 5", plan.Projects[0].DestinationGeneration, plan.Projects[0].Generation)
	}
	if _, err := ApplyDelete(f.Dir, f.movedReq()); err != nil {
		t.Fatal(err)
	}
	if rec, _ := departure.Find(f.Dir, "p"); rec.Generation != 5 {
		t.Fatalf("recorded generation %d, want 5", rec.Generation)
	}
}

// G6: the postcheck's departed check. A file written under the deleted tree
// between the plan and the commit (untracked, not ignored: the -uno postcheck
// cannot see it) leaves p not departed, so the commit is reset, nothing is
// published, and the file survives.
func TestDeletePostcheckRefusesWhenTheProjectIsNotDeparted(t *testing.T) {
	f := newDelFixture(t, "origin")
	parent := f.head(t)
	seam(t, &deleteAfterPlan, func() { writeFile(t, f.Dir, "Projects/p/sessions/late.md", "written during the delete\n") })
	_, err := ApplyDelete(f.Dir, DeleteRequest{Projects: []string{"p"}, Discard: true})
	if err == nil || !strings.Contains(err.Error(), "does not read as departed") {
		t.Fatalf("err = %v", err)
	}
	if f.head(t) != parent || markerFound(t, f.Dir) || gitRun(t, f.Bares["origin"], "rev-parse", "main") != parent {
		t.Fatal("want: reset to the parent, marker cleared, nothing published")
	}
	if _, err := os.Stat(filepath.Join(f.Dir, "Projects/p/sessions/late.md")); err != nil {
		t.Fatalf("the late file was lost: %v", err)
	}
}
