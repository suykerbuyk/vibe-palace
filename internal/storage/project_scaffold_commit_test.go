// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// initOpts is `vp init`'s rule: commit the stamp with the markers.
var initOpts = ScaffoldCommitOptions{IncludeStamp: true}

// layScaffold writes what the project-scaffold reconciler writes: the two
// marker READMEs at the current stub bytes, and the .surface stamp the write
// leaves beside them.
func layScaffold(t *testing.T, dir, project string) {
	t.Helper()
	writeFile(t, dir, "Projects/"+project+"/commands/README.md", templates.RenderReadmeStub("commands"))
	writeFile(t, dir, "Projects/"+project+"/skills/README.md", templates.RenderReadmeStub("skills"))
	writeFile(t, dir, "Projects/"+project+"/.surface", "surface = 8\n")
}

// scaffoldCommitFiles lists the paths HEAD's commit touched.
func scaffoldCommitFiles(t *testing.T, dir string) []string {
	t.Helper()
	out := gitRun(t, dir, "show", "--name-only", "--format=", "HEAD")
	files := strings.Split(strings.TrimSpace(out), "\n")
	slices.Sort(files)
	return files
}

func TestCommitProjectScaffold_CommitsMarkersAndStampOnly(t *testing.T) {
	dir := initTestRepo(t)
	layScaffold(t, dir, "alpha")
	writeFile(t, dir, "Projects/other/x.md", "not the scaffold\n")
	writeFile(t, dir, "README.md", "seed, edited\n")

	sc, err := CommitProjectScaffold(dir, "alpha", initOpts)
	if err != nil {
		t.Fatalf("CommitProjectScaffold: %v", err)
	}
	if !sc.Committed || sc.SHA == "" {
		t.Fatalf("want a commit, got %+v", sc)
	}
	want := []string{"Projects/alpha/.surface", "Projects/alpha/commands/README.md", "Projects/alpha/skills/README.md"}
	if got := scaffoldCommitFiles(t, dir); !slices.Equal(got, want) {
		t.Errorf("commit touched %q, want exactly %q", got, want)
	}
	if st := gitRun(t, dir, "status", "--porcelain", "--", "Projects/alpha"); st != "" {
		t.Errorf("Projects/alpha still dirty: %q", st)
	}
	// Never git add -A: unrelated dirt is untouched.
	st := gitRun(t, dir, "status", "--porcelain")
	for _, want := range []string{"?? Projects/other/", "M README.md"} {
		if !strings.Contains(st, want) {
			t.Errorf("unrelated dirt %q was committed or lost; status:\n%s", want, st)
		}
	}
}

func TestCommitProjectScaffold_ConvergedIsNoOp(t *testing.T) {
	dir := initTestRepo(t)
	layScaffold(t, dir, "alpha")
	if _, err := CommitProjectScaffold(dir, "alpha", initOpts); err != nil {
		t.Fatal(err)
	}
	head := gitRun(t, dir, "rev-parse", "HEAD")
	sc, err := CommitProjectScaffold(dir, "alpha", initOpts)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Committed || gitRun(t, dir, "rev-parse", "HEAD") != head {
		t.Errorf("a converged scaffold made a commit: %+v", sc)
	}
}

// An edited marker and an older binary's stub text are the same case: bytes vp
// does not vouch for. Neither is committed, and each is named in Kept.
func TestCommitProjectScaffold_NonStubMarkerIsKeptNotCommitted(t *testing.T) {
	for name, body := range map[string]string{
		"operator edit": "# my own notes\n",
		"older stub":    strings.Replace(templates.RenderReadmeStub("skills"), "skill", "persona", 1),
	} {
		t.Run(name, func(t *testing.T) {
			dir := initTestRepo(t)
			layScaffold(t, dir, "alpha")
			writeFile(t, dir, "Projects/alpha/skills/README.md", body)
			head := gitRun(t, dir, "rev-parse", "HEAD")

			sc, err := CommitProjectScaffold(dir, "alpha", initOpts)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(sc.Kept, []string{"Projects/alpha/skills/README.md"}) {
				t.Errorf("Kept = %q, want the skills marker", sc.Kept)
			}
			if gitRun(t, dir, "rev-parse", "HEAD") == head {
				t.Fatal("the stub marker beside it was not committed")
			}
			if got := scaffoldCommitFiles(t, dir); slices.Contains(got, "Projects/alpha/skills/README.md") {
				t.Errorf("a non-stub marker was committed: %q", got)
			}
			if st := gitRun(t, dir, "status", "--porcelain", "--", "Projects/alpha/skills/README.md"); st == "" {
				t.Error("the kept marker is no longer dirty")
			}
		})
	}
}

