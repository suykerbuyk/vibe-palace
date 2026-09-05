// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/taskgraph"
)

var tasksFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--done", Help: "Include completed and cancelled tasks (implies --flat, except under --epic/--standalone where it keeps the tree)"},
	{Name: "--all", Help: "Include iceboxed tasks (known, not scheduled)"},
	{Name: "--flat", Help: "List tasks as a flat table instead of grouping them by epic"},
	{Name: "--epic", Arg: "SLUG", Help: "Show only the subtree rooted at SLUG (an epic or story), re-rooted so it reads as its own tree"},
	{Name: "--standalone", Help: "Show only the standalone bucket — tasks that are nobody's child and nobody's parent"},
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
		BareInvocation: true,
		Flags:          tasksFlags,
		Examples: []cli.Example{
			{Cmd: "vp tasks", Comment: "Open work, grouped by epic, most urgent group first"},
			{Cmd: "vp tasks --all", Comment: "Include the icebox"},
			{Cmd: "vp tasks --epic big-epic", Comment: "Just the subtree under big-epic, re-rooted"},
			{Cmd: "vp tasks --standalone", Comment: "Only the tasks that belong to no epic"},
			{Cmd: "vp tasks --flat --done --json", Comment: "Every task, including the archive, as JSON"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(tasksFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp tasks: %v\n", err)
				return cli.ExitUser
			}

			epic := fv.Get("--epic")
			standalone := fv.Bool("--standalone")
			flat := fv.Bool("--flat")

			// --epic and --standalone are two spellings of the same intent (show
			// ONE grouped tree); asking for both is ambiguous.
			if epic != "" && standalone {
				fmt.Fprintln(os.Stderr, "vp tasks: --epic and --standalone are mutually exclusive")
				return cli.ExitUser
			}
			// Both are grouped-tree views. --flat is the opposite request (a flat
			// table), so it cannot be combined with either.
			if (epic != "" || standalone) && flat {
				fmt.Fprintln(os.Stderr, "vp tasks: --epic/--standalone render a grouped tree and cannot be combined with --flat")
				return cli.ExitUser
			}

			proj := detectTasksProject(fv)
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
				// asking for the flat table. The --epic/--standalone views
				// DECOUPLE this: there --done means "include the archive" while
				// STILL rendering a tree (see runTasksFiltered).
				flat:       flat || (fv.Bool("--done") && epic == "" && !standalone),
				epic:       epic,
				standalone: standalone,
				asJSON:     fv.Bool("--json"),
			}
			return runTasks(vault, proj, opts, os.Stdout)
		},
	}
}

// detectTasksProject resolves the project the tasks commands operate on:
// the explicit --project value, else auto-detected from the working directory.
// Returns "" when neither yields a name, so callers emit one consistent error.
func detectTasksProject(fv *cli.FlagValues) string {
	proj := fv.Get("--project")
	if proj == "" {
		proj, _ = project.DetectProject(".")
	}
	return proj
}

type taskListOpts struct {
	includeDone   bool
	includeIcebox bool
	flat          bool
	epic          string
	standalone    bool
	asJSON        bool
}

