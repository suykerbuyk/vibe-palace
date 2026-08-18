// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// archiveRepo builds a real git project repo (with a .vibe-palace.toml naming
// it) and commits each message in msgs as an empty commit after a root commit.
// It returns the repo dir. The tree is left CLEAN — mirroring the
// feature-branch flow where the commit has ALREADY landed by wrap time.
func archiveRepo(t *testing.T, name string, msgs ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.ConfigFileName),
		[]byte("[project]\nname = \""+name+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("commit", "--allow-empty", "-m", "root")
	for _, m := range msgs {
		f := filepath.Join(dir, "MSG")
		if err := os.WriteFile(f, []byte(m), 0o644); err != nil {
			t.Fatal(err)
		}
		run("commit", "--allow-empty", "-F", f)
	}
	// MSG is scratch — drop it so the tree is clean like a real post-commit
	// feature branch.
	_ = os.Remove(filepath.Join(dir, "MSG"))
	return dir
}

func runArchive(t *testing.T, handler func(context.Context, json.RawMessage) (any, error), projDir string) map[string]any {
	t.Helper()
	params, _ := json.Marshal(map[string]string{"project_path": projDir})
	res, err := handler(context.Background(), params)
	if err != nil {
		t.Fatalf("archive handler: %v", err)
	}
	return res.(map[string]any)
}

// TestArchiveCommitLog_FeatureBranchFlow is the DoD test: a feature-branch
// commit has already LANDED (clean tree), and the archive-at-wrap step — not a
// per-commit ingest — must carry its full message into commit-log.md with no
// call-order discipline. It also covers the empty-anchor first run and
// multi-line body integrity.
func TestArchiveCommitLog_FeatureBranchFlow(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ArchiveCommitLogTool(vault)

	multi := "feat: land it\n\nThis body spans\nseveral lines.\n\n- one\n- two\n\nCloses #7"
	projDir := archiveRepo(t, "feat-proj", "chore: earlier\n", multi)

	res := runArchive(t, tool.Handler, projDir)
	if got := res["commits_archived"]; got != 2 {
		t.Fatalf("commits_archived = %v, want 2 (root excluded on first run)", got)
	}
	if res["anchor_from"] != "" {
		t.Errorf("anchor_from = %v, want empty on first run", res["anchor_from"])
	}

	logPath, _ := vault.CommitLogFile("feat-proj")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read commit-log.md: %v", err)
	}
	log := string(data)
	if !strings.Contains(log, strings.TrimRight(multi, "\n")) {
		t.Errorf("commit-log.md missing the full multi-line message:\n%s", log)
	}
	if !strings.Contains(log, "chore: earlier") {
		t.Errorf("commit-log.md missing the earlier commit:\n%s", log)
	}

	// The anchor advanced to HEAD.
	anchor, err := vault.ReadCommitLogAnchor("feat-proj")
	if err != nil {
		t.Fatal(err)
	}
	if anchor != res["anchor_to"] {
		t.Errorf("anchor file = %q, want HEAD %v", anchor, res["anchor_to"])
	}
}

// TestArchiveCommitLog_Idempotent — a second run with no new commits appends
// nothing and leaves commit-log.md byte-identical, and the anchor unmoved.
func TestArchiveCommitLog_Idempotent(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ArchiveCommitLogTool(vault)
	projDir := archiveRepo(t, "idem-proj", "feat: only one\n")

	first := runArchive(t, tool.Handler, projDir)
	if first["commits_archived"] != 1 {
		t.Fatalf("first run archived %v, want 1", first["commits_archived"])
	}
	logPath, _ := vault.CommitLogFile("idem-proj")
	before, _ := os.ReadFile(logPath)
	anchorBefore, _ := vault.ReadCommitLogAnchor("idem-proj")

	second := runArchive(t, tool.Handler, projDir)
	if second["commits_archived"] != 0 {
		t.Errorf("second run archived %v, want 0 (idempotent)", second["commits_archived"])
	}
	after, _ := os.ReadFile(logPath)
	if string(before) != string(after) {
		t.Errorf("commit-log.md changed on a no-op re-run:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	anchorAfter, _ := vault.ReadCommitLogAnchor("idem-proj")
	if anchorBefore != anchorAfter {
		t.Errorf("anchor moved on no-op re-run: %q -> %q", anchorBefore, anchorAfter)
	}
}

// TestArchiveCommitLog_AnchorAdvances — after a first archive, a NEW commit is
// archived on the next run and only that one, proving the anchor is the cursor.
func TestArchiveCommitLog_AnchorAdvances(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ArchiveCommitLogTool(vault)
	projDir := archiveRepo(t, "adv-proj", "feat: first landed\n")

	runArchive(t, tool.Handler, projDir)

	// Land another commit, then archive again.
	cmd := exec.Command("git", "-C", projDir, "commit", "--allow-empty", "-m", "feat: second landed")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second commit: %v\n%s", err, out)
	}

	second := runArchive(t, tool.Handler, projDir)
	if second["commits_archived"] != 1 {
		t.Errorf("second archive = %v, want 1 (only the new commit)", second["commits_archived"])
	}
	logPath, _ := vault.CommitLogFile("adv-proj")
	data, _ := os.ReadFile(logPath)
	log := string(data)
	if !strings.Contains(log, "feat: second landed") {
		t.Errorf("second commit not archived:\n%s", log)
	}
	// The first must appear exactly once (not duplicated by the second run).
	if n := strings.Count(log, "feat: first landed"); n != 1 {
		t.Errorf("first commit appears %d times, want exactly 1", n)
	}
}

// TestArchiveCommitLog_NonRepo — a non-repo project_path is a no-op, not an
// error: the wrap must never fail because a project happens not to be a git
// repo.
func TestArchiveCommitLog_NonRepo(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ArchiveCommitLogTool(vault)
	projDir := newProjectDir(t, "norepo") // t.TempDir(), no git

	res := runArchive(t, tool.Handler, projDir)
	if res["commits_archived"] != 0 {
		t.Errorf("non-repo archived %v, want 0", res["commits_archived"])
	}
}

// TestArchiveCommitLog_MissingProjectPath is refused.
func TestArchiveCommitLog_MissingProjectPath(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ArchiveCommitLogTool(vault)
	params, _ := json.Marshal(map[string]string{})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for missing project_path")
	}
}

