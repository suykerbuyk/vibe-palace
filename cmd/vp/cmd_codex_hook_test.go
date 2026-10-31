// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func TestCodexHookCommandRegistered(t *testing.T) {
	cmd := registeredCommand(t, "codex hook session-start")
	if cmd.Name != "codex hook session-start" {
		t.Fatalf("registered command = %q", cmd.Name)
	}
}

func TestCodexSessionEndHookCommandRegisteredUnwrapped(t *testing.T) {
	cmd := registeredCommand(t, "codex hook session-end")
	if cmd.Name != "codex hook session-end" || cmd.MutatesVault {
		t.Fatalf("SessionEnd registration = %+v; expected unwrapped command for Codex's 3s ceiling", cmd)
	}
}

func TestCodexHookSessionStartEmitsAdvisoryContext(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".vibe-palace.toml"), []byte("[project]\nname = \"hook-test\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input := `{"cwd":"` + cwd + `","hook_event_name":"SessionStart"}`
	var out, errb bytes.Buffer
	if code := runCodexSessionStart(strings.NewReader(input), &out, &errb); code != cli.ExitOK {
		t.Fatalf("exit=%d stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "vp_bootstrap_context") || !strings.Contains(out.String(), "hook-test") {
		t.Fatalf("hook output lacks explicit bootstrap context: %s", out.String())
	}
}

func TestCodexHookFailureDoesNotBlock(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runCodexSessionStart(strings.NewReader(`{"cwd":""}`), &out, &errb); code != cli.ExitOK {
		t.Fatalf("hook error blocked session with exit %d", code)
	}
	if !strings.Contains(errb.String(), "no cwd") || !strings.Contains(out.String(), "{}") {
		t.Fatalf("honest hook failure missing: stdout=%s stderr=%s", out.String(), errb.String())
	}
}
