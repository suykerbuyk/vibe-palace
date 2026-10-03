// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// layBornVault writes what init's vault step writes into a vault it created:
// the canonical .gitignore and the RequiredDataFormat stamp.
func layBornVault(t *testing.T, dir string) {
	t.Helper()
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatalf("ReconcileVaultGitignore: %v", err)
	}
	if err := surface.WriteFormat(dir, surface.RequiredDataFormat); err != nil {
		t.Fatalf("WriteFormat: %v", err)
	}
}

func TestCommitVaultInit_BornVaultCommitsGitignoreAndStampAsTheRootCommit(t *testing.T) {
	dir := initUnbornTestRepo(t)
	layBornVault(t, dir)
	writeFile(t, dir, "stray.md", "not init's\n")

	vc, err := CommitVaultInit(dir, VaultInitCommitOptions{CreatedVault: true, Wrote: true})
	if err != nil {
		t.Fatalf("CommitVaultInit: %v", err)
	}
	want := []string{".gitignore", ".vibe-palace/vault.toml"}
	if !vc.Committed || !slices.Equal(vc.Paths, want) {
		t.Fatalf("got %+v, want a commit of %q", vc, want)
	}
	if got := scaffoldCommitFiles(t, dir); !slices.Equal(got, want) {
		t.Errorf("commit touched %q, want exactly %q", got, want)
	}
	if msg := gitRun(t, dir, "log", "-1", "--format=%s"); msg != "Create the vault .gitignore and record its data-format stamp" {
		t.Errorf("message = %q", msg)
	}
	if n := gitRun(t, dir, "rev-list", "--count", "HEAD"); n != "1" {
		t.Errorf("want the vault's root commit, HEAD has %s commits", n)
	}
	// Never git add -A.
	if st := gitRun(t, dir, "status", "--porcelain"); st != "?? stray.md" {
		t.Errorf("status = %q, want only the unrelated stray untouched", st)
	}
}

func TestCommitVaultInit_TopUpOfATrackedGitignoreIsCommitted(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, ".gitignore", ".DS_Store\n")
	gitRun(t, dir, "add", ".gitignore")
	gitRun(t, dir, "commit", "-q", "-m", "old gitignore")
	if _, err := TopUpVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}

	vc, err := CommitVaultInit(dir, VaultInitCommitOptions{Wrote: true})
	if err != nil {
		t.Fatalf("CommitVaultInit: %v", err)
	}
	if !vc.Committed || !slices.Equal(vc.Paths, []string{".gitignore"}) {
		t.Fatalf("got %+v, want a commit of .gitignore", vc)
	}
	if st := gitRun(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("still dirty: %q", st)
	}
	if msg := gitRun(t, dir, "log", "-1", "--format=%s"); !strings.Contains(msg, "Top up") {
		t.Errorf("message %q does not say it is a top-up", msg)
	}
}

func TestCommitVaultInit_GitignoreWithAnyOtherEditIsKept(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, ".gitignore", ".DS_Store\n")
	gitRun(t, dir, "add", ".gitignore")
	gitRun(t, dir, "commit", "-q", "-m", "old gitignore")
	if _, err := TopUpVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	// One byte vp did not write.
	f, err := os.OpenFile(filepath.Join(dir, ".gitignore"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("x")
	f.Close()
	head := gitRun(t, dir, "rev-parse", "HEAD")

	vc, err := CommitVaultInit(dir, VaultInitCommitOptions{Wrote: true})
	if err != nil {
		t.Fatalf("CommitVaultInit: %v", err)
	}
	if vc.Committed || !slices.Equal(vc.Kept, []string{".gitignore"}) {
		t.Errorf("got %+v, want .gitignore Kept and no commit", vc)
	}
	if gitRun(t, dir, "rev-parse", "HEAD") != head {
		t.Error("HEAD moved")
	}
}

// The stamp is committed ONLY on a vault this run created. A byte-exact,
// untracked stamp in a vault init did not create — the shape a format bump
// leaves before its data is committed — is never committed here.
func TestCommitVaultInit_StampNeverCommittedUnlessThisRunCreatedTheVault(t *testing.T) {
	dir := initTestRepo(t)
	if err := surface.WriteFormat(dir, surface.RequiredDataFormat); err != nil {
		t.Fatal(err)
	}
	head := gitRun(t, dir, "rev-parse", "HEAD")

	vc, err := CommitVaultInit(dir, VaultInitCommitOptions{CreatedVault: false, Wrote: true})
	if err != nil {
		t.Fatalf("CommitVaultInit: %v", err)
	}
	if vc.Committed || !slices.Equal(vc.Kept, []string{".vibe-palace/vault.toml"}) {
		t.Errorf("got %+v, want the stamp Kept and no commit", vc)
	}
	if gitRun(t, dir, "rev-parse", "HEAD") != head {
		t.Error("HEAD moved: a stamp-only commit was made on a vault init did not create")
	}
}

func TestCommitVaultInit_TrackedModifiedStampIsNeverCommitted(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, ".vibe-palace/vault.toml", "format = 1\n")
	gitRun(t, dir, "add", "-f", ".vibe-palace/vault.toml")
	gitRun(t, dir, "commit", "-q", "-m", "old stamp")
	want, err := surface.FormatManifestBytes(surface.RequiredDataFormat)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".vibe-palace/vault.toml", string(want))
	head := gitRun(t, dir, "rev-parse", "HEAD")

	vc, err := CommitVaultInit(dir, VaultInitCommitOptions{CreatedVault: true, Wrote: true})
	if err != nil {
		t.Fatalf("CommitVaultInit: %v", err)
	}
	if vc.Committed || !slices.Equal(vc.Kept, []string{".vibe-palace/vault.toml"}) {
		t.Errorf("got %+v, want the tracked stamp Kept", vc)
	}
	if gitRun(t, dir, "rev-parse", "HEAD") != head {
		t.Error("HEAD moved")
	}
}

