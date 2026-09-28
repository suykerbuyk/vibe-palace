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

// The CLI half of the departed-project write refusal. `vp vault write|edit|move`
// and the MCP raw file tools are call sites of the same vaultfs functions; a
// gate asserted only through one surface would stay green if a refactor gave
// the other its own path. The MCP half is in
// internal/tools/vault_departed_write_test.go.

const cliDepartedTo = "git@gitlab.example.com:q/vibe-palace-vault.git"

func TestVaultCLIRefusesWritesIntoADepartedProject(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		run  func(args []string) int
	}{
		{"vault write", []string{"Projects/p/resume.md", "--content", "stale"},
			func(a []string) int { return cmdVaultWrite().Run(a) }},
		{"vault edit", []string{"Projects/p/resume.md.bak", "--old", "old", "--new", "new"},
			func(a []string) int { return cmdVaultEdit().Run(a) }},
		{"vault move into", []string{"Projects/keep/notes.md", "Projects/p/notes.md"},
			func(a []string) int { return cmdVaultMove().Run(a) }},
		{"vault move out", []string{"Projects/p/resume.md.bak", "Projects/keep/resume.md.bak"},
			func(a []string) int { return cmdVaultMove().Run(a) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vaultDir := setupTestVaultEnv(t)
			seedVaultFile(t, vaultDir, "Audits/departures/p.json",
				`{"format":"1","slug":"p","kind":"moved-to-vault","to":"`+cliDepartedTo+`","date":"2026-09-27"}`+"\n")
			bak := seedVaultFile(t, vaultDir, "Projects/p/resume.md.bak", "old\n")
			seedVaultFile(t, vaultDir, "Projects/keep/notes.md", "n\n")

			var code int
			stderr := captureStderr(t, func() { code = tc.run(tc.args) })
			if code == cli.ExitOK {
				t.Fatalf("vp %s: exit 0, want a refusal", tc.name)
			}
			if !strings.Contains(stderr, cliDepartedTo) || !strings.Contains(stderr, "vp config bind p") {
				t.Fatalf("vp %s: stderr must name where p went and the bind, got %q", tc.name, stderr)
			}
			if got, _ := os.ReadFile(bak); string(got) != "old\n" {
				t.Fatalf("vp %s changed the leftover: %q", tc.name, got)
			}
			for _, rel := range []string{"Projects/p/resume.md", "Projects/p/notes.md", "Projects/keep/resume.md.bak"} {
				if _, err := os.Lstat(filepath.Join(vaultDir, filepath.FromSlash(rel))); err == nil {
					t.Fatalf("vp %s created %s", tc.name, rel)
				}
			}
		})
	}
}
