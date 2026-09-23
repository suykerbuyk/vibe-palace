// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
)

// A stale checkout — its marker still names a project that was renamed or
// removed from the vault — must not re-scaffold Projects/<old>/. The removal is
// recognised from the vault's git history (RemovedSlug).

func vaultGit(t *testing.T, vault string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", vault}, args...)...)
	cmd.Env = gitenv.SafeGitEnv("GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func vaultFile(t *testing.T, vault, rel, body string) {
	t.Helper()
	abs := filepath.Join(vault, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitVaultWithHistory makes a git vault in which Projects/<slug>/ existed and
// was then taken away by `how` ("rm" or "mv"). It always holds one other
// project, so the vault is not empty.
func gitVaultWithHistory(t *testing.T, slug, how string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	vault := t.TempDir()
	vaultGit(t, vault, "init", "-q", "-b", "main")
	vaultFile(t, vault, "Projects/other/resume.md", "other\n")
	vaultFile(t, vault, "Projects/"+slug+"/resume.md", "old\n")
	vaultGit(t, vault, "add", "-A")
	vaultGit(t, vault, "commit", "-q", "-m", "seed")
	switch how {
	case "rm":
		vaultGit(t, vault, "rm", "-q", "-r", "Projects/"+slug)
		vaultGit(t, vault, "commit", "-q", "-m", "remove "+slug)
	case "mv":
		vaultGit(t, vault, "mv", "Projects/"+slug, "Projects/new-"+slug)
		vaultGit(t, vault, "commit", "-q", "-m", "migrate: "+slug+" -> new-"+slug+" (1/2 rename)")
	}
	return vault
}

func markerRepo(t *testing.T, slug string) string {
	t.Helper()
	repo := t.TempDir()
	writeConfig(t, repo, "[project]\nname = \""+slug+"\"\n")
	return repo
}

// R1. Reverting the marker-arm check makes this authorize, and the next write
// re-creates Projects/old/.
func TestRequireKnownProject_MarkerNamingRemovedSlugRefuses(t *testing.T) {
	vault := gitVaultWithHistory(t, "old", "rm")
	err := RequireKnownProject("old", vault, markerRepo(t, "old"))
	if err == nil {
		t.Fatal("a marker naming a slug removed from the vault must not authorize resurrecting it")
	}
	for _, want := range []string{`"old"`, "Projects/old/ was removed", "remove old", "[project].name", "vp init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must contain %q, got %q", want, err)
		}
	}
	// C1: no command that does not exist today.
	if strings.Contains(err.Error(), "vp project rename") {
		t.Errorf("refusal names a command that does not exist: %q", err)
	}
	if _, statErr := os.Stat(filepath.Join(vault, "Projects", "old")); statErr == nil {
		t.Error("the gate must not create Projects/old/")
	}
}

// R2. The shape a rename actually leaves: the tree renamed away (K1 is an
// all-R100 commit), not deleted.
func TestRequireKnownProject_MarkerNamingRenamedSlugRefuses(t *testing.T) {
	vault := gitVaultWithHistory(t, "old", "mv")
	if err := RequireKnownProject("old", vault, markerRepo(t, "old")); err == nil {
		t.Fatal("a marker naming a renamed-away slug must not authorize")
	}
	// The new slug is present, so its own marker still authorizes.
	if err := RequireKnownProject("new-old", vault, markerRepo(t, "new-old")); err != nil {
		t.Errorf("the renamed-to slug must authorize: %v", err)
	}
}

// G2. A non-git vault has no history, so no evidence: today's behaviour.
func TestRequireKnownProject_MarkerOnNonGitVaultAuthorizes(t *testing.T) {
	if err := RequireKnownProject("proj", t.TempDir(), markerRepo(t, "proj")); err != nil {
		t.Errorf("no history is no evidence of removal: %v", err)
	}
}

// G3. `vp init` scaffolds Projects/<slug>/ directly; once the directory is
// back, the slug is known again and the marker authorizes.
func TestRequireKnownProject_RemovedThenReinitedAuthorizes(t *testing.T) {
	vault := gitVaultWithHistory(t, "old", "rm")
	vaultFile(t, vault, "Projects/old/commands/README.md", "re-inited\n")
	if err := RequireKnownProject("old", vault, markerRepo(t, "old")); err != nil {
		t.Errorf("a re-scaffolded project must authorize: %v", err)
	}
}

// A slug that never existed in a git vault is the first-ever write: not
// removed.
func TestRemovedSlug_NeverExistedIsNotRemoved(t *testing.T) {
	vault := gitVaultWithHistory(t, "old", "rm")
	if removed, _, _ := RemovedSlug(vault, "brand-new"); removed {
		t.Error("a slug with no history is not removed")
	}
	if removed, commit, subject := RemovedSlug(vault, "old"); !removed || commit == "" || subject != "remove old" {
		t.Errorf("RemovedSlug(old) = %v %q %q, want true, a sha and \"remove old\"", removed, commit, subject)
	}
}

// C3. No git at all: fail open, promptly.
func TestRemovedSlug_FailsOpenWithoutGit(t *testing.T) {
	vault := gitVaultWithHistory(t, "old", "rm")
	defer func(g string) { removedSlugGit = g }(removedSlugGit)
	removedSlugGit = filepath.Join(t.TempDir(), "no-such-git")

	if removed, _, _ := RemovedSlug(vault, "old"); removed {
		t.Error("with no git binary the probe must fail open (not removed)")
	}
	if err := RequireKnownProject("old", vault, markerRepo(t, "old")); err != nil {
		t.Errorf("fail-open must leave today's authorization in place: %v", err)
	}
}

// C3. A vault whose .git cannot be read: git errors, the probe fails open.
func TestRemovedSlug_FailsOpenOnUnreadableGitDir(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	vault := gitVaultWithHistory(t, "old", "rm")
	gitDir := filepath.Join(vault, ".git")
	if err := os.Chmod(gitDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })

	if removed, _, _ := RemovedSlug(vault, "old"); removed {
		t.Error("an unreadable .git must fail open (not removed)")
	}
}

