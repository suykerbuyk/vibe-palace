// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// earlierRestartFixture is a version of commands/restart.md that vibe-palace
// shipped before its current one (23bedcc). internal/templates'
// TestEarlierFixtureIsAShippedVersion pins that it classifies "earlier".
const earlierRestartFixture = "../../internal/templates/testdata/earlier/commands/restart.md"

// TestIntegrationTemplateProvenance drives the real binary through the
// shipped-version manifest: whether a vault Templates/ copy is vp's is decided
// by the binary alone — the current embedded copy, or an earlier version
// vibe-palace shipped (frozen in internal/templates/shipped.txt), line endings
// aside — never by a host-local templates.lock. A host without that lock used
// to prompt about every such copy on every sync (and keep it, shadowing the
// built-in with stale bytes), and the lock itself was untracked dirt that made
// `vp vault sync` refuse on a canonically configured git vault.
func TestIntegrationTemplateProvenance(t *testing.T) {
	bin := buildVPBinary(t)
	earlier, err := os.ReadFile(earlierRestartFixture)
	if err != nil {
		t.Fatal(err)
	}
	const mine = "# my wrap override\n"

	// canonicalGitVault: an initialised vault with production's canonical
	// .gitignore (and nothing more: .vibe-palace/ is not in it), committed and
	// pushed to a bare origin. files are planted before the first commit.
	canonicalGitVault := func(t *testing.T, env *testEnv, files map[string]string) string {
		t.Helper()
		runVP(t, bin, env, nil, "init", env.projectDir,
			"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")
		putFile(t, env.vaultPath, ".gitignore", strings.Join(storage.CanonicalGitignorePatterns, "\n")+"\n")
		for rel, body := range files {
			putFile(t, env.vaultPath, rel, body)
		}
		return gitifyVaultWithOrigin(t, env.vaultPath)
	}
	fileBytes := func(t *testing.T, p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		return string(b)
	}
	assertAbsent := func(t *testing.T, p string) {
		t.Helper()
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s is present (err=%v)", p, err)
		}
	}

	t.Run("two-host-lockless-host-prunes-stale-and-keeps-override", func(t *testing.T) {
		a := setupFreshEnv(t)
		origin := canonicalGitVault(t, a, map[string]string{
			"Templates/commands/restart.md": string(earlier),
			"Templates/commands/wrap.md":    mine,
		})

		// Host B: a fresh clone with its own HOME and XDG, and no lock.
		b := setupFreshEnv(t)
		clone := exec.Command("git", "clone", "-q", origin, b.vaultPath)
		clone.Env = gitEnv()
		if out, err := clone.CombinedOutput(); err != nil {
			t.Fatalf("clone: %v\n%s", err, out)
		}
		gitIn(t, b.vaultPath, "config", "user.email", "hostb@example.invalid")
		gitIn(t, b.vaultPath, "config", "user.name", "Host B")
		runVP(t, bin, b, nil, "init", b.projectDir,
			"--name", b.projectName, "--vault-path", b.vaultPath, "--no-git")
		assertAbsent(t, filepath.Join(b.vaultPath, retiredLockRel))

		out := runVP(t, bin, b, nil, "config", "sync", "--yes", "--project-root", b.projectDir)
		if strings.Contains(out, "[Prompt]") {
			t.Errorf("host B prompted:\n%s", out)
		}
		if !strings.Contains(out, "matched an earlier shipped version of commands/restart.md") {
			t.Errorf("no earlier-version prune row:\n%s", out)
		}
		restartB := filepath.Join(b.vaultPath, "Templates", "commands", "restart.md")
		wrapB := filepath.Join(b.vaultPath, "Templates", "commands", "wrap.md")
		assertAbsent(t, restartB)
		if got := fileBytes(t, wrapB); got != mine {
			t.Errorf("host B's wrap.md changed: %q", got)
		}
		assertAbsent(t, filepath.Join(b.vaultPath, retiredLockRel))
		msg := gitIn(t, b.vaultPath, "log", "-1", "--format=%B")
		if !strings.Contains(msg, "- Templates/commands/restart.md (earlier shipped version of commands/restart.md)") {
			t.Errorf("the prune commit does not name the earlier version:\n%s", msg)
		}
		if names := gitIn(t, b.vaultPath, "show", "--name-status", "--format=", "HEAD"); names != "D\tTemplates/commands/restart.md" {
			t.Errorf("the prune commit carries %q", names)
		}
		if tip, head := gitIn(t, origin, "rev-parse", "main"), gitIn(t, b.vaultPath, "rev-parse", "HEAD"); tip != head {
			t.Errorf("the prune was not pushed: origin %s, host B %s", tip, head)
		}
		enableGit(t, b)
		if sync, code := runVPCode(t, bin, b, nil, nil, "vault", "sync"); code != 0 {
			t.Errorf("vp vault sync on host B: exit %d\n%s", code, sync)
		}

		gitIn(t, a.vaultPath, "pull", "-q", "origin", "main")
		assertAbsent(t, filepath.Join(a.vaultPath, "Templates", "commands", "restart.md"))
		if got := fileBytes(t, filepath.Join(a.vaultPath, "Templates", "commands", "wrap.md")); got != mine {
			t.Errorf("host A's wrap.md after the pull: %q", got)
		}
	})

	t.Run("stale-untracked-lock-removed-and-vault-sync-unblocked", func(t *testing.T) {
		env := setupFreshEnv(t)
		canonicalGitVault(t, env, nil)
		// What any earlier `vp config sync` left: an untracked lock.
		const leftover = "[entries]\n  [entries.\"Templates/commands/wrap.md\"]\n    embedded_sha = \"" +
			"0000000000000000000000000000000000000000000000000000000000000000\"\n    written_at = 2026-09-01T00:00:00Z\n"
		lock := filepath.Join(env.vaultPath, retiredLockRel)
		putFile(t, env.vaultPath, retiredLockRel, leftover)
		if st := gitIn(t, env.vaultPath, "status", "--porcelain", "--", retiredLockRel); !strings.HasPrefix(st, "??") {
			t.Fatalf("fixture: the lock should be untracked and not ignored, status %q", st)
		}

		plan := runVP(t, bin, env, nil, "config", "sync", "--dry-run", "--project-root", env.projectDir)
		if !strings.Contains(plan, "remove the retired .vibe-palace/templates.lock (untracked and not ignored") {
			t.Errorf("--dry-run does not show the removal:\n%s", plan)
		}
		if got := fileBytes(t, lock); got != leftover {
			t.Errorf("--dry-run changed the lock: %q", got)
		}

		out := runVP(t, bin, env, nil, "config", "sync", "--yes", "--project-root", env.projectDir)
		if !strings.Contains(out, "removed the retired .vibe-palace/templates.lock") {
			t.Errorf("no removal line:\n%s", out)
		}
		assertAbsent(t, lock)
		enableGit(t, env)
		if sync, code := runVPCode(t, bin, env, nil, nil, "vault", "sync"); code != 0 {
			t.Errorf("vp vault sync: exit %d\n%s", code, sync)
		}
	})

	t.Run("crlf-mirror-pruned", func(t *testing.T) {
		env := setupFreshEnv(t)
		crlf := strings.ReplaceAll(string(embeddedBytesFor(t, "commands/wrap.md")), "\n", "\r\n")
		origin := canonicalGitVault(t, env, map[string]string{"Templates/commands/wrap.md": crlf})
		// The fixture is committed with its CRLF bytes (no .gitattributes).
		if !strings.Contains(gitIn(t, env.vaultPath, "cat-file", "-p", "HEAD:Templates/commands/wrap.md"), "\r\n") {
			t.Fatal("fixture: HEAD should hold the CRLF form")
		}

		out := runVP(t, bin, env, nil, "config", "sync", "--yes", "--project-root", env.projectDir)
		if strings.Contains(out, "[Prompt]") || !strings.Contains(out, "pruned=1") {
			t.Errorf("the CRLF mirror was not pruned:\n%s", out)
		}
		assertAbsent(t, filepath.Join(env.vaultPath, "Templates", "commands", "wrap.md"))
		if msg := gitIn(t, env.vaultPath, "log", "-1", "--format=%B"); !strings.Contains(msg, "- Templates/commands/wrap.md (current embedded copy)") {
			t.Errorf("no prune commit for the CRLF mirror:\n%s", msg)
		}
		if tip, head := gitIn(t, origin, "rev-parse", "main"), gitIn(t, env.vaultPath, "rev-parse", "HEAD"); tip != head {
			t.Errorf("the prune was not pushed: origin %s, HEAD %s", tip, head)
		}
		if st := gitIn(t, env.vaultPath, "status", "--porcelain", "--", "Templates/"); st != "" {
			t.Errorf("Templates/ is dirty: %q", st)
		}
	})

	t.Run("tracked-legacy-lock-untouched", func(t *testing.T) {
		env := setupFreshEnv(t)
		// A lock an old host committed, whose "baseline" for wrap.md is the
		// OVERRIDE's own bytes. An old binary took a copy equal to its lock
		// baseline for its own mirror, and pruned it — the operator's override.
		sum := sha256.Sum256([]byte(mine))
		lockBody := "[entries]\n  [entries.\"Templates/commands/wrap.md\"]\n    embedded_sha = \"" +
			hex.EncodeToString(sum[:]) + "\"\n    written_at = 2026-09-01T00:00:00Z\n"
		origin := canonicalGitVault(t, env, map[string]string{
			"Templates/commands/wrap.md":    mine,
			"Templates/commands/restart.md": string(embeddedBytesFor(t, "commands/restart.md")),
			retiredLockRel:                  lockBody,
		})
		if tracked := gitIn(t, env.vaultPath, "ls-files", "--", retiredLockRel); tracked != retiredLockRel {
			t.Fatalf("fixture: the lock should be tracked, ls-files %q", tracked)
		}

		out := runVP(t, bin, env, nil, "config", "sync", "--yes", "--project-root", env.projectDir)
		wrap := filepath.Join(env.vaultPath, "Templates", "commands", "wrap.md")
		if got := fileBytes(t, wrap); got != mine {
			t.Errorf("the override changed or went: %q\n%s", got, out)
		}
		if got := fileBytes(t, filepath.Join(env.vaultPath, retiredLockRel)); got != lockBody {
			t.Errorf("the tracked lock changed:\n%s", got)
		}
		if strings.Contains(out, "remove the retired") {
			t.Errorf("a tracked lock was planned for removal:\n%s", out)
		}
		if st := gitIn(t, env.vaultPath, "status", "--porcelain", "--", retiredLockRel, "Templates/"); st != "" {
			t.Errorf("dirty after the sync: %q", st)
		}
		// The mirror beside it is still pruned, and only it.
		assertAbsent(t, filepath.Join(env.vaultPath, "Templates", "commands", "restart.md"))
		if names := gitIn(t, env.vaultPath, "show", "--name-status", "--format=", "HEAD"); names != "D\tTemplates/commands/restart.md" {
			t.Errorf("the prune commit carries %q", names)
		}
		hostB := filepath.Join(t.TempDir(), "hostB")
		clone := exec.Command("git", "clone", "-q", origin, hostB)
		clone.Env = gitEnv()
		if o, err := clone.CombinedOutput(); err != nil {
			t.Fatalf("clone: %v\n%s", err, o)
		}
		if got := fileBytes(t, filepath.Join(hostB, "Templates", "commands", "wrap.md")); got != mine {
			t.Errorf("another host lost the override: %q", got)
		}
	})
}
