// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/taskgraph"
)

var boardFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdBoard() *cli.Command {
	return &cli.Command{
		Name:     "board",
		Synopsis: "vp board [--project P] [--json]",
		Description: "Render a chronological Active/Icebox/History report: every open epic and " +
			"standalone task with its own creation date and its children's dates, the icebox " +
			"(always shown, never hidden), then everything completed, most-recent-first. " +
			"Unlike `vp tasks`, this is a history view, not a work-queue view — nothing is " +
			"filtered by default.",
		Flags: boardFlags,
		Examples: []cli.Example{
			{Cmd: "vp board", Comment: "Active epics/standalone with dates, then icebox, then history"},
			{Cmd: "vp board --project other-proj", Comment: "Run from outside that project's directory"},
			{Cmd: "vp board --json", Comment: "Emit the raw BoardView as JSON"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(boardFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp board: %v\n", err)
				return cli.ExitUser
			}
			proj := detectTasksProject(fv)
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp board: could not detect project (use --project)")
				return cli.ExitUser
			}
			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp board: %v\n", err)
				return cli.ExitUser
			}
			return runBoard(vault, proj, fv.Bool("--json"), os.Stdout)
		},
	}
}

func runBoard(vault *storage.Vault, proj string, asJSON bool, out io.Writer) int {
	g, err := taskgraph.BuildFromVault(vault, proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp board: %v\n", err)
		return cli.ExitSystem
	}
	view := g.Board()

	if asJSON {
		return writeJSON(out, view)
	}
	renderBoard(out, g, view)
	return cli.ExitOK
}

// dateOrUnknown renders a CalendarDay string, or the literal "unknown" when
// the field is absent — an empty CreateTime/ModTime means the task predates
// board-reporting-createtime-modtime-fields and was never migrated, not that
// its date is the empty string.
func dateOrUnknown(d string) string {
	if d == "" {
		return "unknown"
	}
	return d
}

// renderBoard is the text renderer for `vp board`: three fixed sections
// (ACTIVE, ICEBOX, HISTORY), each printed unconditionally — even empty — so
// the shape of the report never depends on the data. Board() has already
// bucketed and sorted every group; renderBoard prints that order as-is.
func renderBoard(out io.Writer, g *taskgraph.Graph, view taskgraph.BoardView) {
	sections := []struct {
		title   string
		groups  []taskgraph.Group
		history bool
	}{
		{"ACTIVE", view.Active, false},
		{"ICEBOX", view.Icebox, false},
		{"HISTORY", view.History, true},
	}
	for i, sec := range sections {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, sec.title)
		renderBoardGroups(out, g, sec.groups, sec.history)
	}
	printProblems(out, g)
}

// renderBoardGroups renders one section's groups, imitating renderGroups'
// idiom (cmd_tasks.go): measure-then-pad column widths (scoped to just this
// section, not the whole board), tree branches via ├─/└─, tier labels via
// g.IsStory, and indentation measured from the group's own root depth. It is
// a distinct function rather than a call into renderGroups because
// renderGroups has the status/priority formatting inlined in its own loop,
// with no factored-out primitive this can reuse, and because board rows carry
// dates and history labels renderGroups has no concept of.
func renderBoardGroups(out io.Writer, g *taskgraph.Graph, groups []taskgraph.Group, history bool) {
	if len(groups) == 0 {
		fmt.Fprintln(out, "(none)")
		return
	}

	// rootDepth/indentOf/bodyMembers mirror renderGroups exactly: the group
	// root is the header, never a body row, and a direct child of the group
	// root lands at indent 0. Standalone (Epic=="") measures from -1 so its
	// members, which have no shared root, also sit at indent 0.
	rootDepth := func(grp taskgraph.Group) int {
		if grp.Epic != "" {
			if rn, ok := g.Nodes[grp.Epic]; ok {
				return rn.Depth
			}
		}
		return -1
	}
	indentOf := func(grp taskgraph.Group, m string) string {
		return strings.Repeat("  ", max(0, g.Nodes[m].Depth-rootDepth(grp)-1))
	}
	bodyMembers := func(grp taskgraph.Group) []string {
		body := make([]string, 0, len(grp.Members))
		for _, m := range grp.Members {
			if m != grp.Epic {
				body = append(body, m)
			}
		}
		return body
	}

	// Measure, then pad — scoped to just the groups in THIS section, not the
	// whole board: Active/Icebox/History are rendered as three independent
	// tables, each with its own column widths.
	wSlug, wPri := 0, 0
	for _, grp := range groups {
		for _, m := range bodyMembers(grp) {
			wSlug = max(wSlug, len(indentOf(grp, m))+len(m))
			wPri = max(wPri, len(priorityOrDash(g.Nodes[m].Meta.Priority)))
		}
	}

	for i, grp := range groups {
		if i > 0 {
			fmt.Fprintln(out)
		}
		members := bodyMembers(grp)

		switch grp.Epic {
		case "":
			fmt.Fprintln(out, "STANDALONE")
		default:
			n := g.Nodes[grp.Epic]
			tier := "EPIC"
			if g.IsStory(grp.Epic) {
				tier = "STORY"
			}
			if history {
				// HistoryLabel is called UNCONDITIONALLY: it already returns the
				// bare status for the ordinary case and only appends the
				// superseded-by suffix when that link exists.
				fmt.Fprintf(out, "%s  %s → %s  (completed %s)\n",
					tier, grp.Epic, n.HistoryLabel(), dateOrUnknown(n.Meta.ModTime))
			} else {
				open, done := 0, 0
				for _, m := range members {
					if g.Nodes[m].Meta.Done {
						done++
					} else {
						open++
					}
				}
				fmt.Fprintf(out, "%s  %s  (created %s, %d open, %d done)\n",
					tier, grp.Epic, dateOrUnknown(n.Meta.CreateTime), open, done)
			}
		}

		for j, m := range members {
			n := g.Nodes[m]
			branch := "├─"
			if j == len(members)-1 {
				branch = "└─"
			}
			var row string
			if history {
				row = fmt.Sprintf("%s %-*s  %s  %-*s  completed %s",
					branch, wSlug, indentOf(grp, m)+m, n.HistoryLabel(), wPri, priorityOrDash(n.Meta.Priority),
					dateOrUnknown(n.Meta.ModTime))
			} else {
				dates := "created " + dateOrUnknown(n.Meta.CreateTime)
				if n.Meta.Done {
					dates += ", completed " + dateOrUnknown(n.Meta.ModTime)
				}
				row = fmt.Sprintf("%s %-*s  %-*s  %s",
					branch, wSlug, indentOf(grp, m)+m, wPri, priorityOrDash(n.Meta.Priority), dates)
			}
			fmt.Fprintln(out, "  "+row)
		}
	}
}
