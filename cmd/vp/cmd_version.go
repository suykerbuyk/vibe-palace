// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func cmdVersion(info cli.BuildInfo) *cli.Command {
	return &cli.Command{
		Name:        "version",
		Synopsis:    "vp version",
		Description: "Print version, commit, and build date.",
		Examples: []cli.Example{
			{Cmd: "vp version", Comment: "Show version information"},
		},
		Run: func(args []string) int {
			fmt.Println(info)
			return cli.ExitOK
		},
	}
}