func TestCommitProjectScaffold_OnlyNonStubMarkersMakesNoCommit(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "Projects/alpha/commands/README.md", "mine\n")
	writeFile(t, dir, "Projects/alpha/.surface", "surface = 8\n")
	head := gitRun(t, dir, "rev-parse", "HEAD")
	sc, err := CommitProjectScaffold(dir, "alpha", initOpts)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Committed || gitRun(t, dir, "rev-parse", "HEAD") != head {
		t.Errorf("committed with no stub marker eligible: %+v", sc)
	}
	if !slices.Equal(sc.Kept, []string{"Projects/alpha/commands/README.md"}) {
		t.Errorf("Kept = %q", sc.Kept)
	}
}

// A dirty stamp alone must never commit: every vault write in the process
// stamps it, so it would label an unrelated write's churn as a scaffold.
func TestCommitProjectScaffold_StampOnlyJoinsAnActualCommit(t *testing.T) {
	dir := initTestRepo(t)
	layScaffold(t, dir, "alpha")
	if _, err := CommitProjectScaffold(dir, "alpha", initOpts); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "Projects/alpha/.surface", "surface = 9\n")
	head := gitRun(t, dir, "rev-parse", "HEAD")
	sc, err := CommitProjectScaffold(dir, "alpha", initOpts)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Committed || gitRun(t, dir, "rev-parse", "HEAD") != head {
		t.Errorf("a stamp-only change committed: %+v", sc)
	}
}

func TestCommitProjectScaffold_GitDisabledRunsNoGit(t *testing.T) {
	dir := initTestRepo(t)
	layScaffold(t, dir, "alpha")
	hostConfig(t, str("git_enabled = false\n"))
	head := gitRun(t, dir, "rev-parse", "HEAD")
	_, err := CommitProjectScaffold(dir, "alpha", initOpts)
	if !errors.Is(err, ErrGitDisabled) {
		t.Fatalf("err = %v, want ErrGitDisabled", err)
	}
	if gitRun(t, dir, "rev-parse", "HEAD") != head {
		t.Error("HEAD moved on a git-disabled host")
	}
	if st := gitRun(t, dir, "diff", "--cached", "--name-only"); st != "" {
		t.Errorf("index changed on a git-disabled host: %q", st)
	}

	// The refusal comes BEFORE any git runs: with no git reachable at all, a
	// probe would read the vault as non-git (clean) and return nil. Only the
	// up-front gate can still answer ErrGitDisabled.
	t.Setenv("PATH", t.TempDir())
	if _, err := CommitProjectScaffold(dir, "alpha", initOpts); !errors.Is(err, ErrGitDisabled) {
		t.Fatalf("with no git on PATH: err = %v, want ErrGitDisabled from the up-front gate", err)
	}
}

func TestCommitProjectScaffold_NonGitVaultIsANoOp(t *testing.T) {
	dir := t.TempDir()
	layScaffold(t, dir, "alpha")
	sc, err := CommitProjectScaffold(dir, "alpha", ScaffoldCommitOptions{IncludeStamp: true, Wrote: true})
	if err != nil || sc.Committed || len(sc.Kept) > 0 || !strings.Contains(sc.Skipped, "not a git repository") {
		t.Errorf("non-git vault, scaffold written this run: %+v, %v", sc, err)
	}
}

// Review G2: with nothing scaffolded on this run, a vault-wide reason is not
// returned — otherwise every converged `vp init` / `vp config sync` on a
// non-git (or nested) vault would print "not committed" for a commit it had
// nothing to make.
func TestCommitProjectScaffold_NoWriteNoVaultWideReason(t *testing.T) {
	dir := t.TempDir()
	layScaffold(t, dir, "alpha")
	sc, err := CommitProjectScaffold(dir, "alpha", initOpts)
	if err != nil || sc.Committed || sc.Skipped != "" {
		t.Errorf("clean non-git vault, nothing written: %+v, %v — want no reason", sc, err)
	}
}

