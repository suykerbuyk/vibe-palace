// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// TestIntegrationTemplateResetViaBinary drives the real `vp` binary through
// upgrade-overwrite-resets-vault-template-overrides:
//
//   - `vp commands upgrade --overwrite` and `vp skills upgrade --overwrite`,
//     with stdin not a terminal, keep every vault Templates/ override
//     byte-for-byte and list it as [keep] — at 1f3bb62 they reset both;
//   - `vp commands reset` / `vp skills reset` remove an override and keep a
//     backup named by its content that no later reset overwrites;
//   - on a vault that is its own repository the removal is committed locally,
//     `vp vault sync` publishes it, and a fresh clone no longer has the file;
//   - a refused commit leaves the removal unstaged, so the next config sync
//     defers nothing;
//   - `vp config sync` never restores a reset file.
func TestIntegrationTemplateResetViaBinary(t *testing.T) {
	bin := buildVPBinary(t)

	// bodyOnly returns the embedded bytes of rel with a line appended at the
	// end: the command's brief and the skill's frontmatter are unchanged, so
	// no shim goes stale and the override is no pending work.
	bodyOnly := func(t *testing.T, rel string) string {
		return string(embeddedBytesFor(t, rel)) + "\nAn operator's addition.\n"
	}
	backupOf := func(vault, rel, body string) string {
		return filepath.Join(vault, filepath.FromSlash(templates.BackupName(rel, []byte(body))))
	}
	fresh := func(t *testing.T) *testEnv {
		env := setupFreshEnv(t)
		runVP(t, bin, env, nil, "init", env.projectDir,
			"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")
		return env
	}
	// gitVault turns an initialised vault into its own repository with an
	// origin and production's canonical .gitignore — which is what keeps the
	// backups (*.bak) and the lock sidecars out of `vp vault sync`'s way.
	gitVault := func(t *testing.T, env *testEnv) string {
		putFile(t, env.vaultPath, ".gitignore", strings.Join(storage.CanonicalGitignorePatterns, "\n")+"\n")
		return gitifyVaultWithOrigin(t, env.vaultPath)
	}

	t.Run("overwrite-keeps", func(t *testing.T) {
		env := fresh(t)
		wrap, chair := bodyOnly(t, "commands/wrap.md"), bodyOnly(t, "skills/chair/SKILL.md")
		putFile(t, env.vaultPath, "Templates/commands/wrap.md", wrap)
		putFile(t, env.vaultPath, "Templates/skills/chair/SKILL.md", chair)

		out, code := runVPCode(t, bin, env, nil, nil, "commands", "upgrade", "--overwrite")
		if code != 0 || !strings.Contains(out, "[keep] Templates/commands/wrap.md — override of a built-in") {
			t.Errorf("commands upgrade --overwrite: exit %d\n%s", code, out)
		}
		out, code = runVPCode(t, bin, env, nil, nil, "skills", "upgrade", "--overwrite")
		if code != 0 || !strings.Contains(out, "[keep] Templates/skills/chair/ — override of a built-in") {
			t.Errorf("skills upgrade --overwrite: exit %d\n%s", code, out)
		}
		for rel, body := range map[string]string{"Templates/commands/wrap.md": wrap, "Templates/skills/chair/SKILL.md": chair} {
			p := filepath.Join(env.vaultPath, filepath.FromSlash(rel))
			if got, err := os.ReadFile(p); err != nil || string(got) != body {
				t.Errorf("%s changed (err=%v)", rel, err)
			}
			if _, err := os.Stat(p + ".bak"); !os.IsNotExist(err) {
				t.Errorf("%s.bak written", rel)
			}
		}
	})

	t.Run("reset-removes-with-backup", func(t *testing.T) {
		env := fresh(t)
		const a, b = "# override A\n", "# override B\n"
		chair := bodyOnly(t, "skills/chair/SKILL.md")
		putFile(t, env.vaultPath, "Templates/commands/wrap.md", a)
		putFile(t, env.vaultPath, "Templates/skills/chair/SKILL.md", chair)
		wrap := filepath.Join(env.vaultPath, "Templates", "commands", "wrap.md")

		out := runVP(t, bin, env, nil, "commands", "reset", "wrap")
		if !strings.Contains(out, "reset Templates/commands/wrap.md: removed your override; the built-in now serves it (backup: ") {
			t.Errorf("commands reset:\n%s", out)
		}
		out = runVP(t, bin, env, nil, "skills", "reset", "chair")
		if !strings.Contains(out, "reset Templates/skills/chair/SKILL.md: removed your override") ||
			!strings.Contains(out, "Shims in each project may still carry the removed override's text") {
			t.Errorf("skills reset:\n%s", out)
		}
		for p, body := range map[string]string{
			backupOf(env.vaultPath, "Templates/commands/wrap.md", a):          a,
			backupOf(env.vaultPath, "Templates/skills/chair/SKILL.md", chair): chair,
		} {
			if got, err := os.ReadFile(p); err != nil || string(got) != body {
				t.Errorf("backup %s = %q (err=%v)", p, got, err)
			}
		}
		if _, err := os.Stat(wrap); !os.IsNotExist(err) {
			t.Errorf("wrap.md not removed (err=%v)", err)
		}
		// A second, different override: a second backup, the first unchanged.
		putFile(t, env.vaultPath, "Templates/commands/wrap.md", b)
		runVP(t, bin, env, nil, "commands", "reset", "wrap")
		for p, body := range map[string]string{
			backupOf(env.vaultPath, "Templates/commands/wrap.md", a): a,
			backupOf(env.vaultPath, "Templates/commands/wrap.md", b): b,
		} {
			if got, err := os.ReadFile(p); err != nil || string(got) != body {
				t.Errorf("backup %s = %q (err=%v)", p, got, err)
			}
		}
		out = runVP(t, bin, env, nil, "commands", "list")
		for _, line := range strings.Split(out, "\n") {
			if f := strings.Fields(line); len(f) > 2 && f[0] == "wrap" && !strings.Contains(line, "embedded") {
				t.Errorf("wrap is not served by the embedded tier: %q", line)
			}
		}
	})

	t.Run("git-vault-commit", func(t *testing.T) {
		env := fresh(t)
		const mine = "# my wrap override\n"
		putFile(t, env.vaultPath, "Templates/commands/wrap.md", mine)
		origin := gitVault(t, env)
		tip := gitIn(t, origin, "rev-parse", "main")

		out := runVP(t, bin, env, nil, "commands", "reset", "wrap")
		if !strings.Contains(out, "committed the removal locally as ") {
			t.Errorf("no local commit reported:\n%s", out)
		}
		if st := gitIn(t, env.vaultPath, "status", "--porcelain", "--", "Templates/"); st != "" {
			t.Errorf("Templates/ is dirty: %q", st)
		}
		if subj := gitIn(t, env.vaultPath, "log", "-1", "--format=%s"); subj != "chore(templates): remove 1 operator-reset override(s) of built-ins" {
			t.Errorf("subject = %q", subj)
		}
		if got := gitIn(t, origin, "rev-parse", "main"); got != tip {
			t.Errorf("the reset pushed: origin %s -> %s", tip, got)
		}

		enableGit(t, env)
		out, code := runVPCode(t, bin, env, nil, nil, "vault", "sync")
		if code != 0 {
			t.Fatalf("vault sync: exit %d\n%s", code, out)
		}
		if got, local := gitIn(t, origin, "rev-parse", "main"), gitIn(t, env.vaultPath, "rev-parse", "HEAD"); got != local {
			t.Errorf("vault sync did not publish the reset commit: origin %s, local %s", got, local)
		}
		clone := filepath.Join(t.TempDir(), "hostB")
		cmd := exec.Command("git", "clone", "-q", origin, clone)
		cmd.Env = gitEnv()
		if o, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("clone: %v\n%s", err, o)
		}
		if _, err := os.Stat(filepath.Join(clone, "Templates", "commands", "wrap.md")); !os.IsNotExist(err) {
			t.Errorf("another host still gets the reset file (err=%v)", err)
		}
	})

	t.Run("commit-failure-unstages", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("shell hooks need a POSIX shell")
		}
		env := fresh(t)
		putFile(t, env.vaultPath, "Templates/commands/wrap.md", "# my wrap override\n")
		gitVault(t, env)
		embSHA, _ := templates.EmbeddedSHA("commands/wrap.md")
		seedTrackedOverride(t, env.vaultPath, "commands/wrap.md", []byte("# my wrap override\n"), embSHA)
		if err := os.WriteFile(filepath.Join(env.vaultPath, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		out, code := runVPCode(t, bin, env, nil, nil, "commands", "reset", "wrap")
		if code != 2 || !strings.Contains(out, "removed, not committed") {
			t.Errorf("exit %d\n%s", code, out)
		}
		if staged := gitIn(t, env.vaultPath, "diff", "--cached", "--name-only", "--", "Templates/commands/wrap.md"); staged != "" {
			t.Errorf("the removal is still staged: %q", staged)
		}
		out = runVP(t, bin, env, nil, "config", "sync", "--yes", "--project-root", env.projectDir)
		if strings.Contains(out, "prune deferred") || strings.Contains(out, "staged change") || strings.Contains(out, "restored") {
			t.Errorf("the next sync deferred or restored the reset:\n%s", out)
		}
	})

	t.Run("sync-does-not-undo", func(t *testing.T) {
		env := fresh(t)
		const mine = "# my wrap override\n"
		putFile(t, env.vaultPath, "Templates/commands/wrap.md", mine)
		gitVault(t, env)
		embSHA, _ := templates.EmbeddedSHA("commands/wrap.md")
		seedTrackedOverride(t, env.vaultPath, "commands/wrap.md", []byte(mine), embSHA)
		runVP(t, bin, env, nil, "commands", "reset", "wrap")
		head := gitIn(t, env.vaultPath, "rev-parse", "HEAD")
		for i := 0; i < 2; i++ {
			out := runVP(t, bin, env, nil, "config", "sync", "--yes", "--project-root", env.projectDir)
			if strings.Contains(out, "restored") {
				t.Errorf("sync %d restored the reset file:\n%s", i+1, out)
			}
		}
		if _, err := os.Stat(filepath.Join(env.vaultPath, "Templates", "commands", "wrap.md")); !os.IsNotExist(err) {
			t.Errorf("the reset file came back (err=%v)", err)
		}
		if got := gitIn(t, env.vaultPath, "rev-parse", "HEAD"); got != head {
			t.Errorf("HEAD moved: %s", gitIn(t, env.vaultPath, "log", "-1", "--stat"))
		}
	})
}

