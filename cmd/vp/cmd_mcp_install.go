// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"os"
	"slices"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/mcphost"
	"github.com/suykerbuyk/vibe-palace/internal/plugin"
)

// hasFlag reports whether name appears in args.
func hasFlag(args []string, name string) bool {
	return slices.Contains(args, name)
}

// selectHosts returns the registry hosts whose selector flag is present in
// args, in registry order. An empty result means no host flag was passed.
func selectHosts(args []string) []mcphost.Host {
	var selected []mcphost.Host
	for _, h := range mcphost.Registry() {
		if hasFlag(args, h.Flag()) {
			selected = append(selected, h)
		}
	}
	return selected
}

func cmdMCPInstall(info cli.BuildInfo) *cli.Command {
	return &cli.Command{
		Name:     "mcp install",
		Synopsis: "vp mcp install [--claude-plugin] [--grok] [--zed]",
		Description: "Register the vibe-palace MCP server with one or more AI coding hosts, " +
			"making the vp_* tools available in every session. Re-running --claude-plugin " +
			"refreshes on-disk plugin files even when already enabled (cache stamp uses " +
			"build commit, not the frozen product version). Pass one or more host flags: " +
			"--claude-plugin (Claude Code local plugin/marketplace), --grok (xAI Grok Build " +
			"via `grok mcp add`), --zed (Zed editor context_servers). The same MCP server " +
			"backs every host; only the registration differs. Restart the host to activate.",
		Examples: []cli.Example{
			{Cmd: "vp mcp install --claude-plugin", Comment: "Register the Claude Code plugin"},
			{Cmd: "vp mcp install --grok", Comment: "Register with Grok Build via grok mcp add"},
			{Cmd: "vp mcp install --zed", Comment: "Add a Zed context_servers entry (comment-safe)"},
			{Cmd: "vp mcp install --grok --zed", Comment: "Register with multiple hosts at once"},
		},
		Run: func(args []string) int {
			hosts := selectHosts(args)
			if len(hosts) == 0 {
				fmt.Fprintln(os.Stderr, "vp mcp install: pass one or more host flags: --claude-plugin, --grok, --zed")
				return cli.ExitUser
			}
			cwd, _ := os.Getwd()
			// SurfaceStamp prefers commit over frozen BASE_VERSION so Claude's
			// plugin cache path actually moves on rebuild (Phase 0.5 / C2).
			stamp := plugin.SurfaceStamp(info.Version, info.Commit)
			failed := false
			for _, h := range hosts {
				if err := h.Install(stamp, cwd, os.Stderr); err != nil {
					fmt.Fprintf(os.Stderr, "vp mcp install --%s: %v\n", h.Name(), err)
					failed = true
				}
			}
			if failed {
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

func cmdMCPUninstall() *cli.Command {
	return &cli.Command{
		Name:     "mcp uninstall",
		Synopsis: "vp mcp uninstall [--claude-plugin] [--grok] [--zed]",
		Description: "Remove the vibe-palace MCP registration from one or more hosts " +
			"(--claude-plugin, --grok, --zed). Idempotent: a host with nothing " +
			"registered is reported, not an error.",
		Examples: []cli.Example{
			{Cmd: "vp mcp uninstall --claude-plugin", Comment: "Remove the Claude Code plugin"},
			{Cmd: "vp mcp uninstall --grok", Comment: "Remove the Grok registration"},
			{Cmd: "vp mcp uninstall --zed", Comment: "Remove the Zed context_servers entry"},
		},
		Run: func(args []string) int {
			hosts := selectHosts(args)
			if len(hosts) == 0 {
				fmt.Fprintln(os.Stderr, "vp mcp uninstall: pass one or more host flags: --claude-plugin, --grok, --zed")
				return cli.ExitUser
			}
			failed := false
			for _, h := range hosts {
				if err := h.Uninstall(os.Stderr); err != nil {
					fmt.Fprintf(os.Stderr, "vp mcp uninstall --%s: %v\n", h.Name(), err)
					failed = true
				}
			}
			if failed {
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}
