// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/hook"
)

// TestRunHookExitsOKWhenNoVaultOpens is the exit-2 defect fix (ADR-014
// implementation notes; this child's Scope 4). When neither the payload's vault
// nor the global vault opens, the hook must write the error to stderr, emit a
// result body naming it, and return ExitOK — never ExitSystem (2, Claude Code's
// reserved BLOCKING code). Before the fix this path returned ExitSystem.
func TestRunHookExitsOKWhenNoVaultOpens(t *testing.T) {
	tmp := t.TempDir()
	// A bare cwd with no .vibe-palace marker: OpenVaultFromCwd finds no binding,
	// and the hermetic test env has no global vault, so the global fallback
	// fails too — the branch under test.
	cwd := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(map[string]string{
		"hook_event_name": "Stop",
		"session_id":      "orphan-session",
		"cwd":             cwd,
	})
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

	// Capture stdout to inspect the emitted result body.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdin, origStdout := os.Stdin, os.Stdout
	os.Stdin = f
	os.Stdout = stdoutW
	t.Cleanup(func() { os.Stdin, os.Stdout = origStdin, origStdout })

	code := runHook(cli.BuildInfo{Version: "test"})
	_ = stdoutW.Close()

	if code == cli.ExitSystem {
		t.Fatalf("runHook exited ExitSystem (2) when no vault opened — 2 is Claude Code's BLOCKING code; must be ExitOK")
	}
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want ExitOK (a hook never blocks the turn)", code)
	}

	// The result body on stdout must name the failure.
	var buf [4096]byte
	n, _ := stdoutR.Read(buf[:])
	var res hook.Result
	if err := json.Unmarshal(buf[:n], &res); err != nil {
		t.Fatalf("stdout is not a hook.Result JSON: %v (raw: %q)", err, string(buf[:n]))
	}
	if res.Error == "" {
		t.Errorf("result body carries no Error naming why no vault opened: %+v", res)
	}
}
