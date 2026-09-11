// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// The reset verbs are new behaviour: at 1f3bb62 `vp commands reset` is an
// unknown subcommand. These tests pin what they do; the substantive must-fail
// tests of this change are the upgrade-keeps tests in cmd_commands_test.go and
// cmd_skills_upgrade_test.go. TestConfigSyncDoesNotUndoAReset is a regression
// pin of Task A's HEAD-restore logic, which already leaves a removal alone.

// runReset drives runTemplateReset with captured output.
func runReset(t *testing.T, vault, resourceType string, dryRun bool, names ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	code = runTemplateReset(templateResetOpts{
		ResourceType:      resourceType,
		Names:             names,
		DryRun:            dryRun,
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride: vault,
	})
	return out.String(), errb.String(), code
}

func backupPath(vault, rel string, data string) string {
	return filepath.Join(vault, filepath.FromSlash(templates.BackupName(rel, []byte(data))))
}

func assertAbsent(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Errorf("%s still exists (err=%v)", p, err)
	}
}

// TestCommandsResetRemovesOverrideAndKeepsBackup drives the verb through the
// real dispatch (so the surface gate's pre-run hook runs) on a non-git vault.
func TestCommandsResetRemovesOverrideAndKeepsBackup(t *testing.T) {
	vaultPath, _ := overrideVault(t)
	wrap := putVaultFile(t, vaultPath, "Templates/commands/wrap.md", myWrap)

	info := cli.BuildInfo{Version: "test"}
	reg := cli.NewRegistry(info)
	reg.SetPreRun(preRun)
	registerAll(reg, info)
	var code int
	var out string
	errOut := captureStderr(t, func() {
		out = captureStdout(t, func() { code = reg.Dispatch([]string{"commands", "reset", "wrap"}) })
	})
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	assertAbsent(t, wrap)
	bak := backupPath(vaultPath, "Templates/commands/wrap.md", myWrap)
	assertFileBytes(t, bak, myWrap)
	want := "reset Templates/commands/wrap.md: removed your override; the built-in now serves it (backup: " +
		templates.BackupName("Templates/commands/wrap.md", []byte(myWrap)) + ")"
	if !strings.Contains(out, want) {
		t.Errorf("stdout lacks %q:\n%s", want, out)
	}
	if !strings.Contains(out, "Shims in each project may still carry the removed override's text") ||
		!strings.Contains(out, "`vp commands upgrade --overwrite` (or `vp init`) in each project") {
		t.Errorf("no shim notice:\n%s", out)
	}
	sums, err := commands.List(vpctx.NewResolver(vaultPath), "command", "", "", "", 60)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sums {
		if s.Name == "wrap" && s.Source != "embedded" {
			t.Errorf("wrap is served by %q after the reset, want embedded", s.Source)
		}
	}
}

// TestCommandsResetNeverOverwritesABackup is M3: an old binary's bare
// wrap.md.bak is never touched or written; two different overrides get two
// backups; the same bytes again reuse one; and a forced name collision with
// different bytes refuses and removes nothing.
func TestCommandsResetNeverOverwritesABackup(t *testing.T) {
	vault := t.TempDir()
	const legacy, a, b = "an old binary's single .bak\n", "# override A\n", "# override B\n"
	wrap := putVaultFile(t, vault, "Templates/commands/wrap.md", a)
	putVaultFile(t, vault, "Templates/commands/wrap.md.bak", legacy)

	if _, errOut, code := runReset(t, vault, "command", false, "wrap"); code != cli.ExitOK {
		t.Fatalf("reset A: exit %d\n%s", code, errOut)
	}
	assertFileBytes(t, wrap+".bak", legacy)
	bakA := backupPath(vault, "Templates/commands/wrap.md", a)
	assertFileBytes(t, bakA, a)

	putVaultFile(t, vault, "Templates/commands/wrap.md", b)
	if _, errOut, code := runReset(t, vault, "command", false, "wrap"); code != cli.ExitOK {
		t.Fatalf("reset B: exit %d\n%s", code, errOut)
	}
	assertFileBytes(t, bakA, a)
	assertFileBytes(t, backupPath(vault, "Templates/commands/wrap.md", b), b)

	putVaultFile(t, vault, "Templates/commands/wrap.md", a)
	entriesBefore, _ := os.ReadDir(filepath.Dir(wrap))
	out, _, code := runReset(t, vault, "command", false, "wrap")
	if code != cli.ExitOK || !strings.Contains(out, "which already held these bytes") {
		t.Fatalf("reset A again: exit %d\n%s", code, out)
	}
	entriesAfter, _ := os.ReadDir(filepath.Dir(wrap))
	if len(entriesAfter) != len(entriesBefore)-1 { // wrap.md gone, no new backup
		t.Errorf("entries %d -> %d: a new backup was written for reused bytes", len(entriesBefore), len(entriesAfter))
	}
	assertFileBytes(t, wrap+".bak", legacy)

	// A forced collision: the backup name for C already holds other bytes.
	const c = "# override C\n"
	putVaultFile(t, vault, "Templates/commands/wrap.md", c)
	seeded := putVaultFile(t, vault, templates.BackupName("Templates/commands/wrap.md", []byte(c)), "edited backup\n")
	_, errOut, code := runReset(t, vault, "command", false, "wrap")
	if code != cli.ExitSystem {
		t.Errorf("collision: exit %d, want %d", code, cli.ExitSystem)
	}
	if !strings.Contains(errOut, "move or rename it") || !strings.Contains(errOut, "Nothing was removed") {
		t.Errorf("the collision does not say how to fix it:\n%s", errOut)
	}
	assertFileBytes(t, wrap, c)
	assertFileBytes(t, seeded, "edited backup\n")
}

