// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func TestCheckCommand(t *testing.T) {
	// runCheck depends on real config; just verify it doesn't panic
	// and returns a valid exit code.
	code := runCheck("test")
	if code != cli.ExitOK && code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitOK or ExitUser", code)
	}
}

func TestCheckCommandConstructor(t *testing.T) {
	info := cli.BuildInfo{Version: "test"}
	cmd := cmdCheck(info)
	if cmd.Name != "check" {
		t.Errorf("name = %q", cmd.Name)
	}
	if cmd.Run == nil {
		t.Error("Run is nil")
	}
}
