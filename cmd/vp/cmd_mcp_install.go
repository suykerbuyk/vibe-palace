// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/plugin"
)

// hasFlag reports whether name appears in args.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func cmdMCPInstall(info cli.BuildInfo) *cli.Command {
	return &cli.Command{
		Name:     "mcp install",
		Synopsis: "vp mcp install --claude-plugin",
		Description: "Register the vibe-palace MCP server with Claude Code as a local " +
			"plugin/marketplace, making the vp_* tools and /vpc-* command shims " +
			"available in every session. Coexists with any existing vv plugin. " +
			"Restart Claude Code to activate.",
		Examples: []cli.Example{
			{Cmd: "vp mcp install --claude-plugin", Comment: "Generate + register the plugin"},
		},
		Run: func(args []string) int {
			if !hasFlag(args, "--claude-plugin") {
				fmt.Fprintln(os.Stderr, "vp mcp install: pass --claude-plugin to register the Claude Code plugin")
				return cli.ExitUser
			}
			if err := plugin.InstallClaudePlugin(info.Version, os.Stderr); err != nil {
				fmt.Fprintf(os.Stderr, "vp mcp install: %v\n", err)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

func cmdMCPUninstall() *cli.Command {
	return &cli.Command{
		Name:        "mcp uninstall",
		Synopsis:    "vp mcp uninstall",
		Description: "Remove the vibe-palace Claude Code plugin/marketplace registration and generated files.",
		Run: func(args []string) int {
			if err := plugin.UninstallClaudePlugin(os.Stderr); err != nil {
				fmt.Fprintf(os.Stderr, "vp mcp uninstall: %v\n", err)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}