// TestSkillsResetRemovesEveryOverrideFileWithBackups: every override file of
// the skill is removed with its own backup, a byte-identical mirror with none,
// a vault-only extra file is kept and listed, and the embedded skill serves
// again. One unknown name among several removes nothing.
func TestSkillsResetRemovesEveryOverrideFileWithBackups(t *testing.T) {
	vault := t.TempDir()
	skill := bodyOnlySkillOverride(t, "startup-analyst")
	skillMD := putVaultFile(t, vault, "Templates/skills/startup-analyst/SKILL.md", skill)
	capex := putVaultFile(t, vault, "Templates/skills/startup-analyst/references/capex-opex.md", "my capex\n")
	funding := putVaultFile(t, vault, "Templates/skills/startup-analyst/references/funding-sources.md", "my funding\n")
	mirror := putVaultFile(t, vault, "Templates/skills/startup-analyst/references/reality-validation.md",
		string(embeddedTemplateBytes(t, "skills/startup-analyst/references/reality-validation.md")))
	extra := putVaultFile(t, vault, "Templates/skills/startup-analyst/references/my-notes.md", "mine\n")

	// One unknown name refuses the whole invocation.
	if _, errOut, code := runReset(t, vault, "skill", false, "startup-analyst", "no-such-skill"); code != cli.ExitUser ||
		!strings.Contains(errOut, `no built-in skill or skill file named "no-such-skill"`) {
		t.Fatalf("unknown name: exit %d\n%s", code, errOut)
	}
	assertFileBytes(t, skillMD, skill)

	out, errOut, code := runReset(t, vault, "skill", false, "startup-analyst", "startup-analyst/SKILL.md")
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	for p, body := range map[string]string{skillMD: skill, capex: "my capex\n", funding: "my funding\n"} {
		assertAbsent(t, p)
		rel, _ := filepath.Rel(vault, p)
		assertFileBytes(t, backupPath(vault, filepath.ToSlash(rel), body), body)
	}
	assertAbsent(t, mirror)
	if !strings.Contains(out, "reality-validation.md: removed it; the built-in now serves it (identical to the built-in; no backup needed)") {
		t.Errorf("no mirror line:\n%s", out)
	}
	assertFileBytes(t, extra, "mine\n")
	if !strings.Contains(out, "left Templates/skills/startup-analyst/references/my-notes.md: not a built-in file") {
		t.Errorf("the extra file is not listed:\n%s", out)
	}
	// De-duplicated: SKILL.md named twice is reset once.
	if c := strings.Count(out, "reset Templates/skills/startup-analyst/SKILL.md:"); c != 1 {
		t.Errorf("SKILL.md reported %d times", c)
	}
	if _, src, err := vpctx.NewResolver(vault).ResolveSkillDir("startup-analyst", "", "", ""); err != nil || src != "embedded" {
		t.Errorf("startup-analyst served by %q (err=%v), want embedded", src, err)
	}
}

// TestSkillsResetBackupOnDirtyReference is the old skills-upgrade backup test,
// now where the backup lives: resetting one reference keeps its bytes in a
// hash-named backup and leaves every other file alone.
func TestSkillsResetBackupOnDirtyReference(t *testing.T) {
	vault := t.TempDir()
	seedMatchingSkillsVault(t, vault)
	capex := filepath.Join(vault, "Templates/skills/startup-analyst/references/capex-opex.md")
	if err := os.WriteFile(capex, []byte("USER EDIT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := runReset(t, vault, "skill", false, "startup-analyst/references/capex-opex.md"); code != cli.ExitOK {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
	assertFileBytes(t, backupPath(vault, "Templates/skills/startup-analyst/references/capex-opex.md", "USER EDIT\n"), "USER EDIT\n")
	if _, err := os.Stat(filepath.Join(vault, "Templates/skills/startup-analyst/SKILL.md")); err != nil {
		t.Errorf("an unnamed file was removed: %v", err)
	}
}

// gitResetVault is a --no-git-initialised vault turned into its own repository
// with an origin, holding a committed override of wrap.
func gitResetVault(t *testing.T) (vaultPath, projDir, origin string) {
	t.Helper()
	vaultPath, projDir = overrideVault(t)
	putVaultFile(t, vaultPath, "Templates/commands/wrap.md", myWrap)
	origin = gitifyVault(t, vaultPath)
	return vaultPath, projDir, origin
}

// TestCommandsResetOnGitVaultCommitsTheRemoval is C1: one local commit
// carrying exactly the removal, an honest message, nothing pushed, and no
// Templates/ dirt for `vp vault sync` to refuse on.
func TestCommandsResetOnGitVaultCommitsTheRemoval(t *testing.T) {
	vaultPath, _, origin := gitResetVault(t)
	head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))

	out, errOut, code := runReset(t, vaultPath, "command", false, "wrap")
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if parent := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD~1")); parent != head {
		t.Errorf("not exactly one new commit on %s", head)
	}
	if names := strings.TrimSpace(gitInVault(t, vaultPath, "show", "--name-status", "--format=", "HEAD")); names != "D\tTemplates/commands/wrap.md" {
		t.Errorf("the commit carries %q", names)
	}
	msg := gitInVault(t, vaultPath, "log", "-1", "--format=%B")
	host, _ := os.Hostname()
	bak := templates.BackupName("Templates/commands/wrap.md", []byte(myWrap))
	for _, want := range []string{
		"chore(templates): operator reset of 1 vault Templates/ file(s)",
		"`vp commands reset wrap` removed these vault Templates/ files at the operator's request",
		"vp removed only the files\nnamed.",
		"The committed copy of each is in this commit's parent; the\nworking-tree bytes at reset time are in the backup named beside it, on\nhost " + host,
		"- Templates/commands/wrap.md (backup: " + bak + ")",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(out, "committed the removal locally as ") || !strings.Contains(out, "not pushed; `vp vault sync` publishes it") {
		t.Errorf("stdout does not report the local commit:\n%s", out)
	}
	if got := strings.TrimSpace(gitInVault(t, origin, "rev-parse", "main")); got != head {
		t.Errorf("origin moved to %s: the reset pushed", got)
	}
	if st := strings.TrimSpace(gitInVault(t, vaultPath, "status", "--porcelain", "--", "Templates/")); st != "" {
		t.Errorf("Templates/ is dirty: %q", st)
	}
	scan, err := storage.TidyScan(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range scan.GenuineDirt() {
		if strings.HasPrefix(p, "Templates/") {
			t.Errorf("vault sync would refuse on %s", p)
		}
	}
}

// TestCommandsResetUntrackedOverrideNeedsNoCommit: an override never
// committed is removed and backed up, and no commit is made.
func TestCommandsResetUntrackedOverrideNeedsNoCommit(t *testing.T) {
	vaultPath, _ := overrideVault(t)
	gitifyVault(t, vaultPath)
	head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))
	putVaultFile(t, vaultPath, "Templates/commands/restart.md", "# untracked override\n")
	if out, errOut, code := runReset(t, vaultPath, "command", false, "restart"); code != cli.ExitOK || strings.Contains(out, "committed") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if got := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD")); got != head {
		t.Error("a commit was made for an untracked file")
	}
}