// C3. A git that hangs — here a script whose child keeps stdout open, so
// killing the script alone does not end the read — must not hold the caller:
// the context timeout kills it and WaitDelay stops the wait on its pipes.
func TestRemovedSlug_HungGitReturnsPromptly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell script as the fake git")
	}
	vault := gitVaultWithHistory(t, "old", "rm")
	fake := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	defer func(g string, d time.Duration) { removedSlugGit, removedSlugTimeout = g, d }(removedSlugGit, removedSlugTimeout)
	removedSlugGit, removedSlugTimeout = fake, 200*time.Millisecond

	start := time.Now()
	removed, _, _ := RemovedSlug(vault, "old")
	elapsed := time.Since(start)
	if removed {
		t.Error("a timed-out probe must fail open (not removed)")
	}
	if elapsed > 5*time.Second {
		t.Errorf("the probe took %v; it must return within timeout + WaitDelay", elapsed)
	}
}

// D1. A vault that is NOT its own git repository, nested in one whose history
// once held <vault>/Projects/ghost/. `git -C <vault> log` walks up to the
// enclosing repo and finds "rm ghost" — evidence about a DIFFERENT repository.
// Answering "removed" from it would refuse a legitimate write (fail-closed),
// so the probe must answer only from the vault's own top level. Mirrors the
// Chair's probe at ~/vp-eval-2026-09-23/chair-probe/outer.
func TestRemovedSlug_IgnoresAnEnclosingRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	outer := t.TempDir()
	vault := filepath.Join(outer, "vault")
	vaultGit(t, outer, "init", "-q", "-b", "main")
	vaultFile(t, outer, "vault/Projects/ghost/a.md", "a\n")
	vaultFile(t, outer, "vault/Projects/keep/b.md", "b\n")
	vaultGit(t, outer, "add", "-A")
	vaultGit(t, outer, "commit", "-q", "-m", "outer has ghost")
	vaultGit(t, outer, "rm", "-q", "-r", "vault/Projects/ghost")
	vaultGit(t, outer, "commit", "-q", "-m", "rm ghost")

	if removed, commit, subject := RemovedSlug(vault, "ghost"); removed {
		t.Errorf("an enclosing repository's history is not the vault's: got removed by %s %q", commit, subject)
	}
	if err := RequireKnownProject("ghost", vault, markerRepo(t, "ghost")); err != nil {
		t.Errorf("a nested vault with no history of its own must keep authorizing: %v", err)
	}
}
