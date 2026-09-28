// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func spbWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stale binding, running server: a server started while this host was bound
// to B and B held the project. The move is then undone (B's copy reverted).
// The server's root never changed, so only the stale-binding rule can see it;
// it must reach the dispatch gate as a StaleBindingError and refuse writes.
func TestStaleProjectBindingRefusesWritesOnARunningServer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	vaultA := filepath.Join(home, "vault-a")
	vaultB := filepath.Join(home, "vault-b")
	launchCwd := filepath.Join(home, "code", "qa")
	manifest := fmt.Sprintf("format = %d\n", surface.RequiredDataFormat)
	spbWrite(t, filepath.Join(vaultA, ".vibe-palace", "vault.toml"), manifest)
	spbWrite(t, filepath.Join(vaultB, ".vibe-palace", "vault.toml"), manifest)
	spbWrite(t, filepath.Join(vaultA, "Audits", "departures", "qa.json"),
		`{"format":"vp-departure/1","slug":"qa","kind":"moved-to-vault","to":"git@example.com:x/b.git","date":"2026-09-27"}`+"\n")
	spbWrite(t, filepath.Join(vaultB, "Projects", "qa", "resume.md"), "# qa\n")
	spbWrite(t, filepath.Join(launchCwd, ".vibe-palace.toml"), "[project]\nname = \"qa\"\n")
	cfg, err := storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	spbWrite(t, cfg, "vault_path = \""+vaultA+"\"\n\n[project_vaults]\nqa = \""+vaultB+"\"\n")

	ctx := context.WithValue(context.Background(), vaultKey, storage.NewVault(vaultB))
	reg := testRegistry(t)
	note := filepath.Join(vaultB, "Projects", "qa", "sessions", "new.md")
	if err := reg.Register(Tool{
		Name:     "write_history",
		Mutating: true,
		Handler: func(context.Context, json.RawMessage) (any, error) {
			spbWrite(t, note, "work\n")
			return "written", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	reg.WatchVaultBinding(launchCwd)

	if _, err := reg.Dispatch(ctx, "write_history", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("a live binding refused: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(vaultB, "Projects", "qa")); err != nil {
		t.Fatal(err)
	}

	_, err = reg.Dispatch(ctx, "write_history", json.RawMessage(`{}`))
	if _, serr := os.Stat(note); serr == nil {
		t.Fatalf("the running server wrote a fresh qa into the vault it is stale for (err was %v)", err)
	}
	var sbe *storage.StaleBindingError
	if !errors.As(err, &sbe) || sbe.Reason == "" {
		t.Fatalf("err = %v, want a *storage.StaleBindingError carrying the stale-binding reason", err)
	}
}
