// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/shims"
)

// boolPtr returns a *bool — used for InteractiveOverride.
func boolPtr(b bool) *bool { return &b }

// grokOff makes shims.GrokPresent report false for the duration of the test
// by pointing HOME and PATH at Grok-free temp dirs. The legacy upgrade tests
// here assert the Claude/Cursor shim surfaces only; without this they fail on
// any host that has the `grok` CLI on PATH or a `~/.grok/` directory, since
// GrokPresent is host-wide by design.
func grokOff(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
}

// TestRunCommandsUpgrade_DryRun_NonZeroOnPendingWork pins WHICH row is the
// pending work. It is not the templates: with no vault copy, every embedded
// command is "unneeded" — override-only, the embedded floor serves it, and
// upgrade never writes one. What makes the dry-run exit ExitUser on an empty
// project is the shim half: the project has no .claude/commands/vpc-*.md yet.
// grokOff pins the host so a grok binary on PATH cannot add or remove rows.
func TestRunCommandsUpgrade_DryRun_NonZeroOnPendingWork(t *testing.T) {
	grokOff(t)
	vault := t.TempDir()
	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		DryRun:              true,
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: t.TempDir(),
	})
	if code != cli.ExitUser {
		t.Fatalf("dry-run with pending work: exit=%d, want ExitUser", code)
	}
	var templateRow, shimRow bool
	for _, line := range strings.Split(out.String(), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if f[0] == "unneeded" && f[1] == "wrap" {
			templateRow = true
		}
		if f[0] == "new" && strings.HasSuffix(f[1], filepath.Join(".claude", "commands", "vpc-wrap.md")) {
			shimRow = true
		}
		if f[0] == "new" && f[1] == "wrap" {
			t.Errorf("an absent vault template planned as new; override-only never writes one:\n%s", line)
		}
	}
	if !templateRow {
		t.Errorf("dry-run missing the 'unneeded  wrap' template row:\n%s", out.String())
	}
	if !shimRow {
		t.Errorf("dry-run missing the pending 'new …/vpc-wrap.md' shim row:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Summary (dry run):") {
		t.Errorf("dry-run missing summary line:\n%s", out.String())
	}
}

func TestRunCommandsUpgrade_DryRun_ZeroWhenClean(t *testing.T) {
	grokOff(t)
	vault := t.TempDir()
	seedMatchingVault(t, vault)
	projectRoot := t.TempDir()
	seedMatchingShims(t, vault, projectRoot)

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		DryRun:              true,
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
	})
	if code != cli.ExitOK {
		t.Fatalf("dry-run with clean vault: exit=%d, want ExitOK\nstderr: %s", code, errb.String())
	}
}

// bodyOnlyCommandOverride is the embedded command with a line appended at the
// end: its first paragraph — the shim's brief — is unchanged, so the override
// moves no shim and is, on its own, no pending work.
func bodyOnlyCommandOverride(t *testing.T, name string) string {
	t.Helper()
	emb, err := vpctx.NewResolver(t.TempDir()).EmbeddedContent("command:" + name)
	if err != nil {
		t.Fatal(err)
	}
	return emb + "\nAn operator's addition at the end of the body.\n"
}