// TestCommandsResetCommitFailureUnstages is H1 with N2: a refused commit
// leaves the removal unstaged, exits 2 with the manual commands, and the next
// config sync neither defers, restores nor commits it (the committed copy is
// operator content) and writes no templates.lock. With the hook gone, running
// the same reset again finishes the commit.
func TestCommandsResetCommitFailureUnstages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell hooks need a POSIX shell")
	}
	vaultPath, projDir, _ := gitResetVault(t)
	wrap := filepath.Join(vaultPath, "Templates", "commands", "wrap.md")
	head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))
	hook := filepath.Join(vaultPath, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, errOut, code := runReset(t, vaultPath, "command", false, "wrap")
	if code != cli.ExitSystem {
		t.Errorf("exit %d, want %d", code, cli.ExitSystem)
	}
	for _, want := range []string{
		"removed, not committed",
		"git -C " + vaultPath + " commit -m",
		"-- Templates/commands/wrap.md",
		"git -C " + vaultPath + " checkout HEAD -- Templates/commands/wrap.md",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
	if staged := strings.TrimSpace(gitInVault(t, vaultPath, "diff", "--cached", "--name-only", "--", "Templates/commands/wrap.md")); staged != "" {
		t.Errorf("the removal is still staged: %q", staged)
	}
	if st := gitInVault(t, vaultPath, "status", "--porcelain", "--", "Templates/commands/wrap.md"); st != " D Templates/commands/wrap.md\n" {
		t.Errorf("status = %q, want an unstaged deletion", st)
	}
	assertAbsent(t, wrap)

	out := syncVault(t, projDir, "", "--yes")
	if strings.Contains(out, "prune deferred") || strings.Contains(out, "staged change") || strings.Contains(out, "restored") {
		t.Errorf("the next sync deferred or restored the reset:\n%s", out)
	}
	assertAbsent(t, wrap)
	if !strings.Contains(out, "Templates/commands/wrap.md removed from the worktree; the committed copy is operator content — finish removing it with vp commands reset wrap") {
		t.Errorf("the pending removal of operator content is not named with its reset verb:\n%s", out)
	}
	if got := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD")); got != head {
		t.Errorf("the sync committed a removal of operator content: %s", gitInVault(t, vaultPath, "log", "-1", "--stat"))
	}
	assertAbsent(t, filepath.Join(vaultPath, ".vibe-palace", "templates.lock"))

	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	out, errOut, code = runReset(t, vaultPath, "command", false, "wrap")
	if code != cli.ExitOK {
		t.Fatalf("rerun: exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "the removal was never committed; committing it now") {
		t.Errorf("the rerun did not say it finishes the commit:\n%s", out)
	}
	if names := strings.TrimSpace(gitInVault(t, vaultPath, "show", "--name-status", "--format=", "HEAD")); names != "D\tTemplates/commands/wrap.md" {
		t.Errorf("the rerun did not commit the removal: %q", names)
	}
	if parent := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD~1")); parent != head {
		t.Errorf("unexpected history: HEAD~1=%s, want %s", parent, head)
	}
	// L3: the finishing commit is the durable pointer to the bytes, so it
	// names the backup the first run wrote.
	bak := templates.BackupName("Templates/commands/wrap.md", []byte(myWrap))
	if msg := gitInVault(t, vaultPath, "log", "-1", "--format=%B"); !strings.Contains(msg,
		"- Templates/commands/wrap.md (removed by an earlier reset and left uncommitted; backup: "+bak+")") {
		t.Errorf("the finishing commit does not name the earlier backup:\n%s", msg)
	}
}

// TestCommandsResetFinishesAStagedDeletion is review L2: a pending removal
// whose deletion is STAGED (`git rm`, or a CommitRemovals whose own unstage
// failed) holds no bytes, so the rerun commits it instead of refusing it as
// "a staged change", and the finishing commit says no backup is on the host.
func TestCommandsResetFinishesAStagedDeletion(t *testing.T) {
	vaultPath, _, _ := gitResetVault(t)
	gitInVault(t, vaultPath, "rm", "-q", "Templates/commands/wrap.md")
	head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))

	out, errOut, code := runReset(t, vaultPath, "command", false, "wrap")
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if strings.Contains(errOut, "staged change") {
		t.Errorf("a staged deletion was refused as a staged change:\n%s", errOut)
	}
	if parent := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD~1")); parent != head {
		t.Errorf("not one commit on %s", head)
	}
	if names := strings.TrimSpace(gitInVault(t, vaultPath, "show", "--name-status", "--format=", "HEAD")); names != "D\tTemplates/commands/wrap.md" {
		t.Errorf("the commit carries %q", names)
	}
	if msg := gitInVault(t, vaultPath, "log", "-1", "--format=%B"); !strings.Contains(msg, "removed earlier and left uncommitted; no backup of it is on this host") {
		t.Errorf("message:\n%s", msg)
	}
	if st := strings.TrimSpace(gitInVault(t, vaultPath, "status", "--porcelain")); st != "" {
		t.Errorf("status not clean: %q", st)
	}
}

