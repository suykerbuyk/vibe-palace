// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

// logLines returns the log lines under vaultRoot, or nil if no log exists.
func logLines(t *testing.T, vaultRoot string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(vaultRoot, "palace", ".local", "vp.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read log under %s: %v", vaultRoot, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// TestInitLoggingForVaultWritesToThatVault pins the single definition of where
// vp.log lives. Every entry point routes through initLoggingFor, so if this
// path is wrong it is wrong everywhere at once.
func TestInitLoggingForVaultWritesToThatVault(t *testing.T) {
	root := t.TempDir()
	v := storage.NewVault(root)

	initLoggingForVault(v)
	slog.Warn("marker: init logging for vault")
	vplog.Close()

	lines := logLines(t, root)
	if len(lines) == 0 {
		t.Fatal("no vp.log written under the vault root")
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
		t.Fatalf("log line is not JSON: %v (%s)", err, lines[len(lines)-1])
	}
	if entry["msg"] != "marker: init logging for vault" {
		t.Errorf("msg = %v, want the marker", entry["msg"])
	}
}

// TestHookLogsWarnToPayloadVault is the load-bearing test for iteration 196's
// logger fix, and it asserts the thing that was actually broken.
//
// Two failures had to be fixed for a `vp hook` warning to become observable:
//
//  1. vplog.Init was reachable only from bootstrap(), which only `vp mcp` and
//     `vp mcp serve` call — so `vp hook` ran with Go's default stderr handler
//     and its 11 slog calls never reached any vp.log at all.
//  2. The CLI pre-run can initialize a logger, but it resolves the vault from
//     the *process* cwd. The hook's vault comes from the CWD inside the JSON
//     payload on stdin, which is parsed later. Fixing only (1) would send the
//     hook's warnings to whichever vault the process happened to start in.
//
// So the test deliberately points the logger at a decoy vault first — standing
// in for the pre-run — and then asserts the hook's warning lands in the vault
// named by its payload, and NOT in the decoy. A version that fixes (1) but not
// (2) passes a naive "did anything get logged" check and fails this one.
func TestHookLogsWarnToPayloadVault(t *testing.T) {
	tmp := t.TempDir()

	// The decoy: where the pre-run would have pointed logging.
	decoyVault := filepath.Join(tmp, "decoy-vault")
	if err := os.MkdirAll(decoyVault, 0o755); err != nil {
		t.Fatal(err)
	}

	// The real target: a project whose .vibe-palace.toml redirects to its own
	// vault. The marker also satisfies the hook's opt-in gate (SignalVibeConfig),
	// without which Run skips capture entirely and emits nothing.
	projectVault := filepath.Join(tmp, "project-vault")
	projectDir := filepath.Join(tmp, "proj")
	for _, d := range []string{projectVault, projectDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(
		filepath.Join(projectDir, ".vibe-palace.toml"),
		[]byte("vault_path = \""+projectVault+"\"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	// Stand in for the pre-run: logging starts out aimed at the decoy.
	initLoggingForVault(storage.NewVault(decoyVault))
	slog.Warn("marker: pre-run logging active")

	// A SessionEnd whose transcript does not exist. archive.Create fails, which
	// is a non-fatal warn (hook.go:120) — a deterministic emission that does not
	// depend on the capture pipeline succeeding.
	payload := map[string]string{
		"hook_event_name": "SessionEnd",
		"session_id":      "test-session-196",
		"transcript_path": filepath.Join(tmp, "does-not-exist.jsonl"),
		"cwd":             projectDir,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	stdinFile := filepath.Join(tmp, "payload.json")
	if err := os.WriteFile(stdinFile, body, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	origStdin := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = origStdin })

	// The hook's exit code is deliberately not asserted: capture may or may not
	// succeed in a bare temp vault, and this test is about observability, not
	// about the capture pipeline. What must hold is that the warning is durable.
	runHook(cli.BuildInfo{Version: "test"})
	vplog.Close()

	projectLines := logLines(t, projectVault)
	if len(projectLines) == 0 {
		t.Fatal("vp hook wrote NO log to the vault named by its payload — " +
			"either the logger was never initialized on the hook path, or it " +
			"was never re-pointed away from the pre-run's vault")
	}
	joined := strings.Join(projectLines, "\n")
	if !strings.Contains(joined, "hook: archive failed") {
		t.Errorf("expected the archive-failed WARN in the project vault's log; got:\n%s", joined)
	}

	// The re-point assertion: the decoy holds only the pre-run marker, and none
	// of the hook's output.
	decoyJoined := strings.Join(logLines(t, decoyVault), "\n")
	if !strings.Contains(decoyJoined, "marker: pre-run logging active") {
		t.Error("decoy vault lost the pre-run marker; the test is not exercising a re-point")
	}
	if strings.Contains(decoyJoined, "hook: archive failed") {
		t.Error("the hook's warning landed in the PROCESS cwd's vault instead of the " +
			"vault from its payload — the re-point in runHook is not working")
	}
}
