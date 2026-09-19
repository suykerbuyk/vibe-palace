// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Provenance by the binary alone: `vp config sync` decides whether a vault
// Templates/ copy is vp's from its bytes — the current embedded copy, or an
// earlier version vibe-palace shipped (the frozen internal/templates/shipped.txt)
// — never from a host-local templates.lock. So a host without the lock prunes a
// stale earlier-version mirror, never prompts about an override, writes no
// lock, removes a stale untracked one, and commits a removal of vp-shipped
// bytes it finds pending.
//
// Every test in this file uses only what existed at 22f0d3f (runConfigSync,
// runReset, runCommandsUpgrade, runSelectedChecks, fixtures via os and git),
// so each can be measured failing there.

// earlierRestart is the version of commands/restart.md just before its current
// one in 1f3bb62's history: a row of shipped.txt, and not the current copy
// (internal/templates TestEarlierFixtureIsAShippedVersion pins both).
func earlierRestart(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(earlierRestartPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == string(embeddedTemplateBytes(t, "commands/restart.md")) {
		t.Fatal("fixture: the earlier restart.md equals the current embedded copy")
	}
	return string(b)
}

// earlierRestartPath is made absolute while the working directory is still the
// package's: most tests here chdir into a project directory.
var earlierRestartPath = func() string {
	p, _ := filepath.Abs(filepath.Join("..", "..", "internal", "templates", "testdata", "earlier", "commands", "restart.md"))
	return p
}()

// bakFilesUnder lists every *.bak under root.
func bakFilesUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".bak") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// canonicalGitVault is host A on a canonically configured git vault: its own
// repository, whose .gitignore is exactly storage.CanonicalGitignorePatterns
// (no hand-added .vibe-palace/ line), with every current file committed and
// pushed to a fresh bare origin. before runs first, to seed files that are
// committed with the rest.
func canonicalGitVault(t *testing.T, before func(vaultPath string)) (vaultPath, projDir, origin string) {
	t.Helper()
	vaultPath, projDir = overrideVault(t)
	if before != nil {
		before(vaultPath)
	}
	putVaultFile(t, vaultPath, ".gitignore", strings.Join(storage.CanonicalGitignorePatterns, "\n")+"\n")
	origin = filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	enableGitInTestConfig(t)
	gitInVault(t, origin, "init", "-q", "--bare", "-b", "main")
	gitInVault(t, vaultPath, "init", "-q", "-b", "main")
	gitInVault(t, vaultPath, "config", "user.email", "test@test.com")
	gitInVault(t, vaultPath, "config", "user.name", "Test")
	gitInVault(t, vaultPath, "add", "-A")
	gitInVault(t, vaultPath, "commit", "-qm", "seed")
	gitInVault(t, vaultPath, "remote", "add", "origin", origin)
	gitInVault(t, vaultPath, "push", "-q", "-u", "origin", "main")
	return vaultPath, projDir, origin
}

func headOf(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(gitInVault(t, dir, "rev-parse", "HEAD"))
}

func assertTemplatesClean(t *testing.T, vaultPath string) {
	t.Helper()
	if st := strings.TrimSpace(gitInVault(t, vaultPath, "status", "--porcelain", "--", "Templates/")); st != "" {
		t.Errorf("Templates/ is dirty: %q", st)
	}
}

// assertVaultSyncNotRefused runs the real `vp vault sync` engine.
func assertVaultSyncNotRefused(t *testing.T, vaultPath string) {
	t.Helper()
	res, err := storage.SyncVault(vaultPath, []string{"origin"})
	if err != nil || res == nil || res.Refused {
		var dirt []string
		if res != nil {
			dirt = res.GenuineDirt
		}
		t.Errorf("vp vault sync refused (err=%v, dirt=%v)", err, dirt)
	}
}

const retiredLockBody = "[entries]\n  [entries.\"Templates/commands/wrap.md\"]\n    embedded_sha = \"0000000000000000000000000000000000000000000000000000000000000000\"\n    written_at = 2026-09-01T00:00:00Z\n"