func TestCommitVaultInit_StampWithOtherBytesIsKept(t *testing.T) {
	dir := initUnbornTestRepo(t)
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".vibe-palace/vault.toml", "format = 1\n")

	vc, err := CommitVaultInit(dir, VaultInitCommitOptions{CreatedVault: true, Wrote: true})
	if err != nil {
		t.Fatalf("CommitVaultInit: %v", err)
	}
	if !slices.Equal(vc.Paths, []string{".gitignore"}) || !slices.Equal(vc.Kept, []string{".vibe-palace/vault.toml"}) {
		t.Errorf("got %+v, want .gitignore committed and the foreign stamp Kept", vc)
	}
	if msg := gitRun(t, dir, "log", "-1", "--format=%s"); msg != "Create the vault .gitignore with vp's canonical lines" {
		t.Errorf("message = %q, want a .gitignore CREATE that claims no stamp", msg)
	}
}

func TestCommitVaultInit_WhereItDoesNothing(t *testing.T) {
	t.Run("git disabled", func(t *testing.T) {
		dir := initUnbornTestRepo(t)
		layBornVault(t, dir)
		hostConfig(t, str("git_enabled = false\n"))
		if _, err := CommitVaultInit(dir, VaultInitCommitOptions{CreatedVault: true, Wrote: true}); !errors.Is(err, ErrGitDisabled) {
			t.Fatalf("err = %v, want ErrGitDisabled", err)
		}
		if st := gitRun(t, dir, "diff", "--cached", "--name-only"); st != "" {
			t.Errorf("index changed on a git-disabled host: %q", st)
		}
	})
	t.Run("not a repository", func(t *testing.T) {
		dir := t.TempDir()
		layBornVault(t, dir)
		vc, err := CommitVaultInit(dir, VaultInitCommitOptions{CreatedVault: true, Wrote: true})
		if err != nil || vc.Committed || !strings.Contains(vc.Skipped, "not a git repository") {
			t.Errorf("non-git vault: %+v, %v", vc, err)
		}
		vc, err = CommitVaultInit(dir, VaultInitCommitOptions{CreatedVault: true})
		if err != nil || vc.Skipped != "" {
			t.Errorf("non-git vault, nothing written this run: %+v, %v — want no reason", vc, err)
		}
	})
	t.Run("nested", func(t *testing.T) {
		repo := initTestRepo(t)
		vault := filepath.Join(repo, "vault")
		if err := os.MkdirAll(vault, 0o755); err != nil {
			t.Fatal(err)
		}
		layBornVault(t, vault)
		head := gitRun(t, repo, "rev-parse", "HEAD")
		vc, err := CommitVaultInit(vault, VaultInitCommitOptions{CreatedVault: true, Wrote: true})
		if err != nil || vc.Committed || !strings.Contains(vc.Skipped, "inside another repository") {
			t.Errorf("nested vault: %+v, %v", vc, err)
		}
		if gitRun(t, repo, "rev-parse", "HEAD") != head {
			t.Error("committed into the enclosing repository")
		}
	})
}

