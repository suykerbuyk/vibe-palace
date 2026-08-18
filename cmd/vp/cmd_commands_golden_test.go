// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateGolden, when true, rewrites golden files from captured output.
// Run `go test ./cmd/vp -run TestRunCommandsUpgrade_InteractiveGolden -update-golden`.
var updateGolden = flag.Bool("update-golden", false,
	"rewrite *.golden fixtures from captured output (commands-upgrade golden tests)")

// TestRunCommandsUpgrade_InteractiveGolden pins the prompt sequence
// emitted by `vp commands upgrade` during an interactive accept-all
// pass against an empty vault. The golden file lives at
// testdata/commands_upgrade_prompt.golden and is regenerated via
// --update-golden.
//
// The commands path of `vp commands upgrade` must remain byte-identical
// across refactors; that's why a golden is the only defensible
// assertion — the refactor in Phase 5 of vps-skill-artifacts-cross-ide
// threads the same prompt sequence through the shared runUpgradePrompt
// helper, and any drift is caught here.
//
// The golden is ALSO the operator-facing record of the override-only fix.
// It used to open with 15 "(new)" vault-template prompts — byte-identical
// mirrors of the embedded corpus — before reaching the first shim, which is
// what made accept-all the natural keystroke. It now opens on the shim the
// command was run for, and no vault template is offered at all.
func TestRunCommandsUpgrade_InteractiveGolden(t *testing.T) {
	grokOff(t)
	vault := t.TempDir()
	projectRoot := t.TempDir()
	// "A\n" → accept-all on the first prompt; remainder of the output
	// is deterministic (no diff variance, no block/shim surfaces since
	// projectRoot is empty = everything new for shims too, but all get
	// the same [accept] style line once accept-all flips).
	var out, errb bytes.Buffer
	code := runCommandsUpgrade(commandsUpgradeOpts{
		Stdin:               strings.NewReader("A\n"),
		Stdout:              &out,
		Stderr:              &errb,
		VaultRootOverride:   vault,
		ProjectRootOverride: projectRoot,
		InteractiveOverride: boolPtr(true),
	})
	_ = code
	// Normalize the two t.TempDir() roots and nothing else.
	//
	// This used to say the random tmpdir paths "don't appear in interactive
	// output (header/diff only)" and compared raw bytes. That was true only
	// while the first prompt was a vault template, whose header prints a byte
	// count and no path. Now that no vault template is offered, the first
	// prompt is a shim — and a shim header names its absolute target, so the
	// raw comparison became non-deterministic across runs.
	got := strings.ReplaceAll(out.String(), projectRoot, "<PROJECT_ROOT>")
	got = strings.ReplaceAll(got, vault, "<VAULT_ROOT>")
	goldenPath := filepath.Join("testdata", "commands_upgrade_prompt.golden")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote golden %s (%d bytes)", goldenPath, len(got))
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (re-run with -update-golden to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("commands-upgrade prompt sequence drifted from golden.\n--- got ---\n%s\n--- want ---\n%s",
			got, string(want))
	}

	// The golden must not offer a single vault template: every entry is a
	// shim. Asserted here as well as by the bytes so the intent survives a
	// careless -update-golden.
	if strings.Contains(got, "=== cancel-plan (new) ===") {
		t.Error("a byte-identical vault mirror was offered for creation")
	}
}