// TestConfigSyncPrunesEarlierVersionOnLocklessHost is the defect the task
// exists for: a stale mirror from before 807bae7, on a host whose lock never
// recorded it, used to plan a keep/.new prompt forever (--yes kept it), and
// the stale bytes shadowed the embedded floor. It is an earlier shipped version
// of the built-in, so it is pruned — with no prompt and no backup — and the
// removal is auditable.
func TestConfigSyncPrunesEarlierVersionOnLocklessHost(t *testing.T) {
	const row = "prune Templates/commands/restart.md (matched an earlier shipped version of commands/restart.md; recoverable from vibe-palace history)"
	const noBackup = "pruned Templates/commands/restart.md (earlier shipped version of commands/restart.md; no backup)"

	t.Run("non-git", func(t *testing.T) {
		vaultPath, projDir := overrideVault(t)
		restart := putVaultFile(t, vaultPath, "Templates/commands/restart.md", earlierRestart(t))
		out := syncVault(t, projDir, "", "--yes")
		if _, err := os.Stat(restart); !os.IsNotExist(err) {
			t.Errorf("the earlier-version mirror is still there (err=%v)\n%s", err, out)
		}
		for _, want := range []string{row, noBackup, "pruned=1"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
		if baks := bakFilesUnder(t, filepath.Join(vaultPath, "Templates")); len(baks) != 0 {
			t.Errorf("a backup was written: %v", baks)
		}
	})

	t.Run("git, committed", func(t *testing.T) {
		vaultPath, projDir := overrideVault(t)
		restart := putVaultFile(t, vaultPath, "Templates/commands/restart.md", earlierRestart(t))
		origin := gitifyVault(t, vaultPath)
		out := syncVault(t, projDir, "", "--yes")
		if _, err := os.Stat(restart); !os.IsNotExist(err) {
			t.Errorf("the earlier-version mirror is still there (err=%v)\n%s", err, out)
		}
		if !strings.Contains(out, row) {
			t.Errorf("no earlier-version prune row:\n%s", out)
		}
		if names := strings.TrimSpace(gitInVault(t, vaultPath, "show", "--name-status", "--format=", "HEAD")); names != "D\tTemplates/commands/restart.md" {
			t.Errorf("the prune commit carries %q", names)
		}
		msg := gitInVault(t, vaultPath, "log", "-1", "--format=%B")
		if !strings.Contains(msg, "- Templates/commands/restart.md (earlier shipped version of commands/restart.md)") {
			t.Errorf("the commit does not name the basis:\n%s", msg)
		}
		if tip := strings.TrimSpace(gitInVault(t, origin, "rev-parse", "main")); tip != headOf(t, vaultPath) {
			t.Errorf("the prune was not pushed")
		}
		assertTemplatesClean(t, vaultPath)
		if baks := bakFilesUnder(t, filepath.Join(vaultPath, "Templates")); len(baks) != 0 {
			t.Errorf("a backup was written: %v", baks)
		}
	})

	t.Run("git, untracked", func(t *testing.T) {
		vaultPath, projDir := overrideVault(t)
		gitifyVault(t, vaultPath)
		head := headOf(t, vaultPath)
		restart := putVaultFile(t, vaultPath, "Templates/commands/restart.md", earlierRestart(t))
		out := syncVault(t, projDir, "", "--yes")
		if _, err := os.Stat(restart); !os.IsNotExist(err) {
			t.Errorf("the earlier-version mirror is still there (err=%v)\n%s", err, out)
		}
		if !strings.Contains(out, noBackup) {
			t.Errorf("no audit line for a prune no commit carries:\n%s", out)
		}
		if headOf(t, vaultPath) != head {
			t.Error("an untracked prune made a commit")
		}
	})
}

// TestConfigSyncNeverPromptsForAnOverride: a host with no templates.lock used
// to plan a keep/.new prompt for every override of a built-in on every
// interactive sync. Answerless, the overrides are kept silently.
func TestConfigSyncNeverPromptsForAnOverride(t *testing.T) {
	vaultPath, projDir := overrideVault(t)
	wrap := putVaultFile(t, vaultPath, "Templates/commands/wrap.md", myWrap)
	workflow := putVaultFile(t, vaultPath, "Templates/workflow.md", myWorkflow)

	out, code := runSyncWithStdin(t, "", []string{"--project-root", projDir, "--tier", "vault"})
	if code != cli.ExitOK {
		t.Errorf("exit %d\n%s", code, out)
	}
	for _, bad := range []string{"[Prompt]", "[q]uit:", "[keep]"} {
		if strings.Contains(out, bad) {
			t.Errorf("the sync prompted (%q):\n%s", bad, out)
		}
	}
	for _, rel := range []string{"Templates/commands/wrap.md", "Templates/workflow.md"} {
		if !strings.Contains(out, "[Unchanged] TemplateTree:Templates: "+rel+" operator override of a built-in (kept)") {
			t.Errorf("no keep row for %s:\n%s", rel, out)
		}
	}
	if !strings.Contains(out, "Nothing to do") {
		t.Errorf("not a no-op:\n%s", out)
	}
	assertFileBytes(t, wrap, myWrap)
	assertFileBytes(t, workflow, myWorkflow)
}

// TestConfigSyncWritesNoTemplatesLock is Task A's round-2 T1: every Templates
// Apply wrote .vibe-palace/templates.lock, which a canonically configured git
// vault does not ignore — so the first `vp config sync` made `vp vault sync`
// refuse until a human committed or ignored it.
func TestConfigSyncWritesNoTemplatesLock(t *testing.T) {
	vaultPath, projDir, _ := canonicalGitVault(t, func(v string) {
		putVaultFile(t, v, "Templates/commands/wrap.md", string(embeddedTemplateBytes(t, "commands/wrap.md")))
	})
	out := syncVault(t, projDir, "", "--yes")
	if _, err := os.Stat(filepath.Join(vaultPath, ".vibe-palace", "templates.lock")); !os.IsNotExist(err) {
		t.Errorf("the sync wrote .vibe-palace/templates.lock (err=%v)\n%s", err, out)
	}
	scan, err := storage.TidyScan(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if dirt := scan.GenuineDirt(); len(dirt) != 0 {
		t.Errorf("vault sync would refuse on %v", dirt)
	}
	assertVaultSyncNotRefused(t, vaultPath)
}

// TestConfigSyncRemovesAStaleUntrackedLock: every canonically configured git
// vault that ever ran `vp config sync` holds an untracked templates.lock that
// makes `vp vault sync` refuse. On a vault that is its own repository an
// untracked, un-ignored lock is removed — shown by --dry-run first. A tracked
// lock, an ignored one, a `git rm --cached` one HEAD still holds, and any lock
// on a non-git vault are left.
func TestConfigSyncRemovesAStaleUntrackedLock(t *testing.T) {
	const row = "remove the retired .vibe-palace/templates.lock (untracked and not ignored; no vp from this release reads it, and it blocks vp vault sync)"

	t.Run("untracked, not ignored, own-repo git vault", func(t *testing.T) {
		vaultPath, projDir, _ := canonicalGitVault(t, nil)
		lock := putVaultFile(t, vaultPath, ".vibe-palace/templates.lock", retiredLockBody)

		dry := syncVault(t, projDir, "", "--dry-run")
		if !strings.Contains(dry, row) {
			t.Errorf("--dry-run does not show the removal:\n%s", dry)
		}
		assertFileBytes(t, lock, retiredLockBody)

		out := syncVault(t, projDir, "", "--yes")
		if !strings.Contains(out, "removed the retired .vibe-palace/templates.lock") {
			t.Errorf("no removal line:\n%s", out)
		}
		if _, err := os.Stat(lock); !os.IsNotExist(err) {
			t.Errorf("the lock is still there (err=%v)", err)
		}
		assertVaultSyncNotRefused(t, vaultPath)
	})

	t.Run("tracked", func(t *testing.T) {
		vaultPath, projDir, _ := canonicalGitVault(t, func(v string) {
			putVaultFile(t, v, ".vibe-palace/templates.lock", retiredLockBody)
		})
		out := syncVault(t, projDir, "", "--yes")
		assertFileBytes(t, filepath.Join(vaultPath, ".vibe-palace", "templates.lock"), retiredLockBody)
		if strings.Contains(out, "retired .vibe-palace/templates.lock") {
			t.Errorf("a tracked lock was planned for removal:\n%s", out)
		}
	})

	t.Run("ignored", func(t *testing.T) {
		vaultPath, projDir, _ := canonicalGitVault(t, nil)
		gi := strings.Join(storage.CanonicalGitignorePatterns, "\n") + "\n.vibe-palace/\n"
		putVaultFile(t, vaultPath, ".gitignore", gi)
		gitInVault(t, vaultPath, "commit", "-qam", "ignore .vibe-palace/")
		lock := putVaultFile(t, vaultPath, ".vibe-palace/templates.lock", retiredLockBody)
		out := syncVault(t, projDir, "", "--yes")
		assertFileBytes(t, lock, retiredLockBody)
		if strings.Contains(out, "retired .vibe-palace/templates.lock") {
			t.Errorf("an ignored lock was planned for removal:\n%s", out)
		}
	})

	t.Run("removed from the index, still in HEAD", func(t *testing.T) {
		vaultPath, projDir, _ := canonicalGitVault(t, func(v string) {
			putVaultFile(t, v, ".vibe-palace/templates.lock", retiredLockBody)
		})
		gitInVault(t, vaultPath, "rm", "-q", "--cached", ".vibe-palace/templates.lock")
		out := syncVault(t, projDir, "", "--yes")
		assertFileBytes(t, filepath.Join(vaultPath, ".vibe-palace", "templates.lock"), retiredLockBody)
		if strings.Contains(out, "retired .vibe-palace/templates.lock") {
			t.Errorf("a lock HEAD still holds was planned for removal:\n%s", out)
		}
	})

	t.Run("non-git vault", func(t *testing.T) {
		vaultPath, projDir := overrideVault(t)
		lock := putVaultFile(t, vaultPath, ".vibe-palace/templates.lock", retiredLockBody)
		out := syncVault(t, projDir, "", "--yes")
		assertFileBytes(t, lock, retiredLockBody)
		if strings.Contains(out, "retired .vibe-palace/templates.lock") {
			t.Errorf("a non-git vault's lock was planned for removal:\n%s", out)
		}
		rows, err := runSelectedChecks("template-drift")
		if err != nil {
			t.Fatal(err)
		}
		var all string
		for _, r := range rows {
			all += r.Summary + "\n" + strings.Join(r.Details, "\n") + "\n"
		}
		if !strings.Contains(all, "a retired templates.lock is present; no vp from this release reads it") {
			t.Errorf("template-drift does not report the retained lock:\n%s", all)
		}
	})
}

// TestConfigSyncLeavesALegacyLockUnread: a hand-written, tracked lock whose
// baseline for wrap.md equals the operator's override bytes. The lock-reading
// reconciler took that for "vp wrote these bytes" and pruned the override.
// Nothing reads the lock now: the override is kept, a real mirror beside it is
// pruned, and the lock is byte-identical.
func TestConfigSyncLeavesALegacyLockUnread(t *testing.T) {
	vaultPath, projDir := overrideVault(t)
	sum := sha256.Sum256([]byte(myWrap))
	forged := "[entries]\n  [entries.\"Templates/commands/wrap.md\"]\n    embedded_sha = \"" + hex.EncodeToString(sum[:]) +
		"\"\n    written_at = 2026-09-01T00:00:00Z\n"
	wrap := putVaultFile(t, vaultPath, "Templates/commands/wrap.md", myWrap)
	restart := putVaultFile(t, vaultPath, "Templates/commands/restart.md", string(embeddedTemplateBytes(t, "commands/restart.md")))
	lock := putVaultFile(t, vaultPath, ".vibe-palace/templates.lock", forged)
	gitifyVault(t, vaultPath)

	out := syncVault(t, projDir, "", "--yes")
	assertFileBytes(t, wrap, myWrap)
	if _, err := os.Stat(restart); !os.IsNotExist(err) {
		t.Errorf("the mirror beside it was not pruned (err=%v)\n%s", err, out)
	}
	assertFileBytes(t, lock, forged)
	if names := strings.TrimSpace(gitInVault(t, vaultPath, "show", "--name-status", "--format=", "HEAD")); names != "D\tTemplates/commands/restart.md" {
		t.Errorf("the prune commit carries %q", names)
	}
}

// TestConfigSyncPrunesCRLFMirror: a mirror checked out with CRLF line endings
// (core.autocrlf=true on Windows) is vp's bytes, line endings aside. It used to
// be a lock-less prompt forever.
func TestConfigSyncPrunesCRLFMirror(t *testing.T) {
	crlf := strings.ReplaceAll(string(embeddedTemplateBytes(t, "commands/wrap.md")), "\n", "\r\n")
	const row = "prune Templates/commands/wrap.md (byte-identical, line endings aside, to the current embedded copy)"

	t.Run("non-git", func(t *testing.T) {
		vaultPath, projDir := overrideVault(t)
		wrap := putVaultFile(t, vaultPath, "Templates/commands/wrap.md", crlf)
		out := syncVault(t, projDir, "", "--yes")
		if _, err := os.Stat(wrap); !os.IsNotExist(err) {
			t.Errorf("the CRLF mirror is still there (err=%v)\n%s", err, out)
		}
		if !strings.Contains(out, row) || !strings.Contains(out, "pruned=1") {
			t.Errorf("no CRLF prune row:\n%s", out)
		}
	})
	t.Run("git", func(t *testing.T) {
		vaultPath, projDir := overrideVault(t)
		wrap := putVaultFile(t, vaultPath, "Templates/commands/wrap.md", crlf)
		gitifyVault(t, vaultPath)
		out := syncVault(t, projDir, "", "--yes")
		if _, err := os.Stat(wrap); !os.IsNotExist(err) {
			t.Errorf("the CRLF mirror is still there (err=%v)\n%s", err, out)
		}
		if names := strings.TrimSpace(gitInVault(t, vaultPath, "show", "--name-status", "--format=", "HEAD")); names != "D\tTemplates/commands/wrap.md" {
			t.Errorf("the prune commit carries %q\n%s", names, out)
		}
		assertTemplatesClean(t, vaultPath)
	})
}

// TestConfigSyncCommitsAPendingRemovalWithoutALock: a tracked Templates/ file
// removed from the worktree and never committed (" D"). Its removal used to be
// committed only when this host's lock had an entry for it. Now: when the
// committed copy is vp-shipped bytes the sync commits it, pushed, and says it
// found it pending; when the committed copy is operator content nothing is
// committed, and the row names the reset verb and the restore.
func TestConfigSyncCommitsAPendingRemovalWithoutALock(t *testing.T) {
	for _, tc := range []struct {
		name, rel, body, basis string
	}{
		{"current embedded copy", "Templates/commands/wrap.md", "", "current embedded copy"},
		{"earlier shipped version", "Templates/commands/restart.md", "earlier", "earlier shipped version of commands/restart.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vaultPath, projDir := overrideVault(t)
			body := string(embeddedTemplateBytes(t, "commands/wrap.md"))
			if tc.body == "earlier" {
				body = earlierRestart(t)
			}
			p := putVaultFile(t, vaultPath, tc.rel, body)
			origin := gitifyVault(t, vaultPath)
			head := headOf(t, vaultPath)
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			out := syncVault(t, projDir, "", "--yes")
			if parent := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD~1")); parent != head {
				t.Fatalf("not exactly one new commit:\n%s", out)
			}
			if names := strings.TrimSpace(gitInVault(t, vaultPath, "show", "--name-status", "--format=", "HEAD")); names != "D\t"+tc.rel {
				t.Errorf("the commit carries %q", names)
			}
			msg := gitInVault(t, vaultPath, "log", "-1", "--format=%B")
			for _, want := range []string{
				"vp found these removals already pending in the worktree",
				"committed them because the committed copy of each is\nvp-shipped bytes",
				"- " + tc.rel + " (" + tc.basis + ")",
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q:\n%s", want, msg)
				}
			}
			if tip := strings.TrimSpace(gitInVault(t, origin, "rev-parse", "main")); tip != headOf(t, vaultPath) {
				t.Error("the commit was not pushed")
			}
			// Review L4: the commit is this run's, so the run says what it
			// published, and counts it.
			if want := "committed the pending removal of " + tc.rel + " (" + tc.basis + ")"; !strings.Contains(out, want) {
				t.Errorf("no outcome line %q:\n%s", want, out)
			}
			if !strings.Contains(out, "pruned=1") {
				t.Errorf("the committed pending removal is not counted:\n%s", out)
			}
			assertTemplatesClean(t, vaultPath)
		})
	}

	t.Run("operator content in HEAD", func(t *testing.T) {
		vaultPath, projDir := overrideVault(t)
		wrap := putVaultFile(t, vaultPath, "Templates/commands/wrap.md", myWrap)
		gitifyVault(t, vaultPath)
		head := headOf(t, vaultPath)
		if err := os.Remove(wrap); err != nil {
			t.Fatal(err)
		}
		out := syncVault(t, projDir, "", "--yes")
		if headOf(t, vaultPath) != head {
			t.Errorf("a removal of operator content was committed:\n%s", gitInVault(t, vaultPath, "log", "-1", "--stat"))
		}
		for _, want := range []string{
			"Templates/commands/wrap.md removed from the worktree; the committed copy is operator content",
			"vp commands reset wrap",
			"checkout HEAD -- Templates/commands/wrap.md",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
		if st := gitInVault(t, vaultPath, "status", "--porcelain", "--", "Templates/"); st != " D Templates/commands/wrap.md\n" {
			t.Errorf("status = %q, want the removal left as it was", st)
		}
	})
}

