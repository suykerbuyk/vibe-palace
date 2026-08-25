// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// embeddedTemplateBytes returns the live embedded bytes for a template
// resource. It reads the real corpus rather than a fixture: a mirror only
// classifies as prunable when its bytes equal what the binary ships.
func embeddedTemplateBytes(t *testing.T, embeddedRel string) []byte {
	t.Helper()
	rs, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded: %v", err)
	}
	for _, res := range rs {
		if res.RelPath == embeddedRel {
			return res.Bytes
		}
	}
	t.Fatalf("could not locate %q in embedded corpus", embeddedRel)
	return nil
}

// gitInVault runs git in dir with the ambient user/system config neutralized,
// so a developer's ~/.gitconfig cannot change what these fixtures mean.
func gitInVault(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// gitifyVault turns an already-seeded vault into host A: a git repo whose
// whole current tree is committed and pushed to a fresh bare origin. Returns
// the origin path, which a second clone can be made from.
func gitifyVault(t *testing.T, vaultPath string) (origin string) {
	t.Helper()
	origin = filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatalf("mkdir origin: %v", err)
	}
	gitInVault(t, origin, "init", "--bare", "-b", "main")

	gitInVault(t, vaultPath, "init", "-b", "main")
	gitInVault(t, vaultPath, "config", "user.email", "test@test.com")
	gitInVault(t, vaultPath, "config", "user.name", "Test")
	// Match production's canonical ignores for the two sidecars this path
	// creates: the prune's .bak backup and the commit lock's directory.
	if err := os.WriteFile(filepath.Join(vaultPath, ".gitignore"),
		[]byte(".vp-locks/\n*.bak\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	gitInVault(t, vaultPath, "add", "-A")
	gitInVault(t, vaultPath, "commit", "-m", "seed")
	gitInVault(t, vaultPath, "remote", "add", "origin", origin)
	gitInVault(t, vaultPath, "push", "-u", "origin", "main")
	return origin
}

// cloneVault makes an independent clone of origin — "host B". Returns its
// worktree root.
func cloneVault(t *testing.T, origin string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "hostB")
	cmd := exec.Command("git", "clone", origin, dst)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	return dst
}

// TestConfigSyncPrunePropagatesToAnotherHost is the load-bearing case: a prune
// performed on host A must be durable across a sync to host B.
//
// Before this fix `ActionDelete` was os.Remove and nothing else, so the removal
// existed only in host A's worktree and every fresh clone resurrected the
// mirror — while the arm that puts mirrors back (a human committing them)
// reached git. The assertion that matters is therefore on HOST B.
//
// NAMED ANTI-TEST, so it cannot be produced by accident: asserting that host
// A's worktree no longer holds the file PASSES against the unfixed code. That
// is the bug wearing a green check and must never be the only assertion.
//
// Mutation-proof: delete the propagatePrunedMirrors call in runConfigSync (or
// have it return before committing) and the host-B assertion below goes red
// while every other prune test stays green.
func TestConfigSyncPrunePropagatesToAnotherHost(t *testing.T) {
	vaultPath, target, _ := syncPruneSetup(t, "commands/wrap.md")
	origin := gitifyVault(t, vaultPath)

	mirrorRel := "Templates/commands/wrap.md"

	// Fixture guard: host B starts WITH the mirror. Without this a broken
	// fixture (mirror never committed) would make the real assertion below
	// pass for the wrong reason.
	before := cloneVault(t, origin)
	if _, err := os.Stat(filepath.Join(before, filepath.FromSlash(mirrorRel))); err != nil {
		t.Fatalf("fixture: host B should start with the mirror: %v", err)
	}

	out, code := runSyncWithStdin(t, "", []string{
		"--project-root", filepath.Dir(target), "--tier", "vault", "--yes",
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if !strings.Contains(out, "pruned=1") {
		t.Fatalf("summary missing pruned=1 — the fixture did not classify Case 2/3 Delete:\n%s", out)
	}

	// THE assertion: a second, independent clone must not carry the mirror.
	after := cloneVault(t, origin)
	if _, err := os.Stat(filepath.Join(after, filepath.FromSlash(mirrorRel))); !os.IsNotExist(err) {
		t.Errorf("host B still holds %s after host A pruned it (err=%v) — the prune did not reach git", mirrorRel, err)
	}

	// The commit is scoped to the pruned path and nothing else.
	names := gitInVault(t, vaultPath, "show", "--name-status", "--format=", "HEAD")
	if !strings.Contains(names, mirrorRel) {
		t.Errorf("prune commit does not name %s:\n%s", mirrorRel, names)
	}
	for _, line := range strings.Split(strings.TrimSpace(names), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "D\t") {
			t.Errorf("prune commit carries a non-deletion: %q", line)
		}
	}
	// The message explains itself: nobody reviewed it at write time.
	msg := gitInVault(t, vaultPath, "log", "-1", "--format=%B")
	if !strings.Contains(msg, mirrorRel) {
		t.Errorf("commit message does not name what was pruned:\n%s", msg)
	}
}

// TestConfigSyncNoPruneOnGitVaultStillSucceeds pins the zero-path guard.
// CommitAndPushPaths errors on an empty path list, so a sync that prunes
// nothing — the common case — must never reach it. Without the guard every
// no-op `vp config sync` against a git vault would start exiting ExitSystem:
// a durability fix breaking the command it fixes.
func TestConfigSyncNoPruneOnGitVaultStillSucceeds(t *testing.T) {
	vaultPath, target, _ := syncPruneSetup(t, "commands/wrap.md")
	gitifyVault(t, vaultPath)

	// First sync prunes the seeded mirror.
	if out, code := runSyncWithStdin(t, "", []string{
		"--project-root", filepath.Dir(target), "--tier", "vault", "--yes",
	}); code != cli.ExitOK {
		t.Fatalf("first sync exit = %d\n%s", code, out)
	}
	// Second sync has nothing to prune.
	out, code := runSyncWithStdin(t, "", []string{
		"--project-root", filepath.Dir(target), "--tier", "vault", "--yes",
	})
	if code != cli.ExitOK {
		t.Fatalf("no-prune sync exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, "pruned=0") {
		t.Errorf("second sync should report pruned=0:\n%s", out)
	}
}

// TestConfigSyncPruneOnNonGitVaultStillSucceeds pins the other degenerate
// environment. A vault that is not a git repo is supported (GitInit is
// offered, never required), so the propagation skips silently rather than
// reporting an error — finishSync turns a Report error into ExitSystem, and
// the prune itself succeeded.
func TestConfigSyncPruneOnNonGitVaultStillSucceeds(t *testing.T) {
	_, target, _ := syncPruneSetup(t, "commands/wrap.md")

	out, code := runSyncWithStdin(t, "", []string{
		"--project-root", filepath.Dir(target), "--tier", "vault", "--yes",
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if !strings.Contains(out, "pruned=1") {
		t.Errorf("summary missing pruned=1:\n%s", out)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("pruned file still present (err=%v)", err)
	}
}

// TestConfigSyncPruneOfUntrackedMirrorDoesNotPoisonTheBatch pins the measured
// batch-poisoning hazard: `git add -- <tracked-deleted> <never-tracked-deleted>`
// exits 128 and leaves the REAL deletion unstaged. filterStageablePaths inside
// CommitAndPushPaths is what dissolves it, and this test is what notices if a
// later rewrite routes around that helper.
//
// The untracked mirror is a real occurrence, not a hypothetical: the mirrors
// were `?? Templates/commands/` on 2026-08-10, and the prune removes untracked
// mirrors too.
func TestConfigSyncPruneOfUntrackedMirrorDoesNotPoisonTheBatch(t *testing.T) {
	vaultPath, target, _ := syncPruneSetup(t, "commands/wrap.md")
	origin := gitifyVault(t, vaultPath)

	// A second byte-identical mirror, seeded AFTER the seed commit, so it is
	// pruned in the same batch while never having been tracked.
	seedTemplateOverride(t, vaultPath, "commands/restart.md",
		embeddedTemplateBytes(t, "commands/restart.md"), strings.Repeat("0", 64))

	out, code := runSyncWithStdin(t, "", []string{
		"--project-root", filepath.Dir(target), "--tier", "vault", "--yes",
	})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if !strings.Contains(out, "pruned=2") {
		t.Fatalf("expected both mirrors pruned:\n%s", out)
	}

	after := cloneVault(t, origin)
	if _, err := os.Stat(filepath.Join(after, "Templates", "commands", "wrap.md")); !os.IsNotExist(err) {
		t.Errorf("the tracked deletion did not reach git — one untracked path poisoned the batch (err=%v)", err)
	}
}
