// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var frictionFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--top", Short: "-n", Arg: "N", Help: "Number of sessions in the triage list", Default: "10"},
	{Name: "--json", Help: "Output JSON"},
}

// frictionResult is the combined --json payload: the most-recent-week friction
// window plus the top-friction triage list.
type frictionResult struct {
	RecentWeek capture.FrictionWindow    `json:"recent_week"`
	Top        []capture.FrictionSession `json:"top"`
}

func cmdFriction() *cli.Command {
	return &cli.Command{
		Name:        "friction",
		Synopsis:    "vp friction [--project P] [--top N] [--json]",
		Description: "Report per-session friction for a project: the most-recent-week average friction plus a top-friction triage list of the highest-friction sessions worth reviewing.",
		Flags:       frictionFlags,
		Examples: []cli.Example{
			{Cmd: "vp friction", Comment: "Recent-week friction and top-friction triage for the current project"},
			{Cmd: "vp friction -p myapp --top 5 --json", Comment: "Top 5 friction sessions as JSON"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(frictionFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp friction: %v\n", err)
				return cli.ExitUser
			}

			proj := fv.Get("--project")
			if proj == "" {
				proj, _ = project.DetectProject(".")
			}
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp friction: could not detect project (use --project)")
				return cli.ExitUser
			}

			top := fv.Int("--top")
			if top == 0 {
				top = 10
			}

			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp friction: %v\n", err)
				return cli.ExitUser
			}

			return runFriction(vault, proj, top, fv.Bool("--json"), os.Stdout)
		},
	}
}

func runFriction(vault *storage.Vault, proj string, top int, asJSON bool, out io.Writer) int {
	sessions, err := vault.ListSessions(proj, "", "", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp friction: %v\n", err)
		return cli.ExitSystem
	}

	recent := capture.GetFrictionWindows(sessions, time.Now(), []int{7})[0]
	topSessions := capture.TopFrictionSessions(sessions, top)

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.Encode(frictionResult{RecentWeek: recent, Top: topSessions})
		return cli.ExitOK
	}

	fmt.Fprintf(out, "Recent week (7d): %d sessions, avg friction %.1f\n\n",
		recent.SessionCount, recent.AvgFriction)

	if len(topSessions) == 0 {
		fmt.Fprintln(out, "No sessions found.")
		return cli.ExitOK
	}

	fmt.Fprintf(out, "Top-friction triage:\n")
	fmt.Fprintf(out, "%-12s %-4s %-5s %s\n", "DATE", "ITER", "FRIC", "TITLE")
	for _, s := range topSessions {
		title := s.Title
		if len(title) > 50 {
			title = title[:47] + "..."
		}
		fmt.Fprintf(out, "%-12s %-4d %-5d %s\n", s.Date, s.Iteration, s.FrictionScore, title)
	}
	return cli.ExitOK
}
