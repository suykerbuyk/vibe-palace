// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/worktree"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// worktreeManager resolves the project repo root from the current directory and
// returns a Manager over it. All worktree operations target the PROJECT repo,
// not the vault — so these commands are registered UNWRAPPED (no mutates()).
func worktreeManager() (*worktree.Manager, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return worktree.New(wrapstate.ResolveProjectRoot(cwd)), nil
}

// worktreeTarget is the mutually-exclusive selection of a worktree from the
// --epic/--task flags and a positional <slug>.
type worktreeTarget struct {
	slug string // single-task tier; "" when an epic form is selected
	epic string // epic tier (integration or task)
	task string // "" => epic integration tier; non-empty => epic task tier
}

// resolveWorktreeTarget enforces the selection rules shared by create and
// remove: exactly one of a positional <slug> or --epic must be given, --task
// requires --epic, and --epic is mutually exclusive with a positional <slug>.
func resolveWorktreeTarget(epic, task string, pos []string) (worktreeTarget, error) {
	if epic != "" {
		if len(pos) != 0 {
			return worktreeTarget{}, fmt.Errorf("--epic is mutually exclusive with a positional <slug>")
		}
		return worktreeTarget{epic: epic, task: task}, nil
	}
	if task != "" {
		return worktreeTarget{}, fmt.Errorf("--task requires --epic")
	}
	if len(pos) != 1 {
		return worktreeTarget{}, fmt.Errorf("requires exactly one <slug> argument (or --epic [--task])")
	}
	return worktreeTarget{slug: pos[0]}, nil
}

// cmdWorktree is the read-me container for the worktree subcommands.
func cmdWorktree() *cli.Command {
	return &cli.Command{
		Name:     "worktree",
		Synopsis: "vp worktree <command> [flags]",
		Description: "Manage isolated git worktrees for concurrent /vpc-execute-plan runs. Each plan " +
			"executes in a tree at <root>/.wt/<slug> on a branch of the same name cut from main, so " +
			"multiple plans proceed without contending for the main checkout; the human then " +
			"fast-forward-merges each finished branch to main and coordinates the ordering. Epics use " +
			"two tiers under .wt/ — integration .wt/<epic> on <epic>, task .wt/<epic>__<task> on " +
			"<epic>__<task>. These commands never merge and never touch main.",
	}
}