// TestCommandsResetStillRefusesAStagedModificationOfAPendingPath: only a
// staged DELETION is accepted for a pending removal; staged bytes are not.
func TestCommandsResetStillRefusesAStagedModificationOfAPendingPath(t *testing.T) {
	vaultPath, _, _ := gitResetVault(t)
	putVaultFile(t, vaultPath, "Templates/commands/wrap.md", "# staged, then removed from the worktree\n")
	gitInVault(t, vaultPath, "add", "Templates/commands/wrap.md")
	if err := os.Remove(filepath.Join(vaultPath, "Templates", "commands", "wrap.md")); err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))
	_, errOut, code := runReset(t, vaultPath, "command", false, "wrap")
	if code != cli.ExitSystem || !strings.Contains(errOut, "the index holds a staged change") {
		t.Errorf("exit %d\n%s", code, errOut)
	}
	if got := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD")); got != head {
		t.Error("HEAD moved")
	}
}

// TestSkillsResetOneFileNeverMislabelsBuiltins is review L1: resetting one
// file of a skill must not call the skill's other built-in files "not a
// built-in file", and leaves them alone; a genuinely extra file is listed.
func TestSkillsResetOneFileNeverMislabelsBuiltins(t *testing.T) {
	vault := t.TempDir()
	putVaultFile(t, vault, "Templates/skills/startup-analyst/SKILL.md", bodyOnlySkillOverride(t, "startup-analyst"))
	capex := putVaultFile(t, vault, "Templates/skills/startup-analyst/references/capex-opex.md", "my capex\n")
	putVaultFile(t, vault, "Templates/skills/startup-analyst/references/my-notes.md", "mine\n")
	for _, dry := range []bool{true, false} {
		out, errOut, code := runReset(t, vault, "skill", dry, "startup-analyst/SKILL.md")
		if code != cli.ExitOK {
			t.Fatalf("dry=%v: exit %d\n%s", dry, code, errOut)
		}
		if strings.Contains(out, "capex-opex.md: not a built-in file") {
			t.Errorf("dry=%v: a built-in reference was called not a built-in file:\n%s", dry, out)
		}
		if !strings.Contains(out, "left Templates/skills/startup-analyst/references/my-notes.md: not a built-in file") {
			t.Errorf("dry=%v: the genuine extra is not listed:\n%s", dry, out)
		}
	}
	assertFileBytes(t, capex, "my capex\n")
}

// TestSkillsResetSaysNothingToResetOncePerName: a skill named whole with some
// files reset says nothing about its other built-in files, and a skill with no
// vault copy at all gets one line, not one per file.
func TestSkillsResetSaysNothingToResetOncePerName(t *testing.T) {
	vault := t.TempDir()
	putVaultFile(t, vault, "Templates/skills/startup-analyst/SKILL.md", bodyOnlySkillOverride(t, "startup-analyst"))
	out, _, code := runReset(t, vault, "skill", false, "startup-analyst")
	if code != cli.ExitOK || strings.Contains(out, "nothing to reset") {
		t.Errorf("exit %d; per-file nothing-to-reset noise:\n%s", code, out)
	}
	out, _, _ = runReset(t, vault, "skill", false, "startup-analyst")
	if c := strings.Count(out, "nothing to reset:"); c != 1 || !strings.Contains(out, "nothing to reset: startup-analyst has no vault override") {
		t.Errorf("%d nothing-to-reset lines, want 1 naming the skill:\n%s", c, out)
	}
}

// TestCommandsResetDryRunMatchesTheRealRun is review L4: the dry run runs the
// same path checks, pending classification and git preflights as the real
// run, so it refuses where the real run refuses, with the same exit status,
// and writes nothing.
func TestCommandsResetDryRunMatchesTheRealRun(t *testing.T) {
	t.Run("symlinked-skill-directory", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need a privilege on Windows")
		}
		vault := t.TempDir()
		putVaultFile(t, vault, "Projects/p/skills/chair/SKILL.md", "---\nname: chair\n---\nproject chair\n")
		if err := os.MkdirAll(filepath.Join(vault, "Templates", "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(vault, "Projects", "p", "skills", "chair"), filepath.Join(vault, "Templates", "skills", "chair")); err != nil {
			t.Fatal(err)
		}
		before := linkDigest(t, vault)
		dryOut, dryErr, dry := runReset(t, vault, "skill", true, "chair")
		_, realErr, real := runReset(t, vault, "skill", false, "chair")
		if dry != cli.ExitUser || real != cli.ExitUser {
			t.Errorf("dry exit %d, real exit %d, want both %d", dry, real, cli.ExitUser)
		}
		if !strings.Contains(dryErr, "symlink") || !strings.Contains(realErr, "symlink") {
			t.Errorf("dry:\n%s\nreal:\n%s", dryErr, realErr)
		}
		if strings.Contains(dryOut, "would reset") {
			t.Errorf("the dry run previewed a reset the real run refuses:\n%s", dryOut)
		}
		if linkDigest(t, vault) != before {
			t.Error("the vault changed")
		}
	})
	t.Run("pending-removal", func(t *testing.T) {
		vaultPath, _, _ := gitResetVault(t)
		if err := os.Remove(filepath.Join(vaultPath, "Templates", "commands", "wrap.md")); err != nil {
			t.Fatal(err)
		}
		head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))
		out, errOut, code := runReset(t, vaultPath, "command", true, "wrap")
		if code != cli.ExitOK {
			t.Fatalf("exit %d\n%s", code, errOut)
		}
		for _, want := range []string{
			"would commit Templates/commands/wrap.md: already removed from the worktree",
			"would commit the removal of the tracked files locally (not pushed)",
			"(dry run: nothing was written)",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("dry run lacks %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "nothing to reset") {
			t.Errorf("the dry run says nothing to reset for a pending removal:\n%s", out)
		}
		if got := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD")); got != head {
			t.Error("the dry run committed")
		}
	})
	t.Run("tracked-override", func(t *testing.T) {
		vaultPath, _, _ := gitResetVault(t)
		before := treeDigest(t, filepath.Join(vaultPath, "Templates"))
		out, errOut, code := runReset(t, vaultPath, "command", true, "wrap")
		if code != cli.ExitOK || !strings.Contains(out, "would commit the removal of the tracked files locally (not pushed)") {
			t.Errorf("exit %d\n%s\n%s", code, out, errOut)
		}
		if treeDigest(t, filepath.Join(vaultPath, "Templates")) != before {
			t.Error("the dry run changed Templates/")
		}
	})
	t.Run("no-identity", func(t *testing.T) {
		vaultPath, _, _ := gitResetVault(t)
		isolateGitIdentity(t, vaultPath)
		_, dryErr, dry := runReset(t, vaultPath, "command", true, "wrap")
		_, _, real := runReset(t, vaultPath, "command", false, "wrap")
		if dry != cli.ExitSystem || real != cli.ExitSystem || !strings.Contains(dryErr, "identity") {
			t.Errorf("dry exit %d, real exit %d\n%s", dry, real, dryErr)
		}
		if _, err := os.Stat(filepath.Join(vaultPath, "Templates", "commands", "wrap.md")); err != nil {
			t.Errorf("the file was removed: %v", err)
		}
	})
	t.Run("staged-change", func(t *testing.T) {
		vaultPath, _, _ := gitResetVault(t)
		putVaultFile(t, vaultPath, "Templates/commands/wrap.md", "# a staged edit\n")
		gitInVault(t, vaultPath, "add", "Templates/commands/wrap.md")
		_, dryErr, dry := runReset(t, vaultPath, "command", true, "wrap")
		if dry != cli.ExitSystem || !strings.Contains(dryErr, "staged change") {
			t.Errorf("dry exit %d\n%s", dry, dryErr)
		}
	})
	t.Run("stale-copy", resetStaleCopyDryRunMatchesTheRealRun)
	t.Run("crlf-mirror", func(t *testing.T) {
		vault := t.TempDir()
		crlf := strings.ReplaceAll(string(embeddedTemplateBytes(t, "commands/restart.md")), "\n", "\r\n")
		putVaultFile(t, vault, "Templates/commands/restart.md", crlf)
		dry, _, _ := runReset(t, vault, "command", true, "restart")
		if !strings.Contains(dry, "would reset Templates/commands/restart.md: remove it (identical, line endings aside, to the built-in; no backup needed)") {
			t.Errorf("dry run:\n%s", dry)
		}
		out, _, code := runReset(t, vault, "command", false, "restart")
		if code != cli.ExitOK || !strings.Contains(out, "removed it; the built-in now serves it (identical, line endings aside, to the built-in; no backup needed)") {
			t.Errorf("exit %d\n%s", code, out)
		}
		if baks := bakFilesUnder(t, filepath.Join(vault, "Templates")); len(baks) != 0 {
			t.Errorf("a backup was written: %v", baks)
		}
	})
}

