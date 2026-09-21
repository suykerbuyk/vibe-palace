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
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// newRepoWithOrigin builds a standalone vault repository wired to its own bare
// origin and pushed in sync, WITHOUT touching the host config — it is the
// throwaway copy a --vault rehearsal names, never the configured vault.
// Returns the repository root and its bare origin.
func newRepoWithOrigin(t *testing.T) (dir, bare string) {
	t.Helper()
	dir, bare = t.TempDir(), t.TempDir()
	gitRun(t, bare, "init", "--bare", "-b", "main")
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "test@test.com")
	gitRun(t, dir, "config", "user.name", "Test")
	gitRun(t, dir, "remote", "add", "origin", bare)
	mkfile(t, dir, ".gitignore", ".vp-locks/\n")
	mkfile(t, dir, "seed.txt", "seed\n")
	gitRun(t, dir, "add", ".gitignore", "seed.txt")
	gitRun(t, dir, "commit", "-m", "seed")
	gitRun(t, dir, "push", "-u", "origin", "main")
	return dir, bare
}

// gitRun runs git in dir against no global or system git config.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareMain returns the main ref of a bare repository.
func bareMain(t *testing.T, bare string) string {
	t.Helper()
	return gitRun(t, bare, "rev-parse", "refs/heads/main")
}

const flagTestArtifact = "Projects/vibe-palace/sessions/2026-09-21.md"

func TestResolveVaultRootFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	called := false
	fallback := func() (string, string, error) {
		called = true
		return "/configured", "global:/cfg/config.toml", nil
	}

	root, source, err := resolveVaultRootFlag("~/copy", fallback)
	if err != nil {
		t.Fatalf("resolveVaultRootFlag(~/copy): %v", err)
	}
	if want := filepath.Join(home, "copy"); root != want {
		t.Errorf("root = %q, want the tilde-expanded %q", root, want)
	}
	if source != "flag:--vault" {
		t.Errorf("source = %q, want %q", source, "flag:--vault")
	}
	if called {
		t.Error("fallback consulted although --vault was given")
	}

	root, source, err = resolveVaultRootFlag("  ", fallback)
	if err != nil {
		t.Fatalf("resolveVaultRootFlag(blank): %v", err)
	}
	if root != "/configured" || source != "global:/cfg/config.toml" {
		t.Errorf("blank flag = (%q, %q), want the fallback's (/configured, global:/cfg/config.toml)", root, source)
	}
}

// TestVaultFlagRehearsalIsolation is the task's acceptance test in miniature:
// with the host config pointing at a stand-in LIVE vault, every mutating
// command run with --vault COPY must move the copy and leave the live vault's
// HEAD and origin at their literal before-shas. The live vault is given the
// same work to do as the copy, so a command that ignored the flag would visibly
// move it rather than pass by having nothing to act on.
func TestVaultFlagRehearsalIsolation(t *testing.T) {
	for _, tc := range []struct {
		name string
		// prepare gives dir something for the command to act on.
		prepare func(t *testing.T, dir string)
		args    []string
		cmd     func() *cli.Command
		// movesOrigin: the command publishes, so the copy's ORIGIN must move;
		// otherwise it commits locally and the copy's HEAD must move.
		movesOrigin bool
	}{
		{
			name:    "tidy",
			prepare: func(t *testing.T, dir string) { mkfile(t, dir, flagTestArtifact, "session\n") },
			args:    []string{"--no-push"},
			cmd:     cmdVaultTidy,
		},
		{
			name:    "commit",
			prepare: func(t *testing.T, dir string) { mkfile(t, dir, flagTestArtifact, "session\n") },
			args:    []string{"--paths", flagTestArtifact, "--message", "rehearse"},
			cmd:     cmdVaultCommit,
		},
		{
			name: "push",
			prepare: func(t *testing.T, dir string) {
				mkfile(t, dir, "local.txt", "ahead\n")
				gitRun(t, dir, "add", "local.txt")
				gitRun(t, dir, "commit", "-m", "ahead of origin")
			},
			cmd:         cmdVaultPush,
			movesOrigin: true,
		},
		{
			name:        "sync",
			prepare:     func(t *testing.T, dir string) { mkfile(t, dir, flagTestArtifact, "session\n") },
			cmd:         cmdVaultSync,
			movesOrigin: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := setupVaultWithOrigin(t)
			liveBare := gitRun(t, live, "remote", "get-url", "origin")
			copyDir, copyBare := newRepoWithOrigin(t)
			tc.prepare(t, live)
			tc.prepare(t, copyDir)

			liveHead, liveOrigin := gitHead(t, live), bareMain(t, liveBare)
			copyHead, copyOrigin := gitHead(t, copyDir), bareMain(t, copyBare)

			stdout, stderr, code := runVaultCmdCapturingBoth(t, tc.cmd(), append(tc.args, "--vault", copyDir)...)
			if code != cli.ExitOK {
				t.Fatalf("exit code = %d, want %d\nstdout:%s\nstderr:%s", code, cli.ExitOK, stdout, stderr)
			}

			if got := gitHead(t, live); got != liveHead {
				t.Errorf("LIVE vault HEAD moved %s -> %s: --vault was not honoured", liveHead, got)
			}
			if got := bareMain(t, liveBare); got != liveOrigin {
				t.Errorf("LIVE vault origin moved %s -> %s: --vault was not honoured", liveOrigin, got)
			}
			if tc.movesOrigin {
				if got := bareMain(t, copyBare); got == copyOrigin {
					t.Errorf("copy's origin did not move (still %s): the named vault received nothing", copyOrigin)
				}
			} else if got := gitHead(t, copyDir); got == copyHead {
				t.Errorf("copy's HEAD did not move (still %s): the named vault received nothing", copyHead)
			}
		})
	}
}