// TestRunCommandsUpgrade_OverwriteKeepsOverrides is the must-fail of
// upgrade-overwrite-resets-vault-template-overrides: at 1f3bb62 `--overwrite`
// — the documented non-TTY path — replaced this override with the embedded
// bytes and kept no backup. Now it applies only vp-owned changes and lists the
// override as [keep], byte-for-byte untouched.
func TestRunCommandsUpgrade_OverwriteKeepsOverrides(t *testing.T) {
	vault := t.TempDir()
	override := bodyOnlyCommandOverride(t, "restart")
	writeVaultFile(t, vault, "Templates/commands/restart.md", override)
	restart := filepath.Join(vault, "Templates", "commands", "restart.md")

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Overwrite:           true,
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: t.TempDir(),
	})
	if code != cli.ExitOK {
		t.Fatalf("overwrite: exit=%d\nstderr: %s", code, errb.String())
	}
	assertFileBytes(t, restart, override)
	assertNoBakUnder(t, vault)
	for _, want := range []string{
		"[keep] Templates/commands/restart.md — override of a built-in",
		"to remove it: vp commands reset restart",
		"1 override(s) of built-ins kept.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "[accept] restart") {
		t.Errorf("the override was accepted as a change:\n%s", out.String())
	}
	entries, err := os.ReadDir(filepath.Join(vault, "Templates", "commands"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "restart.md" {
		t.Errorf("Templates/commands/ = %v, want only restart.md", entries)
	}
}

// installShims runs one --overwrite pass so the project's shims are current.
func installShims(t *testing.T, vault, projectRoot string) {
	t.Helper()
	var out, errb bytes.Buffer
	if code := runCommandsUpgrade(commandsUpgradeOpts{
		Overwrite: true, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
		VaultRootOverride: vault, ProjectRootOverride: projectRoot,
	}); code != cli.ExitOK {
		t.Fatalf("install shims: exit=%d\n%s", code, errb.String())
	}
}

// TestRunCommandsUpgrade_NonTTYOverridesAreNotPendingWork: with the shims
// current, a body-only override is no work to do, so a non-TTY run without
// --overwrite reports it and exits 0. At 1f3bb62 it refused (exit 1) and
// pointed at --overwrite, which would have reset it.
func TestRunCommandsUpgrade_NonTTYOverridesAreNotPendingWork(t *testing.T) {
	grokOff(t)
	vault := t.TempDir()
	projectRoot := t.TempDir()
	installShims(t, vault, projectRoot)
	override := bodyOnlyCommandOverride(t, "restart")
	writeVaultFile(t, vault, "Templates/commands/restart.md", override)

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
		InteractiveOverride: boolPtr(false),
	})
	if code != cli.ExitOK {
		t.Fatalf("exit=%d, want 0\nstdout: %s\nstderr: %s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "[keep] Templates/commands/restart.md") || !strings.Contains(out.String(), "Nothing to do") {
		t.Errorf("stdout:\n%s", out.String())
	}
	assertFileBytes(t, filepath.Join(vault, "Templates", "commands", "restart.md"), override)
}

// TestRunCommandsUpgrade_DryRunOverrideIsNotPendingWork: the same seed, dry
// run — exit 0 with an override row.
func TestRunCommandsUpgrade_DryRunOverrideIsNotPendingWork(t *testing.T) {
	grokOff(t)
	vault := t.TempDir()
	projectRoot := t.TempDir()
	installShims(t, vault, projectRoot)
	writeVaultFile(t, vault, "Templates/commands/restart.md", bodyOnlyCommandOverride(t, "restart"))

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		DryRun:              true,
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
	})
	if code != cli.ExitOK {
		t.Fatalf("dry-run: exit=%d, want 0\n%s", code, out.String())
	}
	for _, want := range []string{
		"  override  restart  (vault ",
		"kept — vp commands reset restart removes it)",
		"Summary (dry run): 1 override(s) kept,",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run lacks %q:\n%s", want, out.String())
		}
	}
}

// TestRunCommandsUpgrade_OverrideThatChangesShimTextIsPendingShimWork pins
// the H2 behaviour, which is correct and not a regression: shims render from
// the resolved (vault) copy, so an override with a new brief or description
// makes the project's shim stale. That shim is pending vp-owned work; the
// override itself is still kept.
func TestRunCommandsUpgrade_OverrideThatChangesShimTextIsPendingShimWork(t *testing.T) {
	grokOff(t)
	t.Run("command-brief", func(t *testing.T) {
		vault := t.TempDir()
		projectRoot := t.TempDir()
		installShims(t, vault, projectRoot)
		const override = "An operator's own restart brief.\n\nDo it my way.\n"
		writeVaultFile(t, vault, "Templates/commands/restart.md", override)

		var out, errb bytes.Buffer
		code := runCommandsUpgrade(commandsUpgradeOpts{
			DryRun: true, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
			VaultRootOverride: vault, ProjectRootOverride: projectRoot,
		})
		if code != cli.ExitUser {
			t.Errorf("dry-run: exit=%d, want ExitUser (a stale shim is pending)", code)
		}
		for _, want := range []string{
			"shims: 0 new, 1 updated",
			"modified  " + filepath.Join(projectRoot, ".claude", "commands", "vpc-restart.md"),
			"override  restart",
		} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("dry run lacks %q:\n%s", want, out.String())
			}
		}

		out.Reset()
		if code := runCommandsUpgrade(commandsUpgradeOpts{
			Overwrite: true, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
			VaultRootOverride: vault, ProjectRootOverride: projectRoot,
		}); code != cli.ExitOK {
			t.Fatalf("overwrite: exit=%d\n%s", code, errb.String())
		}
		shim, err := os.ReadFile(filepath.Join(projectRoot, ".claude", "commands", "vpc-restart.md"))
		if err != nil || !strings.Contains(string(shim), "An operator's own restart brief.") {
			t.Errorf("the shim does not carry the override's brief (err=%v):\n%s", err, shim)
		}
		assertFileBytes(t, filepath.Join(vault, "Templates", "commands", "restart.md"), override)
	})
	t.Run("skill-description", func(t *testing.T) {
		vault := t.TempDir()
		projectRoot := t.TempDir()
		installShims(t, vault, projectRoot)
		const override = "---\nname: chair\ndescription: An operator's own chair.\n---\n\nMine.\n"
		writeVaultFile(t, vault, "Templates/skills/chair/SKILL.md", override)

		var out, errb bytes.Buffer
		code := runCommandsUpgrade(commandsUpgradeOpts{
			DryRun: true, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
			VaultRootOverride: vault, ProjectRootOverride: projectRoot,
		})
		if code != cli.ExitUser {
			t.Errorf("dry-run: exit=%d, want ExitUser (a stale skill shim is pending)", code)
		}
		if !strings.Contains(out.String(), "modified  "+claudeSkillPath(projectRoot, "chair")) {
			t.Errorf("the vps-chair shim is not modified:\n%s", out.String())
		}
		assertFileBytes(t, filepath.Join(vault, "Templates", "skills", "chair", "SKILL.md"), override)
	})
}