func runTasks(vault *storage.Vault, proj string, opts taskListOpts, out io.Writer) int {
	// The filtered grouped-tree views branch BEFORE the flat/tree split: they are
	// neither the flat table nor the full grouped tree, and unlike the default
	// tree they honor --done as "include the archive" without collapsing to flat.
	if opts.epic != "" || opts.standalone {
		return runTasksFiltered(vault, proj, opts, out)
	}

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

// runTasksFiltered renders exactly one focused slice of the graph as a grouped
// tree: the standalone bucket (--standalone) or the subtree rooted at an epic or
// story (--epic SLUG). Here --done means includeArchived=true and STILL renders a
// tree — the archive-buries-the-open-work rule that turns --done into a flat
// table for the full listing does not apply to a single, deliberately narrowed
// view.
func runTasksFiltered(vault *storage.Vault, proj string, opts taskListOpts, out io.Writer) int {
	g, err := taskgraph.BuildFromVault(vault, proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp tasks: %v\n", err)
		return cli.ExitSystem
	}
	includeArchived := opts.includeDone

	var groups []taskgraph.Group
	if opts.standalone {
		for _, grp := range g.GroupedArchived(opts.includeIcebox, includeArchived) {
			if grp.Epic == "" {
				groups = append(groups, grp)
			}
		}
	} else {
		slug := opts.epic
		if _, ok := g.Nodes[slug]; !ok {
			fmt.Fprintf(os.Stderr, "vp tasks: no such task: %s\n", slug)
			return cli.ExitUser
		}
		// A leaf has no subtree to re-root. There is deliberately NO `vp tasks
		// read` CLI command, so the hint points only at things that exist: the
		// full tree, or the slash command that reads a task body.
		if g.Role(slug) == "task" {
			fmt.Fprintf(os.Stderr,
				"vp tasks: --epic needs an epic or story (a task with children); %q is a leaf task\n"+
					"          run `vp tasks` to see the tree, or `/vpc-tasks-read %s` to read this task's body\n",
				slug, slug)
			return cli.ExitUser
		}
		sub, _ := g.Subtree(slug, opts.includeIcebox, includeArchived)
		groups = []taskgraph.Group{sub}
	}

	if opts.asJSON {
		return writeJSON(out, groups)
	}
	renderGroups(out, g, groups, opts.includeIcebox)
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
// problems, then the icebox count. The tree body itself is rendered by
// renderGroups, shared with the filtered --epic/--standalone views.
func printTaskTree(out io.Writer, g *taskgraph.Graph, includeIcebox bool) {
	renderGroups(out, g, g.Grouped(includeIcebox), includeIcebox)
	printProblems(out, g)
	printIceboxCount(out, g, includeIcebox)
}

// renderGroups is the shared tree renderer behind both the default full listing
// and the filtered --epic/--standalone views. It prints each group as a tier
// header (EPIC/STORY <slug>, or STANDALONE) followed by its members as a tree.
//
// Two behaviors matter:
//
//   - TIER LABEL is derived from Role, not depth: a group root that is its own
//     root epic prints EPIC, one nested under a resolvable parent prints STORY.
//
//   - INDENTATION is measured from the GROUP ROOT's depth, not the true root's.
//     A group root (its epic) is the HEADER, not a body row, so its direct
//     children sit flush beneath it at indent 0; only deeper descendants step in.
//     Subtree returns the root itself as its first member — it is skipped as a
//     body row here, because it already names the header above it.
func renderGroups(out io.Writer, g *taskgraph.Graph, groups []taskgraph.Group, includeIcebox bool) {
	_ = includeIcebox // filtering already happened upstream (Grouped/Subtree)
	if len(groups) == 0 {
		fmt.Fprintln(out, "No open tasks.")
		return
	}

	// rootDepth is the depth indentation is measured from. The group root heads
	// the group as its header, so a direct child (root depth + 1) lands at indent
	// 0. Standalone (Epic=="") has no root node; its members are all depth-0, so
	// measuring from -1 keeps them at indent 0 too.
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
	// bodyMembers is a group's members minus the root itself: the root is the
	// header, never a row beside its own work.
	bodyMembers := func(grp taskgraph.Group) []string {
		body := make([]string, 0, len(grp.Members))
		for _, m := range grp.Members {
			if m != grp.Epic {
				body = append(body, m)
			}
		}
		return body
	}

	// One width for the whole listing so every row lines up across groups. No
	// truncation — the widest slug sets the column.
	wSlug, wStatus := 0, 0
	for _, grp := range groups {
		for _, m := range bodyMembers(grp) {
			wSlug = max(wSlug, len(indentOf(grp, m))+len(m))
			wStatus = max(wStatus, len(g.Nodes[m].Meta.Status))
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
			epic := g.Nodes[grp.Epic]
			tier := "EPIC"
			if g.IsStory(grp.Epic) {
				tier = "STORY"
			}
			header := fmt.Sprintf("%s  %s  (%d open, %s)",
				tier, grp.Epic, len(members), priorityOrDash(epic.Meta.Priority))
			if len(epic.Blockers) > 0 {
				header += fmt.Sprintf("  [blocked by: %s]", strings.Join(epic.Blockers, ", "))
			}
			fmt.Fprintln(out, header)
		}

		for j, m := range members {
			n := g.Nodes[m]
			branch := "├─"
			if j == len(members)-1 {
				branch = "└─"
			}
			row := fmt.Sprintf("  %s %-*s  %-*s  %-8s",
				branch, wSlug, indentOf(grp, m)+m, wStatus, n.Meta.Status, priorityOrDash(n.Meta.Priority))
			if len(n.Blockers) > 0 {
				row += fmt.Sprintf("  [blocked by: %s]", strings.Join(n.Blockers, ", "))
			}
			fmt.Fprintln(out, strings.TrimRight(row, " "))
		}
	}
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

// ---------------------------------------------------------------------------
// tasks epics — the root-epic roll-up.
// ---------------------------------------------------------------------------

var tasksEpicsFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--all", Help: "Include iceboxed descendants in the counts"},
	{Name: "--done", Help: "Include archived (done/cancelled) descendants in the counts"},
	{Name: "--json", Help: "Output JSON"},
}

// epicSummary is one row of `tasks epics`, and one element of its --json array.
// Open/Total are TRANSITIVE descendant counts (the whole subtree), excluding the
// epic node itself — the number a reader wants is "how much work is under this",
// not "does the epic file itself count".
type epicSummary struct {
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
	Open     int    `json:"open"`
	Total    int    `json:"total"`
}

func cmdTasksEpics() *cli.Command {
	return &cli.Command{
		Name:     "tasks epics",
		Synopsis: "vp tasks epics [--project P] [--all] [--done] [--json]",
		Description: "List the ROOT epics of a project — tasks that head their own chain and " +
			"have children — with the transitive open/total descendant counts under each. " +
			"Nested stories are not listed here; they appear inside their parent epic's tree " +
			"via `vp tasks --epic`. OPEN counts the open descendants; TOTAL includes the " +
			"archive. Neither count includes the epic node itself.",
		Flags: tasksEpicsFlags,
		Examples: []cli.Example{
			{Cmd: "vp tasks epics", Comment: "Root epics with open/total descendant counts"},
			{Cmd: "vp tasks epics --done", Comment: "Count the archive too in TOTAL"},
			{Cmd: "vp tasks epics --json", Comment: "Emit the roll-up as JSON"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(tasksEpicsFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp tasks epics: %v\n", err)
				return cli.ExitUser
			}
			proj := detectTasksProject(fv)
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp tasks epics: could not detect project (use --project)")
				return cli.ExitUser
			}
			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp tasks epics: %v\n", err)
				return cli.ExitUser
			}
			return runTasksEpics(vault, proj, fv.Bool("--all"), fv.Bool("--done"), fv.Bool("--json"), os.Stdout)
		},
	}
}

func runTasksEpics(vault *storage.Vault, proj string, includeIcebox, includeDone, asJSON bool, out io.Writer) int {
	g, err := taskgraph.BuildFromVault(vault, proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp tasks epics: %v\n", err)
		return cli.ExitSystem
	}

	// descendants counts a subtree's members EXCLUDING the root epic itself.
	// Subtree returns "root + transitive descendants", so the root is dropped.
	descendants := func(root string, members []string) int {
		n := 0
		for _, m := range members {
			if m != root {
				n++
			}
		}
		return n
	}

	var rows []epicSummary
	for _, slug := range g.Epics {
		if !g.IsRootEpic(slug) {
			continue // g.Epics also carries nested stories; roots only here.
		}
		n := g.Nodes[slug]
		// A fully-archived epic (retired/cancelled) is history: it has no place in
		// the open-work roll-up by default, the same rule the tree listing follows.
		// --done surfaces it. (The COUNTS below are fixed: OPEN never counts the
		// archive, TOTAL always does — --done governs only which epics are listed.)
		if n.Meta.Done && !includeDone {
			continue
		}
		total, _ := g.Subtree(slug, includeIcebox, true)
		open, _ := g.Subtree(slug, includeIcebox, false)
		rows = append(rows, epicSummary{
			Slug:     slug,
			Title:    n.Meta.Title,
			Priority: n.Meta.Priority,
			Status:   n.Meta.Status,
			Open:     descendants(slug, open.Members),
			Total:    descendants(slug, total.Members),
		})
	}

	if asJSON {
		if rows == nil {
			rows = []epicSummary{}
		}
		return writeJSON(out, rows)
	}

	if len(rows) == 0 {
		fmt.Fprintln(out, "No epics.")
		return cli.ExitOK
	}

	// Measure, then pad. Slugs and counts are never truncated.
	wSlug, wCount, wPri, wStatus := len("SLUG"), len("OPEN/TOTAL"), len("PRIORITY"), len("STATUS")
	counts := make([]string, len(rows))
	for i, r := range rows {
		counts[i] = fmt.Sprintf("%d/%d", r.Open, r.Total)
		wSlug = max(wSlug, len(r.Slug))
		wCount = max(wCount, len(counts[i]))
		wPri = max(wPri, len(priorityOrDash(r.Priority)))
		wStatus = max(wStatus, len(r.Status))
	}

	fmt.Fprintf(out, "%-*s  %-*s  %-*s  %-*s  %s\n",
		wSlug, "SLUG", wCount, "OPEN/TOTAL", wPri, "PRIORITY", wStatus, "STATUS", "TITLE")
	for i, r := range rows {
		fmt.Fprintf(out, "%-*s  %-*s  %-*s  %-*s  %s\n",
			wSlug, r.Slug, wCount, counts[i], wPri, priorityOrDash(r.Priority), wStatus, r.Status, r.Title)
	}
	return cli.ExitOK
}

// ---------------------------------------------------------------------------
// tasks edit — open a task body in $VISUAL/$EDITOR and write it back validated.
// ---------------------------------------------------------------------------

var tasksEditFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
}

