// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// cvHost sets up a hermetic host with two real vaults — A (the server's startup
// vault) and B — and returns their roots plus the global config path. The
// caller writes the config body (vault_path + any [project_vaults]) and builds
// the snapshot with cvSnap.
func cvHost(t *testing.T) (vaultA, vaultB, cfg string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	vaultA = filepath.Join(home, "vault-a")
	vaultB = filepath.Join(home, "vault-b")
	manifest := fmt.Sprintf("format = %d\n", surface.RequiredDataFormat)
	spbWrite(t, filepath.Join(vaultA, ".vibe-palace", "vault.toml"), manifest)
	spbWrite(t, filepath.Join(vaultB, ".vibe-palace", "vault.toml"), manifest)
	c, err := storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	return vaultA, vaultB, c
}

// cvSnap writes the config body and builds the [project_vaults] snapshot from it.
func cvSnap(t *testing.T, cfg, body string) storage.ProjectVaultSnapshot {
	t.Helper()
	spbWrite(t, cfg, body)
	snap, err := storage.BuildProjectVaultSnapshot(context.Background())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	return snap
}

// cvRun registers projectTool on a fresh registry with snap installed and vault
// A (startupRoot) in context, dispatches params, and returns (handlerRan, err).
// It uses NewRegistry directly (not testRegistry) so the test controls the
// snapshot rather than the built-empty one testRegistry installs.
func cvRun(t *testing.T, startupRoot string, snap storage.ProjectVaultSnapshot, params string) (bool, error) {
	t.Helper()
	reg := NewRegistry(server.NewMCPServer("test", "0.1.0", server.WithToolCapabilities(true)))
	reg.SetProjectVaultSnapshot(snap)
	ran := false
	if err := reg.Register(projectTool(&ran)); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), vaultKey, storage.NewVault(startupRoot))
	_, err := reg.Dispatch(ctx, "vp_fake_project_writer", json.RawMessage(params))
	return ran, err
}

// A project bound to a vault OTHER than the startup vault: a mutating write is
// refused and the handler never runs.
func TestCrossVault_BoundElsewhere_Refuses(t *testing.T) {
	vaultA, vaultB, cfg := cvHost(t)
	snap := cvSnap(t, cfg, "vault_path = \""+vaultA+"\"\n\n[project_vaults]\nqa = \""+vaultB+"\"\n")

	ran, err := cvRun(t, vaultA, snap, `{"project":"qa"}`)
	if err == nil {
		t.Fatal("a cross-vault mutating write must be refused")
	}
	if !strings.Contains(err.Error(), "wrong vault") || !strings.Contains(err.Error(), vaultB) {
		t.Fatalf("refusal must name the wrong vault and the bound root: %v", err)
	}
	if ran {
		t.Fatal("the handler ran despite the cross-vault refusal")
	}
}

// A project bound to the startup vault itself is allowed.
func TestCrossVault_BoundHere_Allows(t *testing.T) {
	vaultA, _, cfg := cvHost(t)
	snap := cvSnap(t, cfg, "vault_path = \""+vaultA+"\"\n\n[project_vaults]\nqa = \""+vaultA+"\"\n")

	ran, err := cvRun(t, vaultA, snap, `{"project":"qa"}`)
	if err != nil {
		t.Fatalf("a same-vault write must be allowed: %v", err)
	}
	if !ran {
		t.Fatal("the handler should have run for a same-vault project")
	}
}

// A project with no [project_vaults] entry is unbound — allowed (the fail-safe's
// known coverage limit).
func TestCrossVault_Unbound_Allows(t *testing.T) {
	vaultA, _, cfg := cvHost(t)
	snap := cvSnap(t, cfg, "vault_path = \""+vaultA+"\"\n")

	ran, err := cvRun(t, vaultA, snap, `{"project":"brand-new"}`)
	if err != nil {
		t.Fatalf("an unbound project must be allowed: %v", err)
	}
	if !ran {
		t.Fatal("the handler should have run for an unbound project")
	}
}

// An UNBUILT snapshot fails a project-keyed mutating write CLOSED.
func TestCrossVault_UnbuiltSnapshot_FailsClosed(t *testing.T) {
	vaultA, _, _ := cvHost(t)
	var unbuilt storage.ProjectVaultSnapshot // zero value: built==false

	ran, err := cvRun(t, vaultA, unbuilt, `{"project":"anything"}`)
	if err == nil {
		t.Fatal("an unbuilt snapshot must fail a project-keyed mutating write closed")
	}
	if !strings.Contains(err.Error(), "routing snapshot") {
		t.Fatalf("refusal should name the unbuilt snapshot: %v", err)
	}
	if ran {
		t.Fatal("the handler ran despite the fail-closed unbuilt snapshot")
	}
}

// A READ-only invocation is UNAFFECTED even under an unbuilt snapshot: the gate
// returns before any refusal for a read.
func TestCrossVault_UnbuiltSnapshot_ReadUnaffected(t *testing.T) {
	vaultA, _, _ := cvHost(t)
	var unbuilt storage.ProjectVaultSnapshot

	ran, err := cvRun(t, vaultA, unbuilt, `{"project":"anything","mode":"read"}`)
	if err != nil {
		t.Fatalf("a read must pass under an unbuilt snapshot: %v", err)
	}
	if !ran {
		t.Fatal("the read should have reached the handler")
	}
}

// FLAG-2 scope lock: a mutating tool naming NEITHER project NOR to_project is
// ALLOWED even under an unbuilt snapshot (whole-vault tools / vp_config_bind
// cannot be misrouted). A later "refuse-all-on-Invalid" edit fails this test.
func TestCrossVault_UnbuiltSnapshot_NoProjectToolAllowed(t *testing.T) {
	vaultA, _, _ := cvHost(t)
	var unbuilt storage.ProjectVaultSnapshot

	ran, err := cvRun(t, vaultA, unbuilt, `{}`)
	if err != nil {
		t.Fatalf("a mutating tool with no project must be allowed even unbuilt: %v", err)
	}
	if !ran {
		t.Fatal("the no-project mutating tool should have run")
	}
}

// M1: a move whose to_project resolves to the wrong vault is refused.
func TestCrossVault_ToProjectMove_Refuses(t *testing.T) {
	vaultA, vaultB, cfg := cvHost(t)
	snap := cvSnap(t, cfg, "vault_path = \""+vaultA+"\"\n\n[project_vaults]\nqa = \""+vaultB+"\"\n")

	ran, err := cvRun(t, vaultA, snap, `{"to_project":"qa"}`)
	if err == nil {
		t.Fatal("a move into a wrong-vault to_project must be refused")
	}
	if ran {
		t.Fatal("the handler ran despite the cross-vault to_project refusal")
	}
}
