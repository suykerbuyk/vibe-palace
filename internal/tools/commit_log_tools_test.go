// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
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