func TestRunCommandsUpgrade_NonInteractive_RefusesWithoutOverwrite(t *testing.T) {
	vault := t.TempDir() // no vault templates; the shim plan is what is actionable

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: t.TempDir(),
		InteractiveOverride: boolPtr(false),
	})
	if code != cli.ExitUser {
		t.Fatalf("non-interactive without --overwrite: exit=%d, want ExitUser\nstderr: %s",
			code, errb.String())
	}
	if !strings.Contains(errb.String(), "--overwrite to accept every shim and agent-file change (it never resets a Templates/ override)") {
		t.Errorf("expected guidance to set --overwrite, got:\n%s", errb.String())
	}
}

// TestRunCommandsUpgrade_DevNullStdinRefuses is the regression test for
// commands-upgrade-treats-dev-null-stdin-as-a-terminal: a char-device check
// (fi.Mode()&os.ModeCharDevice != 0) misdetects /dev/null — a character
// device — as a terminal, so `vp commands upgrade </dev/null` used to take
// the interactive branch, hit EOF on every prompt, silently skip every
// pending change, and exit 0. Deliberately uses a real os.Open(os.DevNull)
// file, NOT os.Pipe(): a pipe was never ModeCharDevice and was already
// correctly refused before this fix — only real /dev/null exercises the
// misdetection this test guards against. Also leaves InteractiveOverride
// nil so the real cli.IsTerminal(os.Stdin) check is exercised, not bypassed.
func TestRunCommandsUpgrade_DevNullStdinRefuses(t *testing.T) {
	vault := t.TempDir() // no vault templates; the shim plan is what is actionable

	oldStdin := os.Stdin
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	os.Stdin = devNull
	t.Cleanup(func() { os.Stdin = oldStdin; _ = devNull.Close() })

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: t.TempDir(),
	})
	if code != cli.ExitUser {
		t.Fatalf("/dev/null stdin without --overwrite: exit=%d, want ExitUser\nstdout: %s\nstderr: %s",
			code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "stdin is not a terminal and --overwrite was not set.") {
		t.Errorf("expected the non-terminal refusal, got:\n%s", errb.String())
	}
}

// TestRunCommandsUpgrade_Interactive_NeverPromptsForATemplate replaces the
// accept-one/skip-one template test. Interactively, no vault template is ever
// offered: the overrides are listed as [keep] and left byte-for-byte, and the
// answers typed go to the shim prompts, never to a template.
func TestRunCommandsUpgrade_Interactive_NeverPromptsForATemplate(t *testing.T) {
	grokOff(t)
	vault := t.TempDir()
	writeVaultFile(t, vault, "Templates/commands/restart.md", "stale restart\n")
	writeVaultFile(t, vault, "Templates/commands/wrap.md", "stale wrap\n")
	projectRoot := t.TempDir()

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Stdin:               strings.NewReader("A\n"),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
		InteractiveOverride: boolPtr(true),
	})
	if code != cli.ExitOK {
		t.Fatalf("interactive: exit=%d\nstderr: %s", code, errb.String())
	}
	for _, header := range []string{"=== restart (", "=== wrap ("} {
		if strings.Contains(out.String(), header) {
			t.Errorf("a vault template was prompted (%q):\n%s", header, out.String())
		}
	}
	for _, keep := range []string{"[keep] Templates/commands/restart.md", "[keep] Templates/commands/wrap.md"} {
		if !strings.Contains(out.String(), keep) {
			t.Errorf("no %q line:\n%s", keep, out.String())
		}
	}
	assertFileBytes(t, filepath.Join(vault, "Templates", "commands", "restart.md"), "stale restart\n")
	assertFileBytes(t, filepath.Join(vault, "Templates", "commands", "wrap.md"), "stale wrap\n")
	// The "A" went to the first shim prompt: every shim was accepted.
	if _, err := os.Stat(filepath.Join(projectRoot, shims.ShimDir, shims.Filename("wrap"))); err != nil {
		t.Errorf("the accept-all answer did not reach the shim prompts: %v", err)
	}
	if !strings.Contains(out.String(), "2 override(s) of built-ins kept.") {
		t.Errorf("no override count after Done:\n%s", out.String())
	}
}

