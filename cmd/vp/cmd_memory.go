// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/memory"
	"github.com/suykerbuyk/vibe-palace/internal/project"
)

func cmdMemory() *cli.Command {
	return &cli.Command{
		Name:        "memory",
		Synopsis:    "vp memory <command> [flags]",
		Description: "Manage Claude native auto-memory. Memories live host-local under ~/.claude until harvested into the synced vault at Projects/<project>/memory/.",
		Subcommands: []string{"memory harvest"},
	}
}

var memoryHarvestFlags = []cli.FlagDef{
	{Name: "--dry-run", Help: "Classify and report what would be harvested, without mutating anything"},
	{Name: "--no-push", Help: "Commit the harvested memory locally without pushing to remotes"},
}

// printMemoryList prints a counted, indented "label (N):" block for a set of
// harvest rels. An empty set still prints its header so the routed/skipped
// split is always visible.
func printMemoryList(label string, rels []string) {
	fmt.Printf("%s (%d):\n", label, len(rels))
	for _, r := range rels {
		fmt.Printf("  %s\n", r)
	}
}

func cmdMemoryHarvest() *cli.Command {
	return &cli.Command{
		Name:     "memory harvest",
		Synopsis: "vp memory harvest [--dry-run] [--no-push]",
		Description: "Drain Claude's host-local auto-memory for the current working directory " +
			"into the synced vault at Projects/<project>/memory/, then DELETE the host-local " +
			"originals (a one-way harvest: the vault becomes the single source of truth). " +
			"Typed memory files are routed by metadata.type; identical content already in the " +
			"vault is deduped; a same-name file with different content is written with a " +
			".harvested-<ts> suffix rather than clobbered; the MEMORY.md index is dropped. " +
			"The routed files are committed and pushed to all remotes (downgrading to a " +
			"local-only commit when none are configured); --no-push commits locally only, " +
			"and --dry-run classifies without mutating anything. A non-Claude host with no " +
			"native memory dir is a clean no-op.",
		Flags: memoryHarvestFlags,
		Examples: []cli.Example{
			{Cmd: "vp memory harvest --dry-run", Comment: "Preview the route/skip split without mutating"},
			{Cmd: "vp memory harvest --no-push", Comment: "Harvest and commit locally only"},
			{Cmd: "vp memory harvest", Comment: "Harvest, commit, and push to all remotes"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(memoryHarvestFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp memory harvest: %v\n", err)
				return cli.ExitUser
			}
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp memory harvest: %v\n", err)
				return cli.ExitSystem
			}
			slug, err := project.DetectProject(cwd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp memory harvest: %v\n", err)
				return cli.ExitUser
			}
			root, err := vaultRoot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp memory harvest: %v\n", err)
				return cli.ExitUser
			}
			nativeDir, err := memory.NativeDirFromCwd(cwd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp memory harvest: %v\n", err)
				return cli.ExitSystem
			}

			res, err := memory.Harvest(memory.Options{
				VaultRoot: root,
				Project:   slug,
				NativeDir: nativeDir,
				DryRun:    fv.Bool("--dry-run"),
				Push:      !fv.Bool("--no-push"),
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp memory harvest: %v\n", err)
				return cli.ExitSystem
			}

			if res.NativeDirMissing {
				fmt.Printf("no native memory dir for %s (%s) — nothing to harvest\n", slug, nativeDir)
				return cli.ExitOK
			}

			fmt.Printf("project: %s\nnative dir: %s\n", slug, res.NativeDir)
			printMemoryList("Routed", res.Routed)
			printMemoryList("Conflicted (written with .harvested-<ts> suffix)", res.Conflicted)
			printMemoryList("Deduped (identical, dropped)", res.DedupSkipped)
			printMemoryList("Index skipped", res.IndexSkipped)
			if len(res.Unclassified) > 0 {
				printMemoryList("Unclassified (defaulted to project)", res.Unclassified)
			}

			if fv.Bool("--dry-run") {
				printMemoryList("Would delete host-local", res.DeletedHostLocal)
				fmt.Println("dry run: nothing was mutated")
				return cli.ExitOK
			}

			printMemoryList("Deleted host-local", res.DeletedHostLocal)
			if res.Committed {
				fmt.Printf("Committed %s\n", res.CommitSHA)
			} else {
				fmt.Println("nothing to commit")
			}
			if res.PushDowngraded {
				fmt.Println("no remotes configured — committed locally only")
			}
			return cli.ExitOK
		},
	}
}
