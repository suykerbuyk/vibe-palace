// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// `vp config sync` commits the vault .gitignore its Vault tier created or
// topped up (task config-sync-vault-gitignore-top-up-left-uncommitted), with
// `vp init`'s rule (storage.CommitVaultInit): only vp's exact bytes, locally,
// and also a top-up an earlier run left uncommitted.

// droppedLine is the canonical line the fixtures remove from a vault's
// committed .gitignore.
var droppedLine = storage.CanonicalGitignorePatterns[len(storage.CanonicalGitignorePatterns)-1]

// syncVaultTier runs `vp config sync --tier vault` from a project directory and
// returns its output and exit code. yes adds --yes; without it the run is
// interactive with empty stdin.
func syncVaultTier(t *testing.T, project string, yes bool, extra ...string) (string, int) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	args := append([]string{"--tier", "vault"}, extra...)
	if yes {
		args = append(args, "--yes")
	}
	return runSyncWithStdin(t, "", args)
}

// cleanGitVault is a git vault `vp init` made and left clean, with one
// project; it returns the vault and the project directory.
func cleanGitVault(t *testing.T) (vault, project string) {
	t.Helper()
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}
	_, _ = initTestEnv(t, false)
	vault = filepath.Join(t.TempDir(), "vault")
	project = newProject(t)
	if code, out := runInit(t, project, "--name", "p", "--vault-path", vault); code != cli.ExitOK {
		t.Fatalf("init: exit %d\n%s", code, out)
	}
	if st := porcelain(t, vault); st != "" {
		t.Fatalf("fixture: init left the vault dirty:\n%s", st)
	}
	return vault, project
}

// commitGitignore writes .gitignore and commits it, as an older vp (or a
// clone of an older vault) would have left it.
func commitGitignore(t *testing.T, vault, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(vault, ".gitignore"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, vault, "add", "--", ".gitignore")
	gitIn(t, vault, "commit", "-q", "-m", "an older .gitignore")
}

// withoutLine is content minus every line equal to line.
func withoutLine(content, line string) string {
	var out []string
	for _, l := range strings.SplitAfter(content, "\n") {
		if strings.TrimSuffix(l, "\n") != line {
			out = append(out, l)
		}
	}
	return strings.Join(out, "")
}