// TestVaultFlagReportsItsSource pins assertion 3: an operator mid-rehearsal
// can tell which vault a command addressed and WHY, on every run.
func TestVaultFlagReportsItsSource(t *testing.T) {
	live := setupVaultWithOrigin(t)
	copyDir, _ := newRepoWithOrigin(t)

	for _, tc := range []struct {
		name string
		cmd  func() *cli.Command
		args []string
	}{
		{"tidy", cmdVaultTidy, []string{"--dry-run"}},
		{"status", cmdVaultStatus, []string{"--no-fetch"}},
	} {
		t.Run(tc.name+" with --vault", func(t *testing.T) {
			_, stderr, code := runVaultCmdCapturingBoth(t, tc.cmd(), append(tc.args, "--vault", copyDir)...)
			if code != cli.ExitOK {
				t.Fatalf("exit code = %d\nstderr:%s", code, stderr)
			}
			for _, want := range []string{"vault_path = " + copyDir + "\n", "vault_path source = flag:--vault\n"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr missing %q; got:\n%s", want, stderr)
				}
			}
		})
		t.Run(tc.name+" without --vault", func(t *testing.T) {
			_, stderr, code := runVaultCmdCapturingBoth(t, tc.cmd(), tc.args...)
			if code != cli.ExitOK {
				t.Fatalf("exit code = %d\nstderr:%s", code, stderr)
			}
			if !strings.Contains(stderr, "vault_path = "+live+"\n") ||
				!strings.Contains(stderr, "vault_path source = global:") {
				t.Errorf("stderr does not name the configured vault with a global: source; got:\n%s", stderr)
			}
		})
	}
}

// TestVaultFlagRefusesSubfolderOfAVault is the case the positive guard exists
// for. git walks up from whatever directory it is handed, so a --vault naming
// a folder INSIDE the live vault is answered by the live vault's repository.
//
// Without the guard, `vault status` exits 0 reporting the enclosing repository's
// branch, remotes and dirt as the named vault's. `vault tidy` is stopped later,
// at commit time, by storage's own nested-repository check — but as a system
// failure after scanning, not as a refusal naming the state. The live vault
// carries a sweepable artifact, so a commit that did land would show as a moved
// HEAD.
func TestVaultFlagRefusesSubfolderOfAVault(t *testing.T) {
	live := setupVaultWithOrigin(t)
	mkfile(t, live, flagTestArtifact, "session\n")
	sub := filepath.Join(live, "Projects")
	before := gitHead(t, live)

	for _, tc := range []struct {
		name string
		cmd  func() *cli.Command
		args []string
	}{
		{"tidy", cmdVaultTidy, []string{"--no-push"}},
		{"status", cmdVaultStatus, []string{"--no-fetch"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runVaultCmdCapturingBoth(t, tc.cmd(), append(tc.args, "--vault", sub)...)
			if code != cli.ExitUser {
				t.Errorf("exit code = %d, want %d\nstdout:%s\nstderr:%s", code, cli.ExitUser, stdout, stderr)
			}
			if !strings.Contains(stderr, "inside another repository") {
				t.Errorf("refusal does not name the nested state; stderr:\n%s", stderr)
			}
			if got := gitHead(t, live); got != before {
				t.Errorf("the enclosing (live) vault's HEAD moved %s -> %s", before, got)
			}
		})
	}
}

