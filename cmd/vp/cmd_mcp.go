// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
	"github.com/suykerbuyk/vibe-palace/internal/project"
)

// startupIngestLauncher is the launcher the vp mcp startup backstop uses to
// spawn the pending-archive ingester. It is a package variable defaulting to
// the real detached launcher so cmd/vp tests can substitute a recording fake.
var startupIngestLauncher detachlaunch.LaunchFunc = detachlaunch.Launch

func cmdMCP() *cli.Command {
	return &cli.Command{
		Name:        "mcp",
		Synopsis:    "vp mcp",
		Description: "Start the MCP server on stdio (JSON-RPC). Used by AI assistants to access palace tools via the Model Context Protocol.",
		Examples: []cli.Example{
			{Cmd: "vp mcp", Comment: "Start MCP server on stdin/stdout"},
			{Cmd: "vp mcp serve", Comment: "Start a bearer-authed Streamable-HTTP MCP server"},
			{Cmd: "vp mcp install --claude-plugin", Comment: "Register vibe-palace with Claude Code"},
			{Cmd: "vp mcp install --grok", Comment: "Register vibe-palace with Grok Build"},
			{Cmd: "vp mcp install --zed", Comment: "Register vibe-palace with Zed"},
		},
		// vp mcp (bare) starts the stdio server; install/uninstall are
		// subcommands. BareInvocation routes the bare form back to Run while
		// still erroring on unknown subcommand tokens.
		BareInvocation: true,
		Run: func(args []string) int {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return serveMCP(ctx, os.Stdin, os.Stdout)
		},
	}
}

// serveMCP runs the MCP stdio server. Blocks until ctx is cancelled or EOF.
func serveMCP(ctx context.Context, in io.Reader, out io.Writer) int {
	stack, err := bootstrap()
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp: %v\n", err)
		return cli.ExitSystem
	}
	defer stack.close()

	// Startup backstop (ADR-014 decision 7): spawn the pending-archive ingester
	// once, BEFORE Listen reads its first message, so an archive whose own
	// trigger was lost (the host killed the SessionEnd hook before its last
	// step) is still picked up by the next session start. It adds no wait to the
	// handshake. `vp mcp serve` deliberately does NOT do this — its startup is
	// not a session start.
	spawnStartupIngestBackstop(stack)

	if err := stack.srv.Listen(ctx, in, out); err != nil {
		fmt.Fprintf(os.Stderr, "vp: %v\n", err)
		return cli.ExitSystem
	}
	return cli.ExitOK
}

// spawnStartupIngestBackstop starts one detached `vp drain archives` run at vp
// mcp startup. --project is the project resolved with high confidence from the
// launch cwd; when none resolves it is the vault's first project in slug order,
// because the ingester takes exactly one project and then serves every other
// project of the vault. A vault with no project spawns nothing. It passes no
// --first: startup is not the host that created any archive. Best-effort: a
// launch failure is a vp.log warning, never fatal, and no lock is taken so no
// trigger is lost.
func spawnStartupIngestBackstop(stack *serverStack) {
	if startupIngestLauncher == nil {
		return
	}
	proj, _ := project.DetectProjectHighConfidence(stack.launchCwd)
	if proj == "" {
		all, err := stack.vault.ListAllProjects()
		if err != nil || len(all) == 0 {
			return // a vault with no project spawns nothing
		}
		slugs := make([]string, 0, len(all))
		for _, p := range all {
			slugs = append(slugs, p.Slug)
		}
		sort.Strings(slugs)
		proj = slugs[0]
	}
	args := []string{"drain", "archives", "--vault-root", stack.vault.Root, "--project", proj}
	logPath := filepath.Join(stack.vault.VaultLocalDir(), "ingester.log")
	if _, err := startupIngestLauncher("", args, logPath); err != nil {
		slog.Warn("vp mcp: could not launch the startup archive-ingester backstop (non-fatal)",
			"project", proj, "error", err)
	}
}