func readGitignore(t *testing.T, vault string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(vault, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// assertSyncedClean fails unless the vault has no uncommitted path and a sync
// preview does not refuse it.
func assertSyncedClean(t *testing.T, vault, out string) {
	t.Helper()
	if st := porcelain(t, vault); st != "" {
		t.Errorf("vp config sync left the vault dirty:\n%s\n--- sync output ---\n%s", st, out)
	}
	if _, refused, err := storage.SyncPreview(vault); refused || err != nil {
		t.Errorf("a sync preview refuses the vault config sync left (refused=%v): %v\n%s", refused, err, out)
	}
}

// headFiles is the paths HEAD's commit touched.
func headFiles(t *testing.T, vault string) []string {
	t.Helper()
	return strings.Fields(gitIn(t, vault, "show", "--name-only", "--format=", "HEAD"))
}

func setGitEnabled(t *testing.T, on bool) {
	t.Helper()
	p, err := storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.NewReplacer("git_enabled = true", "", "git_enabled = false", "").Replace(string(data))
	val := "false"
	if on {
		val = "true"
	}
	if err := os.WriteFile(p, []byte("git_enabled = "+val+"\n"+s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Row 1: a tracked .gitignore lacking a canonical line is topped up and
// committed, locally, alone.
func TestConfigSyncCommitsTheVaultGitignoreTopUp(t *testing.T) {
	vault, project := cleanGitVault(t)
	commitGitignore(t, vault, withoutLine(readGitignore(t, vault), droppedLine))
	out, code := syncVaultTier(t, project, true)
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	assertSyncedClean(t, vault, out)
	if f := headFiles(t, vault); !slices.Equal(f, []string{".gitignore"}) {
		t.Errorf("HEAD touched %v, want only .gitignore", f)
	}
	if !strings.Contains(readGitignore(t, vault), droppedLine) {
		t.Error("the canonical line was not added")
	}
	if !strings.Contains(out, "[Pass] vault .gitignore committed") {
		t.Errorf("no [Pass] row for the commit:\n%s", out)
	}
}

// Row 2: on a migrated vault the top-up includes the derived lines, and the
// commit recognises them as vp's (the shared VaultGitignorePatterns).
func TestConfigSyncCommitsAMigratedVaultsDerivedLines(t *testing.T) {
	vault, project := cleanGitVault(t)
	if err := surface.WriteVaultManifest(vault, surface.VaultManifest{Format: surface.RequiredDataFormat, AuthoredOnly: "2026-10-04"}); err != nil {
		t.Fatal(err)
	}
	gitIn(t, vault, "add", "--", ".vibe-palace/vault.toml")
	gitIn(t, vault, "commit", "-q", "-m", "migration marker, without the derived lines")
	out, code := syncVaultTier(t, project, true)
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	assertSyncedClean(t, vault, out)
	got := readGitignore(t, vault)
	for _, l := range storage.MigratedVaultGitignorePatterns {
		if !strings.Contains(got, l) {
			t.Errorf("derived line %q not added:\n%s", l, got)
		}
	}
}

// Row 3: an absent .gitignore on a git vault is created and committed.
func TestConfigSyncCommitsACreatedVaultGitignore(t *testing.T) {
	vault, project := cleanGitVault(t)
	gitIn(t, vault, "rm", "-q", "--", ".gitignore")
	gitIn(t, vault, "commit", "-q", "-m", "no .gitignore")
	out, code := syncVaultTier(t, project, true)
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	assertSyncedClean(t, vault, out)
	if f := headFiles(t, vault); !slices.Equal(f, []string{".gitignore"}) {
		t.Errorf("HEAD touched %v, want only .gitignore", f)
	}
}

// Row 4: sync git-inits a non-git vault; the .gitignore it created is the
// first commit, and nothing else of the vault is committed by it.
func TestConfigSyncCommitsTheGitignoreOfAVaultItGitInits(t *testing.T) {
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}
	_, _ = initTestEnv(t, false)
	vault := filepath.Join(t.TempDir(), "vault")
	project := newProject(t)
	if code, out := runInit(t, project, "--name", "p", "--vault-path", vault, "--no-git"); code != cli.ExitOK {
		t.Fatalf("init: exit %d\n%s", code, out)
	}
	if err := os.Remove(filepath.Join(vault, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	setGitEnabled(t, true)
	out, code := syncVaultTier(t, project, true)
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if f := gitIn(t, vault, "ls-files"); f != ".gitignore" {
		t.Errorf("tracked after sync: %q, want only .gitignore", f)
	}
	if st := porcelain(t, vault); strings.Contains(st, ".gitignore") {
		t.Errorf(".gitignore left uncommitted:\n%s", st)
	}
}

// Row 5: an operator's own uncommitted edit is not vp's write: kept, named,
// never committed.
func TestConfigSyncKeepsAnOperatorEditedGitignore(t *testing.T) {
	vault, project := cleanGitVault(t)
	head := gitIn(t, vault, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(vault, ".gitignore"), []byte(readGitignore(t, vault)+"my-own-line/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := syncVaultTier(t, project, true)
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if gitIn(t, vault, "rev-parse", "HEAD") != head {
		t.Errorf("an operator-edited .gitignore was committed:\n%s", out)
	}
	if !strings.Contains(out, "left uncommitted") || !strings.Contains(out, ".gitignore") {
		t.Errorf("the kept .gitignore is not named:\n%s", out)
	}
}

// Row 6: a top-up an earlier run left uncommitted is committed (the heal),
// both with --yes and on an interactive run that has nothing to do.
func TestConfigSyncHealsAnEarlierUncommittedTopUp(t *testing.T) {
	for _, yes := range []bool{true, false} {
		name := "interactive"
		if yes {
			name = "yes"
		}
		t.Run(name, func(t *testing.T) {
			vault, project := cleanGitVault(t)
			full := readGitignore(t, vault)
			commitGitignore(t, vault, withoutLine(full, droppedLine))
			// What an earlier run's top-up wrote and never committed.
			if _, err := storage.TopUpVaultGitignore(vault); err != nil {
				t.Fatal(err)
			}
			if porcelain(t, vault) == "" {
				t.Fatal("fixture: the earlier top-up is not dirty")
			}
			out, code := syncVaultTier(t, project, yes)
			if code != cli.ExitOK {
				t.Fatalf("exit %d\n%s", code, out)
			}
			if !yes && !strings.Contains(out, "Nothing to do") {
				t.Fatalf("fixture: the interactive run had something to do:\n%s", out)
			}
			assertSyncedClean(t, vault, out)
		})
	}
}

// Row 7: --dry-run writes and commits nothing.
func TestConfigSyncDryRunCommitsNothing(t *testing.T) {
	vault, project := cleanGitVault(t)
	commitGitignore(t, vault, withoutLine(readGitignore(t, vault), droppedLine))
	head, before := gitIn(t, vault, "rev-parse", "HEAD"), readGitignore(t, vault)
	out, code := syncVaultTier(t, project, false, "--dry-run")
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if gitIn(t, vault, "rev-parse", "HEAD") != head || readGitignore(t, vault) != before {
		t.Errorf("--dry-run wrote or committed:\n%s", out)
	}
}

// Row 8: with git_enabled = false the top-up is written but never committed,
// and the run says why, without failing.
func TestConfigSyncGitDisabledWritesButDoesNotCommit(t *testing.T) {
	vault, project := cleanGitVault(t)
	commitGitignore(t, vault, withoutLine(readGitignore(t, vault), droppedLine))
	head := gitIn(t, vault, "rev-parse", "HEAD")
	setGitEnabled(t, false)
	out, code := syncVaultTier(t, project, true)
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if gitIn(t, vault, "rev-parse", "HEAD") != head {
		t.Errorf("committed with git_enabled = false:\n%s", out)
	}
	if !strings.Contains(readGitignore(t, vault), droppedLine) {
		t.Error("the top-up was not written")
	}
	if !strings.Contains(out, "git_enabled = false") {
		t.Errorf("no row says the commit was skipped for git_enabled:\n%s", out)
	}
}

// Row 9: a vault nested in another repository: no commit into the enclosing
// one.
func TestConfigSyncCommitsNothingIntoAnEnclosingRepository(t *testing.T) {
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}
	_, _ = initTestEnv(t, false)
	outer := t.TempDir()
	gitIn(t, outer, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(outer, "README"), []byte("outer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, outer, "add", "--", "README")
	gitIn(t, outer, "commit", "-q", "-m", "outer")
	head := gitIn(t, outer, "rev-parse", "HEAD")
	vault := filepath.Join(outer, "vault")
	project := newProject(t)
	if code, out := runInit(t, project, "--name", "p", "--vault-path", vault, "--no-git"); code != cli.ExitOK {
		t.Fatalf("init: exit %d\n%s", code, out)
	}
	if err := os.WriteFile(filepath.Join(vault, ".gitignore"), []byte(withoutLine(readGitignore(t, vault), droppedLine)), 0o644); err != nil {
		t.Fatal(err)
	}
	setGitEnabled(t, true)
	out, _ := syncVaultTier(t, project, true)
	if gitIn(t, outer, "rev-parse", "HEAD") != head {
		t.Errorf("a commit landed in the enclosing repository:\n%s", out)
	}
	if !strings.Contains(out, "vault .gitignore not committed") {
		t.Errorf("no row says why nothing was committed:\n%s", out)
	}
}

// Row 10: the commit is local; the remote is not touched.
func TestConfigSyncVaultCommitIsLocalOnly(t *testing.T) {
	vault, project := cleanGitVault(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, filepath.Dir(origin), "init", "-q", "--bare", "-b", "main", origin)
	gitIn(t, vault, "remote", "add", "origin", origin)
	commitGitignore(t, vault, withoutLine(readGitignore(t, vault), droppedLine))
	gitIn(t, vault, "push", "-q", "origin", "main")
	remote := gitIn(t, origin, "rev-parse", "main")
	out, code := syncVaultTier(t, project, true)
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if gitIn(t, vault, "rev-parse", "HEAD") == remote {
		t.Fatalf("fixture: nothing was committed:\n%s", out)
	}
	if got := gitIn(t, origin, "rev-parse", "main"); got != remote {
		t.Errorf("the commit was pushed: origin main %s -> %s", remote, got)
	}
}

// Row 11: a malformed migration marker: nothing written to .gitignore and
// nothing committed, and the run names the marker.
func TestConfigSyncMalformedMarkerWritesAndCommitsNothing(t *testing.T) {
	vault, project := cleanGitVault(t)
	commitGitignore(t, vault, withoutLine(readGitignore(t, vault), droppedLine))
	if err := os.WriteFile(filepath.Join(vault, ".vibe-palace", "vault.toml"), []byte("format = 2\nauthored_only = 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, vault, "add", "--", ".vibe-palace/vault.toml")
	gitIn(t, vault, "commit", "-q", "-m", "a malformed marker")
	head, before := gitIn(t, vault, "rev-parse", "HEAD"), readGitignore(t, vault)
	out, _ := syncVaultTier(t, project, true)
	if gitIn(t, vault, "rev-parse", "HEAD") != head || readGitignore(t, vault) != before {
		t.Errorf("a malformed marker still wrote or committed:\n%s", out)
	}
	if !strings.Contains(out, "authored_only") {
		t.Errorf("the run does not name the bad marker key:\n%s", out)
	}
}
