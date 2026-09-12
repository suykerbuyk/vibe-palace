// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

// None of the four cases in this file touch HOME/vault state — they only
// exercise internal/cli/registry.go's parent-command dispatch gate (bare
// parent → help; unknown subcommand → usage error; known two-word
// subcommand → framework help), so they run against the ambient process
// environment rather than an isolated testinfra.Env. testinfra.RunCLI never
// falls back to os.Environ() implicitly, so that ambient environment is
// passed explicitly here rather than left as a nil ("forgot to isolate")
// case.
func ambientEnv() []string { return os.Environ() }

// TestIntegrationDispatchParentBareShowsHelp proves the framework's
// parent-command dispatch gate renders help on stdout with exit 0 when
// a real parent (`vp config`) is invoked without a subcommand. Guards
// against regressions in internal/cli/registry.go's dispatchCommand
// from end users' perspective.
func TestIntegrationDispatchParentBareShowsHelp(t *testing.T) {
	r := testinfra.RunCLI(t, ambientEnv(), t.TempDir(), nil, "config")
	r.Must(t)

	if !strings.Contains(r.Stdout, "Usage: vp config") {
		t.Errorf("stdout missing usage line, got:\n%s", r.Stdout)
	}
	if !strings.Contains(r.Stdout, "Commands:") {
		t.Errorf("stdout missing Commands section, got:\n%s", r.Stdout)
	}
	if !strings.Contains(r.Stdout, "config sync") {
		t.Errorf("stdout missing subcommand listing, got:\n%s", r.Stdout)
	}
	if r.Stderr != "" {
		t.Errorf("stderr should be empty, got: %q", r.Stderr)
	}
}

// TestIntegrationDispatchParentUnknownSubcommand proves unknown
// subcommands produce an ExitUser error on stderr with a useful
// message, not a silent ExitOK like the old per-parent Run closures.
func TestIntegrationDispatchParentUnknownSubcommand(t *testing.T) {
	r := testinfra.RunCLI(t, ambientEnv(), t.TempDir(), nil, "config", "bogus")

	if r.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1 (ExitUser)", r.ExitCode)
	}
	if r.Stdout != "" {
		t.Errorf("stdout should be empty, got: %q", r.Stdout)
	}
	if !strings.Contains(r.Stderr, `unknown subcommand "bogus"`) {
		t.Errorf("stderr should mention unknown subcommand, got:\n%s", r.Stderr)
	}
	if !strings.Contains(r.Stderr, "vp config") {
		t.Errorf("stderr should name the parent, got:\n%s", r.Stderr)
	}
	if !strings.Contains(r.Stderr, "Commands:") {
		t.Errorf("stderr should include parent help, got:\n%s", r.Stderr)
	}
}

// TestIntegrationDispatchKnownSubcommandHelp is a regression guard
// that `vp <parent> <sub> --help` still routes through the two-word
// lookup cleanly after the dispatch gate changes.
func TestIntegrationDispatchKnownSubcommandHelp(t *testing.T) {
	r := testinfra.RunCLI(t, ambientEnv(), t.TempDir(), nil, "hook", "install", "--help")
	r.Must(t)

	if !strings.Contains(r.Stdout, "hook install") {
		t.Errorf("stdout missing hook install reference, got:\n%s", r.Stdout)
	}
	if r.Stderr != "" {
		t.Errorf("stderr should be empty, got: %q", r.Stderr)
	}
}

// TestIntegrationDispatchBareParentExitDiscipline is the Go port of the
// retired test/e2e/dispatch/04-bare-parent-exit-code-discipline.sh: it proves
// the bare-parent-shows-help / unknown-subcommand-is-exit-user contract holds
// across several parent commands, not just `vp config` (covered above).
// Every parent with Subcommands must exit 0 on a bare invocation (help
// emitted) and exit 1 when given a non-flag unknown token.
func TestIntegrationDispatchBareParentExitDiscipline(t *testing.T) {
	for _, parent := range []string{"vault", "commands", "migrate", "skills", "archive"} {
		t.Run(parent, func(t *testing.T) {
			bare := testinfra.RunCLI(t, ambientEnv(), t.TempDir(), nil, parent)
			bare.Must(t)
			if !strings.Contains(bare.Stdout, "Commands:") {
				t.Errorf("vp %s: stdout missing Commands section, got:\n%s", parent, bare.Stdout)
			}

			unknown := testinfra.RunCLI(t, ambientEnv(), t.TempDir(), nil, parent, "a-token-that-will-never-be-a-subcommand")
			if unknown.ExitCode != 1 {
				t.Errorf("vp %s a-token-...: exit code = %d, want 1 (ExitUser)", parent, unknown.ExitCode)
			}
			if !strings.Contains(unknown.Stderr, "unknown subcommand") {
				t.Errorf("vp %s a-token-...: stderr missing 'unknown subcommand', got:\n%s", parent, unknown.Stderr)
			}
		})
	}
}
