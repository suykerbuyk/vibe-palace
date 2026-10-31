// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"io"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/codexhook"
)

func cmdCodex() *cli.Command {
	return &cli.Command{Name: "codex", Synopsis: "vp codex <command>", Description: "Codex host integration commands."}
}

func cmdCodexHook() *cli.Command {
	return &cli.Command{Name: "codex hook", Synopsis: "vp codex hook <event>", Description: "Codex hook handlers."}
}

func cmdCodexHookSessionStart() *cli.Command {
	return &cli.Command{
		Name:        "codex hook session-start",
		Synopsis:    "vp codex hook session-start",
		Description: "Emit the explicit-project bootstrap instruction for a Codex SessionStart hook.",
		Run: func(args []string) int {
			if len(args) != 0 {
				fmt.Fprintln(os.Stderr, "vp codex hook session-start: does not accept arguments")
				return cli.ExitUser
			}
			return runCodexSessionStart(os.Stdin, os.Stdout, os.Stderr)
		},
	}
}

func cmdCodexHookSessionEnd() *cli.Command {
	return &cli.Command{
		Name: "codex hook session-end", Synopsis: "vp codex hook session-end",
		Description: "Archive the transcript Codex supplies at SessionEnd and write a typed receipt.",
		Run: func(args []string) int {
			if len(args) != 0 {
				fmt.Fprintln(os.Stderr, "vp codex hook session-end: does not accept arguments")
				return cli.ExitUser
			}
			if err := codexhook.HandleSessionEnd(os.Stdin, os.Stdout); err != nil {
				fmt.Fprintf(os.Stderr, "vp codex hook session-end: %v\n", err)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

func runCodexSessionStart(in io.Reader, out, errw io.Writer) int {
	if err := codexhook.HandleSessionStart(in, out); err != nil {
		fmt.Fprintf(errw, "vp codex hook session-start: %v\n", err)
		// Codex hook output is advisory. A failure to produce context must not
		// block the session; AGENTS.md remains the instruction fallback.
	}
	return cli.ExitOK
}
