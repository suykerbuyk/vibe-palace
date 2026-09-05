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

// cmdWorktree is the read-me container for the worktree subcommands.
func cmdWorktree() *cli.Command {
	return &cli.Command{
		Name:     "worktree",
		Synopsis: "vp worktree <command> [flags]",
		Description: "Manage isolated git worktrees for concurrent /vpc-execute-plan runs. Each plan " +
			"executes in a sibling ../wt/<slug> tree on a plan/<slug> branch cut from main, so " +
			"multiple plans proceed without contending for the main checkout; the human then " +
			"fast-forward-merges each finished branch to main and coordinates the ordering. These " +
			"commands never merge and never touch main.",
	}
}

var worktreeCreateFlags = []cli.FlagDef{
	{Name: "--base", Arg: "BRANCH", Help: "Base branch or commit to cut from", Default: "main"},
	{Name: "--branch", Arg: "NAME", Help: "Override the branch name (default plan/<slug>)"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdWorktreeCreate() *cli.Command {
	return &cli.Command{
		Name:     "worktree create",
		Synopsis: "vp worktree create <slug> [--base main] [--branch NAME] [--json]",
		Description: "Create an isolated worktree on a plan/<slug> branch cut from --base (local main " +
			"by default). The tree is checked out at the sibling path ../wt/<slug>; create refuses " +
			"if that path or the branch already exists, so a re-run never clobbers in-flight work.",
		Flags: worktreeCreateFlags,
		Examples: []cli.Example{
			{Cmd: "vp worktree create add-auth", Comment: "New worktree at ../wt/add-auth on plan/add-auth"},
			{Cmd: "vp worktree create add-auth --json", Comment: "Emit {slug,path,branch,base} for scripting"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(worktreeCreateFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree create: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) != 1 {
				fmt.Fprintln(os.Stderr, "vp worktree create: requires exactly one <slug> argument")
				return cli.ExitUser
			}
			mgr, err := worktreeManager()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree create: %v\n", err)
				return cli.ExitSystem
			}
			res, err := mgr.Create(pos[0], fv.Get("--base"), fv.Get("--branch"))
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
	{Name: "--force", Help: "Force removal even with a dirty or locked worktree"},
	{Name: "--delete-branch", Help: "Also delete the plan/<slug> branch (safe delete; refuses if unmerged)"},
}

func cmdWorktreeRemove() *cli.Command {
	return &cli.Command{
		Name:     "worktree remove",
		Synopsis: "vp worktree remove <slug> [--force] [--delete-branch]",
		Description: "Remove slug's worktree. --delete-branch also safe-deletes the plan/<slug> branch, " +
			"which git refuses when the branch is unmerged — so an un-fast-forwarded plan is preserved.",
		Flags: worktreeRemoveFlags,
		Examples: []cli.Example{
			{Cmd: "vp worktree remove add-auth", Comment: "Remove the worktree, keep the branch"},
			{Cmd: "vp worktree remove add-auth --delete-branch", Comment: "Remove worktree and delete plan/add-auth if merged"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(worktreeRemoveFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree remove: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) != 1 {
				fmt.Fprintln(os.Stderr, "vp worktree remove: requires exactly one <slug> argument")
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
			if err := mgr.Remove(pos[0], opts); err != nil {
				fmt.Fprintf(os.Stderr, "vp worktree remove: %v\n", err)
				return cli.ExitUser
			}
			fmt.Printf("Removed worktree for %s\n", pos[0])
			return cli.ExitOK
		},
	}
}

var worktreeListFlags = []cli.FlagDef{
	{Name: "--all", Help: "List all worktrees, not just plan/* ones"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdWorktreeList() *cli.Command {
	return &cli.Command{
		Name:     "worktree list",
		Synopsis: "vp worktree list [--all] [--json]",
		Description: "List the plan/* worktrees in flight — the coordination surface for concurrent " +
			"plan runs. --all includes every worktree (the main checkout and any others).",
		Flags: worktreeListFlags,
		Examples: []cli.Example{
			{Cmd: "vp worktree list", Comment: "Show in-flight plan worktrees"},
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
				fmt.Println("No plan worktrees in flight.")
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
