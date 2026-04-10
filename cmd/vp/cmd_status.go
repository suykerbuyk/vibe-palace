// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var statusFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdStatus() *cli.Command {
	return &cli.Command{
		Name:        "status",
		Synopsis:    "vp status [--project P] [--json]",
		Description: "Show palace overview for a project.",
		Flags:       statusFlags,
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(statusFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp status: %v\n", err)
				return cli.ExitUser
			}

			proj := fv.Get("--project")
			if proj == "" {
				proj, _ = project.DetectProject(".")
			}
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp status: could not detect project (use --project)")
				return cli.ExitUser
			}

			vault, err := storage.OpenVault("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp status: %v\n", err)
				return cli.ExitUser
			}

			return runStatus(vault, proj, fv.Bool("--json"), os.Stdout)
		},
	}
}

type statusResult struct {
	Project  string              `json:"project"`
	Palace   *palace.PalaceStats `json:"palace,omitempty"`
	Tasks    int                 `json:"active_tasks"`
	Sessions int                 `json:"recent_sessions"`
	KG       *storage.KGStats    `json:"knowledge_graph,omitempty"`
}

func runStatus(vault *storage.Vault, proj string, asJSON bool, out io.Writer) int {
	result := statusResult{Project: proj}

	if g, err := palace.BuildGraph(vault, proj); err == nil {
		stats := g.Stats()
		result.Palace = &stats
	}

	if tasks, err := vault.ListTasks(proj, false); err == nil {
		result.Tasks = len(tasks)
	}

	if sessions, err := vault.ListSessions(proj, "", "", 0); err == nil {
		result.Sessions = len(sessions)
	}

	if stats, err := vault.KGStats(proj); err == nil {
		result.KG = &stats
	}

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.Encode(result)
		return cli.ExitOK
	}

	fmt.Fprintf(out, "Project: %s\n", result.Project)
	if result.Palace != nil {
		fmt.Fprintf(out, "Palace:  %d wings, %d rooms, %d drawers\n",
			result.Palace.Wings, result.Palace.Rooms, result.Palace.Drawers)
	}
	fmt.Fprintf(out, "Tasks:   %d active\n", result.Tasks)
	fmt.Fprintf(out, "Sessions: %d total\n", result.Sessions)
	if result.KG != nil {
		fmt.Fprintf(out, "KG:      %d entities, %d triples (%d current)\n",
			result.KG.EntityCount, result.KG.TripleCount, result.KG.CurrentFacts)
	}
	return cli.ExitOK
}
