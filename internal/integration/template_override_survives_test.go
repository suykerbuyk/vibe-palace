// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// TestIntegrationTemplateOverrideSurvivesSync drives the real `vp` binary
// through the three ways `vp config sync` used to lose an operator's vault
// Templates/ override of a built-in, on a git vault with a bare origin — the
// configuration in which the loss reached every host:
//
//   - two `--yes` syncs over lock-less overrides (the first overwrote them,
//     the second pruned the result and pushed the deletion);
//   - a second host whose templates.lock lacks the entry the first host has;
//   - an answerless sync over a mirror an old binary (or an upgrade reset)
//     left on top of a committed override — no --yes and no answer needed.
//
// Root-level resources take the same path as commands and skills, so
// workflow.md rides in the seed; a NEW vault-wide name (mycmd.md) rides along
// as the control nothing ever touched.
func TestIntegrationTemplateOverrideSurvivesSync(t *testing.T) {
	bin := buildVPBinary(t)

	t.Run("override-survives-repeated-sync-yes", func(t *testing.T) {
		env := setupFreshEnv(t)
		runVP(t, bin, env, nil, "init", env.projectDir,
			"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")
		overrides := map[string]string{
			"Templates/commands/wrap.md":      "# my wrap override\n",
			"Templates/workflow.md":           "# my workflow override\n",
			"Templates/skills/chair/SKILL.md": "# my chair override\n",
			"Templates/commands/mycmd.md":     "# my new command\n",
		}
		for rel, body := range overrides {
			putFile(t, env.vaultPath, rel, body)
		}
		origin := gitifyVaultWithOrigin(t, env.vaultPath)
		head := gitIn(t, env.vaultPath, "rev-parse", "HEAD")

		for i := 1; i <= 2; i++ {
			out := runVP(t, bin, env, nil, "config", "sync", "--yes", "--project-root", env.projectDir)
			if !strings.Contains(out, "updated=0") || !strings.Contains(out, "pruned=0") {
				t.Errorf("sync %d touched a template:\n%s", i, out)
			}
		}
		for rel, body := range overrides {
			p := filepath.Join(env.vaultPath, filepath.FromSlash(rel))
			if got, err := os.ReadFile(p); err != nil || string(got) != body {
				t.Errorf("%s: got %q (err=%v), want %q", rel, got, err, body)
			}
			for _, side := range []string{".bak", ".new"} {
				if _, err := os.Stat(p + side); err == nil {
					t.Errorf("%s%s written", rel, side)
				}
			}
		}
		if got := gitIn(t, env.vaultPath, "rev-parse", "HEAD"); got != head {
			t.Errorf("HEAD moved to %s", gitIn(t, env.vaultPath, "log", "-1", "--stat"))
		}
		if got := gitIn(t, origin, "rev-parse", "main"); got != head {
			t.Errorf("origin tip moved to %s", got)
		}
	})

	t.Run("two-host-override-survives", func(t *testing.T) {
		const mine = "# my wrap override\n"
		embSHA, ok := templates.EmbeddedSHA("commands/wrap.md")
		if !ok {
			t.Fatal("no embedded SHA for commands/wrap.md")
		}

		// Host A keeps the override: its lock records the embedded baseline.
		// The lock is written AFTER the commit, as on a real host, where vp's
		// committers never stage it — so it does not travel to host B.
		a := setupFreshEnv(t)
		runVP(t, bin, a, nil, "init", a.projectDir,
			"--name", a.projectName, "--vault-path", a.vaultPath, "--no-git")
		putFile(t, a.vaultPath, "Templates/commands/wrap.md", mine)
		origin := gitifyVaultWithOrigin(t, a.vaultPath)
		seedTrackedOverride(t, a.vaultPath, "commands/wrap.md", []byte(mine), embSHA)
		tip := gitIn(t, origin, "rev-parse", "main")
		runVP(t, bin, a, nil, "config", "sync", "--yes", "--project-root", a.projectDir)

		// Host B is a fresh clone: no lock, so the override is case 6 there.
		b := setupFreshEnv(t)
		cmd := exec.Command("git", "clone", "-q", origin, b.vaultPath)
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("clone: %v\n%s", err, out)
		}
		// A real host can commit: without an identity an old binary's prune
		// commit would fail on the identity check instead of pushing.
		gitIn(t, b.vaultPath, "config", "user.email", "hostb@example.invalid")
		gitIn(t, b.vaultPath, "config", "user.name", "Host B")
		runVP(t, bin, b, nil, "init", b.projectDir,
			"--name", b.projectName, "--vault-path", b.vaultPath, "--no-git")
		if _, err := os.Stat(filepath.Join(b.vaultPath, templates.LockRelPath)); !os.IsNotExist(err) {
			t.Fatalf("fixture: host B must start without a templates.lock (err=%v)", err)
		}
		for i := 1; i <= 2; i++ {
			out := runVP(t, bin, b, nil, "config", "sync", "--yes", "--project-root", b.projectDir)
			if !strings.Contains(out, "[keep] Templates/commands/wrap.md — ") {
				t.Errorf("host B sync %d did not keep the override:\n%s", i, out)
			}
		}

		if got := gitIn(t, origin, "rev-parse", "main"); got != tip {
			t.Errorf("host B pushed: origin tip %s -> %s", tip, got)
		}
		gitIn(t, a.vaultPath, "pull", "-q", "origin", "main")
		for _, vault := range []string{a.vaultPath, b.vaultPath} {
			p := filepath.Join(vault, "Templates", "commands", "wrap.md")
			if got, err := os.ReadFile(p); err != nil || string(got) != mine {
				t.Errorf("%s: got %q (err=%v), want the override", p, got, err)
			}
		}
	})

	t.Run("answerless-sync-restores-committed-override", func(t *testing.T) {
		const mine = "# my wrap override\n"
		env := setupFreshEnv(t)
		runVP(t, bin, env, nil, "init", env.projectDir,
			"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")
		putFile(t, env.vaultPath, "Templates/commands/wrap.md", mine)
		origin := gitifyVaultWithOrigin(t, env.vaultPath)
		head := gitIn(t, env.vaultPath, "rev-parse", "HEAD")

		// What an old binary's `o`/--yes sync or an upgrade reset leaves: the
		// embedded bytes over the committed override, with a lock entry at the
		// embedded SHA.
		embSHA, _ := templates.EmbeddedSHA("commands/wrap.md")
		seedTrackedOverride(t, env.vaultPath, "commands/wrap.md", embeddedBytesFor(t, "commands/wrap.md"), embSHA)

		// No --yes, and stdin is /dev/null: nobody answers anything.
		out := runVP(t, bin, env, nil, "config", "sync", "--project-root", env.projectDir)
		if !strings.Contains(out, "restored Templates/commands/wrap.md from HEAD") {
			t.Errorf("no restore reported:\n%s", out)
		}
		p := filepath.Join(env.vaultPath, "Templates", "commands", "wrap.md")
		if got, err := os.ReadFile(p); err != nil || string(got) != mine {
			t.Errorf("wrap.md: got %q (err=%v), want the committed override", got, err)
		}
		if st := gitIn(t, env.vaultPath, "status", "--porcelain", "--", "Templates/"); st != "" {
			t.Errorf("Templates/ left dirty: %q", st)
		}
		if got := gitIn(t, env.vaultPath, "rev-parse", "HEAD"); got != head {
			t.Errorf("a commit was made: %s", gitIn(t, env.vaultPath, "log", "-1", "--stat"))
		}
		if got := gitIn(t, origin, "rev-parse", "main"); got != head {
			t.Errorf("origin tip moved to %s", got)
		}
	})
}