// TestCheckTemplateDriftSeesAnEarlierVersion: `vp check --check
// template-drift` on a stale earlier-version mirror said "override of a
// built-in with no lock entry on this host (kept)". It is vp's bytes, pending
// a prune, and it shadows the current built-in until then.
func TestCheckTemplateDriftSeesAnEarlierVersion(t *testing.T) {
	vaultPath, _ := overrideVault(t)
	putVaultFile(t, vaultPath, "Templates/commands/restart.md", earlierRestart(t))
	rows, err := runSelectedChecks("template-drift")
	if err != nil {
		t.Fatal(err)
	}
	var all string
	for _, r := range rows {
		all += r.Summary + "\n" + strings.Join(r.Details, "\n") + "\n"
	}
	if !strings.Contains(all, "Templates:Templates/commands/restart.md: drift (an earlier shipped version of commands/restart.md; pending prune") {
		t.Errorf("template-drift does not see the earlier version:\n%s", all)
	}
	if !strings.Contains(all, "1 mirror(s) pending a prune") || strings.Contains(all, "override(s) of built-ins kept") {
		t.Errorf("the earlier version is not counted as a mirror pending a prune:\n%s", all)
	}
}

// TestRunCommandsUpgrade_EarlierCopyIsStaleNotOverride (Review M3): the same
// file `vp config sync` prunes was listed by `vp commands upgrade` as an
// operator's override, "kept". It is a stale copy of the built-in.
func TestRunCommandsUpgrade_EarlierCopyIsStaleNotOverride(t *testing.T) {
	grokOff(t)
	vault := t.TempDir()
	projectRoot := t.TempDir()
	installShims(t, vault, projectRoot)
	writeVaultFile(t, vault, "Templates/commands/restart.md", earlierRestart(t))
	const stale = "[stale] Templates/commands/restart.md — an earlier shipped version of the built-in; vp config sync prunes it"

	var out, errb bytes.Buffer
	_ = runCommandsUpgrade(commandsUpgradeOpts{
		DryRun: true, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
		VaultRootOverride: vault, ProjectRootOverride: projectRoot,
	})
	if !strings.Contains(out.String(), stale) {
		t.Errorf("dry run has no stale row:\n%s", out.String())
	}
	if strings.Contains(out.String(), "override  restart") || !strings.Contains(out.String(), "Summary (dry run): 0 override(s) kept,") {
		t.Errorf("the stale copy is reported as an override kept:\n%s", out.String())
	}

	no := false
	out.Reset()
	errb.Reset()
	_ = runCommandsUpgrade(commandsUpgradeOpts{
		Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb, InteractiveOverride: &no,
		VaultRootOverride: vault, ProjectRootOverride: projectRoot,
	})
	if !strings.Contains(out.String(), stale) || strings.Contains(out.String(), "[keep] Templates/commands/restart.md") ||
		strings.Contains(out.String(), "override(s) of built-ins kept") {
		t.Errorf("the report lists the stale copy as kept:\n%s\n%s", out.String(), errb.String())
	}
}

