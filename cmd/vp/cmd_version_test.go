// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func TestVersionCommand(t *testing.T) {
	info := cli.BuildInfo{Version: "1.2.3", Commit: "abc", BuildDate: "2026-01-01"}
	cmd := cmdVersion(info)
	code := cmd.Run(nil)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
}

func TestVersionSurfaceFlag(t *testing.T) {
	info := cli.BuildInfo{Version: "1.2.3"}
	cmd := cmdVersion(info)
	out := captureStdout(t, func() {
		if code := cmd.Run([]string{"--surface"}); code != cli.ExitOK {
			t.Errorf("exit code = %d", code)
		}
	})
	want := fmt.Sprintf("surface: %d", surface.MCPSurfaceVersion)
	if strings.TrimSpace(out) != want {
		t.Errorf("output = %q, want %q", strings.TrimSpace(out), want)
	}
}
