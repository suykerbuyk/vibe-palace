// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// copyFix is a source vault S published to a bare remote, and a destination
// vault V with its own bare remote, both at the binary's data format.
type copyFix struct {
	Src, SrcBare string
	V, VBare     string
}

func formatManifest() string {
	return fmt.Sprintf("format = %d\n", surface.RequiredDataFormat)
}

func newCopyFix(t *testing.T) *copyFix {
	t.Helper()
	f := &copyFix{}
	f.Src = initTestRepo(t)
	f.SrcBare = initBareRemote(t)
	gitRun(t, f.SrcBare, "config", "uploadpack.allowFilter", "true")
	gitRun(t, f.SrcBare, "config", "uploadpack.allowAnySHA1InWant", "true")
	writeFile(t, f.Src, ".vibe-palace/vault.toml", formatManifest())
	writeFile(t, f.Src, ".gitignore", "*.bak\npalace/.local/\n")
	writeFile(t, f.Src, "Projects/p/resume.md", "resume of p\n")
	writeFile(t, f.Src, "Projects/p/sessions/s1.md", "session 1\n")
	writeFile(t, f.Src, "Projects/p/commit-log.anchor", "9b6da95804608aeb80a7dc3ec22f7a79ba01efc8\n")
	writeFile(t, f.Src, "Projects/p/.surface", "surface = 999 # the source vault stamp\n")
	writeFile(t, f.Src, "palace/p/kg/entities.jsonl", "{}\n")
	writeFile(t, f.Src, "Projects/orch/resume.md", "projects-only\n")
	writeFile(t, f.Src, "Projects/other/resume.md", "other\n")
	gitRun(t, f.Src, "add", "-A")
	gitRun(t, f.Src, "commit", "-q", "-m", "source projects")
	gitRun(t, f.Src, "remote", "add", "origin", fileURL(f.SrcBare))
	gitRun(t, f.Src, "push", "-q", "origin", "main")

	home := os.Getenv("HOME")
	f.V = initTestRepo(t)
	t.Setenv("HOME", home) // keep one HOME, so the snapshot cache is stable
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	f.VBare = initBareRemote(t)
	writeFile(t, f.V, ".vibe-palace/vault.toml", formatManifest())
	writeFile(t, f.V, "Projects/resident/resume.md", "resident\n")
	gitRun(t, f.V, "add", "-A")
	gitRun(t, f.V, "commit", "-q", "-m", "destination vault")
	gitRun(t, f.V, "remote", "add", "origin", fileURL(f.VBare))
	gitRun(t, f.V, "push", "-q", "origin", "main")
	return f
}

func (f *copyFix) req(projects ...string) CopyRequest {
	return CopyRequest{Vault: f.V, Projects: projects, From: fileURL(f.SrcBare)}
}

// srcPush commits one change in the source and publishes it.
func (f *copyFix) srcPush(t *testing.T, rel, content string) {
	t.Helper()
	writeFile(t, f.Src, rel, content)
	gitRun(t, f.Src, "add", "-A")
	gitRun(t, f.Src, "commit", "-q", "-m", "source change "+rel)
	gitRun(t, f.Src, "push", "-q", "origin", "main")
}

func withCopyHook(t *testing.T, fn func(stage string, i int, path string) error) {
	t.Helper()
	copyTestHook = fn
	t.Cleanup(func() { copyTestHook = nil })
}

func pathPresent(p string) bool { _, err := os.Lstat(p); return err == nil }

// assertCopyAbsent: V's HEAD is head, the remote is at head, the projects'
// trees are gone, and no marker stands.
func assertCopyAbsent(t *testing.T, f *copyFix, head string, projects ...string) {
	t.Helper()
	if got := gitRun(t, f.V, "rev-parse", "HEAD"); got != head {
		t.Errorf("V HEAD moved: %s, want %s", got, head)
	}
	if got := gitRun(t, f.VBare, "rev-parse", "main"); got != head {
		t.Errorf("V's remote moved: %s, want %s", got, head)
	}
	for _, p := range projects {
		for _, tree := range ProjectTrees(p) {
			if pathPresent(filepath.Join(f.V, tree)) {
				t.Errorf("%s is still in V after the refusal", tree)
			}
		}
	}
	if markerFound(t, f.V) {
		t.Error("the pending marker still stands")
	}
}