// Review C2: a vault that is a directory inside ANOTHER repository's work
// tree. Its paths probe dirty against the enclosing repository, but vp must
// never stage into that repository: Skipped, no error, nothing committed.
func TestCommitProjectScaffold_NestedVaultIsSkippedNotAnError(t *testing.T) {
	repo := initTestRepo(t)
	vault := repo + "/vault2"
	layScaffold(t, vault, "alpha")
	head := gitRun(t, repo, "rev-parse", "HEAD")

	sc, err := CommitProjectScaffold(vault, "alpha", ScaffoldCommitOptions{IncludeStamp: true, Wrote: true})
	if err != nil {
		t.Fatalf("nested vault: err = %v, want a skip", err)
	}
	if sc.Committed || !strings.Contains(sc.Skipped, "inside another repository") {
		t.Errorf("nested vault: %+v, want Skipped naming the enclosing repository", sc)
	}
	if gitRun(t, repo, "rev-parse", "HEAD") != head {
		t.Error("committed into the enclosing repository")
	}
	if st := gitRun(t, repo, "diff", "--cached", "--name-only"); st != "" {
		t.Errorf("staged into the enclosing repository: %q", st)
	}
}

// Review C1, unit half: under RequireTrackedStamp (config sync's rule) a
// project whose .surface is untracked — a stray nobody initialised — gets no
// commit at all, markers included.
func TestCommitProjectScaffold_RequireTrackedStampSkipsAnUntrackedStamp(t *testing.T) {
	dir := initTestRepo(t)
	layScaffold(t, dir, "stray")
	writeFile(t, dir, "Projects/stray/sessions/x.md", "captured\n")
	head := gitRun(t, dir, "rev-parse", "HEAD")

	sc, err := CommitProjectScaffold(dir, "stray", ScaffoldCommitOptions{RequireTrackedStamp: true})
	if err != nil {
		t.Fatal(err)
	}
	if sc.Committed || gitRun(t, dir, "rev-parse", "HEAD") != head {
		t.Errorf("committed for a stray: %+v", sc)
	}
	if !strings.Contains(sc.Skipped, "Projects/stray/.surface is not tracked") || !strings.Contains(sc.Skipped, "run `vp init` in this project's checkout") {
		t.Errorf("Skipped = %q, want the untracked-stamp reason", sc.Skipped)
	}
}

// RequireTrackedStamp with a tracked stamp commits the markers — and, without
// IncludeStamp, never the stamp, even when it is dirty.
func TestCommitProjectScaffold_TrackedStampCommitsMarkersOnly(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "Projects/beta/.surface", "surface = 8\n")
	gitRun(t, dir, "add", "Projects/beta/.surface")
	gitRun(t, dir, "commit", "-q", "-m", "beta stamp")
	layScaffold(t, dir, "beta")
	writeFile(t, dir, "Projects/beta/.surface", "surface = 9\n")

	sc, err := CommitProjectScaffold(dir, "beta", ScaffoldCommitOptions{RequireTrackedStamp: true})
	if err != nil || !sc.Committed {
		t.Fatalf("want a commit: %+v, %v", sc, err)
	}
	want := []string{"Projects/beta/commands/README.md", "Projects/beta/skills/README.md"}
	if got := scaffoldCommitFiles(t, dir); !slices.Equal(got, want) {
		t.Errorf("commit touched %q, want exactly %q", got, want)
	}
	if st := gitRun(t, dir, "status", "--porcelain", "--", "Projects/beta/.surface"); !strings.Contains(st, "M Projects/beta/.surface") {
		t.Errorf("the stamp was committed or lost: %q", st)
	}
}

// git collapses an untracked directory to ONE porcelain line, so a marker that
// does not exist inside one still probes dirty. It must be neither committed
// nor kept: here only commands/README.md exists, at the stub bytes.
func TestCommitProjectScaffold_AbsentMarkerInAnUntrackedDirIsNeitherCommittedNorKept(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "Projects/alpha/commands/README.md", templates.RenderReadmeStub("commands"))

	sc, err := CommitProjectScaffold(dir, "alpha", initOpts)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Kept) != 0 {
		t.Errorf("Kept = %q, want none — an absent, never-tracked marker is nothing to keep", sc.Kept)
	}
	if got := scaffoldCommitFiles(t, dir); !slices.Equal(got, []string{"Projects/alpha/commands/README.md"}) {
		t.Errorf("commit touched %q, want only the commands marker", got)
	}
}

func TestCommitProjectScaffold_RejectsAnInvalidSlug(t *testing.T) {
	if _, err := CommitProjectScaffold(t.TempDir(), "../escape", initOpts); err == nil {
		t.Error("an invalid slug was accepted")
	}
}
