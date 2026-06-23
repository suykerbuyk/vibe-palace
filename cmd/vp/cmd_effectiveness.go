// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var effectivenessFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdEffectiveness() *cli.Command {
	return &cli.Command{
		Name:        "effectiveness",
		Synopsis:    "vp effectiveness [--project P] [--json]",
		Description: "Report context effectiveness: per-week and overall outcome (100 - friction) for sessions captured WITH vs WITHOUT context (decisions or files changed), plus the delta between the two.",
		Flags:       effectivenessFlags,
		Examples: []cli.Example{
			{Cmd: "vp effectiveness", Comment: "With-vs-without-context outcome delta for the current project"},
			{Cmd: "vp effectiveness -p myapp --json", Comment: "Effectiveness data as JSON"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(effectivenessFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp effectiveness: %v\n", err)
				return cli.ExitUser
			}

			proj := fv.Get("--project")
			if proj == "" {
				proj, _ = project.DetectProject(".")
			}
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp effectiveness: could not detect project (use --project)")
				return cli.ExitUser
			}

			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp effectiveness: %v\n", err)
				return cli.ExitUser
			}

			return runEffectiveness(vault, proj, fv.Bool("--json"), os.Stdout)
		},
	}
}

func runEffectiveness(vault *storage.Vault, proj string, asJSON bool, out io.Writer) int {
	sessions, err := vault.ListSessions(proj, "", "", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp effectiveness: %v\n", err)
		return cli.ExitSystem
	}

	result := capture.ComputeEffectiveness(proj, sessions)

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.Encode(result)
		return cli.ExitOK
	}

	o := result.Overall
	if o.TotalSessions == 0 {
		fmt.Fprintln(out, "No sessions found.")
		return cli.ExitOK
	}

	fmt.Fprintf(out, "Effectiveness (outcome = 100 - friction), with vs without context:\n\n")
	fmt.Fprintf(out, "Overall: %d sessions, avg outcome %.1f\n", o.TotalSessions, o.AvgOutcome)
	fmt.Fprintf(out, "  with context:    %d sessions, avg outcome %.1f\n", o.WithContext, o.AvgCtxOutcome)
	fmt.Fprintf(out, "  without context: %d sessions, avg outcome %.1f\n", o.WithoutContext, o.AvgNoCtxOutcome)
	fmt.Fprintf(out, "  delta (with − without): %+.1f\n\n", o.Delta)

	if len(result.Weeks) == 0 {
		return cli.ExitOK
	}
	fmt.Fprintf(out, "%-12s %-9s %-8s %-8s %-8s\n", "WEEK", "SESSIONS", "OUTCOME", "WITH_CTX", "NO_CTX")
	for _, w := range result.Weeks {
		fmt.Fprintf(out, "%-12s %-9d %-8.1f %-8.1f %-8.1f\n",
			w.WeekStart, w.SessionCount, w.AvgOutcome, w.AvgCtxOutcome, w.AvgNoCtxOutcome)
	}
	return cli.ExitOK
}
