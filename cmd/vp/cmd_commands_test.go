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
)

// boolPtr returns a *bool — used for InteractiveOverride.
func boolPtr(b bool) *bool { return &b }

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
	vault := t.TempDir()
	seedMatchingVault(t, vault)

	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		DryRun:            true,
		Stdin:             strings.NewReader(""),
		Stdout:            &out,
		Stderr:            &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: t.TempDir(),
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

	// Plan order is alphabetical: cancel-plan, capture, restart, review-plan, wrap.
	// Skip the three "new" entries (cancel-plan, capture at positions 0,1;
	// review-plan at position 3). Accept restart (position 2), skip wrap (4).
	input := strings.Join([]string{"s", "s", "a", "s", "s"}, "\n") + "\n"

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