// The commit is local only: push=false contacts no remote, so the remote's
// branch is where it was.
func TestCommitVaultInit_IsLocalOnly(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "-q", "origin", "main")
	remoteHead := gitRun(t, bare, "rev-parse", "main")
	if err := ReconcileVaultGitignore(dir); err != nil {
		t.Fatal(err)
	}
	vc, err := CommitVaultInit(dir, VaultInitCommitOptions{Wrote: true})
	if err != nil || !vc.Committed {
		t.Fatalf("got %+v, %v", vc, err)
	}
	if gitRun(t, bare, "rev-parse", "main") != remoteHead {
		t.Error("the commit was pushed")
	}
}

// GitInit points the unborn HEAD at main whatever the host's
// init.defaultBranch says, so every new vault — `vp init`'s included — is born
// on main.
func TestGitInitBornOnMainWhateverTheHostDefault(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	gc := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(gc, []byte("[init]\n\tdefaultBranch = master\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gc)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	dir := t.TempDir()
	if err := GitInit(dir); err != nil {
		t.Fatalf("GitInit: %v", err)
	}
	if b := gitRun(t, dir, "symbolic-ref", "HEAD"); b != "refs/heads/main" {
		t.Errorf("HEAD = %s, want refs/heads/main (host default is master)", b)
	}
}

// vp vault init no longer sets the branch itself — GitInit does — so it
// verifies the scaffold left HEAD on main, as it verifies the stamp, and
// refuses a scaffold that did not.
func TestInitVault_RefusesAScaffoldNotOnMain(t *testing.T) {
	initEnv(t) // host init.defaultBranch = master
	origin, github := twoEmptyRemotes(t)
	path := filepath.Join(t.TempDir(), "v")
	req := initReq(path, origin, github)
	req.Scaffold = func(_ context.Context, dest string) error {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		if out, err := exec.Command("git", "init", "-q", dest).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w", out, err)
		}
		if err := ReconcileVaultGitignore(dest); err != nil {
			return err
		}
		return surface.WriteFormat(dest, surface.RequiredDataFormat)
	}
	if _, err := InitVault(context.Background(), req); err == nil || !strings.Contains(err.Error(), "HEAD on main") {
		t.Fatalf("err = %v, want the refusal naming HEAD on main", err)
	}
	if bareHasRefs(t, origin) || bareHasRefs(t, github) {
		t.Error("a vault not on main was published")
	}
}

// K2: the .gitignore half of the message is derived from HEAD, never from the
// stamp. A pre-created empty directory that init turns into a vault gets a
// .gitignore CREATE and no stamp (the stamp is written only when init creates
// the directory itself) — it is a create, not a top-up. A repository whose HEAD
// already has a .gitignore gets a top-up.
func TestCommitVaultInit_MessageNamesCreateOrTopUpFromHEAD(t *testing.T) {
	t.Run("no .gitignore at HEAD, no stamp", func(t *testing.T) {
		dir := initTestRepo(t) // born HEAD without a .gitignore
		if err := ReconcileVaultGitignore(dir); err != nil {
			t.Fatal(err)
		}
		if vc, err := CommitVaultInit(dir, VaultInitCommitOptions{Wrote: true}); err != nil || !vc.Committed {
			t.Fatalf("got %+v, %v", vc, err)
		}
		if msg := gitRun(t, dir, "log", "-1", "--format=%s"); msg != "Create the vault .gitignore with vp's canonical lines" {
			t.Errorf("message = %q, want a create", msg)
		}
	})
	t.Run(".gitignore at HEAD", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, dir, ".gitignore", ".DS_Store\n")
		gitRun(t, dir, "add", ".gitignore")
		gitRun(t, dir, "commit", "-q", "-m", "old gitignore")
		if _, err := TopUpVaultGitignore(dir); err != nil {
			t.Fatal(err)
		}
		if vc, err := CommitVaultInit(dir, VaultInitCommitOptions{Wrote: true}); err != nil || !vc.Committed {
			t.Fatalf("got %+v, %v", vc, err)
		}
		if msg := gitRun(t, dir, "log", "-1", "--format=%s"); msg != "Top up the vault .gitignore with vp's canonical lines" {
			t.Errorf("message = %q, want a top-up", msg)
		}
	})
}
