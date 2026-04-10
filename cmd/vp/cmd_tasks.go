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

var tasksFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--done", Help: "Include completed and cancelled tasks"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdTasks() *cli.Command {
	return &cli.Command{
		Name:        "tasks",
		Synopsis:    "vp tasks [--project P] [--done] [--json]",
		Description: "List tasks for a project.",
		Flags:       tasksFlags,
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(tasksFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp tasks: %v\n", err)
				return cli.ExitUser
			}

			proj := fv.Get("--project")
			if proj == "" {
				proj, _ = project.DetectProject(".")
			}
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp tasks: could not detect project (use --project)")
				return cli.ExitUser
			}

			vault, err := storage.OpenVault("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp tasks: %v\n", err)
				return cli.ExitUser
			}

			return runTasks(vault, proj, fv.Bool("--done"), fv.Bool("--json"), os.Stdout)
		},
	}
}

func runTasks(vault *storage.Vault, proj string, includeDone, asJSON bool, out io.Writer) int {
	tasks, err := vault.ListTasks(proj, includeDone)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp tasks: %v\n", err)
		return cli.ExitSystem
	}

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.Encode(tasks)
		return cli.ExitOK
	}

	if len(tasks) == 0 {
		fmt.Fprintln(out, "No tasks found.")
		return cli.ExitOK
	}

	fmt.Fprintf(out, "%-12s %-35s %-12s %s\n", "PRIORITY", "SLUG", "STATUS", "TITLE")
	for _, t := range tasks {
		slug := t.Slug
		if len(slug) > 35 {
			slug = slug[:32] + "..."
		}
		title := t.Title
		if len(title) > 40 {
			title = title[:37] + "..."
		}
		pri := t.Priority
		if pri == "" {
			pri = "-"
		}
		fmt.Fprintf(out, "%-12s %-35s %-12s %s\n", pri, slug, t.Status, title)
	}
	return cli.ExitOK
}
