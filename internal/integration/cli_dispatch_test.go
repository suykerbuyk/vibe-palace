// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// TestIntegrationDispatchParentBareShowsHelp proves the framework's
// parent-command dispatch gate renders help on stdout with exit 0 when
// a real parent (`vp config`) is invoked without a subcommand. Guards
// against regressions in internal/cli/registry.go's dispatchCommand
// from end users' perspective.
func TestIntegrationDispatchParentBareShowsHelp(t *testing.T) {
	bin := buildVPBinary(t)

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "config")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		t.Fatalf("exit error: %v\nstderr: %s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Usage: vp config") {
		t.Errorf("stdout missing usage line, got:\n%s", out)
	}
	if !strings.Contains(out, "Commands:") {
		t.Errorf("stdout missing Commands section, got:\n%s", out)
	}
	if !strings.Contains(out, "config sync") {
		t.Errorf("stdout missing subcommand listing, got:\n%s", out)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got: %q", stderr.String())
	}
}

// TestIntegrationDispatchParentUnknownSubcommand proves unknown
// subcommands produce an ExitUser error on stderr with a useful
// message, not a silent ExitOK like the old per-parent Run closures.
func TestIntegrationDispatchParentUnknownSubcommand(t *testing.T) {
	bin := buildVPBinary(t)

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "config", "bogus")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected non-zero exit for unknown subcommand; stdout: %s", stdout.String())
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1 (ExitUser)", exitErr.ExitCode())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty, got: %q", stdout.String())
	}
	se := stderr.String()
	if !strings.Contains(se, `unknown subcommand "bogus"`) {
		t.Errorf("stderr should mention unknown subcommand, got:\n%s", se)
	}
	if !strings.Contains(se, "vp config") {
		t.Errorf("stderr should name the parent, got:\n%s", se)
	}
	if !strings.Contains(se, "Commands:") {
		t.Errorf("stderr should include parent help, got:\n%s", se)
	}
}

// TestIntegrationDispatchKnownSubcommandWorks is a regression guard
// that `vp <parent> <sub> --help` still routes through the two-word
// lookup cleanly after the dispatch gate changes.
func TestIntegrationDispatchKnownSubcommandHelp(t *testing.T) {
	bin := buildVPBinary(t)

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, "hook", "install", "--help")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("exit error: %v\nstderr: %s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "hook install") {
		t.Errorf("stdout missing hook install reference, got:\n%s", out)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got: %q", stderr.String())
	}
}
