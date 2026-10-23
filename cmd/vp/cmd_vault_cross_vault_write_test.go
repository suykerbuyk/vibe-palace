// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// The CLI half of the cross-vault write refusal (U3) — the thing U2's MCP-only
// dispatch gate cannot guarantee. `vp vault write` goes through vaultfs.Write →
// atomicfile.writeAtomic → the injected cross-vault guard, so a CLI write for a
// project bound to another vault is refused at the shared funnel. (U1+U2 land the
// seam + MCP gate; U3 adds this funnel guard.)
func TestVaultCLIRefusesCrossVaultWrite(t *testing.T) {
	vaultA := setupTestVaultEnv(t) // configured vault_path (the write target W)

	// vaultB: a real vault, to bind project p elsewhere.
	home := t.TempDir()
	vaultB := filepath.Join(home, "vault-b")
	if err := os.MkdirAll(filepath.Join(vaultB, ".vibe-palace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultB, ".vibe-palace", "vault.toml"),
		[]byte(fmt.Sprintf("format = %d\n", surface.RequiredDataFormat)), 0o644); err != nil {
		t.Fatal(err)
	}

	// Rewrite the global config: vault_path = A, and bind p -> B.
	cfg, err := storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg,
		[]byte("vault_path = \""+vaultA+"\"\n\n[project_vaults]\np = \""+vaultB+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate the real binary: main() arms the guard, preRun builds+installs the
	// snapshot before the command runs (this test calls Run directly, bypassing
	// both, so do them here).
	t.Cleanup(storage.ArmProjectVaultGuard())
	installProjectVaultSnapshot(&cli.Command{Name: "vault write", MutatesVault: true})

	abs := filepath.Join(vaultA, "Projects", "p", "resume.md")
	var code int
	stderr := captureStderr(t, func() {
		code = cmdVaultWrite().Run([]string{"Projects/p/resume.md", "--content", "mislocated"})
	})
	if code == cli.ExitOK {
		t.Fatalf("vault write into a cross-vault project: exit 0, want a refusal (stderr=%q)", stderr)
	}
	if !strings.Contains(stderr, vaultB) || !strings.Contains(stderr, "p") {
		t.Fatalf("refusal must name the bound vault and project, got %q", stderr)
	}
	if _, serr := os.Lstat(abs); serr == nil {
		t.Fatal("vault write created the file in the wrong vault despite the refusal")
	}
}