// TestIntegrationTemplatePruneFailsSafe drives the real binary through the
// code review's H1 and M1 reproductions. Each starts from a mirror whose
// removal the sync will attempt — the state an old binary's overwrite or an
// upgrade reset leaves — and breaks something the prune depends on. Before the
// fix the prune removed the file first and asked git afterwards, so each of
// these left a deletion (H1) or a stranded, silent prune commit (M1).
func TestIntegrationTemplatePruneFailsSafe(t *testing.T) {
	bin := buildVPBinary(t)
	embSHA, ok := templates.EmbeddedSHA("commands/wrap.md")
	if !ok {
		t.Fatal("no embedded SHA for commands/wrap.md")
	}
	emb := embeddedBytesFor(t, "commands/wrap.md")

	// mirrorVault: a git vault with a bare origin whose committed wrap.md is
	// the embedded copy, with this host's lock entry recording it — a prune
	// the sync will attempt and, with git healthy, commit.
	mirrorVault := func(t *testing.T) (*testEnv, string) {
		env := setupFreshEnv(t)
		runVP(t, bin, env, nil, "init", env.projectDir,
			"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")
		putFile(t, env.vaultPath, "Templates/commands/wrap.md", string(emb))
		origin := gitifyVaultWithOrigin(t, env.vaultPath)
		seedTrackedOverride(t, env.vaultPath, "commands/wrap.md", emb, embSHA)
		return env, origin
	}
	wrapOf := func(env *testEnv) string {
		return filepath.Join(env.vaultPath, "Templates", "commands", "wrap.md")
	}

	t.Run("no-git-identity-removes-nothing", func(t *testing.T) {
		env, _ := mirrorVault(t)
		gitIn(t, env.vaultPath, "config", "--unset", "user.email")
		gitIn(t, env.vaultPath, "config", "--unset", "user.name")
		gitIn(t, env.vaultPath, "config", "user.useConfigOnly", "true")
		out, code := runVPCode(t, bin, env, nil, noIdentityEnv(), "config", "sync", "--yes", "--project-root", env.projectDir)
		if code == 0 {
			t.Errorf("exit 0 with no git identity:\n%s", out)
		}
		if _, err := os.Stat(wrapOf(env)); err != nil {
			t.Errorf("the file was removed although its removal could not be committed: %v\n%s", err, out)
		}
		if st := gitIn(t, env.vaultPath, "status", "--porcelain", "--", "Templates/"); strings.Contains(st, " D ") {
			t.Errorf("a deletion was left behind: %q", st)
		}
	})

	t.Run("corrupt-index-removes-nothing", func(t *testing.T) {
		env, _ := mirrorVault(t)
		if err := os.WriteFile(filepath.Join(env.vaultPath, ".git", "index"), []byte("not an index"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, code := runVPCode(t, bin, env, nil, nil, "config", "sync", "--yes", "--project-root", env.projectDir)
		if code == 0 {
			t.Errorf("exit 0 with an unreadable index:\n%s", out)
		}
		if _, err := os.Stat(wrapOf(env)); err != nil {
			t.Errorf("the file was removed although git could not read the index: %v\n%s", err, out)
		}
	})

	t.Run("remote-override-blocks-prune", func(t *testing.T) {
		env, origin := mirrorVault(t)
		head := gitIn(t, env.vaultPath, "rev-parse", "HEAD")
		hostB := filepath.Join(t.TempDir(), "hostB")
		clone := exec.Command("git", "clone", "-q", origin, hostB)
		clone.Env = gitEnv()
		if o, err := clone.CombinedOutput(); err != nil {
			t.Fatalf("clone: %v\n%s", err, o)
		}
		gitIn(t, hostB, "config", "user.email", "hostb@example.invalid")
		gitIn(t, hostB, "config", "user.name", "Host B")
		putFile(t, hostB, "Templates/commands/wrap.md", "# host B's override\n")
		gitIn(t, hostB, "commit", "-qam", "override from host B")
		gitIn(t, hostB, "push", "-q", "origin", "main")
		tip := gitIn(t, origin, "rev-parse", "main")

		out, _ := runVPCode(t, bin, env, nil, nil, "config", "sync", "--yes", "--project-root", env.projectDir)
		if got := gitIn(t, env.vaultPath, "rev-parse", "HEAD"); got != head {
			t.Errorf("a prune commit was made behind host B's override:\n%s\n%s", gitIn(t, env.vaultPath, "log", "-1", "--stat"), out)
		}
		if got := gitIn(t, origin, "rev-parse", "main"); got != tip {
			t.Errorf("origin moved to %s", got)
		}
		if _, err := os.Stat(wrapOf(env)); err != nil {
			t.Errorf("the mirror was removed: %v", err)
		}
		if !strings.Contains(out, "holds operator content") {
			t.Errorf("no report of the remote override:\n%s", out)
		}
	})

	t.Run("rejected-push-is-reported", func(t *testing.T) {
		env, origin := mirrorVault(t)
		if err := os.WriteFile(filepath.Join(origin, "hooks", "pre-receive"), []byte("#!/bin/sh\necho refused >&2\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		out, code := runVPCode(t, bin, env, nil, nil, "config", "sync", "--yes", "--project-root", env.projectDir)
		if code == 0 {
			t.Errorf("exit 0 although the prune commit did not reach origin:\n%s", out)
		}
		for _, want := range []string{"did not reach origin", "Templates/commands/wrap.md", "operator content: keep it"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})
}

// runVPCode runs the vp binary like runVP but returns the exit code instead
// of failing on a non-zero one, with extra environment entries appended.
func runVPCode(t *testing.T, bin string, env *testEnv, stdin []byte, extra []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(append(os.Environ(), "HOME="+env.home, "XDG_CONFIG_HOME="+env.xdgConfig), extra...)
	cmd.Dir = env.projectDir
	if stdin != nil {
		cmd.Stdin = strings.NewReader(string(stdin))
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode()
	}
	t.Fatalf("vp %s: %v", strings.Join(args, " "), err)
	return "", -1
}

// noIdentityEnv strips every source of a git identity but the repository's
// own config (which the caller unsets).
func noIdentityEnv() []string {
	return []string{
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=", "GIT_AUTHOR_EMAIL=", "GIT_COMMITTER_NAME=", "GIT_COMMITTER_EMAIL=", "EMAIL=",
	}
}

// gitEnv neutralises the developer's global and system git config so a
// fixture means the same thing on every machine.
func gitEnv() []string {
	return append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
}

// gitIn runs git in dir and returns its trimmed combined output, failing the
// test on a git error.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitifyVaultWithOrigin commits the vault's whole tree and pushes it to a
// fresh bare origin, returning the origin path.
func gitifyVaultWithOrigin(t *testing.T, vault string) string {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, origin, "init", "-q", "--bare", "-b", "main")
	gitIn(t, vault, "init", "-q", "-b", "main")
	gitIn(t, vault, "config", "user.email", "test@example.invalid")
	gitIn(t, vault, "config", "user.name", "Test")
	gitIn(t, vault, "add", "-A")
	gitIn(t, vault, "commit", "-q", "-m", "seed with overrides")
	gitIn(t, vault, "remote", "add", "origin", origin)
	gitIn(t, vault, "push", "-q", "-u", "origin", "main")
	return origin
}

func putFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
