// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

var (
	version   = "0.1.0-dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	info := cli.BuildInfo{Version: version, Commit: commit, BuildDate: buildDate}
	reg := cli.NewRegistry(info)
	registerAll(reg, info)
	os.Exit(reg.Dispatch(os.Args[1:]))
}