// TestRunCommandsUpgrade_Only_ScopesToOneTemplate: --only still scopes the
// run, and the named command's override is kept, not reset.
func TestRunCommandsUpgrade_Only_ScopesToOneTemplate(t *testing.T) {
	vault := t.TempDir()
	writeVaultFile(t, vault, "Templates/commands/restart.md", "stale\n")

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Only:                "restart",
		Overwrite:           true,
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: t.TempDir(),
	})
	if code != cli.ExitOK {
		t.Fatalf("--only: exit=%d\nstderr: %s", code, errb.String())
	}
	assertFileBytes(t, filepath.Join(vault, "Templates", "commands", "restart.md"), "stale\n")
	if _, err := os.Stat(filepath.Join(vault, "Templates/commands/wrap.md")); !os.IsNotExist(err) {
		t.Errorf("--only leaked writes to wrap.md (err=%v)", err)
	}
}

func TestRunCommandsUpgrade_DryRun_ReportsShimDrift(t *testing.T) {
	// Clean vault so templates + blocks are a no-op; empty projectRoot so
	// every shim surfaces as "new". Dry-run must exit non-zero and mention
	// shim drift in the summary.
	vault := t.TempDir()
	seedMatchingVault(t, vault)

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		DryRun:              true,
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: t.TempDir(),
	})
	if code != cli.ExitUser {
		t.Fatalf("dry-run with shim drift: exit=%d, want ExitUser\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "Slash-command shims:") {
		t.Errorf("output missing shim plan header:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "shims:") {
		t.Errorf("summary missing shim counts:\n%s", out.String())
	}
}

func TestRunCommandsUpgrade_Overwrite_EmitsShims(t *testing.T) {
	vault := t.TempDir()
	seedMatchingVault(t, vault)
	projectRoot := t.TempDir()

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Overwrite:           true,
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
	})
	if code != cli.ExitOK {
		t.Fatalf("overwrite: exit=%d\nstderr: %s", code, errb.String())
	}
	// At least one vpc-*.md file should now exist under the project's shim dir.
	entries, err := os.ReadDir(filepath.Join(projectRoot, shims.ShimDir))
	if err != nil {
		t.Fatalf("read shim dir: %v", err)
	}
	var vpcCount int
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), shims.FilePrefix) && strings.HasSuffix(e.Name(), ".md") {
			vpcCount++
		}
	}
	if vpcCount == 0 {
		t.Errorf("--overwrite did not emit shims into %s", projectRoot)
	}
	if !strings.Contains(out.String(), "Shims:") {
		t.Errorf("Done line missing shim counts:\n%s", out.String())
	}
}

func TestRunCommandsUpgrade_Only_FiltersShims(t *testing.T) {
	vault := t.TempDir()
	seedMatchingVault(t, vault)
	projectRoot := t.TempDir()

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Only:                "restart",
		Overwrite:           true,
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
	})
	if code != cli.ExitOK {
		t.Fatalf("--only: exit=%d\nstderr: %s", code, errb.String())
	}
	// Only vpc-restart.md should have been written.
	restart := filepath.Join(projectRoot, shims.ShimDir, shims.Filename("restart"))
	if _, err := os.Stat(restart); err != nil {
		t.Errorf("--only restart did not emit %s: %v", restart, err)
	}
	wrap := filepath.Join(projectRoot, shims.ShimDir, shims.Filename("wrap"))
	if _, err := os.Stat(wrap); !os.IsNotExist(err) {
		t.Errorf("--only restart leaked write to %s (err=%v)", wrap, err)
	}
}

func TestRunCommandsUpgrade_PromptsToRemoveStaleShim(t *testing.T) {
	vault := t.TempDir()
	seedMatchingVault(t, vault)
	projectRoot := t.TempDir()
	seedMatchingShims(t, vault, projectRoot)

	// Drop a well-formed stale shim for a command that does not exist.
	orphan := filepath.Join(projectRoot, shims.ShimDir, shims.Filename("ghost"))
	body := shims.Render("ghost", "A ghost from an older vibe-palace.", "", "")
	if err := os.WriteFile(orphan, []byte(body), 0o644); err != nil {
		t.Fatalf("write ghost shim: %v", err)
	}

	// Accept-all short-circuits per-prompt input; the stale entry should be
	// deleted and the Done line should report one removal.
	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Overwrite:           true,
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
	})
	if code != cli.ExitOK {
		t.Fatalf("stale removal: exit=%d\nstderr: %s", code, errb.String())
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("stale shim not removed (err=%v)", err)
	}
	if !strings.Contains(out.String(), "1 removed") {
		t.Errorf("Done line missing '1 removed':\n%s", out.String())
	}
}