// TestCommitTemplateResetReportsACommitThatLeftPathsInHEAD is review L5: when
// the commit lands but HEAD still holds a path, the landed commit is reported
// as committed and the manual commands name only the paths left.
func TestCommitTemplateResetReportsACommitThatLeftPathsInHEAD(t *testing.T) {
	vaultPath, _, _ := gitResetVault(t)
	putVaultFile(t, vaultPath, "Templates/commands/restart.md", "# restart override\n")
	gitInVault(t, vaultPath, "add", "Templates/commands/restart.md")
	gitInVault(t, vaultPath, "commit", "-qm", "restart override")
	if err := os.Remove(filepath.Join(vaultPath, "Templates", "commands", "wrap.md")); err != nil {
		t.Fatal(err)
	}
	// restart.md is still present, so the commit cannot remove it: the
	// timeable stand-in for a writer racing the commit.
	var out, errb bytes.Buffer
	code := commitTemplateReset(vaultPath, storage.VaultGitOK, []resetCommitEntry{
		{Rel: "Templates/commands/wrap.md", Backup: "b"},
		{Rel: "Templates/commands/restart.md", Backup: "c"},
	}, true, "vp commands reset", "vp commands reset wrap restart", &out, &errb)
	if code != cli.ExitSystem {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(out.String(), "committed the removal locally as ") {
		t.Errorf("the landed commit is not reported:\n%s", out.String())
	}
	e := errb.String()
	if strings.Contains(e, "removed, not committed") {
		t.Errorf("a landed commit is reported as not committed:\n%s", e)
	}
	if !strings.Contains(e, "HEAD still holds Templates/commands/restart.md") ||
		!strings.Contains(e, "checkout HEAD -- Templates/commands/restart.md\n") ||
		strings.Contains(e, "checkout HEAD -- Templates/commands/wrap.md") {
		t.Errorf("the manual commands do not name only the path left:\n%s", e)
	}
}

// TestConfigSyncDoesNotUndoAReset is the collision pin (a regression pin: Task
// A's HEAD restore already leaves an absent path alone). A committed override
// is reset; two syncs, with and without --yes, neither restore it nor move
// HEAD, and no templates.lock exists afterwards.
func TestConfigSyncDoesNotUndoAReset(t *testing.T) {
	vaultPath, projDir, _ := gitResetVault(t)
	if _, errOut, code := runReset(t, vaultPath, "command", false, "wrap"); code != cli.ExitOK {
		t.Fatalf("reset: exit %d\n%s", code, errOut)
	}
	head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))
	for _, extra := range [][]string{{"--yes"}, nil} {
		out := syncVault(t, projDir, "", extra...)
		if strings.Contains(out, "restored") {
			t.Errorf("sync %v restored the reset file:\n%s", extra, out)
		}
	}
	assertAbsent(t, filepath.Join(vaultPath, "Templates", "commands", "wrap.md"))
	if got := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD")); got != head {
		t.Errorf("HEAD moved after the reset commit: %s", gitInVault(t, vaultPath, "log", "-1", "--stat"))
	}
	assertAbsent(t, filepath.Join(vaultPath, ".vibe-palace", "templates.lock"))
}

func TestCommandsResetRefusesWithoutIdentity(t *testing.T) {
	vaultPath, _, _ := gitResetVault(t)
	isolateGitIdentity(t, vaultPath)
	head := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD"))
	before := treeDigest(t, filepath.Join(vaultPath, "Templates"))
	_, errOut, code := runReset(t, vaultPath, "command", false, "wrap")
	if code != cli.ExitSystem || !strings.Contains(errOut, "identity") || !strings.Contains(errOut, "nothing was changed") {
		t.Errorf("exit %d\n%s", code, errOut)
	}
	if treeDigest(t, filepath.Join(vaultPath, "Templates")) != before {
		t.Error("Templates/ changed (a file removed or a backup written)")
	}
	if got := strings.TrimSpace(gitInVault(t, vaultPath, "rev-parse", "HEAD")); got != head {
		t.Error("HEAD moved")
	}
}