// TestVaultFlagRefusesNonGitDirectory: a directory no git repository owns is
// not a vault a git command can act on. It must be refused by name, before any
// git runs — and nothing may be initialised there.
func TestVaultFlagRefusesNonGitDirectory(t *testing.T) {
	setupVaultWithOrigin(t)
	plain := t.TempDir()
	mkfile(t, plain, flagTestArtifact, "session\n")

	stdout, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultTidy(), "--no-push", "--vault", plain)
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d\nstdout:%s\nstderr:%s", code, cli.ExitUser, stdout, stderr)
	}
	if !strings.Contains(stderr, "not a git repository") {
		t.Errorf("refusal does not name the not-a-repository state; stderr:\n%s", stderr)
	}
	if _, err := os.Lstat(filepath.Join(plain, ".git")); err == nil {
		t.Error("a .git was created in the refused directory")
	}
}

// TestRefuseUnlessOwnGitRepo pins the guard itself, state by state: only
// VaultGitOK is admitted, and every refusal names its state.
func TestRefuseUnlessOwnGitRepo(t *testing.T) {
	own, _ := newRepoWithOrigin(t)
	nested := filepath.Join(own, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	plain := t.TempDir()
	broken := t.TempDir()
	mkfile(t, broken, ".git", "gitdir: "+filepath.Join(broken, "missing")+"\n")

	for _, tc := range []struct {
		name, root, want string // want "" means admitted
	}{
		{"VaultGitOK", own, ""},
		{"VaultNotGit", plain, "not a git repository"},
		{"VaultGitNested", nested, "inside another repository"},
		{"VaultGitBroken", broken, "git cannot use this repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseUnlessOwnGitRepo(tc.root)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("refused its own repository: %v", err)
			case tc.want != "" && err == nil:
				t.Errorf("admitted %s; want a refusal naming %q", tc.root, tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Errorf("refusal %q does not name %q", err, tc.want)
			}
		})
	}

	t.Run("VaultGitUnavailable", func(t *testing.T) {
		t.Setenv("PATH", "")
		err := refuseUnlessOwnGitRepo(own)
		if err == nil || !strings.Contains(err.Error(), "git is not on PATH") {
			t.Errorf("with git off PATH: err = %v, want a refusal naming git-unavailable", err)
		}
	})
}

// TestVaultFlagHonoursHostGitDisabled: naming a vault does not bypass the
// host's git_enabled = false. It is host policy on whether vp runs git at all.
func TestVaultFlagHonoursHostGitDisabled(t *testing.T) {
	setupVaultWithOrigin(t)
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "vibe-palace", "config.toml")
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, append(data, []byte("git_enabled = false\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	copyDir, _ := newRepoWithOrigin(t)
	mkfile(t, copyDir, flagTestArtifact, "session\n")
	before := gitHead(t, copyDir)

	// The --dry-run leg matters most: storage.TidyVault re-checks git_enabled
	// on the apply path, but TidyScan does not, so for a dry run the gate in
	// vaultRootFor is the only one.
	for _, args := range [][]string{{"--no-push"}, {"--dry-run"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultTidy(), append(args, "--vault", copyDir)...)
			if code != cli.ExitUser {
				t.Errorf("exit code = %d, want %d\nstderr:%s", code, cli.ExitUser, stderr)
			}
			if !strings.Contains(stderr, "git_enabled") {
				t.Errorf("refusal does not cite git_enabled; stderr:\n%s", stderr)
			}
			if got := gitHead(t, copyDir); got != before {
				t.Errorf("named vault committed under git_enabled = false: HEAD %s -> %s", before, got)
			}
		})
	}
}