func cmdTasksEdit() *cli.Command {
	return &cli.Command{
		Name:     "tasks edit",
		Synopsis: "vp tasks edit <slug> [--project P]",
		Description: "Open a task's whole file in $VISUAL (else $EDITOR) and write the result " +
			"back under lock, validated as a whole. Refuses archived (done/cancelled) tasks — " +
			"their body is history. On identical content it is a no-op; on invalid content the " +
			"edit is preserved in a temp file whose path is printed so nothing is lost.",
		Flags: tasksEditFlags,
		Examples: []cli.Example{
			{Cmd: "vp tasks edit fix-login-bug", Comment: "Edit the task body in your editor"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(tasksEditFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp tasks edit: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) == 0 {
				fmt.Fprintln(os.Stderr, "vp tasks edit: <slug> argument required")
				return cli.ExitUser
			}
			proj := detectTasksProject(fv)
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp tasks edit: could not detect project (use --project)")
				return cli.ExitUser
			}
			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp tasks edit: %v\n", err)
				return cli.ExitUser
			}
			return runTasksEdit(vault, proj, pos[0], os.Stdout, os.Stderr)
		},
	}
}

// resolveEditor resolves the interactive editor: $VISUAL wins over $EDITOR, and
// neither set is an error (vp will not GUESS an editor such as vi). The value is
// whitespace-split so `code -w` or `emacsclient -nw` work: the first field is
// the binary, the rest are leading arguments before the file path.
func resolveEditor() (string, []string, error) {
	ed := strings.TrimSpace(os.Getenv("VISUAL"))
	if ed == "" {
		ed = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if ed == "" {
		return "", nil, fmt.Errorf("no editor set: export $VISUAL or $EDITOR (vp will not guess one)")
	}
	fields := strings.Fields(ed)
	return fields[0], fields[1:], nil
}

func runTasksEdit(vault *storage.Vault, proj, slug string, out, errOut io.Writer) int {
	meta, content, err := vault.GetTask(proj, slug)
	if err != nil {
		fmt.Fprintf(errOut, "vp tasks edit: no such task: %s\n", slug)
		return cli.ExitUser
	}
	// ARCHIVED GUARD: a done/cancelled task's body is a record of what happened.
	// Editing it in place rewrites history silently — refuse before opening.
	if meta.Done {
		fmt.Fprintf(errOut, "vp tasks edit: task '%s' is archived (done/cancelled); its body is history — refusing to edit\n", slug)
		return cli.ExitUser
	}

	bin, editorArgs, err := resolveEditor()
	if err != nil {
		fmt.Fprintf(errOut, "vp tasks edit: %v\n", err)
		return cli.ExitUser
	}

	tmp, err := os.CreateTemp("", "vp-task-*.md")
	if err != nil {
		fmt.Fprintf(errOut, "vp tasks edit: create temp file: %v\n", err)
		return cli.ExitSystem
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		fmt.Fprintf(errOut, "vp tasks edit: write temp file: %v\n", err)
		return cli.ExitSystem
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		fmt.Fprintf(errOut, "vp tasks edit: close temp file: %v\n", err)
		return cli.ExitSystem
	}

	cmd := exec.Command(bin, append(editorArgs, tmpPath)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Editor aborted (non-zero exit). Leave the live file untouched.
		os.Remove(tmpPath)
		fmt.Fprintf(errOut, "vp tasks edit: editor exited abnormally (%v); the task was not changed\n", err)
		return cli.ExitUser
	}

	edited, err := os.ReadFile(tmpPath)
	if err != nil {
		fmt.Fprintf(errOut, "vp tasks edit: re-read temp file: %v\n", err)
		return cli.ExitSystem
	}
	if string(edited) == content {
		os.Remove(tmpPath)
		fmt.Fprintln(out, "no changes")
		return cli.ExitOK
	}

	if err := vault.OverwriteTaskFile(proj, slug, string(edited)); err != nil {
		// Validation or write failure: the error names the breakage. Preserve the
		// edit and print its path so the user can recover — never delete it here.
		fmt.Fprintf(errOut, "vp tasks edit: %v\n", err)
		fmt.Fprintf(errOut, "your edit was NOT written; it is preserved at: %s\n", tmpPath)
		return cli.ExitUser
	}
	os.Remove(tmpPath)
	fmt.Fprintf(out, "updated %s\n", slug)
	return cli.ExitOK
}