// claudeSkillPath returns the .claude/skills/vps-<name>/SKILL.md path for a
// project root.
func claudeSkillPath(projectRoot, name string) string {
	return shims.TargetFile(shims.ClaudeSkill, projectRoot, name)
}

// seedStaleSkillShim writes a marker-bearing ClaudeSkill shim for a skill
// name that the embedded corpus does not provide, so an upgrade run sees it
// as Stale and (with AllowStaleRemoval) removes it.
func seedStaleSkillShim(t *testing.T, projectRoot, name string) string {
	t.Helper()
	item := shims.SkillItem{Name: name}
	p := claudeSkillPath(projectRoot, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir stale skill dir: %v", err)
	}
	if err := os.WriteFile(p, []byte(shims.RenderSkill(shims.ClaudeSkill, item)), 0o644); err != nil {
		t.Fatalf("write stale skill shim: %v", err)
	}
	return p
}

// TestRunCommandsUpgrade_RefreshesMissingSkillShims verifies that an upgrade
// in a project with no skill shims emits them (the upgrade path now mirrors
// vp init's skill-shim wiring).
func TestRunCommandsUpgrade_RefreshesMissingSkillShims(t *testing.T) {
	grokOff(t)
	vault := t.TempDir()
	seedMatchingVault(t, vault)
	projectRoot := t.TempDir()
	// Seed matching command shims so only skill shims are pending.
	seedMatchingShims(t, vault, projectRoot)
	// Remove the skill shim seeded above to force a "missing" state.
	skillPath := claudeSkillPath(projectRoot, "startup-analyst")
	if err := os.RemoveAll(filepath.Dir(skillPath)); err != nil {
		t.Fatalf("rm skill dir: %v", err)
	}

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Overwrite:           true,
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
	})
	if code != cli.ExitOK {
		t.Fatalf("overwrite: exit=%d\nstderr: %s", code, errb.String())
	}
	if _, err := os.Stat(skillPath); err != nil {
		t.Errorf("skill shim not emitted at %s: %v", skillPath, err)
	}
	scan, err := shims.ScanShim(skillPath)
	if err != nil {
		t.Fatalf("scan skill shim: %v", err)
	}
	if !scan.HasMarker {
		t.Errorf("emitted skill shim lacks managed marker")
	}
	if !strings.Contains(out.String(), "Skill shims: 1 added") {
		t.Errorf("Done line missing skill-shim add count:\n%s", out.String())
	}
}

// TestRunCommandsUpgrade_RemovesStaleSkillShim verifies that a skill removed
// from the corpus has its shim cleaned up on upgrade (AllowStaleRemoval).
func TestRunCommandsUpgrade_RemovesStaleSkillShim(t *testing.T) {
	grokOff(t)
	vault := t.TempDir()
	seedMatchingVault(t, vault)
	projectRoot := t.TempDir()
	seedMatchingShims(t, vault, projectRoot)
	stale := seedStaleSkillShim(t, projectRoot, "retired")

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Overwrite:           true,
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
	})
	if code != cli.ExitOK {
		t.Fatalf("overwrite: exit=%d\nstderr: %s", code, errb.String())
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale skill shim not removed (err=%v)", err)
	}
	// The parent vps-retired/ dir should be pruned too.
	if _, err := os.Stat(filepath.Dir(stale)); !os.IsNotExist(err) {
		t.Errorf("stale skill dir not pruned (err=%v)", err)
	}
	if !strings.Contains(out.String(), "Skill shims: 0 added, 0 updated, 1 removed") {
		t.Errorf("Done line missing skill-shim removal count:\n%s", out.String())
	}
}

// TestRunCommandsUpgrade_SkillShimsIdempotent verifies that re-running the
// upgrade against an already-current project writes no skill shims.
func TestRunCommandsUpgrade_SkillShimsIdempotent(t *testing.T) {
	grokOff(t)
	vault := t.TempDir()
	seedMatchingVault(t, vault)
	projectRoot := t.TempDir()
	seedMatchingShims(t, vault, projectRoot) // seeds command + skill shims

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Overwrite:           true,
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
	})
	if code != cli.ExitOK {
		t.Fatalf("overwrite: exit=%d\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "Skill shims: 0 added, 0 updated, 0 removed") {
		t.Errorf("idempotent run wrote skill shims:\n%s", out.String())
	}
}

