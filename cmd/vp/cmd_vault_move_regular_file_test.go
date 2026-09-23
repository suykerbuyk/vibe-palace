// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

// The CLI half of Move's regular-file rule. The rule lives in vaultfs and is
// unit-tested there (move_regular_file_test.go); this pins that `vp vault move`
// still reaches it, so a refactor that gave the CLI its own path goes red. The
// MCP half is TestVaultMoveRefusesADirectory in internal/tools.
func TestVaultCLIMoveRefusesADirectory(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	seedVaultFile(t, vaultDir, "Projects/a/sessions/s.md", "s\n")

	var code int
	stderr := captureStderr(t, func() { code = cmdVaultMove().Run([]string{"Projects/a", "Projects/b"}) })

	if code != cli.ExitSystem {
		t.Errorf("exit code = %d, want %d (refusal)", code, cli.ExitSystem)
	}
	for _, want := range []string{"is a directory", "vp_manage_task action=move", "vp migrate project-slug"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr must say %q, got %q", want, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "Projects", "a", "sessions", "s.md")); err != nil {
		t.Errorf("the source tree must be untouched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "Projects", "b")); err == nil {
		t.Error("Projects/b must not exist")
	}
}
