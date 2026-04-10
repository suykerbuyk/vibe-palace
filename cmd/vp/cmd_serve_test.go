// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func TestServeCommandConstructor(t *testing.T) {
	cmd := cmdServe()
	if cmd.Name != "serve" {
		t.Errorf("name = %q", cmd.Name)
	}
	if len(cmd.Flags) == 0 {
		t.Error("expected flags")
	}
}

func TestServeBadFlags(t *testing.T) {
	cmd := cmdServe()
	code := cmd.Run([]string{"--unknown"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
}

func TestMCPCommandConstructor(t *testing.T) {
	cmd := cmdMCP()
	if cmd.Name != "mcp" {
		t.Errorf("name = %q", cmd.Name)
	}
}
