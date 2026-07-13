// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/taskgraph"
)

var tasksFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--done", Help: "Include completed and cancelled tasks (implies --flat)"},
	{Name: "--all", Help: "Include iceboxed tasks (known, not scheduled)"},
	{Name: "--flat", Help: "List tasks as a flat table instead of grouping them by epic"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdTasks() *cli.Command {
	return &cli.Command{
		Name:     "tasks",
		Synopsis: "vp tasks [--project P] [--done] [--all] [--flat] [--json]",
		Description: "List tasks for a project, grouped by epic and ordered so that a dependency " +
			"always appears above the task it blocks. An epic is any task something names as its " +
			"parent. Iceboxed tasks are hidden by default; the count of what was hidden is always " +
			"shown.",
		Flags: tasksFlags,
		Examples: []cli.Example{
			{Cmd: "vp tasks", Comment: "Open work, grouped by epic, most urgent group first"},
			{Cmd: "vp tasks --all", Comment: "Include the icebox"},
			{Cmd: "vp tasks --flat --done --json", Comment: "Every task, including the archive, as JSON"},
		},
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

			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp tasks: %v\n", err)
				return cli.ExitUser
			}

			opts := taskListOpts{
				includeDone:   fv.Bool("--done"),
				includeIcebox: fv.Bool("--all"),
				// The archive has no place in a graph of OPEN work — a hundred
				// retired tasks would bury the ten live ones. Asking for it is
				// asking for the flat table.
				flat:   fv.Bool("--flat") || fv.Bool("--done"),
				asJSON: fv.Bool("--json"),
			}
			return runTasks(vault, proj, opts, os.Stdout)
		},
	}
}

type taskListOpts struct {
	includeDone   bool
	includeIcebox bool
	flat          bool
	asJSON        bool
}

func runTasks(vault *storage.Vault, proj string, opts taskListOpts, out io.Writer) int {
	if opts.flat {
		return runTasksFlat(vault, proj, opts, out)
	}

	g, err := taskgraph.BuildFromVault(vault, proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp tasks: %v\n", err)
		return cli.ExitSystem
	}

	if opts.asJSON {
		return writeJSON(out, g)
	}
	printTaskTree(out, g, opts.includeIcebox)
	return cli.ExitOK
}

func runTasksFlat(vault *storage.Vault, proj string, opts taskListOpts, out io.Writer) int {
	tasks, err := vault.ListTasks(proj, opts.includeDone)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp tasks: %v\n", err)
		return cli.ExitSystem
	}
	if !opts.includeIcebox {
		tasks = storage.DropIcebox(tasks)
	}

	if opts.asJSON {
		return writeJSON(out, tasks)
	}
	if len(tasks) == 0 {
		fmt.Fprintln(out, "No tasks found.")
		return cli.ExitOK
	}

	// Measure, then pad. Slugs and titles are NEVER truncated: the old fixed
	// 35/40-byte caps cut "bootstrap-payload-exceeds-its-own-token-budget" down
	// to an ellipsis, which is unusable as an identifier — and being byte-based,
	// they could split a multi-byte rune in half. Same idiom as vp skills.
	wPri, wSlug, wStatus := len("PRIORITY"), len("SLUG"), len("STATUS")
	for _, t := range tasks {
		wPri = max(wPri, len(priorityOrDash(t.Priority)))
		wSlug = max(wSlug, len(t.Slug))
		wStatus = max(wStatus, len(t.Status))
	}

	fmt.Fprintf(out, "%-*s  %-*s  %-*s  %s\n", wPri, "PRIORITY", wSlug, "SLUG", wStatus, "STATUS", "TITLE")
	for _, t := range tasks {
		fmt.Fprintf(out, "%-*s  %-*s  %-*s  %s\n",
			wPri, priorityOrDash(t.Priority), wSlug, t.Slug, wStatus, t.Status, t.Title)
	}
	return cli.ExitOK
}

func writeJSON(out io.Writer, v any) int {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "vp tasks: %v\n", err)
		return cli.ExitSystem
	}
	return cli.ExitOK
}

