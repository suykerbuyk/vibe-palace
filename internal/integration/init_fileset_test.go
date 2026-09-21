// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

// TestIntegrationInitFullFileset proves end-to-end that after a clean
// vp init the full config fileset is in place:
//  1. Global config.toml under XDG (with [meta] v1).
//  2. Cwd .vibe-palace.toml with [meta] and [project].name.
//
// Every file must parse as valid TOML. The third file this used to assert, the
// per-project vault config {vault}/Projects/{slug}/config.toml, is retired and
// no longer written (task move-per-project-config-out-of-the-shared-vault).
func TestIntegrationInitFullFileset(t *testing.T) {
	env := testinfra.IsolateEnv(t)

	vaultDir := filepath.Join(env.Home, "vault")
	projectDir := filepath.Join(env.Home, "code", "alpha")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Use the public storage API to reproduce what `vp init` does:
	// WriteGlobalConfig + WriteCwdProjectConfig.
	if _, err := storage.WriteGlobalConfig(vaultDir, false); err != nil {
		t.Fatalf("WriteGlobalConfig: %v", err)
	}
	if _, err := storage.WriteCwdProjectConfig(projectDir, "alpha", "work", []string{"go"}, ""); err != nil {
		t.Fatalf("WriteCwdProjectConfig: %v", err)
	}

	// 1. Global config.
	globalPath, _ := storage.VaultConfigFilePath()
	assertTOMLWithMeta(t, globalPath, "global", "" /* no kind asserted — commented in global template */)

	// 2. Cwd config.
	cwdPath := filepath.Join(projectDir, ".vibe-palace.toml")
	assertTOMLWithMeta(t, cwdPath, "cwd", "")

	cwdData, _ := os.ReadFile(cwdPath)
	if !strings.Contains(string(cwdData), `name = "alpha"`) {
		t.Errorf("cwd file missing active name: %s", cwdData)
	}
	if !strings.Contains(string(cwdData), `domain = "work"`) {
		t.Errorf("cwd file missing active domain: %s", cwdData)
	}
}

// assertTOMLWithMeta reads path, parses as TOML, asserts [meta] exists
// with version_major = 1. label is used for error messages.
func assertTOMLWithMeta(t *testing.T, path, label, _ string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: read %s: %v", label, path, err)
	}
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		t.Fatalf("%s: %s is not valid TOML: %v", label, path, err)
	}
	meta, ok := raw["meta"].(map[string]any)
	if !ok {
		t.Fatalf("%s: %s missing [meta] block", label, path)
	}
	if meta["version_major"] != int64(1) {
		t.Errorf("%s: %s meta.version_major = %v, want 1", label, path, meta["version_major"])
	}
}