var worktreeCreateFlags = []cli.FlagDef{
	{Name: "--base", Arg: "BRANCH", Help: "Base branch or commit to cut from", Default: "main"},
	{Name: "--epic", Arg: "EPIC", Help: "Create an epic worktree (integration tier, or a task with --task)"},
	{Name: "--task", Arg: "TASK", Help: "With --epic, create the epic task worktree .wt/<epic>__<task>"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdWorktreeCreate() *cli.Command {
	return &cli.Command{
		Name:     "worktree create",
		Synopsis: "vp worktree create <slug> | --epic <epic> [--task <task>] [--base main] [--json]",
		Description: "Create an isolated worktree cut from --base (local main by default). A single-task " +
			"worktree is checked out at <root>/.wt/<slug> on branch <slug>. An epic integration worktree " +
			"(--epic <epic>) is at .wt/<epic> on branch <epic>; an epic task worktree (--epic <epic> " +
			"--task <task>) is at .wt/<epic>__<task> on branch <epic>__<task>. create refuses if the " +
			"path or the branch already exists, so a re-run never clobbers in-flight work.",
		Flags: worktreeCreateFlags,
		Examples: []cli.Example{
			{Cmd: "vp worktree create add-auth", Comment: "New worktree at .wt/add-auth on branch add-auth"},
			{Cmd: "vp worktree create --epic big-epic", Comment: "Epic integration worktree at .wt/big-epic on branch big-epic"},
			{Cmd: "vp worktree create --epic big-epic --task phase-1", Comment: "Epic task worktree at .wt/big-epic__phase-1 on branch big-epic__phase-1"},
			{Cmd: "vp worktree create add-auth --json", Comment: "Emit {slug,path,branch,base} for scripting"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(worktreeCreateFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree create: %v\n", err)
				return cli.ExitUser
			}
			tgt, err := resolveWorktreeTarget(fv.Get("--epic"), fv.Get("--task"), fv.Args())
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree create: %v\n", err)
				return cli.ExitUser
			}
			mgr, err := worktreeManager()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree create: %v\n", err)
				return cli.ExitSystem
			}
			base := fv.Get("--base")
			var res worktree.Result
			switch {
			case tgt.slug != "":
				res, err = mgr.Create(tgt.slug, base)
			case tgt.task != "":
				res, err = mgr.CreateEpicTask(tgt.epic, tgt.task, base)
			default:
				res, err = mgr.CreateEpicIntegration(tgt.epic, base)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree create: %v\n", err)
				return cli.ExitUser
			}
			if fv.Bool("--json") {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(res)
				return cli.ExitOK
			}
			fmt.Printf("Created worktree %s\n  branch %s (from %s)\n", res.Path, res.Branch, res.Base)
			fmt.Printf("  cd %s\n", res.Path)
			return cli.ExitOK
		},
	}
}

var worktreeRemoveFlags = []cli.FlagDef{
	{Name: "--epic", Arg: "EPIC", Help: "Remove an epic worktree (integration tier, or a task with --task)"},
	{Name: "--task", Arg: "TASK", Help: "With --epic, remove the epic task worktree .wt/<epic>__<task>"},
	{Name: "--force", Help: "Force removal even with a dirty or locked worktree"},
	{Name: "--delete-branch", Help: "Also delete the worktree's branch (safe delete; refuses if unmerged)"},
}

func cmdWorktreeRemove() *cli.Command {
	return &cli.Command{
		Name:     "worktree remove",
		Synopsis: "vp worktree remove <slug> | --epic <epic> [--task <task>] [--force] [--delete-branch]",
		Description: "Remove a worktree. --delete-branch also safe-deletes its branch (= its directory " +
			"name), which git refuses when the branch is unmerged — so an un-fast-forwarded plan is " +
			"preserved. Removing an epic integration worktree (--epic <epic>) refuses while any of its " +
			"task worktrees or branches (.wt/<epic>__*) still exist.",
		Flags: worktreeRemoveFlags,
		Examples: []cli.Example{
			{Cmd: "vp worktree remove add-auth", Comment: "Remove the worktree, keep the branch"},
			{Cmd: "vp worktree remove add-auth --delete-branch", Comment: "Remove worktree and delete branch add-auth if merged"},
			{Cmd: "vp worktree remove --epic big-epic --task phase-1 --delete-branch", Comment: "Remove an epic task worktree and its branch"},
			{Cmd: "vp worktree remove --epic big-epic --delete-branch", Comment: "Remove the epic integration worktree (refuses while task worktrees remain)"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(worktreeRemoveFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree remove: %v\n", err)
				return cli.ExitUser
			}
			tgt, err := resolveWorktreeTarget(fv.Get("--epic"), fv.Get("--task"), fv.Args())
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree remove: %v\n", err)
				return cli.ExitUser
			}
			mgr, err := worktreeManager()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree remove: %v\n", err)
				return cli.ExitSystem
			}
			opts := worktree.RemoveOptions{
				Force:        fv.Bool("--force"),
				DeleteBranch: fv.Bool("--delete-branch"),
			}
			var label string
			switch {
			case tgt.slug != "":
				err = mgr.Remove(tgt.slug, opts)
				label = tgt.slug
			case tgt.task != "":
				err = mgr.RemoveEpicTask(tgt.epic, tgt.task, opts)
				label = fmt.Sprintf("epic %s task %s", tgt.epic, tgt.task)
			default:
				err = mgr.RemoveEpicIntegration(tgt.epic, opts)
				label = fmt.Sprintf("epic integration %s", tgt.epic)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree remove: %v\n", err)
				return cli.ExitUser
			}
			fmt.Printf("Removed worktree for %s\n", label)
			return cli.ExitOK
		},
	}
}

var worktreeListFlags = []cli.FlagDef{
	{Name: "--all", Help: "List all worktrees, not just the .wt/ ones"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdWorktreeList() *cli.Command {
	return &cli.Command{
		Name:     "worktree list",
		Synopsis: "vp worktree list [--all] [--json]",
		Description: "List the .wt/ worktrees in flight — the coordination surface for concurrent " +
			"plan runs. --all includes every worktree (the main checkout and any others).",
		Flags: worktreeListFlags,
		Examples: []cli.Example{
			{Cmd: "vp worktree list", Comment: "Show in-flight .wt/ worktrees"},
			{Cmd: "vp worktree list --json", Comment: "Emit JSON for scripting"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(worktreeListFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree list: %v\n", err)
				return cli.ExitUser
			}
			mgr, err := worktreeManager()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree list: %v\n", err)
				return cli.ExitSystem
			}
			entries, err := mgr.List(fv.Bool("--all"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree list: %v\n", err)
				return cli.ExitSystem
			}
			if fv.Bool("--json") {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(entries)
				return cli.ExitOK
			}
			if len(entries) == 0 {
				fmt.Println("No .wt/ worktrees in flight.")
				return cli.ExitOK
			}
			for _, e := range entries {
				head := e.Head
				if len(head) > 8 {
					head = head[:8]
				}
				fmt.Printf("%-40s %-24s %s\n", e.Path, e.Branch, head)
			}
			return cli.ExitOK
		},
	}
}
