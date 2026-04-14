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
// pass against a vault whose entire commands corpus is "new" (no
// existing vault copies). The golden file lives at
// testdata/commands_upgrade_prompt.golden and is regenerated via
// --update-golden.
//
// The commands path of `vp commands upgrade` must remain byte-identical
// across refactors; that's why a golden is the only defensible
// assertion — the refactor in Phase 5 of vps-skill-artifacts-cross-ide
// threads the same prompt sequence through the shared runUpgradePrompt
// helper, and any drift is caught here.
func TestRunCommandsUpgrade_InteractiveGolden(t *testing.T) {
	vault := t.TempDir()
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
		ProjectRootOverride: t.TempDir(),
		InteractiveOverride: boolPtr(true),
	})
	_ = code
	// Normalize: the only non-deterministic text is the stale-shim
	// warning for non-existent .git (none) and the random tmpdir paths
	// — but those don't appear in interactive output (header/diff only).
	// We DO NOT normalize bytes; we compare raw.
	goldenPath := filepath.Join("testdata", "commands_upgrade_prompt.golden")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(goldenPath, out.Bytes(), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote golden %s (%d bytes)", goldenPath, out.Len())
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (re-run with -update-golden to create): %v", err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Errorf("commands-upgrade prompt sequence drifted from golden.\n--- got ---\n%s\n--- want ---\n%s",
			out.String(), string(want))
	}
}
