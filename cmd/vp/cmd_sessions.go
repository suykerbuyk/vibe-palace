// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var sessionsFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--last", Short: "-n", Arg: "N", Help: "Number of sessions to show", Default: "10"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdSessions() *cli.Command {
	return &cli.Command{
		Name:        "sessions",
		Synopsis:    "vp sessions [--project P] [--last N] [--json]",
		Description: "List recent sessions for a project. Sessions are captured work units with summaries, tags, and friction scores.",
		Flags:       sessionsFlags,
		Examples: []cli.Example{
			{Cmd: "vp sessions", Comment: "List recent sessions for the current project"},
			{Cmd: "vp sessions -p myapp --last 5 --json", Comment: "Show last 5 sessions as JSON"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(sessionsFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp sessions: %v\n", err)
				return cli.ExitUser
			}

			proj := fv.Get("--project")
			if proj == "" {
				proj, _ = project.DetectProject(".")
			}
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp sessions: could not detect project (use --project)")
				return cli.ExitUser
			}

			limit := fv.Int("--last")
			if limit == 0 {
				limit = 10
			}

			vault, err := storage.OpenVault("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp sessions: %v\n", err)
				return cli.ExitUser
			}

			return runSessions(vault, proj, limit, fv.Bool("--json"), os.Stdout)
		},
	}
}

func runSessions(vault *storage.Vault, proj string, limit int, asJSON bool, out io.Writer) int {
	sessions, err := vault.ListSessions(proj, "", "", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp sessions: %v\n", err)
		return cli.ExitSystem
	}

	// Take last N sessions.
	if len(sessions) > limit {
		sessions = sessions[len(sessions)-limit:]
	}

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.Encode(sessions)
		return cli.ExitOK
	}

	if len(sessions) == 0 {
		fmt.Fprintln(out, "No sessions found.")
		return cli.ExitOK
	}

	fmt.Fprintf(out, "%-12s %-4s %-10s %-5s %s\n", "DATE", "ITER", "TAG", "FRIC", "TITLE")
	for _, s := range sessions {
		title := s.Title
		if len(title) > 50 {
			title = title[:47] + "..."
		}
		tag := s.Tag
		if tag == "" {
			tag = "-"
		}
		fric := "-"
		if s.FrictionScore > 0 {
			fric = fmt.Sprintf("%d", s.FrictionScore)
		}
		fmt.Fprintf(out, "%-12s %-4d %-10s %-5s %s\n", s.Date, s.Iteration, tag, fric, title)
	}
	return cli.ExitOK
}