// TestRunCommandsUpgrade_DryRunShowsSkillShimsNoWrite verifies the dry-run
// surfaces skill-shim drift in the plan and summary but writes nothing.
func TestRunCommandsUpgrade_DryRunShowsSkillShimsNoWrite(t *testing.T) {
	vault := t.TempDir()
	seedMatchingVault(t, vault)
	projectRoot := t.TempDir()
	seedMatchingShims(t, vault, projectRoot)
	// Force a missing skill shim (New) plus a stale one (Stale) so both
	// printSkillShimPlan branches and the summary counters are exercised.
	skillPath := claudeSkillPath(projectRoot, "startup-analyst")
	if err := os.RemoveAll(filepath.Dir(skillPath)); err != nil {
		t.Fatalf("rm skill dir: %v", err)
	}
	stale := seedStaleSkillShim(t, projectRoot, "retired")

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		DryRun:              true,
		Stdin:               strings.NewReader(""),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
	})
	if code != cli.ExitUser {
		t.Fatalf("dry-run with skill drift: exit=%d, want ExitUser\nstderr: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "Skill shims:") {
		t.Errorf("dry-run missing skill-shim plan header:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "skill shims:") {
		t.Errorf("dry-run summary missing skill-shim counts:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "stale") {
		t.Errorf("dry-run missing stale skill-shim line:\n%s", out.String())
	}
	if _, err := os.Stat(skillPath); !os.IsNotExist(err) {
		t.Errorf("dry-run wrote a skill shim at %s", skillPath)
	}
	// Dry-run must not remove the stale shim either.
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("dry-run removed stale skill shim %s: %v", stale, err)
	}
}

// TestRunCommandsUpgrade_Interactive_SkillShimPrompts drives the per-entry
// skill-shim prompt loop: a New skill shim is accepted and a Stale one is
// accepted for removal. Templates/command-shims are pre-seeded current so the
// only prompts are skill-shim prompts (EOF after our inputs skips any others).
func TestRunCommandsUpgrade_Interactive_SkillShimPrompts(t *testing.T) {
	grokOff(t)
	vault := t.TempDir()
	seedMatchingVault(t, vault)
	projectRoot := t.TempDir()
	seedMatchingShims(t, vault, projectRoot)
	skillPath := claudeSkillPath(projectRoot, "startup-analyst")
	if err := os.RemoveAll(filepath.Dir(skillPath)); err != nil {
		t.Fatalf("rm skill dir: %v", err)
	}
	stale := seedStaleSkillShim(t, projectRoot, "retired")

	// Skill plan order is New (startup-analyst) then Stale (retired). Accept
	// both; any trailing prompts EOF-skip.
	input := "a\na\n"
	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Stdin:               strings.NewReader(input),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
		InteractiveOverride: boolPtr(true),
	})
	if code != cli.ExitOK {
		t.Fatalf("interactive: exit=%d\nstderr: %s", code, errb.String())
	}
	if _, err := os.Stat(skillPath); err != nil {
		t.Errorf("accepted New skill shim not written: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("accepted Stale skill shim not removed (err=%v)", err)
	}
	if !strings.Contains(out.String(), "skill-shim claude-skill startup-analyst (new)") {
		t.Errorf("interactive output missing skill-shim New prompt:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Skill shims: 1 added, 0 updated, 1 removed") {
		t.Errorf("Done line wrong skill-shim counts:\n%s", out.String())
	}
	// Grok off in this test; grok shims count should be zero.
	if !strings.Contains(out.String(), "Grok shims: 0 added, 0 updated, 0 removed, 0 custom") {
		t.Errorf("Done line should report zero grok shims when grokOff:\n%s", out.String())
	}
}

// TestRunCommandsUpgrade_GrokShimsSurviveSkillPhaseQuit is the H1 regression
// guard: a Grok command shim accepted in the Grok prompt loop must still be
// applied even when the user quits during the later skill-shim phase. The bug
// passed nil for acceptedGrokShims on the skill-phase "q" abort, silently
// dropping the user's accepted Grok shims.
func TestRunCommandsUpgrade_GrokShimsSurviveSkillPhaseQuit(t *testing.T) {
	vault := t.TempDir()
	seedMatchingVault(t, vault)
	projectRoot := t.TempDir()
	seedMatchingShims(t, vault, projectRoot)
	seedMatchingGrokShims(t, vault, projectRoot) // also creates .grok/ → GrokPresent

	// Force exactly one Grok command shim to be New (the only Grok prompt).
	grokRestart := filepath.Join(projectRoot, shims.GrokCommandsPluginDir, shims.Filename("restart"))
	if err := os.Remove(grokRestart); err != nil {
		t.Fatalf("rm grok restart shim: %v", err)
	}
	// Force exactly one skill prompt (startup-analyst New) to quit at.
	skillPath := claudeSkillPath(projectRoot, "startup-analyst")
	if err := os.RemoveAll(filepath.Dir(skillPath)); err != nil {
		t.Fatalf("rm skill dir: %v", err)
	}

	// Accept the Grok New shim, then quit at the skill prompt.
	input := "a\nq\n"
	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Stdin:               strings.NewReader(input),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
		InteractiveOverride: boolPtr(true),
	})
	if code != cli.ExitOK {
		t.Fatalf("interactive: exit=%d\nstderr: %s", code, errb.String())
	}
	if _, err := os.Stat(grokRestart); err != nil {
		t.Errorf("accepted Grok shim dropped on skill-phase quit (H1 regression): %v\n%s",
			err, out.String())
	}
	// Sanity: we did quit before accepting the skill shim, so it stays absent.
	if _, err := os.Stat(skillPath); !os.IsNotExist(err) {
		t.Errorf("skill shim should not exist after quitting at its prompt (err=%v)", err)
	}
}