// P2/P3/Q1a + P4: the copy is byte-equal to the source, one commit with
// parseable trailers, published to V's remote, marker cleared.
func TestCopy_ByteEqualOneCommitTrailersPublished(t *testing.T) {
	f := newCopyFix(t)
	before := gitRun(t, f.V, "rev-parse", "HEAD")
	res, err := ApplyCopy(f.req("p"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit == "" || gitRun(t, f.V, "rev-parse", "HEAD") != res.Commit {
		t.Fatalf("no copy commit: %+v", res)
	}
	if got := gitRun(t, f.V, "rev-parse", res.Commit+"~1"); got != before {
		t.Fatalf("more than one new commit: parent %s, want %s", got, before)
	}
	if got := gitRun(t, f.VBare, "rev-parse", "main"); got != res.Commit {
		t.Fatalf("V's remote is at %s, want the copy commit %s", got, res.Commit)
	}
	if markerFound(t, f.V) {
		t.Fatal("the marker survived a completed copy")
	}
	srcF, _, err := footprintHash(f.Src, "HEAD", "p")
	if err != nil {
		t.Fatal(err)
	}
	vF, _, err := footprintHash(f.V, "HEAD", "p")
	if err != nil {
		t.Fatal(err)
	}
	if srcF != vF {
		t.Fatalf("F(V HEAD) = %s, F(source) = %s", vF, srcF)
	}
	for _, rel := range []string{"Projects/p/resume.md", "Projects/p/sessions/s1.md", "Projects/p/commit-log.anchor", "palace/p/kg/entities.jsonl"} {
		a, _ := os.ReadFile(filepath.Join(f.Src, rel))
		b, err := os.ReadFile(filepath.Join(f.V, rel))
		if err != nil || string(a) != string(b) {
			t.Errorf("%s: not byte-equal to the source (err %v)", rel, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(f.V, "Projects/p/.surface")); strings.Contains(string(b), "the source vault stamp") {
		t.Error("the source's .surface stamp travelled")
	}
	found, err := commitsWithTrailer(f.V, "HEAD", TrailerCopyProject)
	if err != nil || len(found) != 1 || found[0].SHA != res.Commit {
		t.Fatalf("the copy commit's trailers do not parse: %+v, %v", found, err)
	}
	tr := found[0].Trailers
	if tr[TrailerCopySource][0] != fileURL(f.SrcBare) || tr[TrailerCopySourceCommit][0] != gitRun(t, f.Src, "rev-parse", "HEAD") ||
		tr[TrailerCopyFootprint][0] != "p "+srcF {
		t.Fatalf("trailers = %v", tr)
	}
	if !strings.Contains(gitRun(t, f.V, "log", "-1", "--format=%B"), "\n[") {
		t.Error("the hostname stamp is missing from the copy commit")
	}
	if len(res.Undo) != 2 || !strings.Contains(res.Undo[0], "revert") {
		t.Errorf("undo lines = %v", res.Undo)
	}
}

// P2 break-it row: a byte flipped in the copied output is refused by the
// postcheck; no commit is left and the footprint is rolled back.
func TestCopy_CorruptedCopyRefusedAndRolledBack(t *testing.T) {
	f := newCopyFix(t)
	head := gitRun(t, f.V, "rev-parse", "HEAD")
	withCopyHook(t, func(stage string, i int, path string) error {
		if stage == "copied" && i == 0 {
			full := filepath.Join(f.V, path)
			b, _ := os.ReadFile(full)
			b[0] ^= 0x01
			return os.WriteFile(full, b, 0o644)
		}
		return nil
	})
	_, err := ApplyCopy(f.req("p"))
	if err == nil || !strings.Contains(err.Error(), "postcheck") {
		t.Fatalf("err = %v, want the footprint postcheck to refuse", err)
	}
	assertCopyAbsent(t, f, head, "p")
}

// P6b/Q1b: an ignored .bak in the source's working tree never reaches V.
func TestCopy_IgnoredBakNeverTravels(t *testing.T) {
	f := newCopyFix(t)
	writeFile(t, f.Src, "Projects/p/transcripts/x.manifest.json.1234.bak", "old\n")
	if _, err := ApplyCopy(f.req("p")); err != nil {
		t.Fatal(err)
	}
	if pathPresent(filepath.Join(f.V, "Projects/p/transcripts")) {
		t.Fatal("an ignored .bak from the source's working tree reached V")
	}
}

// Q3: the copy commit touches only the footprint.
func TestCopy_CommitTouchesOnlyTheFootprint(t *testing.T) {
	f := newCopyFix(t)
	res, err := ApplyCopy(f.req("p", "orch"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range strings.Split(gitRun(t, f.V, "show", "--name-only", "--format=", res.Commit), "\n") {
		if !strings.HasPrefix(rel, "Projects/p/") && !strings.HasPrefix(rel, "palace/p/") && !strings.HasPrefix(rel, "Projects/orch/") {
			t.Errorf("the copy commit touches %s", rel)
		}
	}
}

// P1: a Projects-only project copies.
func TestCopy_ProjectsOnlyProject(t *testing.T) {
	f := newCopyFix(t)
	res, err := ApplyCopy(f.req("orch"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Plan.Projects[0].ProjectsOnly || pathPresent(filepath.Join(f.V, "palace/orch")) {
		t.Fatalf("plan %+v", res.Plan.Projects)
	}
	if b, _ := os.ReadFile(filepath.Join(f.V, "Projects/orch/resume.md")); string(b) != "projects-only\n" {
		t.Fatal("orch did not arrive")
	}
}

// P4 break-it row: a CRLF source blob plus core.autocrlf=true in V makes the
// committed oid differ; the postcheck refuses and resets.
func TestCopy_AutocrlfConversionRefusedAndReset(t *testing.T) {
	f := newCopyFix(t)
	f.srcPush(t, "Projects/p/crlf.md", "line one\r\nline two\r\n")
	gitRun(t, f.V, "config", "core.autocrlf", "true")
	head := gitRun(t, f.V, "rev-parse", "HEAD")
	_, err := ApplyCopy(f.req("p"))
	if err == nil || !strings.Contains(err.Error(), "postcheck") {
		t.Fatalf("err = %v, want the footprint postcheck to refuse", err)
	}
	assertCopyAbsent(t, f, head, "p")
}

// rollback: an error after half the files leaves the footprint absent and
// clean, no marker; the re-run succeeds.
func TestCopy_ErrorMidCopyRollsBackAndRerunSucceeds(t *testing.T) {
	f := newCopyFix(t)
	head := gitRun(t, f.V, "rev-parse", "HEAD")
	withCopyHook(t, func(stage string, i int, _ string) error {
		if stage == "copied" && i == 1 {
			return errors.New("injected failure")
		}
		return nil
	})
	if _, err := ApplyCopy(f.req("p")); err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("err = %v", err)
	}
	assertCopyAbsent(t, f, head, "p")
	copyTestHook = nil
	if _, err := ApplyCopy(f.req("p")); err != nil {
		t.Fatalf("the re-run refused: %v", err)
	}
}

// unrelated push: the source gains a commit outside <p> after the dry run;
// the printed line (--at, --expect) still passes.
func TestCopy_UnrelatedSourcePushKeepsTheDigest(t *testing.T) {
	f := newCopyFix(t)
	plan, err := PlanCopy(f.req("p"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Command, "--at "+plan.At) || !strings.Contains(plan.Command, "--expect "+plan.Digest) {
		t.Fatalf("command line %q", plan.Command)
	}
	f.srcPush(t, "Projects/other/new.md", "unrelated\n")
	req := f.req("p")
	req.At, req.Expect = plan.At, plan.Digest
	res, err := ApplyCopy(req)
	if err != nil {
		t.Fatalf("an unrelated push broke the run: %v", err)
	}
	if res.Plan.Digest != plan.Digest || res.Plan.SourceTip == plan.At {
		t.Fatalf("digest %s vs %s; tip %s vs at %s", res.Plan.Digest, plan.Digest, res.Plan.SourceTip, plan.At)
	}
}

// project changed: a commit inside <p> after the dry run refuses.
func TestCopy_ProjectChangedSinceDryRunRefuses(t *testing.T) {
	f := newCopyFix(t)
	plan, err := PlanCopy(f.req("p"))
	if err != nil {
		t.Fatal(err)
	}
	head := gitRun(t, f.V, "rev-parse", "HEAD")
	f.srcPush(t, "Projects/p/sessions/s2.md", "a later session\n")
	// --at alone isolates the moved-tip check from the digest.
	req := f.req("p")
	req.At = plan.At
	if _, err := ApplyCopy(req); err == nil || !strings.Contains(err.Error(), "project p changed since the dry run") {
		t.Fatalf("--at alone: err = %v", err)
	}
	req.Expect = plan.Digest
	if _, err := ApplyCopy(req); err == nil || !strings.Contains(err.Error(), "changed since the dry run") {
		t.Fatalf("--at and --expect: err = %v", err)
	}
	assertCopyAbsent(t, f, head, "p")
}

// --at not an ancestor refuses, and so does an --expect mismatch.
func TestCopy_AtNotAncestorAndExpectMismatchRefuse(t *testing.T) {
	f := newCopyFix(t)
	head := gitRun(t, f.V, "rev-parse", "HEAD")
	req := f.req("p")
	req.At = strings.Repeat("ab", 20)
	if _, err := ApplyCopy(req); err == nil || !strings.Contains(err.Error(), "not an ancestor") {
		t.Fatalf("err = %v", err)
	}
	// A real commit that is not an ancestor: rewrite the source's history.
	old := gitRun(t, f.Src, "rev-parse", "HEAD")
	gitRun(t, f.Src, "commit", "-q", "--amend", "-m", "rewritten")
	gitRun(t, f.Src, "push", "-q", "-f", "origin", "main")
	req.At = old
	if _, err := ApplyCopy(req); err == nil || !strings.Contains(err.Error(), "not an ancestor") {
		t.Fatalf("err = %v", err)
	}
	req.At, req.Expect = "", strings.Repeat("0", 64)
	if _, err := ApplyCopy(req); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("err = %v", err)
	}
	assertCopyAbsent(t, f, head, "p")
}

// scoped clean: dirt in another project of V does not fail the postcheck.
func TestCopy_OtherProjectDirtDoesNotBlock(t *testing.T) {
	f := newCopyFix(t)
	writeFile(t, f.V, "Projects/resident/scratch.md", "uncommitted\n")
	writeFile(t, f.V, "Projects/resident/resume.md", "edited, uncommitted\n")
	if _, err := ApplyCopy(f.req("p")); err != nil {
		t.Fatalf("other-project dirt blocked the copy: %v", err)
	}
	if st := gitRun(t, f.V, "status", "--porcelain", "--", "Projects/resident"); st == "" {
		t.Fatal("the other project's dirt was swept into the copy")
	}
}

// empty footprint, and an absent project, refuse.
func TestCopy_EmptyOrAbsentFootprintRefuses(t *testing.T) {
	f := newCopyFix(t)
	f.srcPush(t, "Projects/husk/.surface", "surface = 7\n")
	head := gitRun(t, f.V, "rev-parse", "HEAD")
	for _, p := range []string{"husk", "nowhere"} {
		if _, err := ApplyCopy(f.req(p)); err == nil || !strings.Contains(err.Error(), "no content file") {
			t.Fatalf("%s: err = %v", p, err)
		}
	}
	assertCopyAbsent(t, f, head, "husk", "nowhere")
}

// Q9a: a second copy refuses, and V is byte- and HEAD-unchanged.
func TestCopy_SecondCopyRefusesAndChangesNothing(t *testing.T) {
	f := newCopyFix(t)
	if _, err := ApplyCopy(f.req("p")); err != nil {
		t.Fatal(err)
	}
	head := gitRun(t, f.V, "rev-parse", "HEAD")
	before := snapshotDir(t, f.V)
	if _, err := ApplyCopy(f.req("p")); err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("err = %v", err)
	}
	if gitRun(t, f.V, "rev-parse", "HEAD") != head {
		t.Fatal("HEAD moved")
	}
	if after := snapshotDir(t, f.V); after != before {
		t.Fatal("V's bytes changed")
	}
}

// I5: copy embeds nothing and touches no cache.
func TestCopy_TouchesNoCache(t *testing.T) {
	f := newCopyFix(t)
	if _, err := ApplyCopy(f.req("p")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"palace/.local", "palace/p/.local"} {
		if pathPresent(filepath.Join(f.V, p)) {
			t.Errorf("copy created %s", p)
		}
	}
}

// killed mid-copy: the marker stands, no commit, partial trees; the re-run
// rolls back this run's trees, clears the marker and succeeds.
func TestCopy_KilledMidCopyRerunRollsBackAndSucceeds(t *testing.T) {
	f := newCopyFix(t)
	withCopyHook(t, func(stage string, i int, _ string) error {
		if stage == "copied" && i == 1 {
			return errCopySimulatedKill
		}
		return nil
	})
	if _, err := ApplyCopy(f.req("p")); !errors.Is(err, errCopySimulatedKill) {
		t.Fatalf("err = %v", err)
	}
	if !markerFound(t, f.V) || !pathPresent(filepath.Join(f.V, "Projects/p")) {
		t.Fatal("the simulated kill left no marker or no partial tree")
	}
	copyTestHook = nil
	res, err := ApplyCopy(f.req("p"))
	if err != nil {
		t.Fatalf("the re-run refused: %v", err)
	}
	if res.Redo != RedoRolledBack || res.Commit == "" || markerFound(t, f.V) {
		t.Fatalf("redo %s, commit %q", res.Redo, res.Commit)
	}
}

// redo, fast-forward: killed after the commit; the re-run publishes the same
// sha and clears the marker.
func TestCopy_KilledAfterCommitRerunPublishesSameSha(t *testing.T) {
	f := newCopyFix(t)
	var sha string
	withCopyHook(t, func(stage string, _ int, path string) error {
		if stage == "publish" {
			sha = path
			return errCopySimulatedKill
		}
		return nil
	})
	if _, err := ApplyCopy(f.req("p")); !errors.Is(err, errCopySimulatedKill) {
		t.Fatalf("err = %v", err)
	}
	copyTestHook = nil
	res, err := ApplyCopy(f.req("p"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Redo != RedoPublished || res.Commit != sha || gitRun(t, f.VBare, "rev-parse", "main") != sha {
		t.Fatalf("redo %s commit %s, want the killed run's %s published", res.Redo, res.Commit, sha)
	}
}

// A pending marker from another copy (other projects) refuses, and its trees
// are left alone: a redo only ever rolls back its own run.
func TestCopy_RedoOnlyForTheSameRun(t *testing.T) {
	f := newCopyFix(t)
	withCopyHook(t, func(stage string, i int, _ string) error {
		if stage == "copied" && i == 1 {
			return errCopySimulatedKill
		}
		return nil
	})
	_, _ = ApplyCopy(f.req("p"))
	copyTestHook = nil
	var pe *LifecyclePendingError
	if _, err := ApplyCopy(f.req("orch")); !errors.As(err, &pe) {
		t.Fatalf("err = %v, want the pending-marker refusal", err)
	}
	if _, err := PlanCopy(f.req("p")); !errors.As(err, &pe) {
		t.Fatalf("a dry run over a pending marker: err = %v", err)
	}
	if !pathPresent(filepath.Join(f.V, "Projects/p")) || !markerFound(t, f.V) {
		t.Fatal("a refused invocation touched the unfinished run")
	}
}

// e4 for copy: another host pushes to V's remote after the copy commit; the
// first push is rejected, the commit reset, the trees rolled back.
func TestCopy_RemoteMovedAfterCommitResetsAndRollsBack(t *testing.T) {
	f := newCopyFix(t)
	withCopyHook(t, func(stage string, _ int, _ string) error {
		if stage == "publish" {
			pushFromOtherHost(t, f.VBare, "Projects/resident/late.md", "late\n")
		}
		return nil
	})
	head := gitRun(t, f.V, "rev-parse", "HEAD")
	var pe *PublishError
	if _, err := ApplyCopy(f.req("p")); !errors.As(err, &pe) || pe.Kind != PublishRemoteMoved {
		t.Fatalf("err = %v, want remote-moved", err)
	}
	if gitRun(t, f.V, "rev-parse", "HEAD") != head || pathPresent(filepath.Join(f.V, "Projects/p")) || markerFound(t, f.V) {
		t.Fatal("the rejected copy was not reset and rolled back")
	}
	if remoteHas(t, f.VBare, "Projects/p/resume.md") {
		t.Fatal("the remote holds the copy")
	}
}

// unpublished state: V ahead of its remote refuses before anything is written.
func TestCopy_UnpublishedDestinationRefuses(t *testing.T) {
	f := newCopyFix(t)
	writeFile(t, f.V, "Projects/resident/local.md", "unpushed\n")
	gitRun(t, f.V, "add", "-A")
	gitRun(t, f.V, "commit", "-q", "-m", "unpushed")
	if _, err := ApplyCopy(f.req("p")); err == nil || !strings.Contains(err.Error(), ErrRemoteNotAtHead.Error()) {
		t.Fatalf("err = %v", err)
	}
	if pathPresent(filepath.Join(f.V, "Projects/p")) || markerFound(t, f.V) {
		t.Fatal("a refused copy wrote to V")
	}
}

// Destination and source refusals.
func TestCopy_DestinationAndSourceRefusals(t *testing.T) {
	f := newCopyFix(t)
	head := gitRun(t, f.V, "rev-parse", "HEAD")

	req := f.req("p")
	req.From = fileURL(f.VBare)
	if _, err := ApplyCopy(req); err == nil {
		t.Fatal("copying from V's own remote was accepted")
	}

	writeFile(t, f.V, "Audits/departures/p.json", `{"format":"vp-departure/1","slug":"p","kind":"moved-to-vault","date":"2026-09-27"}`+"\n")
	gitRun(t, f.V, "add", "-A")
	gitRun(t, f.V, "commit", "-q", "-m", "a record for p")
	gitRun(t, f.V, "push", "-q", "origin", "main")
	if _, err := ApplyCopy(f.req("p")); err == nil || !strings.Contains(err.Error(), "departure record") {
		t.Fatalf("a destination record: err = %v", err)
	}
	head = gitRun(t, f.V, "rev-parse", "HEAD")

	// The source recorded orch as departed and holds only residue for it.
	gitRun(t, f.Src, "rm", "-q", "-r", "Projects/orch")
	writeFile(t, f.Src, "palace/orch/kg/entities.jsonl", "{}\n")
	writeFile(t, f.Src, "Audits/departures/orch.json", `{"format":"vp-departure/1","slug":"orch","kind":"deleted","date":"2026-09-27"}`+"\n")
	gitRun(t, f.Src, "add", "-A")
	gitRun(t, f.Src, "commit", "-q", "-m", "orch departed")
	gitRun(t, f.Src, "push", "-q", "origin", "main")
	if _, err := ApplyCopy(f.req("orch")); err == nil || !strings.Contains(err.Error(), "departed the source") {
		t.Fatalf("a departed source project: err = %v", err)
	}

	f.srcPush(t, ".vibe-palace/vault.toml", fmt.Sprintf("format = %d\n", surface.RequiredDataFormat+1))
	if _, err := ApplyCopy(f.req("other")); err == nil || !strings.Contains(err.Error(), "data format") {
		t.Fatalf("a format mismatch: err = %v", err)
	}
	assertCopyAbsent(t, f, head, "orch", "other")
}

// lock: the copy completes while holding V's root lock once; nothing under it
// re-acquires the lock.
func TestCopy_CompletesUnderItsOwnLock(t *testing.T) {
	f := newCopyFix(t)
	withinDeadline(t, "ApplyCopy", func() {
		if _, err := ApplyCopy(f.req("p")); err != nil {
			t.Error(err)
		}
	})
}

// The copy commit's trailers, Vp-Run included, are what git itself parses
// from the committed message (git interpret-trailers --parse).
func TestCopy_TrailersParseWithInterpretTrailers(t *testing.T) {
	f := newCopyFix(t)
	res, err := ApplyCopy(f.req("p"))
	if err != nil {
		t.Fatal(err)
	}
	msg := gitRun(t, f.V, "log", "-1", "--format=%B", res.Commit)
	cmd := exec.Command("git", "interpret-trailers", "--parse")
	cmd.Stdin = strings.NewReader(msg)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	parsed := string(out)
	for _, key := range []string{TrailerCopyProject + ": p", TrailerCopySource + ": ", TrailerCopySourceCommit + ": ", TrailerCopyFootprint + ": p v1:", lifecycleRunTrailer + ": "} {
		if !strings.Contains(parsed, key) {
			t.Errorf("git does not parse %q from the copy commit:\n%s\nmessage:\n%s", key, parsed, msg)
		}
	}
	if !strings.Contains(msg, "\n[") {
		t.Error("the hostname stamp is missing")
	}
}

// snapshotDir renders every file under dir except .git as path=content.
func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		if info.Mode().IsRegular() {
			data, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dir, p)
			fmt.Fprintf(&b, "%s=%x\n", rel, data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// R2: the copy commit carries the run's Vp-Run trailer, so a run killed
// between its commit and the marker's record of it is adopted by the redo.
func TestCopy_KilledBeforeTheMarkerRecordsTheCommitIsAdopted(t *testing.T) {
	f := newCopyFix(t)
	var sha string
	withCopyHook(t, func(stage string, _ int, path string) error {
		if stage == "committed" {
			sha = path
			return errCopySimulatedKill
		}
		return nil
	})
	if _, err := ApplyCopy(f.req("p")); !errors.Is(err, errCopySimulatedKill) {
		t.Fatalf("err = %v", err)
	}
	m, found, err := readLifecycleMarker(f.V)
	if err != nil || !found || m.Commit != "" {
		t.Fatalf("marker %+v found %v err %v: want one with no commit recorded", m, found, err)
	}
	if run, _ := commitRunTrailer(f.V, sha); run != m.RunID {
		t.Fatalf("the copy commit's Vp-Run is %q, want the run's %q", run, m.RunID)
	}
	copyTestHook = nil
	res, err := ApplyCopy(f.req("p"))
	if err != nil {
		t.Fatalf("the redo refused: %v", err)
	}
	if res.Redo != RedoPublished || res.Commit != sha || gitRun(t, f.VBare, "rev-parse", "main") != sha {
		t.Fatalf("redo %s commit %s, want %s adopted and published", res.Redo, res.Commit, sha)
	}
}

// R3: a 100755 source file keeps its executable bit, so F matches and the
// copy runs (the copy primitive writes 0644).
func TestCopy_PreservesTheExecutableBit(t *testing.T) {
	f := newCopyFix(t)
	writeFile(t, f.Src, "Projects/p/tools/run.sh", "#!/bin/sh\necho hi\n")
	if err := os.Chmod(filepath.Join(f.Src, "Projects/p/tools/run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.Src, "add", "-A")
	gitRun(t, f.Src, "commit", "-q", "-m", "an executable")
	gitRun(t, f.Src, "push", "-q", "origin", "main")
	if _, err := ApplyCopy(f.req("p")); err != nil {
		t.Fatalf("a 100755 file broke the copy: %v", err)
	}
	if got := gitRun(t, f.V, "ls-tree", "HEAD", "Projects/p/tools/run.sh"); !strings.HasPrefix(got, "100755 ") {
		t.Fatalf("committed as %q, want mode 100755", got)
	}
}

// M1: --from is one of V's own remotes, and that remote HOLDS the project.
func TestCopy_FromOneOfTheVaultsOwnRemotesRefuses(t *testing.T) {
	f := newCopyFix(t)
	writeFile(t, f.V, "Projects/p/resume.md", "p lives here already\n")
	gitRun(t, f.V, "add", "-A")
	gitRun(t, f.V, "commit", "-q", "-m", "p in V")
	gitRun(t, f.V, "push", "-q", "origin", "main")
	req := f.req("p")
	req.From = fileURL(f.VBare)
	_, err := PlanCopy(req)
	if err == nil || !strings.Contains(err.Error(), "own remotes") {
		t.Fatalf("err = %v, want the own-remote refusal", err)
	}
}

// M5: a symlink or a submodule in the footprint is refused at the source.
func TestCopy_SymlinkAndSubmoduleRefused(t *testing.T) {
	f := newCopyFix(t)
	if err := os.Symlink("resume.md", filepath.Join(f.Src, "Projects/p/link.md")); err != nil {
		t.Fatal(err)
	}
	sub := gitRun(t, f.Src, "rev-parse", "HEAD")
	gitRun(t, f.Src, "update-index", "--add", "--cacheinfo", "160000,"+sub+",Projects/p/sub")
	gitRun(t, f.Src, "add", "Projects/p/link.md")
	gitRun(t, f.Src, "commit", "-q", "-m", "a symlink and a gitlink")
	gitRun(t, f.Src, "push", "-q", "origin", "main")
	head := gitRun(t, f.V, "rev-parse", "HEAD")
	_, err := ApplyCopy(f.req("p"))
	for _, want := range []string{"Projects/p/link.md is not a regular file at the source (mode 120000)", "Projects/p/sub is not a regular file at the source (mode 160000)"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
	assertCopyAbsent(t, f, head, "p")
}

// M7: dirty footprint paths refuse at preflight, with no tree on disk for the
// membership check to see (a staged addition whose file was then removed).
func TestCopy_DirtyFootprintPathsRefuse(t *testing.T) {
	f := newCopyFix(t)
	writeFile(t, f.V, "Projects/p/staged.md", "staged\n")
	gitRun(t, f.V, "add", "Projects/p/staged.md")
	if err := os.RemoveAll(filepath.Join(f.V, "Projects/p")); err != nil {
		t.Fatal(err)
	}
	head := gitRun(t, f.V, "rev-parse", "HEAD")
	if _, err := ApplyCopy(f.req("p")); err == nil || !strings.Contains(err.Error(), "footprint paths") || !strings.Contains(err.Error(), "not clean") {
		t.Fatalf("err = %v, want the dirty-footprint refusal", err)
	}
	if gitRun(t, f.V, "rev-parse", "HEAD") != head || markerFound(t, f.V) {
		t.Fatal("the refused copy moved HEAD or left a marker")
	}
}

// M9: --from as a host path is refused, even one that git could read.
func TestCopy_FromAHostPathRefuses(t *testing.T) {
	f := newCopyFix(t)
	req := f.req("p")
	req.From = f.SrcBare
	if _, err := PlanCopy(req); err == nil || !strings.Contains(err.Error(), "host path") {
		t.Fatalf("err = %v, want the host-path refusal", err)
	}
}

// M10: V's identity binds the digest: a plan made against one vault does not
// authorise a run against another vault in the same state.
func TestCopy_DigestBindsTheVaultsIdentity(t *testing.T) {
	f := newCopyFix(t)
	plan, err := PlanCopy(f.req("p"))
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	gitRun(t, other, "clone", "-q", fileURL(f.VBare), ".")
	gitRun(t, other, "config", "user.email", "test@example.com")
	gitRun(t, other, "config", "user.name", "Test User")
	otherBare := initBareRemote(t)
	gitRun(t, other, "remote", "set-url", "origin", fileURL(otherBare))
	gitRun(t, other, "push", "-q", "origin", "main")
	req := f.req("p")
	req.Vault, req.At, req.Expect = other, plan.At, plan.Digest
	if _, err := ApplyCopy(req); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("err = %v, want a digest mismatch for another vault", err)
	}
}

// The dry run lists EVERY refusal, not the first.
func TestCopy_DryRunListsEveryRefusal(t *testing.T) {
	f := newCopyFix(t)
	writeFile(t, f.V, "Projects/orch/resume.md", "orch already here\n")
	gitRun(t, f.V, "add", "-A")
	gitRun(t, f.V, "commit", "-q", "-m", "orch in V")
	gitRun(t, f.V, "push", "-q", "origin", "main")
	plan, err := PlanCopy(f.req("nowhere", "orch"))
	var re *CopyRefusalsError
	if !errors.As(err, &re) || len(re.Refusals) != 2 || plan == nil || len(plan.Refusals) != 2 || plan.Command != "" {
		t.Fatalf("err = %v, plan %+v: want both refusals listed and no command line", err, plan)
	}
	if !strings.Contains(err.Error(), "nowhere is absent") || !strings.Contains(err.Error(), "already holds Projects/orch") {
		t.Fatalf("err = %v", err)
	}
}