func TestVaultFlagRefusesNonexistentPath(t *testing.T) {
	setupVaultWithOrigin(t)
	missing := filepath.Join(t.TempDir(), "no-such-vault")

	_, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultTidy(), "--dry-run", "--vault", missing)
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d\nstderr:%s", code, cli.ExitUser, stderr)
	}
	if !strings.Contains(stderr, missing) {
		t.Errorf("refusal does not name the path; stderr:\n%s", stderr)
	}
}

// TestVaultFlagSurfaceGatesTheNamedRoot: surfaceGate's preRun fail-stop checks
// the CONFIGURED vault, so for a mutating command with --vault the named root
// must be checked too. A copy stamped by a newer binary must refuse tidy and
// commit even though the live vault is compatible, and must not be written.
// Read-only status stays ungated, matching its registration.
func TestVaultFlagSurfaceGatesTheNamedRoot(t *testing.T) {
	t.Setenv("VP_SURFACE_GATE", "")
	live := setupVaultWithOrigin(t)
	copyDir, _ := newRepoWithOrigin(t)
	stampDir := filepath.Join(copyDir, "Projects", "vibe-palace")
	if err := os.MkdirAll(stampDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := surface.WriteStamp(stampDir, surface.MCPSurfaceVersion+1, "newer-binary"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, copyDir, "add", "-A")
	gitRun(t, copyDir, "commit", "-m", "stamped by a newer binary")

	// Controls: the fixture must split exactly as described, or the refusal
	// below could come from the live vault rather than the named one.
	if err := surface.CheckCompatible(live); err != nil {
		t.Fatalf("live vault is not compatible, so the fixture measures nothing: %v", err)
	}
	if err := surface.CheckCompatible(copyDir); err == nil {
		t.Fatal("copy is compatible, so the fixture measures nothing")
	}

	for _, tc := range []struct {
		name string
		cmd  func() *cli.Command
		args []string
	}{
		{"tidy", cmdVaultTidy, []string{"--no-push"}},
		{"commit", cmdVaultCommit, []string{"--paths", flagTestArtifact, "--message", "rehearse"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mkfile(t, copyDir, flagTestArtifact, "session\n")
			before := gitHead(t, copyDir)
			stdout, stderr, code := runVaultCmdCapturingBoth(t, tc.cmd(), append(tc.args, "--vault", copyDir)...)
			if code != cli.ExitSystem {
				t.Errorf("exit code = %d, want %d (surface fail-stop)\nstdout:%s\nstderr:%s", code, cli.ExitSystem, stdout, stderr)
			}
			if !strings.Contains(stderr, "surface") {
				t.Errorf("refusal does not come from the surface gate; stderr:\n%s", stderr)
			}
			if got := gitHead(t, copyDir); got != before {
				t.Errorf("an older binary wrote a newer-surface vault: HEAD %s -> %s", before, got)
			}
		})
	}

	t.Run("status stays ungated", func(t *testing.T) {
		_, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultStatus(), "--no-fetch", "--vault", copyDir)
		if code != cli.ExitOK {
			t.Errorf("read-only status refused on a newer-surface copy: exit %d\nstderr:%s", code, stderr)
		}
	})
}

// TestEnforceSurfaceOnRoot pins the helper on its own terms: any resolved root,
// no --vault involved. A newer-surface root fail-stops; a compatible one passes.
func TestEnforceSurfaceOnRoot(t *testing.T) {
	t.Setenv("VP_SURFACE_GATE", "")
	compatible := t.TempDir()
	newer := t.TempDir()
	stampDir := filepath.Join(newer, "Projects", "p")
	if err := os.MkdirAll(stampDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := surface.WriteStamp(stampDir, surface.MCPSurfaceVersion+1, "newer-binary"); err != nil {
		t.Fatal(err)
	}

	if code := enforceSurfaceOnRoot(compatible); code != cli.ExitOK {
		t.Errorf("compatible root: exit %d, want %d", code, cli.ExitOK)
	}
	var code int
	stderr := captureStderr(t, func() { code = enforceSurfaceOnRoot(newer) })
	if code != cli.ExitSystem {
		t.Errorf("newer-surface root: exit %d, want %d", code, cli.ExitSystem)
	}
	if !strings.Contains(stderr, "surface") {
		t.Errorf("fail-stop did not report the gate's error; stderr:\n%s", stderr)
	}
}
