// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func TestVersionCommand(t *testing.T) {
	info := cli.BuildInfo{Version: "1.2.3", Commit: "abc", BuildDate: "2026-01-01"}
	cmd := cmdVersion(info)
	code := cmd.Run(nil)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
}