// TestCommandsResetOfAnEarlierCopyWritesNoBackup (Review M3): a reset of an
// earlier shipped version kept a hash-named backup of vp's own bytes. Those
// bytes are recoverable from vibe-palace's history; no backup is written.
func TestCommandsResetOfAnEarlierCopyWritesNoBackup(t *testing.T) {
	vault := t.TempDir()
	restart := putVaultFile(t, vault, "Templates/commands/restart.md", earlierRestart(t))
	out, errOut, code := runReset(t, vault, "command", false, "restart")
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	assertAbsent(t, restart)
	if baks := bakFilesUnder(t, filepath.Join(vault, "Templates")); len(baks) != 0 {
		t.Errorf("a backup of shipped bytes was written: %v", baks)
	}
	if !strings.Contains(out, "reset Templates/commands/restart.md: removed it; the built-in now serves it (a copy of an earlier shipped version of commands/restart.md; no backup needed — recoverable from vibe-palace history)") {
		t.Errorf("the outcome line does not say what was removed:\n%s", out)
	}
}

// resetStaleCopyDryRunMatchesTheRealRun is N1's must-fail: a dry run of a
// reset over an earlier shipped version printed a backup name — and a diff —
// that the real run then wrote. Both now say no backup, and none is written.
func resetStaleCopyDryRunMatchesTheRealRun(t *testing.T) {
	vault := t.TempDir()
	restart := putVaultFile(t, vault, "Templates/commands/restart.md", earlierRestart(t))
	dry, dryErr, code := runReset(t, vault, "command", true, "restart")
	if code != cli.ExitOK {
		t.Fatalf("dry run: exit %d\n%s", code, dryErr)
	}
	if !strings.Contains(dry, "would reset Templates/commands/restart.md: remove it (a copy of an earlier shipped version of commands/restart.md; no backup needed") ||
		strings.Contains(dry, "backup: ") {
		t.Errorf("the dry run promises a backup, or does not say none is needed:\n%s", dry)
	}
	if _, errOut, code := runReset(t, vault, "command", false, "restart"); code != cli.ExitOK {
		t.Fatalf("real run: exit %d\n%s", code, errOut)
	}
	assertAbsent(t, restart)
	if baks := bakFilesUnder(t, filepath.Join(vault, "Templates")); len(baks) != 0 {
		t.Errorf("the real run wrote a backup the dry run did not name: %v", baks)
	}
}

