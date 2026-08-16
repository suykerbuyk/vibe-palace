// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestRunSelectedChecks_SwallowedVsAbsent pins the distinction the `--check`
// selector path switches on.
//
// This path never runs CheckConfigAt, so nothing else in a `vp check --check …`
// invocation would mention a rejected config. But promoting EVERY resolution
// error would hard-exit on an un-set-up machine — the machine the surface
// preflight exists to diagnose.
//
// MUTATION CONTRACT: drop the sentinel case and the swallowed subtest goes red
// (it degrades to "" and returns rows); promote every error and the absent
// subtest goes red.
func TestRunSelectedChecks_SwallowedVsAbsent(t *testing.T) {
	chdir := func(t *testing.T, dir string) {
		t.Helper()
		orig, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(orig) })
	}

	t.Run("swallowed config is reported", func(t *testing.T) {
		tmp := t.TempDir()
		home := filepath.Join(tmp, "home")
		configDir := filepath.Join(home, ".config", "vibe-palace")
		projectDir := filepath.Join(tmp, "proj")
		for _, d := range []string{configDir, projectDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		if err := os.WriteFile(filepath.Join(configDir, "config.toml"),
			[]byte("vault_path = \""+filepath.Join(tmp, "live-vault")+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(projectDir, ".vibe-palace.toml"),
			[]byte("[project]\nname = \"p\"\n\nvault_path = \"/tmp/tw\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		chdir(t, projectDir)

		_, err := runSelectedChecks("surface")
		if !errors.Is(err, storage.ErrSwallowedVaultPath) {
			t.Fatalf("runSelectedChecks err = %v; want ErrSwallowedVaultPath — otherwise every producer "+
				"reports \"no vault configured\" and the rejected file is never mentioned", err)
		}
	})

	t.Run("absent config still runs the preflight", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
		workDir := filepath.Join(tmp, "work")
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			t.Fatal(err)
		}
		chdir(t, workDir)

		rows, err := runSelectedChecks("surface")
		if err != nil {
			t.Fatalf("runSelectedChecks on an un-set-up machine = %v; want no error — "+
				"`vp check --check surface` is the preflight that has to work THERE", err)
		}
		if len(rows) == 0 {
			t.Error("expected a surface row on an unconfigured machine")
		}
	})
}
