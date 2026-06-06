// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func TestMCPHasInstallSubcommands(t *testing.T) {
	cmd := cmdMCP()
	if !cmd.BareInvocation {
		t.Error("mcp should set BareInvocation so bare `vp mcp` still serves")
	}
	want := map[string]bool{"mcp install": false, "mcp uninstall": false}
	for _, s := range cmd.Subcommands {
		if _, ok := want[s]; ok {
			want[s] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing subcommand %q", name)
		}
	}
}

func TestMCPInstallConstructors(t *testing.T) {
	if got := cmdMCPInstall(cli.BuildInfo{Version: "test"}).Name; got != "mcp install" {
		t.Errorf("name = %q, want \"mcp install\"", got)
	}
	if got := cmdMCPUninstall().Name; got != "mcp uninstall" {
		t.Errorf("name = %q, want \"mcp uninstall\"", got)
	}
}

func TestMCPInstallRequiresFlag(t *testing.T) {
	cmd := cmdMCPInstall(cli.BuildInfo{Version: "test"})
	// Without --claude-plugin the command refuses (user error) rather than
	// mutating settings.
	if code := cmd.Run([]string{}); code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (ExitUser)", code, cli.ExitUser)
	}
}

func TestHasFlag(t *testing.T) {
	if !hasFlag([]string{"a", "--claude-plugin", "b"}, "--claude-plugin") {
		t.Error("hasFlag should find present flag")
	}
	if hasFlag([]string{"a", "b"}, "--claude-plugin") {
		t.Error("hasFlag should not find absent flag")
	}
}