// enableGit sets git_enabled = true in the environment's vp config, which a
// --no-git init leaves false; `vp vault sync` refuses without it.
func enableGit(t *testing.T, env *testEnv) {
	t.Helper()
	p := filepath.Join(env.xdgConfig, "vibe-palace", "config.toml")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "git_enabled = false") {
		s = strings.Replace(s, "git_enabled = false", "git_enabled = true", 1)
	} else if !strings.Contains(s, "git_enabled = true") {
		s = "git_enabled = true\n" + s
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sha12 is the backup-name digest, computed independently of templates.
func sha12(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// TestIntegrationTemplateResetBackupNameIsTheContentHash pins the on-disk
// name through the binary against an independently computed digest.
func TestIntegrationTemplateResetBackupNameIsTheContentHash(t *testing.T) {
	bin := buildVPBinary(t)
	env := setupFreshEnv(t)
	runVP(t, bin, env, nil, "init", env.projectDir,
		"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")
	const mine = "# pinned override\n"
	putFile(t, env.vaultPath, "Templates/commands/restart.md", mine)
	dry := runVP(t, bin, env, nil, "commands", "reset", "restart", "--dry-run")
	want := "Templates/commands/restart.md." + sha12(mine) + ".bak"
	if !strings.Contains(dry, "backup: "+want) {
		t.Errorf("dry run does not predict %s:\n%s", want, dry)
	}
	runVP(t, bin, env, nil, "commands", "reset", "restart")
	if got, err := os.ReadFile(filepath.Join(env.vaultPath, filepath.FromSlash(want))); err != nil || string(got) != mine {
		t.Errorf("backup at %s = %q (err=%v)", want, got, err)
	}
}