// TestRunCommandsUpgrade_CursorSkillShimsGatedOnCursorPresence verifies the
// CursorRule skill shim is only touched when a .cursor layout is present.
func TestRunCommandsUpgrade_CursorSkillShimsGatedOnCursorPresence(t *testing.T) {
	// Without .cursor: no .cursor/rules tree is created.
	t.Run("absent", func(t *testing.T) {
		vault := t.TempDir()
		seedMatchingVault(t, vault)
		projectRoot := t.TempDir()

		var out, errb bytes.Buffer
		code := runCommandsUpgrade(commandsUpgradeOpts{
			Overwrite:           true,
			Stdin:               strings.NewReader(""),
			Stdout:              &out,
			Stderr:              &errb,
			VaultRootOverride:   vault,
			ProjectRootOverride: projectRoot,
		})
		if code != cli.ExitOK {
			t.Fatalf("overwrite: exit=%d\nstderr: %s", code, errb.String())
		}
		if _, err := os.Stat(filepath.Join(projectRoot, shims.CursorRulesDir)); !os.IsNotExist(err) {
			t.Errorf(".cursor/rules created without a Cursor layout (err=%v)", err)
		}
	})

	// With .cursor/ present: the .mdc skill shim is emitted.
	t.Run("present", func(t *testing.T) {
		vault := t.TempDir()
		seedMatchingVault(t, vault)
		projectRoot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(projectRoot, ".cursor"), 0o755); err != nil {
			t.Fatalf("mkdir .cursor: %v", err)
		}

		var out, errb bytes.Buffer
		code := runCommandsUpgrade(commandsUpgradeOpts{
			Overwrite:           true,
			Stdin:               strings.NewReader(""),
			Stdout:              &out,
			Stderr:              &errb,
			VaultRootOverride:   vault,
			ProjectRootOverride: projectRoot,
		})
		if code != cli.ExitOK {
			t.Fatalf("overwrite: exit=%d\nstderr: %s", code, errb.String())
		}
		mdc := shims.TargetFile(shims.CursorRule, projectRoot, "startup-analyst")
		if _, err := os.Stat(mdc); err != nil {
			t.Errorf("cursor skill shim not emitted at %s: %v", mdc, err)
		}
	})
}

func TestListFormatsJSON(t *testing.T) {
	// Drive List directly — we can't easily invoke the CLI without a vault,
	// but List is the layer tested end-to-end everywhere else.
	r := vpctx.NewResolver(t.TempDir())
	summaries, err := commands.List(r, "command", "", "", "", 60)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) == 0 {
		t.Fatal("expected embedded commands")
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(summaries); err != nil {
		t.Fatalf("encode: %v", err)
	}

	var decoded []commands.Summary
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded) != len(summaries) {
		t.Fatalf("round-trip length mismatch: %d vs %d", len(decoded), len(summaries))
	}
}

