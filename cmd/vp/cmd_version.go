// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

var versionFlags = []cli.FlagDef{
	{Name: "--surface", Help: "Print the binary's MCP tool-surface version"},
}

func cmdVersion(info cli.BuildInfo) *cli.Command {
	return &cli.Command{
		Name:        "version",
		Synopsis:    "vp version [--surface]",
		Description: "Print version, commit, and build date. With --surface, print the MCP tool-surface version.",
		Flags:       versionFlags,
		Examples: []cli.Example{
			{Cmd: "vp version", Comment: "Show version information"},
			{Cmd: "vp version --surface", Comment: "Show the MCP tool-surface version"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(versionFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp version: %v\n", err)
				return cli.ExitUser
			}
			if fv.Bool("--surface") {
				fmt.Printf("surface: %d\n", surface.MCPSurfaceVersion)
				return cli.ExitOK
			}
			fmt.Println(info)
			return cli.ExitOK
		},
	}
}