func priorityOrDash(p string) string {
	if p == "" {
		return "-"
	}
	return p
}

// printTaskTree renders the derived structure: epics with their members
// indented beneath them, the standalone bucket last, then any structural
// problems, then the icebox count.
func printTaskTree(out io.Writer, g *taskgraph.Graph, includeIcebox bool) {
	groups := g.Grouped(includeIcebox)
	if len(groups) == 0 {
		fmt.Fprintln(out, "No open tasks.")
		printProblems(out, g)
		printIceboxCount(out, g, includeIcebox)
		return
	}

	// Indentation is relative to the group: a direct child of an epic sits at
	// depth 1 and gets no extra indent, because the epic is the header above it,
	// not a row beside it. Only a grandchild (an epic nested under an epic) is
	// stepped in.
	indentOf := func(m string) string {
		return strings.Repeat("  ", max(0, g.Nodes[m].Depth-1))
	}

	// One width for the whole listing so every row lines up across groups. No
	// truncation — the widest slug sets the column.
	wSlug, wStatus := 0, 0
	for _, grp := range groups {
		for _, m := range grp.Members {
			wSlug = max(wSlug, len(indentOf(m))+len(m))
			wStatus = max(wStatus, len(g.Nodes[m].Meta.Status))
		}
	}

	for i, grp := range groups {
		if i > 0 {
			fmt.Fprintln(out)
		}
		switch {
		case grp.Epic == "":
			fmt.Fprintln(out, "STANDALONE")
		default:
			epic := g.Nodes[grp.Epic]
			header := fmt.Sprintf("EPIC  %s  (%d open, %s)",
				grp.Epic, len(grp.Members), priorityOrDash(epic.Meta.Priority))
			if len(epic.Blockers) > 0 {
				header += fmt.Sprintf("  [blocked by: %s]", strings.Join(epic.Blockers, ", "))
			}
			fmt.Fprintln(out, header)
		}

		for j, m := range grp.Members {
			n := g.Nodes[m]
			branch := "├─"
			if j == len(grp.Members)-1 {
				branch = "└─"
			}
			row := fmt.Sprintf("  %s %-*s  %-*s  %-8s",
				branch, wSlug, indentOf(m)+m, wStatus, n.Meta.Status, priorityOrDash(n.Meta.Priority))
			if len(n.Blockers) > 0 {
				row += fmt.Sprintf("  [blocked by: %s]", strings.Join(n.Blockers, ", "))
			}
			fmt.Fprintln(out, strings.TrimRight(row, " "))
		}
	}

	printProblems(out, g)
	printIceboxCount(out, g, includeIcebox)
}

// printProblems reports structural findings. They are printed loudly rather than
// folded into the listing: a dangling reference or a dependency cycle is not a
// task, it is a lie in the data, and a reader who skims the tree must still see it.
func printProblems(out io.Writer, g *taskgraph.Graph) {
	if !g.HasProblems() {
		return
	}
	fmt.Fprintln(out, "\nPROBLEMS")
	for _, c := range g.Cycles {
		fmt.Fprintf(out, "  %s cycle: %s\n", c.Kind, strings.Join(c.Slugs, " -> "))
	}
	for _, r := range g.Dangling {
		fmt.Fprintf(out, "  dangling %s: %s -> %s (no such task)\n", r.Kind, r.From, r.To)
	}
	for _, r := range g.StaleParents {
		fmt.Fprintf(out, "  stale parent: %s is active but its epic %s is finished\n", r.From, r.To)
	}
}

// printIceboxCount says how much was hidden. An icebox nobody is told about is
// just a deletion with extra steps.
func printIceboxCount(out io.Writer, g *taskgraph.Graph, includeIcebox bool) {
	if includeIcebox {
		return
	}
	n := 0
	for _, node := range g.Nodes {
		if !node.Meta.Done && node.Meta.Status == storage.StatusIcebox {
			n++
		}
	}
	if n > 0 {
		fmt.Fprintf(out, "\n(%d iceboxed — show with --all)\n", n)
	}
}
