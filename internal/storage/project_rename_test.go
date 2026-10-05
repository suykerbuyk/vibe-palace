// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// renFix is a vault V holding project "old" (a full footprint) published to a
// bare remote, at the binary's data format.
type renFix struct {
	V, Bare string
}

// writeOldProject writes a project "old" footprint under v. withPalace adds the
// palace wing; withResume/withWorkflow add those files; withRemote pushes to a
// bare remote and records it in f.Bare.
func newRenFix(t *testing.T, opts ...func(*renOpts)) *renFix {
	t.Helper()
	o := renOpts{palace: true, resume: true, workflow: true, remote: true, bak: true}
	for _, fn := range opts {
		fn(&o)
	}
	v := initTestRepo(t)
	writeFile(t, v, ".vibe-palace/vault.toml", "format = 2\n")
	writeFile(t, v, ".gitignore", "*.bak\npalace/.local/\n")
	// A session with project / note_path / archive identifiers (W1/W2/W3).
	writeFile(t, v, "Projects/old/sessions/2026-10-05-ab-01.md",
		"---\nproject: old\nnote_path: Projects/old/sessions/2026-10-05-ab-01.md\narchive: Projects/old/transcripts/x.manifest.json\n---\nbody that may mention old as prose\n")
	// A note (W1b).
	writeFile(t, v, "Projects/old/notes/n1.md", "---\nproject: old\n---\nnote body\n")
	// A transcript manifest with project_slug / vault_rel_session_note (W4/W5).
	writeFile(t, v, "Projects/old/transcripts/x.manifest.json",
		"{\n  \"project_slug\": \"old\",\n  \"vault_rel_session_note\": \"Projects/old/sessions/2026-10-05-ab-01.md\"\n}\n")
	if o.resume {
		writeFile(t, v, "Projects/old/resume.md", "---\nproject: old\n---\n# old — Working Context\nstuff\n")
	}
	if o.workflow {
		writeFile(t, v, "Projects/old/workflow.md", "# old — Workflow\nflow\n")
	}
	if o.palace {
		writeFile(t, v, "palace/old/kg/entities.jsonl", "{}\n")
	}
	if o.bak {
		writeFile(t, v, "Projects/old/resume.md.bak", "an ignored backup\n")
	}
	gitRun(t, v, "add", "-A")
	gitRun(t, v, "commit", "-q", "-m", "seed old")
	f := &renFix{V: v}
	if o.remote {
		f.Bare = initBareRemote(t)
		gitRun(t, v, "remote", "add", "origin", fileURL(f.Bare))
		gitRun(t, v, "push", "-q", "origin", "main")
	}
	return f
}

type renOpts struct{ palace, resume, workflow, remote, bak bool }

func (f *renFix) req() RenameRequest { return RenameRequest{Vault: f.V, From: "old", To: "new"} }

func renPresent(dir, rel string) bool {
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil
}

