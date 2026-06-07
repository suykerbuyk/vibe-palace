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

func TestRunCommandsUpgrade_DryRun_NonZeroOnPendingWork(t *testing.T) {
	vault := t.TempDir()
	// No vault copies exist → every embedded command is "new".
	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		DryRun:            true,
		Stdin:             strings.NewReader(""),
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: t.TempDir(),
	})
	if code != cli.ExitUser {
		t.Fatalf("dry-run with pending work: exit=%d, want ExitUser", code)
	}
	if !strings.Contains(out.String(), "new") {
		t.Errorf("dry-run output missing 'new' entries:\n%s", out.String())
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
		DryRun:            true,
		Stdin:             strings.NewReader(""),
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
	})
	if code != cli.ExitOK {
		t.Fatalf("dry-run with clean vault: exit=%d, want ExitOK\nstderr: %s", code, errb.String())
	}
}

func TestRunCommandsUpgrade_Overwrite_AppliesAll(t *testing.T) {
	vault := t.TempDir()
	// Pre-seed one divergent copy so there's a guaranteed updated entry.
	writeVaultFile(t, vault, "Templates/commands/restart.md", "stale user content\n")

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Overwrite:         true,
		Stdin:             strings.NewReader(""),
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: t.TempDir(),
	})
	if code != cli.ExitOK {
		t.Fatalf("overwrite: exit=%d\nstderr: %s", code, errb.String())
	}

	// After apply, every embedded command has a matching vault copy.
	r := vpctx.NewResolver(vault)
	plan, err := commands.Plan(r, commands.PlanOptions{})
	if err != nil {
		t.Fatalf("re-plan: %v", err)
	}
	for _, c := range plan {
		if c.Kind != commands.ChangeUnchanged {
			t.Errorf("%s: kind=%q after overwrite, want unchanged", c.Name, c.Kind)
		}
	}
}

func TestRunCommandsUpgrade_NonInteractive_RefusesWithoutOverwrite(t *testing.T) {
	vault := t.TempDir() // non-empty plan (everything new)

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
	if !strings.Contains(errb.String(), "--overwrite") {
		t.Errorf("expected guidance to set --overwrite, got:\n%s", errb.String())
	}
}

func TestRunCommandsUpgrade_Interactive_AcceptOne_SkipOne(t *testing.T) {
	vault := t.TempDir()
	writeVaultFile(t, vault, "Templates/commands/restart.md", "stale restart\n")
	writeVaultFile(t, vault, "Templates/commands/wrap.md", "stale wrap\n")

	// Plan order is alphabetical: cancel-plan, capture, execute-plan, license,
	// makefile, restart, review-plan, wrap. Skip positions 0-4 (cancel-plan,
	// capture, execute-plan, license, makefile — all new); accept restart (5);
	// skip review-plan (6); skip wrap (7, already staged but stale — the test
	// asserts it's NOT updated).
	input := strings.Join([]string{"s", "s", "s", "s", "s", "a", "s", "s"}, "\n") + "\n"

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Stdin:               strings.NewReader(input),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: t.TempDir(),
		InteractiveOverride: boolPtr(true),
	})
	if code != cli.ExitOK {
		t.Fatalf("interactive: exit=%d\nstderr: %s", code, errb.String())
	}

	// restart should now match embedded; wrap should still be "stale wrap\n".
	r := vpctx.NewResolver(vault)
	embedded, _ := r.EmbeddedContent("command:restart")
	got, err := os.ReadFile(filepath.Join(vault, "Templates/commands/restart.md"))
	if err != nil {
		t.Fatalf("read restart: %v", err)
	}
	if string(got) != embedded {
		t.Errorf("restart content did not update:\n%s", got)
	}
	wrap, _ := os.ReadFile(filepath.Join(vault, "Templates/commands/wrap.md"))
	if string(wrap) != "stale wrap\n" {
		t.Errorf("wrap should not have changed, got:\n%s", wrap)
	}
}

func TestRunCommandsUpgrade_Only_ScopesToOneTemplate(t *testing.T) {
	vault := t.TempDir()
	writeVaultFile(t, vault, "Templates/commands/restart.md", "stale\n")

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Only:              "restart",
		Overwrite:         true,
		Stdin:             strings.NewReader(""),
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: t.TempDir(),
	})
	if code != cli.ExitOK {
		t.Fatalf("--only: exit=%d\nstderr: %s", code, errb.String())
	}

	// restart updated; other commands still absent.
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
	body := shims.Render("ghost", "A ghost from an older vibe-palace.")
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
func seedMatchingShims(t *testing.T, vault, projectRoot string) {
	t.Helper()
	r := vpctx.NewResolver(vault)
	summaries, err := commands.List(r, "command", "", "", "", 60)
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