// TestArchiveCommitLog_RefusesUnmanagedDir guards the write-authorization gate
// wiring on this tool: a git repo with no marker and no existing vault project
// must be refused, not scaffolded into a phantom Projects/<slug>/.
func TestArchiveCommitLog_RefusesUnmanagedDir(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ArchiveCommitLogTool(vault)
	projDir := newGitProjectRepoNoMarker(t, false) // committed git repo, no marker

	params, _ := json.Marshal(map[string]string{"project": "junk-project", "project_path": projDir})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected refusal for an unmanaged dir")
	} else if !strings.Contains(err.Error(), "run `vp init`") {
		t.Errorf("refusal message = %q, want it to point at vp init", err)
	}
	if _, serr := os.Stat(filepath.Join(vault.Root, "Projects", "junk-project")); !os.IsNotExist(serr) {
		t.Errorf("vault project scaffolded despite refusal (stat err = %v)", serr)
	}
}

// gitCommitT runs a git command that needs an author/committer identity.
// gitT deliberately carries none, and archiveRepo's identity lives in a local
// closure, so tests that land a commit AFTER archiveRepo returns need this.
func gitCommitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

// setAnchor rewinds the host-local cursor to sha, standing in for the state a
// SECOND host brings to a SHARED commit-log.md: its own anchor is older than
// the log, because the log was advanced by somebody else's wrap.
func setAnchor(t *testing.T, vault *storage.Vault, slug, sha string) {
	t.Helper()
	p, err := vault.CommitLogAnchorFile(slug)
	if err != nil {
		t.Fatal(err)
	}
	// Projects/<slug>/ is created lazily by the first archive; these fixtures
	// set an anchor BEFORE any archive has run.
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(sha+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestArchiveCommitLog_SkipsSHAsTheLogAlreadyHolds is the Cause 1 test, and it
// is the one TestArchiveCommitLog_Idempotent could never be.
//
// That test re-runs with the anchor AT HeAD, so the walk is HEAD..HEAD — empty
// — and the append loop never executes. It proves the walk yields nothing; it
// says nothing about what the writer does when the walk yields a SHA the file
// already carries. This test constructs exactly that: commit-log.md is current
// through HEAD, and then the anchor is rewound the way a second host's
// host-local cursor legitimately is. The walk is non-empty and every SHA in it
// is already in the shared file.
//
// Mutation: drop the seen-check in ArchiveCommitBodies and this goes red with
// duplicate entries; TestArchiveCommitLog_Idempotent stays green.
func TestArchiveCommitLog_SkipsSHAsTheLogAlreadyHolds(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ArchiveCommitLogTool(vault)
	projDir := archiveRepo(t, "dup-proj", "feat: one\n", "feat: two\n", "feat: three\n")

	first := runArchive(t, tool.Handler, projDir)
	if first["commits_archived"] != 3 {
		t.Fatalf("first run archived %v, want 3", first["commits_archived"])
	}
	logPath, _ := vault.CommitLogFile("dup-proj")
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read commit-log.md: %v", err)
	}

	// The other host's cursor: two commits behind the shared log. Still a
	// perfectly valid ancestor of HEAD, so the anchor guard has no quarrel
	// with it — this is the content problem, not the ancestry problem.
	oldAnchor := gitT(t, projDir, "rev-parse", "HEAD~2")
	setAnchor(t, vault, "dup-proj", oldAnchor)

	second := runArchive(t, tool.Handler, projDir)
	if second["commits_archived"] != 0 {
		t.Errorf("second run archived %v, want 0 — every SHA the walk yielded was already in the log",
			second["commits_archived"])
	}
	if second["duplicates_skipped"] != 2 {
		t.Errorf("duplicates_skipped = %v, want 2", second["duplicates_skipped"])
	}

	after, _ := os.ReadFile(logPath)
	if string(before) != string(after) {
		t.Errorf("commit-log.md changed:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	for _, subj := range []string{"feat: one", "feat: two", "feat: three"} {
		if n := strings.Count(string(after), subj); n != 1 {
			t.Errorf("%q appears %d times in the permanent history, want exactly 1", subj, n)
		}
	}
	// And the anchor still advanced to HEAD, so the stale cursor is repaired
	// rather than left to re-yield the same commits at every future wrap.
	anchor, _ := vault.ReadCommitLogAnchor("dup-proj")
	if anchor != second["anchor_to"] {
		t.Errorf("anchor = %q, want HEAD %v", anchor, second["anchor_to"])
	}
}

// TestArchiveCommitLog_RefusesAnchorOffTheHeadLine is the Cause 2 positive
// control the chair required: the anchor object EXISTS and is perfectly
// resolvable — it is simply not an ancestor of HEAD.
//
// A missing-object fixture would not have caught the live defect. `git log
// <missing>..HEAD` already exits 128, so that case was always loud. The silent
// one is this: the walk succeeds and emits the symmetric difference, which
// re-yields commits already archived on the surviving line. That is how iter
// 281 recorded an orphan as landed history.
func TestArchiveCommitLog_RefusesAnchorOffTheHeadLine(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ArchiveCommitLogTool(vault)
	projDir := archiveRepo(t, "orphan-proj", "feat: shared base\n")

	mainBranch := gitT(t, projDir, "rev-parse", "--abbrev-ref", "HEAD")

	// A sibling commit on its own branch: alive (the ref holds it), reachable,
	// and NOT an ancestor of HEAD once we go back to the main line.
	gitCommitT(t, projDir, "checkout", "-b", "abandoned")
	gitCommitT(t, projDir, "commit", "--allow-empty", "-m", "feat: rebased away")
	orphan := gitT(t, projDir, "rev-parse", "HEAD")
	gitCommitT(t, projDir, "checkout", mainBranch)
	gitCommitT(t, projDir, "commit", "--allow-empty", "-m", "feat: survived")

	// Prove the fixture really is the shape we claim before asserting on it:
	// the object resolves, and HEAD does not descend from it.
	gitT(t, projDir, "rev-parse", "--verify", orphan+"^{commit}")
	if err := exec.Command("git", "-C", projDir, "merge-base", "--is-ancestor", orphan, "HEAD").Run(); err == nil {
		t.Fatalf("fixture is wrong: %s IS an ancestor of HEAD", orphan)
	}

	setAnchor(t, vault, "orphan-proj", orphan)

	params, _ := json.Marshal(map[string]string{"project_path": projDir})
	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected a refusal for an anchor that is not an ancestor of HEAD")
	}
	if !errors.Is(err, wrapstate.ErrAnchorNotAncestor) {
		t.Errorf("error = %v, want ErrAnchorNotAncestor", err)
	}
	// The refusal must carry a remediation, not just a verdict.
	for _, want := range []string{"merge-base --is-ancestor", "Remediation", orphan} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q:\n%s", want, err)
		}
	}
	// Nothing was written: a refusal that still appended would defeat itself.
	logPath, _ := vault.CommitLogFile("orphan-proj")
	if _, serr := os.Stat(logPath); !os.IsNotExist(serr) {
		t.Errorf("commit-log.md was written despite the refusal (stat err = %v)", serr)
	}
	anchor, _ := vault.ReadCommitLogAnchor("orphan-proj")
	if anchor != orphan {
		t.Errorf("anchor moved on a refusal: %q", anchor)
	}
}

// TestArchiveCommitLog_RefusesUnresolvableAnchor covers the other half of the
// guard — the isolated-clone / gc'd sibling (277->280). It was already loud as
// a bare `exit status 128` from the walk; the point here is that it is now a
// DIAGNOSIS with a remediation instead of an exit code.
func TestArchiveCommitLog_RefusesUnresolvableAnchor(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ArchiveCommitLogTool(vault)
	projDir := archiveRepo(t, "gone-proj", "feat: only one\n")

	setAnchor(t, vault, "gone-proj", "0123456789abcdef0123456789abcdef01234567")

	params, _ := json.Marshal(map[string]string{"project_path": projDir})
	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected a refusal for an unresolvable anchor")
	}
	if !errors.Is(err, wrapstate.ErrAnchorUnresolvable) {
		t.Errorf("error = %v, want ErrAnchorUnresolvable", err)
	}
	if !strings.Contains(err.Error(), "Remediation") {
		t.Errorf("refusal missing remediation:\n%s", err)
	}
}