func renReadRel(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// TestRename_OneCommitMovesRewritesAndRecords: a real rename moves the
// footprint, rewrites every identifier, writes the renamed record and publishes
// ONE commit — carrying the moves, the rewrites and the record together.
func TestRename_OneCommitMovesRewritesAndRecords(t *testing.T) {
	f := newRenFix(t)
	head0 := gitRun(t, f.V, "rev-parse", "HEAD")

	res, err := ApplyRename(f.req())
	if err != nil {
		t.Fatalf("ApplyRename: %v", err)
	}
	if res.Commit == "" {
		t.Fatal("no commit reported")
	}

	// Exactly one commit landed.
	if n := gitRun(t, f.V, "rev-list", "--count", head0+"..HEAD"); n != "1" {
		t.Errorf("rename made %s commits, want 1", n)
	}
	// Moved: new present, old gone (tracked trees).
	if !renPresent(f.V, "Projects/new/resume.md") || renPresent(f.V, "Projects/old/resume.md") {
		t.Error("footprint not moved from old to new")
	}
	if !renPresent(f.V, "palace/new/kg/entities.jsonl") || renPresent(f.V, "palace/old") {
		t.Error("palace wing not moved")
	}
	// The ignored .bak was carried to the new tree.
	if !renPresent(f.V, "Projects/new/resume.md.bak") {
		t.Error("the ignored .bak backup was not carried to the new tree")
	}
	// Rewrites.
	s := renReadRel(t, f.V, "Projects/new/sessions/2026-10-05-ab-01.md")
	for _, want := range []string{"project: new", "note_path: Projects/new/sessions/2026-10-05-ab-01.md", "archive: Projects/new/transcripts/x.manifest.json"} {
		if !strings.Contains(s, want) {
			t.Errorf("session missing rewrite %q", want)
		}
	}
	man := renReadRel(t, f.V, "Projects/new/transcripts/x.manifest.json")
	if !strings.Contains(man, `"project_slug": "new"`) || !strings.Contains(man, `Projects/new/sessions/`) {
		t.Errorf("manifest not rewritten: %s", man)
	}
	if r := renReadRel(t, f.V, "Projects/new/resume.md"); !strings.Contains(r, "project: new") || !strings.Contains(r, "# new — Working Context") {
		t.Errorf("resume not rewritten: %s", r)
	}
	// The renamed departure record, in the SAME commit (committed, in HEAD).
	rec, found := departure.Read(f.V, "old")
	if !found || rec.Kind != departure.Renamed || rec.To != "new" {
		t.Fatalf("renamed record: found=%v rec=%+v", found, rec)
	}
	if rec.Generation != 1 {
		t.Errorf("first rename generation = %d, want 1", rec.Generation)
	}
	if _, foundInHead, _ := ReadCommittedBlob(f.V, departure.RelPath("old")); !foundInHead {
		t.Error("the renamed record is not committed in HEAD")
	}
	// Commit message and trailers.
	msg := gitRun(t, f.V, "show", "-s", "--format=%B", "HEAD")
	for _, want := range []string{"vault rename: old -> new", "Vp-Rename-From: old", "Vp-Rename-To: new", "Vp-Run:"} {
		if !strings.Contains(msg, want) {
			t.Errorf("commit message missing %q; got:\n%s", want, msg)
		}
	}
	// Published to the remote; marker cleared.
	if tip := gitRun(t, f.Bare, "rev-parse", "main"); tip != gitRun(t, f.V, "rev-parse", "HEAD") {
		t.Error("the remote is not at the rename commit")
	}
	if markerFound(t, f.V) {
		t.Error("the pending marker still stands after a clean publish")
	}
}

// TestRename_FreshTargetRefusals: the new slug must not already exist, as a
// tree or a departure record. Each is a refusal and nothing is written.
func TestRename_FreshTargetRefusals(t *testing.T) {
	t.Run("new tree exists", func(t *testing.T) {
		f := newRenFix(t, func(o *renOpts) {})
		writeFile(t, f.V, "Projects/new/resume.md", "already here\n")
		gitRun(t, f.V, "add", "-A")
		gitRun(t, f.V, "commit", "-q", "-m", "new already exists")
		_, err := ApplyRename(f.req())
		assertRefused(t, err, "already exists")
		if !renPresent(f.V, "Projects/old/resume.md") {
			t.Error("old was moved despite the refusal")
		}
	})
	t.Run("new has a departure record", func(t *testing.T) {
		f := newRenFix(t, func(o *renOpts) {})
		writeFile(t, f.V, departure.RelPath("new"), `{"format":"vp-departure/1","slug":"new","kind":"deleted","date":"2026-10-05"}`+"\n")
		_, err := ApplyRename(f.req())
		assertRefused(t, err, "departure record for the new slug")
	})
}

// TestRename_DigestExpectBindsThePlan: --expect must equal the plan's digest;
// a mismatch, and a plan that changed since the dry run, both refuse.
func TestRename_DigestExpectBindsThePlan(t *testing.T) {
	f := newRenFix(t)
	plan, err := PlanRename(f.req())
	if err != nil {
		t.Fatalf("PlanRename: %v", err)
	}
	if plan.Digest == "" {
		t.Fatal("empty digest")
	}
	// A wrong digest refuses.
	bad := f.req()
	bad.Expect = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	_, err = ApplyRename(bad)
	assertRefused(t, err, "digest mismatch")
	// The right digest runs.
	good := f.req()
	good.Expect = plan.Digest
	if _, err := ApplyRename(good); err != nil {
		t.Fatalf("ApplyRename with the plan's digest: %v", err)
	}
}

// TestRename_DigestChangesWhenTheMoveSetChanges: a new tracked file under the
// source tree since the dry run changes the digest, so the old --expect refuses.
func TestRename_DigestChangesWhenTheMoveSetChanges(t *testing.T) {
	f := newRenFix(t, func(o *renOpts) {})
	plan, err := PlanRename(f.req())
	if err != nil {
		t.Fatalf("PlanRename: %v", err)
	}
	writeFile(t, f.V, "Projects/old/notes/n2.md", "---\nproject: old\n---\nanother note\n")
	gitRun(t, f.V, "add", "-A")
	gitRun(t, f.V, "commit", "-q", "-m", "a note added after the dry run")
	gitRun(t, f.V, "push", "-q", "origin", "main")
	req := f.req()
	req.Expect = plan.Digest
	_, err = ApplyRename(req)
	assertRefused(t, err, "digest mismatch")
}

// TestRename_RefusesWhenNotAtRemoteTip is the no-`--no-push` guarantee: a
// rename never leaves a local commit for a later `vp vault sync` to rebase —
// the e8 hazard, where a rebase replays a kept-local rename over a concurrent
// Projects/<old>/ append and smuggles the un-rewritten text into <new>. The
// command refuses BEFORE it writes anything whenever HEAD is not at every
// remote's tip, so no such local commit can exist. Here the remote is moved
// ahead; the rename must refuse and change nothing.
func TestRename_RefusesWhenNotAtRemoteTip(t *testing.T) {
	f := newRenFix(t)
	head0 := gitRun(t, f.V, "rev-parse", "HEAD")
	// Move the remote ahead of V, independently.
	clone := t.TempDir()
	gitRun(t, clone, "clone", "-q", f.Bare, ".")
	gitRun(t, clone, "config", "user.email", "o@example.com")
	gitRun(t, clone, "config", "user.name", "Other")
	writeFile(t, clone, "unrelated.md", "a commit from elsewhere\n")
	gitRun(t, clone, "add", "-A")
	gitRun(t, clone, "commit", "-q", "-m", "remote moved")
	gitRun(t, clone, "push", "-q", "origin", "main")

	_, err := ApplyRename(f.req())
	if err == nil {
		t.Fatal("rename succeeded though V was behind its remote")
	}
	// No local rename commit was created, and nothing moved.
	if h := gitRun(t, f.V, "rev-parse", "HEAD"); h != head0 {
		t.Errorf("HEAD moved to %s though the rename should have refused before writing", h)
	}
	if !renPresent(f.V, "Projects/old/resume.md") || renPresent(f.V, "Projects/new/resume.md") {
		t.Error("the footprint moved despite the refusal")
	}
	if _, found := departure.Read(f.V, "old"); found {
		t.Error("a departure record was written despite the refusal")
	}
	if markerFound(t, f.V) {
		t.Error("a pending marker was left though nothing ran")
	}
}

// TestRename_ZeroOldPostcheck tests the zero-<old> identifier scan directly: it
// passes over a cleanly rewritten tree and catches a residual identifier.
func TestRename_ZeroOldPostcheck(t *testing.T) {
	f := newRenFix(t, func(o *renOpts) {})
	if _, err := ApplyRename(f.req()); err != nil {
		t.Fatalf("ApplyRename: %v", err)
	}
	p := &SlugPlan{k1Moves: []SlugMove{{Dst: "Projects/new/sessions/2026-10-05-ab-01.md"}, {Dst: "Projects/new/resume.md"}}}
	if err := renameZeroOld(f.V, "old", "new", p); err != nil {
		t.Errorf("zero-old scan flags a clean tree: %v", err)
	}
	// Plant a residual identifier and confirm the scan catches it.
	writeFile(t, f.V, "Projects/new/sessions/2026-10-05-ab-01.md", "---\nproject: old\n---\nbody\n")
	if err := renameZeroOld(f.V, "old", "new", p); err == nil {
		t.Error("zero-old scan missed a residual project: old identifier")
	} else if !strings.Contains(err.Error(), "still name the old slug") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestRename_DataFormatRefusal: a vault at the wrong data format refuses.
func TestRename_DataFormatRefusal(t *testing.T) {
	f := newRenFix(t, func(o *renOpts) {})
	writeFile(t, f.V, ".vibe-palace/vault.toml", "format = 1\n")
	gitRun(t, f.V, "add", "-A")
	gitRun(t, f.V, "commit", "-q", "-m", "downgrade format for the test")
	_, err := ApplyRename(f.req())
	assertRefused(t, err, "data format 1")
}

// TestRename_Fixtures covers the parameterized shapes the plan names: a
// Projects-only project (no palace wing), a project with no resume/workflow,
// and a live vault with no remote (the rename commits locally, publishing to
// zero remotes).
func TestRename_Fixtures(t *testing.T) {
	cases := []struct {
		name string
		opts func(*renOpts)
	}{
		{"full with remote", func(o *renOpts) {}},
		{"projects-only", func(o *renOpts) { o.palace = false }},
		{"no resume or workflow", func(o *renOpts) { o.resume = false; o.workflow = false }},
		{"no bak", func(o *renOpts) { o.bak = false }},
		{"no remote (local-only)", func(o *renOpts) { o.remote = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRenFix(t, tc.opts)
			if _, err := ApplyRename(f.req()); err != nil {
				t.Fatalf("ApplyRename: %v", err)
			}
			if !renPresent(f.V, "Projects/new/sessions/2026-10-05-ab-01.md") || renPresent(f.V, "Projects/old") {
				t.Error("footprint not renamed")
			}
			if rec, found := departure.Read(f.V, "old"); !found || rec.To != "new" {
				t.Errorf("renamed record missing or wrong: found=%v %+v", found, rec)
			}
			if markerFound(t, f.V) {
				t.Error("marker left standing")
			}
		})
	}
}

// TestRename_LocalOnly: a vault with NO remote renames locally and commits,
// publishing nothing (operator ruling 2026-10-05; no remote means no e8
// rebase-over-append hazard). This is the one-shot's "LIVE vault with no remote"
// shape, now a supported rename.
func TestRename_LocalOnly(t *testing.T) {
	f := newRenFix(t, func(o *renOpts) { o.remote = false })
	// No remote configured.
	if remotes := gitRun(t, f.V, "remote"); remotes != "" {
		t.Fatalf("fixture has a remote %q", remotes)
	}
	head0 := gitRun(t, f.V, "rev-parse", "HEAD")

	res, err := ApplyRename(f.req())
	if err != nil {
		t.Fatalf("local-only ApplyRename: %v", err)
	}
	if res.Commit == "" {
		t.Fatal("no commit reported for a local-only rename")
	}
	// A rename commit landed on HEAD (one commit past the start).
	if n := gitRun(t, f.V, "rev-list", "--count", head0+"..HEAD"); n != "1" {
		t.Errorf("local-only rename made %s commits, want 1", n)
	}
	if h := gitRun(t, f.V, "rev-parse", "HEAD"); h != res.Commit && res.Commit != gitRun(t, f.V, "rev-parse", "--short", "HEAD") {
		// res.Commit is the full/verify sha; HEAD must be that commit.
		if !strings.HasPrefix(h, res.Commit) && !strings.HasPrefix(res.Commit, h) {
			t.Errorf("HEAD %s is not the rename commit %s", h, res.Commit)
		}
	}
	// Moves, rewrites and the record all landed.
	if !renPresent(f.V, "Projects/new/resume.md") || renPresent(f.V, "Projects/old") {
		t.Error("footprint not renamed in the local-only run")
	}
	if !strings.Contains(renReadRel(t, f.V, "Projects/new/resume.md"), "project: new") {
		t.Error("identifiers not rewritten in the local-only run")
	}
	if rec, found := departure.Read(f.V, "old"); !found || rec.Kind != departure.Renamed || rec.To != "new" {
		t.Errorf("renamed record missing/wrong: found=%v %+v", found, rec)
	}
	// Nothing published, and the marker is cleared (no remote to confirm).
	if markerFound(t, f.V) {
		t.Error("a pending marker is left standing after a local-only rename")
	}
	// No undo-push lines, since there was nothing to publish.
	for _, l := range res.Undo {
		if strings.Contains(l, "push") {
			t.Errorf("local-only rename reported a push undo line: %q", l)
		}
	}
}

// TestRename_GenerationMonotonicAcrossUndo: the renamed record's generation is
// derived from HEAD history, so a rename, a revert of it, and a re-rename never
// reuse a generation. This is the whole reason RecordDepartureForRename reads
// history rather than the working tree.
func TestRename_GenerationMonotonicAcrossUndo(t *testing.T) {
	// local-only keeps the test simple; no .bak so the revert leaves no ignored
	// residue under the new tree.
	f := newRenFix(t, func(o *renOpts) { o.remote = false; o.bak = false })
	if _, err := ApplyRename(f.req()); err != nil {
		t.Fatalf("first rename: %v", err)
	}
	rec1, _ := departure.Read(f.V, "old")
	if rec1.Generation != 1 {
		t.Fatalf("first rename generation = %d, want 1", rec1.Generation)
	}
	// Undo the rename with plain git: restores the tracked Projects/old files and
	// removes the new ones and the record from the working tree — but HEAD
	// history keeps the record (that is what the generation walk reads).
	gitRun(t, f.V, "revert", "--no-edit", "HEAD")
	if !renPresent(f.V, "Projects/old/resume.md") || renPresent(f.V, "Projects/new/resume.md") {
		t.Fatal("the revert did not restore old / remove new (tracked files)")
	}
	if _, found := departure.Read(f.V, "old"); found {
		t.Fatal("the revert left the departure record in the working tree")
	}
	// Re-rename old -> new.
	if _, err := ApplyRename(f.req()); err != nil {
		t.Fatalf("second rename: %v", err)
	}
	rec2, found := departure.Read(f.V, "old")
	if !found {
		t.Fatal("second rename wrote no record")
	}
	if rec2.Generation <= rec1.Generation {
		t.Errorf("second rename generation = %d, want strictly greater than %d (reused generation)", rec2.Generation, rec1.Generation)
	}
	if rec2.Generation != 2 {
		t.Errorf("second rename generation = %d, want 2", rec2.Generation)
	}
}

// TestRename_ExactPublishFailureRollsBackAndClearsRecord: a first-remote
// publish failure rolls the commit back (old restored) AND removes the
// rename-pending record — the "removed on abort" guarantee (SF1).
func TestRename_ExactPublishFailureRollsBackAndClearsRecord(t *testing.T) {
	f := newRenFix(t)
	head0 := gitRun(t, f.V, "rev-parse", "HEAD")
	// At the publish stage — after the commit, before exactPublish — move the
	// remote ahead so the non-force push is rejected (remote-moved → rollback).
	renameTestHook = func(stage string) error {
		if stage == "publish" {
			clone := t.TempDir()
			gitRun(t, clone, "clone", "-q", f.Bare, ".")
			gitRun(t, clone, "config", "user.email", "o@example.com")
			gitRun(t, clone, "config", "user.name", "Other")
			writeFile(t, clone, "unrelated.md", "a commit from elsewhere\n")
			gitRun(t, clone, "add", "-A")
			gitRun(t, clone, "commit", "-q", "-m", "remote moved mid-publish")
			gitRun(t, clone, "push", "-q", "origin", "main")
		}
		return nil
	}
	t.Cleanup(func() { renameTestHook = nil })

	_, err := ApplyRename(f.req())
	if err == nil {
		t.Fatal("expected a publish failure")
	}
	// Commit rolled back.
	if h := gitRun(t, f.V, "rev-parse", "HEAD"); h != head0 {
		t.Errorf("HEAD is %s, want the pre-rename %s (commit not rolled back)", h, head0)
	}
	if !renPresent(f.V, "Projects/old/resume.md") || renPresent(f.V, "Projects/new/resume.md") {
		t.Error("the footprint was not restored after the rolled-back publish")
	}
	// The rename-pending record is gone (the abort removed it).
	if _, ok, perr := NewVault(f.V).RenamePending("old"); perr != nil {
		t.Fatal(perr)
	} else if ok {
		t.Error("the rename-pending record survived a rolled-back publish (SF1)")
	}
}

func assertRefused(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a refusal containing %q, got nil", substr)
	}
	if !errors.Is(err, ErrRenameRefused) {
		t.Fatalf("expected ErrRenameRefused, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("refusal %q does not contain %q", err.Error(), substr)
	}
}
