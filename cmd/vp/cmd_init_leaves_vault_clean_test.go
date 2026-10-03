// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// gitIn runs git in dir and returns its trimmed output, failing the test on
// error.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %s: %v", args, dir, out, err)
	}
	return strings.TrimSpace(string(out))
}

// porcelain is `git status --porcelain -uall` of dir: every uncommitted path,
// untracked files listed one by one.
func porcelain(t *testing.T, dir string) string {
	t.Helper()
	return gitIn(t, dir, "status", "--porcelain", "-uall")
}

// runInit runs `vp init args...` in-process and returns its exit code and
// stdout.
func runInit(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var code int
	out := captureStdout(t, func() {
		code = cmdInit(cli.BuildInfo{Version: "test"}).Run(args)
	})
	return code, out
}

// newProject is a fresh project directory (a go.mod, nothing else).
func newProject(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	markProjectDir(t, p)
	return p
}

// clonedVault is a vault as a new host meets it: a clone of a vault whose
// committed .gitignore predates vp's canonical lines and which carries no
// data-format stamp, holding one existing project "old".
func clonedVault(t *testing.T) string {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin")
	if err := os.MkdirAll(filepath.Join(origin, "Projects", "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, origin, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, ".gitignore"), []byte(".DS_Store\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(origin, "Projects", "old", "resume.md"), []byte("# old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, origin, "add", "-A")
	gitIn(t, origin, "commit", "-q", "-m", "seed")
	vault := filepath.Join(t.TempDir(), "vault")
	gitIn(t, filepath.Dir(vault), "clone", "-q", origin, vault)
	return vault
}

// seedCleanVault is an existing install whose vault is already clean: a first
// `vp init` creates it, and anything that init left uncommitted is committed by
// hand, so the case built on it measures only what the NEXT init writes.
func seedCleanVault(t *testing.T, vault, project string) {
	t.Helper()
	if code, out := runInit(t, newProject(t), "--name", project, "--vault-path", vault); code != cli.ExitOK {
		t.Fatalf("seed init: exit %d\n%s", code, out)
	}
	if porcelain(t, vault) != "" {
		gitIn(t, vault, "add", "-A")
		gitIn(t, vault, "commit", "-q", "-m", "seed: commit what the first init left")
	}
}

// assertClean fails unless the vault has no uncommitted path and a sync
// preview does not refuse it.
func assertClean(t *testing.T, vault, out string) {
	t.Helper()
	if st := porcelain(t, vault); st != "" {
		t.Errorf("vault left dirty by vp init:\n%s\n--- init output ---\n%s", st, out)
	}
	if _, refused, err := storage.SyncPreview(vault); refused || err != nil {
		t.Errorf("sync preview refuses the vault vp init left (refused=%v): %v", refused, err)
	}
}

// TestInitLeavesTheVaultCleanOnEveryPath is the acceptance gate for "vp init
// can create a dirty vault": on every init path where the vault is its own git
// repository, git is enabled and a committer identity resolves, init exits 0
// and leaves `git status --porcelain -uall` EMPTY — every path it wrote is
// committed — and a sync does not refuse. The paths are the ones the audit
// measured (task fresh-vault-init-leaves-gitignore-and-vault-toml-uncommitted,
// § Audit): A/A2/A3/B/F/F2 were dirty on 7924110, H and MCP were already clean
// and are kept as regression guards. The nested-vault case E is the refusal
// case: init must write nothing into the enclosing repository.
func TestInitLeavesTheVaultCleanOnEveryPath(t *testing.T) {
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}

	t.Run("A first install, fresh vault", func(t *testing.T) {
		initTestEnv(t, false)
		vault := filepath.Join(t.TempDir(), "vault")
		proj := newProject(t)
		code, out := runInit(t, proj, "--name", "fresh", "--vault-path", vault)
		if code != cli.ExitOK {
			t.Fatalf("exit %d\n%s", code, out)
		}
		assertClean(t, vault, out)
		if b := gitIn(t, vault, "symbolic-ref", "--short", "HEAD"); b != "main" {
			t.Errorf("fresh vault is on branch %q, want main", b)
		}
		// The stamp is tracked, in the vault's root commit, with the .gitignore.
		root := gitIn(t, vault, "rev-list", "--max-parents=0", "HEAD")
		if files := gitIn(t, vault, "show", "--name-only", "--format=", root); files != ".gitignore\n.vibe-palace/vault.toml" {
			t.Errorf("root commit holds %q, want .gitignore and .vibe-palace/vault.toml", files)
		}

		t.Run("A2 re-init of the same project", func(t *testing.T) {
			code, out := runInit(t, proj, "--name", "fresh")
			if code != cli.ExitOK {
				t.Fatalf("exit %d\n%s", code, out)
			}
			assertClean(t, vault, out)
		})
		t.Run("A3 second project into the same vault", func(t *testing.T) {
			code, out := runInit(t, newProject(t), "--name", "second")
			if code != cli.ExitOK {
				t.Fatalf("exit %d\n%s", code, out)
			}
			assertClean(t, vault, out)
		})
	})

	t.Run("A0 first install into a pre-created EMPTY directory", func(t *testing.T) {
		initTestEnv(t, false)
		vault := t.TempDir()
		code, out := runInit(t, newProject(t), "--name", "empty", "--vault-path", vault)
		if code != cli.ExitOK {
			t.Fatalf("exit %d\n%s", code, out)
		}
		assertClean(t, vault, out)
		if msg := gitIn(t, vault, "log", "--reverse", "--format=%s"); !strings.HasPrefix(msg, "Create the vault .gitignore") {
			t.Errorf("first commit = %q, want the .gitignore CREATE (not a top-up)", msg)
		}
	})

	t.Run("B existing install, --vault-path naming a new directory", func(t *testing.T) {
		initTestEnv(t, true)
		vault := filepath.Join(t.TempDir(), "new-vault")
		code, out := runInit(t, newProject(t), "--name", "bee", "--vault-path", vault)
		if code != cli.ExitOK {
			t.Fatalf("exit %d\n%s", code, out)
		}
		assertClean(t, vault, out)
	})

	for _, name := range []string{"newp", "old"} {
		t.Run("F first install onto a cloned vault, project "+name, func(t *testing.T) {
			initTestEnv(t, false)
			vault := clonedVault(t)
			code, out := runInit(t, newProject(t), "--name", name, "--vault-path", vault)
			if code != cli.ExitOK {
				t.Fatalf("exit %d\n%s", code, out)
			}
			assertClean(t, vault, out)
			// init never stamps a vault it did not create.
			if _, err := os.Lstat(filepath.Join(vault, ".vibe-palace", "vault.toml")); !os.IsNotExist(err) {
				t.Errorf("init stamped an existing vault it did not create (lstat err: %v)", err)
			}
		})
	}

	t.Run("F-dirty: an operator's uncommitted .gitignore edit is never committed", func(t *testing.T) {
		initTestEnv(t, false)
		vault := clonedVault(t)
		before := gitIn(t, vault, "rev-parse", "HEAD:.gitignore")
		if err := os.WriteFile(filepath.Join(vault, ".gitignore"), []byte(".DS_Store\nmine/\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		code, out := runInit(t, newProject(t), "--name", "newp", "--vault-path", vault)
		if code != cli.ExitOK {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if st := porcelain(t, vault); st != "M .gitignore" {
			t.Errorf("porcelain = %q, want only the operator's own ` M .gitignore`", st)
		}
		if after := gitIn(t, vault, "rev-parse", "HEAD:.gitignore"); after != before {
			t.Errorf("init committed a .gitignore carrying an edit it did not make")
		}
		if !strings.Contains(foldSpace(out), "left uncommitted") {
			t.Errorf("the Vault row does not name the file it left:\n%s", out)
		}
	})

	t.Run("H existing install, existing clean vault, new project", func(t *testing.T) {
		initTestEnv(t, false)
		vault := filepath.Join(t.TempDir(), "vault")
		seedCleanVault(t, vault, "h1")
		code, out := runInit(t, newProject(t), "--name", "h2")
		if code != cli.ExitOK {
			t.Fatalf("exit %d\n%s", code, out)
		}
		assertClean(t, vault, out)
	})

	t.Run("M MCP vp_init into an existing clean vault", func(t *testing.T) {
		initTestEnv(t, false)
		vault := filepath.Join(t.TempDir(), "vault")
		seedCleanVault(t, vault, "seed")
		tool := tools.InitProjectTool(storage.NewVault(vault))
		for _, args := range []map[string]any{
			{"path": newProject(t), "name": "mq"},
			{"name": "mnp"},
		} {
			params, _ := json.Marshal(args)
			if _, err := tool.Handler(context.Background(), params); err != nil {
				t.Fatalf("vp_init %v: %v", args, err)
			}
			assertClean(t, vault, "(vp_init)")
		}
	})

	t.Run("E nested new vault: refused before any write", func(t *testing.T) {
		initTestEnv(t, false)
		outer := t.TempDir()
		gitIn(t, outer, "init", "-q")
		before := porcelain(t, outer)
		vault := filepath.Join(outer, "vault")
		code, out := runInit(t, newProject(t), "--name", "nest", "--vault-path", vault)
		if code != cli.ExitSystem {
			t.Errorf("exit %d, want ExitSystem\n%s", code, out)
		}
		if !strings.Contains(out, "git init skipped") {
			t.Errorf("the Vault row does not give the refusal's reason:\n%s", out)
		}
		if after := porcelain(t, outer); after != before {
			t.Errorf("init wrote into the enclosing repository's work tree:\n%s", after)
		}
		if _, err := os.Lstat(vault); !os.IsNotExist(err) {
			t.Errorf("init created the nested vault directory it refused (lstat err: %v)", err)
		}
	})
}

// TestInitVaultCommitFailureStillOnboardsAndExitsNonZero: with no committer
// identity, the vault step's commit fails. That is a [FAIL] Vault row naming
// the identity remedy and a non-zero exit — but init does not stop there: the
// vault is on disk and usable, so the project steps still run (their own
// scaffold commit fails for the same reason and says so).
func TestInitVaultCommitFailureStillOnboardsAndExitsNonZero(t *testing.T) {
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}
	initTestEnv(t, false)
	for k := range testCommitterIdentity {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("EMAIL", "")
	vault := filepath.Join(t.TempDir(), "vault")
	proj := newProject(t)
	code, out := runInit(t, proj, "--name", "noid", "--vault-path", vault)
	if code != cli.ExitSystem {
		t.Errorf("exit %d, want ExitSystem\n%s", code, out)
	}
	folded := foldSpace(out)
	if !strings.Contains(folded, "[FAIL] Vault") || !strings.Contains(folded, "set a git committer identity") {
		t.Errorf("the Vault row does not fail with the identity remedy:\n%s", out)
	}
	if !strings.Contains(folded, "then commit them with `vp vault commit --paths .gitignore,.vibe-palace/vault.toml") {
		t.Errorf("the vault remedy does not name the hand commit of the vault's own files (and only it — re-running vp init no longer runs the vault step):\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(proj, ".vibe-palace.toml")); err != nil {
		t.Errorf("a failed vault commit stopped onboarding: %v\n%s", err, out)
	}
}

// TestInitVaultCommitFailureAloneExitsNonZero: when ONLY the vault step's
// commit fails — the project scaffold commits fine — init still exits
// non-zero. A pre-commit hook (core.hooksPath, from a sandboxed global git
// config) rejects any commit that stages the vault .gitignore, which fails
// exactly the vault commit.
func TestInitVaultCommitFailureAloneExitsNonZero(t *testing.T) {
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}
	initTestEnv(t, false)
	hooks := t.TempDir()
	hook := "#!/bin/sh\ngit diff --cached --name-only | grep -qx .gitignore && { echo 'hook: .gitignore rejected' >&2; exit 1; }\nexit 0\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	gc := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(gc, []byte("[core]\n\thooksPath = "+hooks+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gc)
	vault := filepath.Join(t.TempDir(), "vault")
	code, out := runInit(t, newProject(t), "--name", "hooked", "--vault-path", vault)
	if code != cli.ExitSystem {
		t.Errorf("exit %d, want ExitSystem\n%s", code, out)
	}
	folded := foldSpace(out)
	if !strings.Contains(folded, "[FAIL] Vault") || strings.Contains(folded, "[FAIL] Project templates commit") {
		t.Errorf("want only the Vault row failed:\n%s", out)
	}
	// Untracked again, not left staged: a refused commit restores the index
	// (storage.stageAndCommitLocked), so a later tidy cannot sweep part of it.
	if st := porcelain(t, vault); st != "?? .gitignore\n?? .vibe-palace/vault.toml" {
		t.Errorf("porcelain = %q, want the vault step's two files untracked and nothing staged", st)
	}
}
