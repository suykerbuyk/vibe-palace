// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"slices"
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
	// Subcommands is registry-derived, so the expectation is too: a hardcoded 2
	// pinned the number rather than the children, and needed editing whenever a
	// real child was added.
	reg := registeredCommand(t, "hook")
	want := registeredChildren(t, "hook")
	if !slices.Equal(reg.Subcommands, want) {
		t.Errorf("Subcommands = %v, want %v", reg.Subcommands, want)
	}
	for _, name := range []string{"hook install", "hook uninstall"} {
		if !slices.Contains(reg.Subcommands, name) {
			t.Errorf("hook does not list %q", name)
		}
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
