// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func TestCmdHookReturnsValidCommand(t *testing.T) {
	info := cli.BuildInfo{Version: "0.1.0-test", Commit: "deadbeef", BuildDate: "2026-04-15"}
	cmd := cmdHook(info)

	if cmd.Name != "hook" {
		t.Errorf("Name = %q, want %q", cmd.Name, "hook")
	}
	if cmd.Synopsis == "" {
		t.Error("Synopsis is empty")
	}
	if cmd.Description == "" {
		t.Error("Description is empty")
	}
	if len(cmd.Subcommands) != 2 {
		t.Errorf("Subcommands = %v, want 2 entries", cmd.Subcommands)
	}
	if cmd.Run == nil {
		t.Error("Run is nil")
	}
	if cmd.Hidden {
		t.Error("hook command should not be hidden")
	}
}

func TestFallbackSlug(t *testing.T) {
	tests := []struct {
		dir  string
		want string
	}{
		{"/home/user/my-project", "my-project"},
		{"/home/user/My Project", "my-project"},
		{"/home/user/UPPER_CASE", "upper-case"},
		{"/tmp/a", "a"},
		{"/", "unknown"}, // basename of "/" is "/"
	}
	for _, tt := range tests {
		got := fallbackSlug(tt.dir)
		if got != tt.want {
			t.Errorf("fallbackSlug(%q) = %q, want %q", tt.dir, got, tt.want)
		}
	}
}