func TestCommandsResetRefusesAStagedChange(t *testing.T) {
	vaultPath, _, _ := gitResetVault(t)
	putVaultFile(t, vaultPath, "Templates/commands/wrap.md", "# a staged edit\n")
	gitInVault(t, vaultPath, "add", "Templates/commands/wrap.md")
	before := treeDigest(t, filepath.Join(vaultPath, "Templates"))
	_, errOut, code := runReset(t, vaultPath, "command", false, "wrap")
	if code != cli.ExitSystem || !strings.Contains(errOut, "the index holds a staged change for Templates/commands/wrap.md") {
		t.Errorf("exit %d\n%s", code, errOut)
	}
	if treeDigest(t, filepath.Join(vaultPath, "Templates")) != before {
		t.Error("Templates/ changed")
	}
	if staged := strings.TrimSpace(gitInVault(t, vaultPath, "diff", "--cached", "--name-only")); staged != "Templates/commands/wrap.md" {
		t.Errorf("the staged change was disturbed: %q", staged)
	}
}

// TestCommandsResetNeverCommitsAnEnclosingRepo: a vault nested in a project
// repository with a remote and an unpushed commit. The file is removed with a
// backup; the enclosing repository's HEAD, index, refs, reflog and remote are
// untouched; the output names the enclosing repository and does not suggest
// `vp vault sync`.
func TestCommandsResetNeverCommitsAnEnclosingRepo(t *testing.T) {
	_, _ = initTestEnv(t, false)
	projDir := t.TempDir()
	markProjectDir(t, projDir)
	parent := t.TempDir()
	gitInVault(t, parent, "init", "-q", "-b", "main")
	gitInVault(t, parent, "config", "user.email", "test@test.com")
	gitInVault(t, parent, "config", "user.name", "Test")
	vaultPath := filepath.Join(parent, "notes", "vault")
	putVaultFile(t, vaultPath, "Templates/commands/wrap.md", myWrap)
	putVaultFile(t, parent, ".gitignore", ".vp-locks/\n*.bak\n")
	gitInVault(t, parent, "add", "-A")
	gitInVault(t, parent, "commit", "-qm", "project with a vault inside")
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitInVault(t, parent, "init", "-q", "--bare", "-b", "main", origin)
	gitInVault(t, parent, "remote", "add", "origin", origin)
	gitInVault(t, parent, "push", "-q", "-u", "origin", "main")
	putVaultFile(t, parent, "WIP.md", "not for push\n")
	gitInVault(t, parent, "add", "WIP.md")
	gitInVault(t, parent, "commit", "-qm", "LOCAL WIP - not for push")
	if code := cmdInit(cli.BuildInfo{Version: "test"}).Run(
		[]string{projDir, "--name", "ovr", "--vault-path", vaultPath, "--no-git"}); code != cli.ExitOK {
		t.Fatalf("init exit code = %d", code)
	}
	state := func() string {
		return strings.Join([]string{
			gitInVault(t, parent, "rev-parse", "HEAD"),
			gitInVault(t, parent, "ls-files", "-s"),
			gitInVault(t, parent, "for-each-ref"),
			gitInVault(t, origin, "rev-parse", "main"),
			gitInVault(t, parent, "reflog", "-n", "5"),
		}, "\n")
	}
	before := state()

	out, errOut, code := runReset(t, vaultPath, "command", false, "wrap")
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if after := state(); after != before {
		t.Errorf("the enclosing repository changed:\n--- before\n%s\n--- after\n%s", before, after)
	}
	assertAbsent(t, filepath.Join(vaultPath, "Templates", "commands", "wrap.md"))
	assertFileBytes(t, backupPath(vaultPath, "Templates/commands/wrap.md", myWrap), myWrap)
	if !strings.Contains(out, "not committed: the vault is inside another repository (") {
		t.Errorf("stdout does not name the enclosing repository:\n%s", out)
	}
	if strings.Contains(out+errOut, "vault sync") {
		t.Errorf("the nested-vault output suggests vp vault sync:\n%s\n%s", out, errOut)
	}
}

// TestCommandsResetWithoutGitOnPath: the vault has .git, git is not on PATH
// (InspectVaultGit: VaultGitUnavailable). The reset still removes with a
// backup, warns that nothing was committed, and exits 0.
func TestCommandsResetWithoutGitOnPath(t *testing.T) {
	vaultPath, _, _ := gitResetVault(t)
	t.Setenv("PATH", t.TempDir())
	out, errOut, code := runReset(t, vaultPath, "command", false, "wrap")
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "not committed: git is not on PATH") {
		t.Errorf("no warning:\n%s", errOut)
	}
	assertAbsent(t, filepath.Join(vaultPath, "Templates", "commands", "wrap.md"))
	assertFileBytes(t, backupPath(vaultPath, "Templates/commands/wrap.md", myWrap), myWrap)
}

// TestCommandsResetRefusesBrokenRepository: a dangling .git file refuses
// before anything is written.
func TestCommandsResetRefusesBrokenRepository(t *testing.T) {
	vault := t.TempDir()
	putVaultFile(t, vault, "Templates/commands/wrap.md", myWrap)
	putVaultFile(t, vault, ".git", "gitdir: /nonexistent\n")
	before := treeDigest(t, vault)
	_, errOut, code := runReset(t, vault, "command", false, "wrap")
	if code != cli.ExitSystem || !strings.Contains(errOut, "cannot be used") {
		t.Errorf("exit %d\n%s", code, errOut)
	}
	if treeDigest(t, vault) != before {
		t.Error("the vault changed")
	}
}