func writeVaultFile(t *testing.T, vault, rel, content string) {
	t.Helper()
	p := filepath.Join(vault, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// seedMatchingShims emits the full shim set into projectRoot so that a
// subsequent plan sees no New/Modified/Stale entries. Used by tests that
// assert the "nothing to do" exit path.
// TestPlanShims_IncludesProjectScopedCommand exercises the Phase 1+2 wiring:
// planShims detects the project slug from projectRoot's .vibe-palace.toml,
// resolves the project-tier command, and produces a managed shim plan whose
// rendered body carries the project="<slug>" param and the command's argument
// hint lifted from frontmatter.
func TestPlanShims_IncludesProjectScopedCommand(t *testing.T) {
	vault := t.TempDir()
	dir := filepath.Join(vault, "Projects", "demoproj", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "foo.md"),
		[]byte("---\nargument-hint: [bar]\n---\n\nDo foo.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, ".vibe-palace.toml"),
		[]byte("[project]\nname = \"demoproj\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := vpctx.NewResolver(vault)
	plan, err := planShims(r, projectRoot, "")
	if err != nil {
		t.Fatalf("planShims: %v", err)
	}
	var foo *shims.Change
	for i := range plan {
		if plan[i].Name == "foo" {
			foo = &plan[i]
		}
	}
	if foo == nil {
		t.Fatalf("project command foo absent from shim plan: %+v", plan)
	}
	if foo.Kind != shims.New {
		t.Errorf("foo Kind = %s, want New", foo.Kind)
	}
	if foo.Project != "demoproj" {
		t.Errorf("foo Project = %q, want demoproj", foo.Project)
	}
	if foo.ArgHint != "[bar]" {
		t.Errorf("foo ArgHint = %q, want [bar]", foo.ArgHint)
	}
	body := shims.Render(foo.Name, foo.Brief, foo.Project, foo.ArgHint)
	if !strings.Contains(body, `project="demoproj"`) {
		t.Errorf("rendered shim missing project param:\n%s", body)
	}
	if !strings.Contains(body, "argument-hint: [bar]") {
		t.Errorf("rendered shim missing specific argument-hint:\n%s", body)
	}
}

func seedMatchingShims(t *testing.T, vault, projectRoot string) {
	t.Helper()
	r := vpctx.NewResolver(vault)
	// Mirror planShims: resolve with the detected project slug so seeded shims
	// carry the same (project-interpolated) briefs production expects.
	slug, _ := project.DetectProject(projectRoot)
	summaries, err := commands.List(r, "command", slug, "", "", 60)
	if err != nil {
		t.Fatalf("list summaries: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, shims.ShimDir), 0o755); err != nil {
		t.Fatalf("mkdir shim dir: %v", err)
	}
	plan, err := shims.Plan(summaries, projectRoot)
	if err != nil {
		t.Fatalf("shim plan: %v", err)
	}
	if _, err := shims.Apply(plan, shims.ApplyOptions{}); err != nil {
		t.Fatalf("shim apply: %v", err)
	}

	// Skill shims ride the same upgrade path now, so a "clean" project must
	// also carry up-to-date skill shims. Seed every detected skill target
	// (ClaudeSkill always; CursorRule when present).
	items, err := skillShimItems(r)
	if err != nil {
		t.Fatalf("skill items: %v", err)
	}
	targets := []shims.TargetKind{shims.ClaudeSkill}
	if shims.CursorPresent(projectRoot) {
		targets = append(targets, shims.CursorRule)
	}
	for _, target := range targets {
		sp, err := shims.PlanSkills(target, items, projectRoot)
		if err != nil {
			t.Fatalf("skill shim plan: %v", err)
		}
		if _, _, err := shims.ApplySkills(sp, shims.ApplyOptions{}); err != nil {
			t.Fatalf("skill shim apply: %v", err)
		}
	}
}

// seedMatchingGrokShims writes up-to-date Grok command shims under
// .grok/plugins/vibe-palace/commands/ (mirrors seedMatchingShims for the Grok
// plugin path). The Apply also creates .grok/, so GrokPresent reports true
// afterward regardless of host state.
func seedMatchingGrokShims(t *testing.T, vault, projectRoot string) {
	t.Helper()
	r := vpctx.NewResolver(vault)
	slug, _ := project.DetectProject(projectRoot)
	summaries, err := commands.List(r, "command", slug, "", "", 60)
	if err != nil {
		t.Fatalf("list summaries: %v", err)
	}
	plan, err := shims.PlanGrokCommands(summaries, projectRoot)
	if err != nil {
		t.Fatalf("grok shim plan: %v", err)
	}
	if _, err := shims.Apply(plan, shims.ApplyOptions{}); err != nil {
		t.Fatalf("grok shim apply: %v", err)
	}
}

func seedMatchingVault(t *testing.T, vault string) {
	t.Helper()
	r := vpctx.NewResolver(vault)
	names, err := r.ListEmbedded("command")
	if err != nil {
		t.Fatalf("list embedded: %v", err)
	}
	for _, n := range names {
		content, err := r.EmbeddedContent("command:" + n)
		if err != nil {
			t.Fatalf("embedded %s: %v", n, err)
		}
		writeVaultFile(t, vault, filepath.Join("Templates/commands", n+".md"), content)
	}
}
