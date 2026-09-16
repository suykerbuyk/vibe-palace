// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testinfra

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// gitConfigValue reads one git config key from root via `git config <key>`,
// failing the test on any error (including "key not set").
func gitConfigValue(t *testing.T, root, key string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", root, "config", key)
	cmd.Env = storage.SafeGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git config %s: %s: %v", key, out, err)
	}
	return strings.TrimSpace(string(out))
}

// gitLogHashes returns the full commit hashes of every commit that ever
// touched path (including a commit that only DELETED it), newest first —
// exactly `git log --format=%H -- <path>`. A path with no history at all
// yields an empty (non-nil-checked-for) slice.
func gitLogHashes(t *testing.T, root, path string) []string {
	t.Helper()
	cmd := exec.Command("git", "-C", root, "log", "--format=%H", "--", path)
	cmd.Env = storage.SafeGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log -- %s: %s: %v", path, out, err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// TestWithGitProducesARealWorkingRepository is the task's first Acceptance
// criterion: WithGit() must produce a real, WORKING git repository, not just
// a `.git` directory stub.
//
// Not asserted here: that the repo is CLEAN immediately after WithGit().
// NewHarnessWithEmbedder stamps the vault born-current (surface.WriteFormat,
// writing .vibe-palace/vault.toml) during harness CONSTRUCTION, before Seed
// runs any SeedOption — so a WithGit()-seeded repo always starts with that
// one pre-existing, untracked path already on disk. That is a fact about
// harness construction order, unrelated to this task, and not something
// WithGit() itself should paper over with an unasked-for initial commit (see
// its own doc comment: deliberately no initial commit, unborn HEAD).
// GitStatusClean returning an ERROR would mean the repo is broken; that it
// returns a definite true/false at all is the actual proof of "working."
func TestWithGitProducesARealWorkingRepository(t *testing.T) {
	h := New(t, WithGit())

	if !storage.GitIsRepo(h.Vault.Root) {
		t.Fatal("WithGit did not initialize a .git directory at the vault root")
	}
	if _, err := storage.GitStatusClean(h.Vault.Root); err != nil {
		t.Fatalf("GitStatusClean against a WithGit()-seeded root returned an error — the repo is not usable: %v", err)
	}
}

// TestWithGitConfiguresLocalIdentityAndDisablesSigning pins WithGit()'s
// local-only config: an identity so checkIdentity's `git var GIT_AUTHOR_IDENT`
// succeeds without depending on host/global git config, and gpgsign=false so
// a signing host/CI cannot hang or fail this test on an unrelated,
// throwaway repository.
func TestWithGitConfiguresLocalIdentityAndDisablesSigning(t *testing.T) {
	h := New(t, WithGit())

	cases := []struct{ key, want string }{
		{"user.name", "testinfra"},
		{"user.email", "testinfra@vibe-palace.invalid"},
		{"commit.gpgsign", "false"},
	}
	for _, tc := range cases {
		if got := gitConfigValue(t, h.Vault.Root, tc.key); got != tc.want {
			t.Errorf("git config %s = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// TestDrivenMoveProducesTwoSeparateCommits is the task's second, decisive
// Acceptance criterion: a real vp_manage_task `move`, driven through the MCP
// dispatch against a WithGit()-seeded vault, must land as TWO separate
// commits (destination add, then source delete+tombstone) — never one
// combined commit. This is asserted by comparing actual commit HASHES, not
// merely commit counts: a bug that produced two commits for the wrong reason
// (or one commit split into two unrelated pieces) would still pass a
// count-only check but fail this one.
func TestDrivenMoveProducesTwoSeparateCommits(t *testing.T) {
	h := New(t, WithGit(), WithProject("src"), WithProject("dst"))

	h.CallTool(t, "vp_manage_task", map[string]any{
		"project": "src", "action": "create", "task": "movable",
		"title": "Movable", "content": TaskBody("Fixture task for the two-commit move test."),
	})
	h.CallTool(t, "vp_manage_task", map[string]any{
		"project": "src", "action": "move", "task": "movable", "to_project": "dst",
	})

	root := h.Vault.Root
	const (
		destPath      = "Projects/dst/tasks/movable.md"
		srcPath       = "Projects/src/tasks/movable.md"
		tombstonePath = "Projects/src/tasks/cancelled/movable.md"
	)

	destHashes := gitLogHashes(t, root, destPath)
	if len(destHashes) != 1 {
		t.Fatalf("commits touching %s = %d (%v), want exactly 1 — the destination add, with no earlier "+
			"history for that path, is the whole point of the two-commit split", destPath, len(destHashes), destHashes)
	}
	destCommit := destHashes[0]

	// Exactly two: the original `create` commit, then the move's source-side
	// delete commit (the newest — git log defaults to newest-first).
	srcHashes := gitLogHashes(t, root, srcPath)
	if len(srcHashes) != 2 {
		t.Fatalf("commits touching %s = %d (%v), want exactly 2 (create, then the move's delete)",
			srcPath, len(srcHashes), srcHashes)
	}
	sourceDeleteCommit := srcHashes[0]

	tombstoneHashes := gitLogHashes(t, root, tombstonePath)
	if len(tombstoneHashes) != 1 {
		t.Fatalf("commits touching %s = %d (%v), want exactly 1", tombstonePath, len(tombstoneHashes), tombstoneHashes)
	}

	// The decisive assertion: the destination ADD and the source DELETE must
	// be different commits.
	if destCommit == sourceDeleteCommit {
		t.Fatalf("destination add (%s) and source delete (%s) landed in the SAME commit — "+
			"the two-commit split this fix depends on did not happen", destCommit, sourceDeleteCommit)
	}
	// The source-side commit must cover BOTH the delete and the tombstone —
	// commitTaskWrite's own three-path scope for the source project.
	if tombstoneHashes[0] != sourceDeleteCommit {
		t.Fatalf("tombstone commit (%s) differs from the source-delete commit (%s) — expected ONE "+
			"source-side commit to cover both halves of the source's change",
			tombstoneHashes[0], sourceDeleteCommit)
	}
}

// TestWithGitCommitProducesTheExactRequestedDate is the task's third
// Acceptance criterion: GitCommitAllAt/WithGitCommit must produce a commit
// whose author/committer date is exactly the requested time, verified the
// same way board-reporting-one-time-migration's own git-log-based derivation
// will read it back: `git log --format=%ad --date=format:%Y-%m-%d`.
func TestWithGitCommitProducesTheExactRequestedDate(t *testing.T) {
	h := New(t, WithGit(), WithProject("a"))

	tasksDir := filepath.Join(h.Vault.Root, "Projects", "a", "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("mkdir tasks dir: %v", err)
	}
	fixture := "# T\n\n**Status:** planning\n**Priority:** high\n\n## Context\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "seeded.md"), []byte(fixture), 0o644); err != nil {
		t.Fatalf("write fixture task file: %v", err)
	}

	requested := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	h.Seed(t, WithGitCommit("seed fixture", requested))

	cmd := exec.Command("git", "-C", h.Vault.Root, "log", "-1", "--format=%ad", "--date=format:%Y-%m-%d")
	cmd.Env = storage.SafeGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %s: %v", out, err)
	}
	if got := strings.TrimSpace(string(out)); got != "2026-03-14" {
		t.Errorf("commit author date = %q, want 2026-03-14", got)
	}
}

// TestGitCommitAllAtWithNothingDirtyReturnsARealError pins the deliberate
// non-tolerance decision in GitCommitAllAt's own doc comment: a caller asking
// to commit with nothing dirty gets the real git error, not a silent no-op.
//
// The FIRST GitCommitAllAt call is not the test — a fresh WithGit()-seeded
// root always has one pre-existing untracked path (the harness's own
// born-current .vibe-palace/vault.toml stamp, written during construction,
// before WithGit() ever runs), so an immediate call would succeed by
// absorbing that stamp rather than proving anything about "nothing dirty."
// The real test is the SECOND call, once that one pre-existing path has
// already been committed and genuinely nothing remains dirty.
func TestGitCommitAllAtWithNothingDirtyReturnsARealError(t *testing.T) {
	h := New(t, WithGit())

	if err := storage.GitCommitAllAt(h.Vault.Root, "absorb the pre-existing vault.toml stamp", time.Now()); err != nil {
		t.Fatalf("first commit (absorbing the pre-existing stamp) should succeed: %v", err)
	}

	err := storage.GitCommitAllAt(h.Vault.Root, "should fail", time.Now())
	if err == nil {
		t.Fatal("expected an error committing with genuinely nothing dirty, got nil")
	}
	if !strings.Contains(err.Error(), "nothing to commit") {
		t.Errorf("error = %v, want it to mention \"nothing to commit\"", err)
	}
}