// TestConfigSyncPreRunErrorsExitTwoOnEveryPath (code review L1): a Templates/
// git read that fails before the run — here the pending-removal listing,
// reading HEAD's copy of a " D" built-in through a smudge that cannot run —
// is exit 2 on every path that includes the Templates reconciler: an
// answerless run with "Nothing to do", and --dry-run. A --tier global run
// never reads Templates/, so it neither reports nor fails on it.
func TestConfigSyncPreRunErrorsExitTwoOnEveryPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the filter fixture needs a POSIX shell")
	}
	vaultPath, projDir, _ := canonicalGitVault(t, func(v string) {
		putVaultFile(t, v, "Templates/commands/wrap.md", string(embeddedTemplateBytes(t, "commands/wrap.md")))
		putVaultFile(t, v, ".gitattributes", "Templates/** filter=rot\n")
	})
	gitInVault(t, vaultPath, "config", "filter.rot.smudge", "/nonexistent/vp-test-smudge")
	if err := os.Remove(filepath.Join(vaultPath, "Templates", "commands", "wrap.md")); err != nil {
		t.Fatal(err)
	}
	head := headOf(t, vaultPath)

	run := func(args ...string) (stdout, stderr string, code int) {
		stderr = captureStderr(t, func() {
			stdout, code = runSyncWithStdin(t, "", append([]string{"--project-root", projDir}, args...))
		})
		return stdout, stderr, code
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"nothing to do", []string{"--tier", "vault"}, "Nothing to do"},
		{"dry run", []string{"--tier", "vault", "--dry-run"}, "Plan:"},
		{"all tiers, nothing to do", nil, "Nothing to do"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := run(tc.args...)
			if code != cli.ExitSystem {
				t.Errorf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, cli.ExitSystem, stdout, stderr)
			}
			if !strings.Contains(stdout, tc.want) || !strings.Contains(stderr, "could not list uncommitted Templates/ removals") {
				t.Errorf("stdout:\n%s\nstderr:\n%s", stdout, stderr)
			}
		})
	}
	t.Run("global tier", func(t *testing.T) {
		stdout, stderr, code := run("--tier", "global", "--yes")
		if code != cli.ExitOK || strings.Contains(stderr, "Templates/") {
			t.Errorf("a --tier global run failed on a Templates/ read: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
	})
	if headOf(t, vaultPath) != head {
		t.Error("a commit was made")
	}
	if st := strings.TrimSpace(gitInVault(t, vaultPath, "status", "--porcelain", "--", "Templates/")); st != "D Templates/commands/wrap.md" {
		t.Errorf("the pending removal changed: %q", st)
	}
}