// TestCommandsResetRefusesSymlinkedPath is M1 through the CLI: each shape exits
// 1 with nothing written, the link and its target untouched. A vault whose
// root is a symlink resets normally.
func TestCommandsResetRefusesSymlinkedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	for _, tc := range []struct {
		name, rt, arg string
		setup         func(t *testing.T, vault string)
	}{
		{"file", "command", "wrap", func(t *testing.T, vault string) {
			target := putVaultFile(t, vault, "Projects/p/commands/wrap.md", "# project wrap\n")
			if err := os.MkdirAll(filepath.Join(vault, "Templates", "commands"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(vault, "Templates", "commands", "wrap.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"skill-directory", "skill", "chair", func(t *testing.T, vault string) {
			putVaultFile(t, vault, "Projects/p/skills/chair/SKILL.md", "---\nname: chair\n---\nproject chair\n")
			if err := os.MkdirAll(filepath.Join(vault, "Templates", "skills"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(vault, "Projects", "p", "skills", "chair"), filepath.Join(vault, "Templates", "skills", "chair")); err != nil {
				t.Fatal(err)
			}
		}},
		{"commands-directory", "command", "wrap", func(t *testing.T, vault string) {
			putVaultFile(t, vault, "Elsewhere/wrap.md", "# elsewhere\n")
			if err := os.MkdirAll(filepath.Join(vault, "Templates"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(vault, "Elsewhere"), filepath.Join(vault, "Templates", "commands")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault := t.TempDir()
			tc.setup(t, vault)
			before := linkDigest(t, vault)
			_, errOut, code := runReset(t, vault, tc.rt, false, tc.arg)
			if code != cli.ExitUser || !strings.Contains(errOut, "symlink") || !strings.Contains(errOut, "Nothing was removed") {
				t.Errorf("exit %d\n%s", code, errOut)
			}
			if linkDigest(t, vault) != before {
				t.Error("the vault changed under a refused reset")
			}
		})
	}
	t.Run("symlinked-root", func(t *testing.T) {
		real := t.TempDir()
		link := filepath.Join(t.TempDir(), "vault")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		putVaultFile(t, real, "Templates/commands/wrap.md", myWrap)
		if out, errOut, code := runReset(t, link, "command", false, "wrap"); code != cli.ExitOK {
			t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
		}
		assertAbsent(t, filepath.Join(real, "Templates", "commands", "wrap.md"))
	})
}

// linkDigest records every entry under root — symlinks as their targets,
// files with their bytes — without following links.
func linkDigest(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if strings.HasPrefix(rel, ".vp-locks") {
			return nil
		}
		fi, _ := os.Lstat(p)
		fmt.Fprintf(&b, "%s %s", rel, fi.Mode())
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			fmt.Fprintf(&b, " -> %s", target)
		case fi.Mode().IsRegular():
			data, _ := os.ReadFile(p)
			fmt.Fprintf(&b, " %s", data)
		}
		b.WriteString("\n")
		return nil
	})
	return b.String()
}

// TestSkillsResetPartialFailureIsResumable: a removal that fails part-way
// exits 2 with the rest removed; once the cause is fixed, the same reset
// finishes — reusing the backups, so no new backup file appears.
func TestSkillsResetPartialFailureIsResumable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX directory permissions and a non-root user")
	}
	vault := t.TempDir()
	putVaultFile(t, vault, "Templates/skills/startup-analyst/SKILL.md", bodyOnlySkillOverride(t, "startup-analyst"))
	capex := putVaultFile(t, vault, "Templates/skills/startup-analyst/references/capex-opex.md", "my capex\n")
	refs := filepath.Dir(capex)
	// Pre-create capex's backup (as a first run would have), then make its
	// directory read-only so only the removal fails.
	if _, err := templates.PreserveBackup(vault, "Templates/skills/startup-analyst/references/capex-opex.md", []byte("my capex\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(refs, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(refs, 0o755) })

	out, errOut, code := runReset(t, vault, "skill", false, "startup-analyst")
	if code != cli.ExitSystem || !strings.Contains(errOut, "removal failed") || !strings.Contains(errOut, "run the reset again to finish") {
		t.Errorf("exit %d\n%s\n%s", code, out, errOut)
	}
	assertAbsent(t, filepath.Join(vault, "Templates", "skills", "startup-analyst", "SKILL.md"))
	count := func() int {
		n := 0
		_ = filepath.WalkDir(filepath.Join(vault, "Templates"), func(p string, d os.DirEntry, err error) error {
			if err == nil && strings.HasSuffix(p, ".bak") {
				n++
			}
			return nil
		})
		return n
	}
	baks := count()

	if err := os.Chmod(refs, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, errOut, code := runReset(t, vault, "skill", false, "startup-analyst"); code != cli.ExitOK {
		t.Fatalf("rerun: exit %d\n%s\n%s", code, out, errOut)
	}
	assertAbsent(t, capex)
	if got := count(); got != baks {
		t.Errorf("%d backups after the rerun, want %d (reused)", got, baks)
	}
}

// TestCommandsResetDryRunWritesNothing: the diff and the exact backup name are
// printed; the vault is byte-identical; exit 0.
func TestCommandsResetDryRunWritesNothing(t *testing.T) {
	vault := t.TempDir()
	putVaultFile(t, vault, "Templates/commands/wrap.md", myWrap)
	putVaultFile(t, vault, "Templates/commands/restart.md", string(embeddedTemplateBytes(t, "commands/restart.md")))
	before := treeDigest(t, vault)
	out, errOut, code := runReset(t, vault, "command", true, "wrap", "restart", "capture")
	if code != cli.ExitOK {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
	for _, want := range []string{
		"would reset Templates/commands/wrap.md: remove it; backup: " + templates.BackupName("Templates/commands/wrap.md", []byte(myWrap)),
		"-# my wrap override",
		"would reset Templates/commands/restart.md: remove it (identical to the built-in; no backup needed)",
		"nothing to reset: capture has no vault override; the built-in already serves it",
		"(dry run: nothing was written)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if treeDigest(t, vault) != before {
		t.Error("the dry run changed the vault")
	}
}

// TestCommandsResetUnknownNameAndNothingToReset: an unknown name exits 1 and
// says a new-named command is not an override; an absent override exits 0
// with "nothing to reset" — qualified (N7) when a project-tier override
// serves the command for the project this runs in.
func TestCommandsResetUnknownNameAndNothingToReset(t *testing.T) {
	vaultPath, _ := overrideVault(t)
	_, errOut, code := runReset(t, vaultPath, "command", false, "my-own-command")
	if code != cli.ExitUser || !strings.Contains(errOut, "not an override of a built-in; it is yours to delete") {
		t.Errorf("unknown: exit %d\n%s", code, errOut)
	}
	if _, errOut, code := runReset(t, vaultPath, "command", false); code != cli.ExitUser || !strings.Contains(errOut, "name at least one") {
		t.Errorf("no names: exit %d\n%s", code, errOut)
	}

	out, _, code := runReset(t, vaultPath, "command", false, "wrap")
	if code != cli.ExitOK || !strings.Contains(out, "nothing to reset: wrap has no vault override; the built-in already serves it") {
		t.Errorf("absent: exit %d\n%s", code, out)
	}

	putVaultFile(t, vaultPath, "Projects/ovr/commands/wrap.md", "# the project's own wrap\n")
	out, _, code = runReset(t, vaultPath, "command", false, "wrap")
	if code != cli.ExitOK || !strings.Contains(out, "for project ovr the project tier (Projects/ovr/commands/wrap.md) serves it") {
		t.Errorf("project tier not named: exit %d\n%s", code, out)
	}
	// A vault override removed while a project override exists says so too.
	putVaultFile(t, vaultPath, "Templates/commands/wrap.md", myWrap)
	out, _, _ = runReset(t, vaultPath, "command", false, "wrap")
	if !strings.Contains(out, "for project ovr the project tier (Projects/ovr/commands/wrap.md) still serves it") {
		t.Errorf("the removal line does not name the project tier:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(vaultPath, "Projects", "ovr", "commands", "wrap.md")); err != nil {
		t.Errorf("the project tier was touched: %v", err)
	}
	// Skills: a project-tier skill is named the same way.
	putVaultFile(t, vaultPath, "Projects/ovr/skills/chair/SKILL.md", "---\nname: chair\ndescription: p\n---\nproject chair\n")
	out, _, _ = runReset(t, vaultPath, "skill", false, "chair")
	if !strings.Contains(out, "for project ovr the project tier (Projects/ovr/skills/chair) serves it") {
		t.Errorf("skill project tier not named:\n%s", out)
	}
}

func TestResetCommandMetadata(t *testing.T) {
	for _, tc := range []struct {
		cmd  *cli.Command
		name string
	}{{cmdCommandsReset(), "commands reset"}, {cmdSkillsReset(), "skills reset"}} {
		if tc.cmd.Name != tc.name || tc.cmd.Run == nil || len(tc.cmd.Flags) != 1 {
			t.Errorf("%s: %+v", tc.name, tc.cmd)
		}
		if rc := tc.cmd.Run([]string{"--nope"}); rc != cli.ExitUser {
			t.Errorf("%s with a bad flag: exit %d", tc.name, rc)
		}
	}
	for parent, child := range map[string]string{"commands": "commands reset", "skills": "skills reset"} {
		found := false
		for _, k := range registeredChildren(t, parent) {
			found = found || k == child
		}
		if !found {
			t.Errorf("%q is not registered under %q", child, parent)
		}
	}
}

// TestTemplateResetCommitMessageShapes covers the mirror and pending lines.
func TestTemplateResetCommitMessageShapes(t *testing.T) {
	msg := templateResetCommitMessage("vp skills reset chair", []resetCommitEntry{
		{Rel: "Templates/skills/chair/SKILL.md", Mirror: true},
		{Rel: "Templates/skills/chair/references/x.md", Pending: true},
		{Rel: "Templates/skills/chair/references/z.md", Pending: true, Backups: []string{"Templates/skills/chair/references/z.md.00112233aabb.bak"}},
		{Rel: "Templates/skills/chair/references/y.md", Backup: "Templates/skills/chair/references/y.md.0123456789ab.bak"},
	}, "hostA")
	for _, want := range []string{
		"chore(templates): operator reset of 4 vault Templates/ file(s)",
		"`vp skills reset chair` removed",
		"host hostA.",
		"- Templates/skills/chair/SKILL.md (identical to the built-in; no backup needed)",
		"- Templates/skills/chair/references/x.md (removed earlier and left uncommitted; no backup of it is on this host)",
		"- Templates/skills/chair/references/z.md (removed by an earlier reset and left uncommitted; backup: Templates/skills/chair/references/z.md.00112233aabb.bak)",
		"- Templates/skills/chair/references/y.md (backup: Templates/skills/chair/references/y.md.0123456789ab.bak)",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.HasSuffix(msg, "\n") {
		t.Error("trailing newline")
	}

	// vp-shipped lines, and a commit with no backup at all: its preamble
	// names none.
	msg = templateResetCommitMessage("vp commands reset restart wrap", []resetCommitEntry{
		{Rel: "Templates/commands/restart.md", Mirror: true, Provenance: templates.ProvenanceEarlier},
		{Rel: "Templates/commands/wrap.md", Mirror: true, Provenance: templates.ProvenanceCurrent, LineEndings: true},
	}, "hostB")
	for _, want := range []string{
		"chore(templates): operator reset of 2 vault Templates/ file(s)",
		"named, on host hostB. The committed copy of each is in this commit's parent.",
		"held vp-shipped bytes",
		"- Templates/commands/restart.md (a copy of an earlier shipped version of commands/restart.md; no backup needed — recoverable from vibe-palace history)",
		"- Templates/commands/wrap.md (identical, line endings aside, to the built-in; no backup needed)",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "backup named beside it") {
		t.Errorf("a backup-less commit names a backup:\n%s", msg)
	}
}

// TestCommandsResetUnreadableVaultCopy: a vault copy that cannot be read (a
// directory where the file belongs) is an error, not an "unknown name".
func TestCommandsResetUnreadableVaultCopy(t *testing.T) {
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "Templates", "commands", "wrap.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, errOut, code := runReset(t, vault, "command", false, "wrap")
	if code != cli.ExitSystem || strings.Contains(errOut, "no built-in") || !strings.Contains(errOut, "Nothing was changed") {
		t.Errorf("exit %d\n%s", code, errOut)
	}
}
