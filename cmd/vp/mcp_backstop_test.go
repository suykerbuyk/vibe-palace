// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

type backstopCall struct {
	args    []string
	logPath string
}

// installBackstopRecorder swaps startupIngestLauncher for a recorder and
// restores it on cleanup.
func installBackstopRecorder(t *testing.T) *[]backstopCall {
	t.Helper()
	calls := &[]backstopCall{}
	old := startupIngestLauncher
	startupIngestLauncher = func(_ string, args []string, logPath string) (int, error) {
		*calls = append(*calls, backstopCall{append([]string(nil), args...), logPath})
		return 1, nil
	}
	t.Cleanup(func() { startupIngestLauncher = old })
	return calls
}

func bsArgVal(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// TestSpawnStartupIngestBackstop_FallbackFirstProject: from a cwd that resolves
// no project, the backstop spawns one ingester naming the vault's first project
// in slug order, with no --first.
func TestSpawnStartupIngestBackstop_FallbackFirstProject(t *testing.T) {
	vaultDir := t.TempDir()
	testutil.InitProject(t, vaultDir, "zeta")
	testutil.InitProject(t, vaultDir, "alpha")
	v := storage.NewVault(vaultDir)

	calls := installBackstopRecorder(t)
	// launchCwd is an unrelated temp dir, so DetectProjectHighConfidence finds
	// no project and the fallback (first slug) applies.
	spawnStartupIngestBackstop(&serverStack{vault: v, launchCwd: t.TempDir()})

	if len(*calls) != 1 {
		t.Fatalf("want exactly one backstop launch, got %d: %+v", len(*calls), *calls)
	}
	got := (*calls)[0].args
	if len(got) < 2 || got[0] != "drain" || got[1] != "archives" {
		t.Errorf("args = %v, want a `drain archives` command", got)
	}
	if val, ok := bsArgVal(got, "--project"); !ok || val != "alpha" {
		t.Errorf("--project = %q, want alpha (first slug)", val)
	}
	if val, ok := bsArgVal(got, "--vault-root"); !ok || val != vaultDir {
		t.Errorf("--vault-root = %q, want %q", val, vaultDir)
	}
	if _, ok := bsArgVal(got, "--first"); ok {
		t.Error("the startup backstop must pass no --first")
	}
}

// TestSpawnStartupIngestBackstop_NoProjectNoSpawn: a vault with no project
// spawns nothing.
func TestSpawnStartupIngestBackstop_NoProjectNoSpawn(t *testing.T) {
	v := storage.NewVault(t.TempDir())
	calls := installBackstopRecorder(t)
	spawnStartupIngestBackstop(&serverStack{vault: v, launchCwd: t.TempDir()})
	if len(*calls) != 0 {
		t.Errorf("want no launch on a vault with no project, got %+v", *calls)
	}
}
