// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var vaultCloneFlags = []cli.FlagDef{
	{Name: "--bind", Arg: "PROJECT", Help: "Name the clone this host's PRIMARY vault for these projects (one or more): one write of [project_vaults]"},
	{Name: "--dry-run", Help: "Clone into scratch, run every check, print the plan, the digest and the real-run line, and remove the scratch"},
	{Name: "--expect", Arg: "DIGEST", Help: "The digest the dry run printed; refuse on any mismatch"},
	{Name: "--json", Help: "Output the report as JSON"},
}

// cmdVaultClone is `vp vault clone`: a host's copy of a published vault, and
// with --bind the primary for those projects on this host. CLI only: it writes
// the host config and creates a vault no running server serves.
func cmdVaultClone() *cli.Command {
	return &cli.Command{
		Name:     "vault clone",
		Synopsis: "vp vault clone <url> <path> [--bind <project>...] [--dry-run] [--expect <digest>] [--json]",
		Description: "Clone a published vault to <path> (which must not exist, and must not be inside a vault or a git work tree). " +
			"Every remote the vault records in .vibe-palace/remotes.toml is added and fetched; <url> must be one of them, and a mirror " +
			"that is unreachable or has diverged refuses. The clone must be at this binary's data format. With --bind, the clone becomes " +
			"this host's primary vault for those projects: the default vault must record each as moved to one of the clone's remotes " +
			"(pull it first), the clone must hold each and record none of them departed, and every [project_vaults] line is written in " +
			"one compare-and-set. No commit and no push in any vault. Run --dry-run first and paste the command it prints.",
		Flags: vaultCloneFlags,
		Examples: []cli.Example{
			{Cmd: "vp vault clone git@gitlab.example.com:q/vault.git ~/quantum-vibe-palace-vault --bind qa-metabuild-system orchestrator --dry-run",
				Comment: "Plan a clone that binds two projects"},
		},
		Run: func(args []string) int { return runVaultClone(args, os.Stdout, os.Stderr) },
	}
}

func runVaultClone(args []string, out, errOut io.Writer) int {
	const name = "vp vault clone"
	fv, err := cli.ParseFlags(vaultCloneFlags, args)
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", name, err)
		return cli.ExitUser
	}
	pos := fv.Args()
	if len(pos) < 2 {
		fmt.Fprintf(errOut, "%s: <url> and <path> are required\n", name)
		return cli.ExitUser
	}
	// `--bind a b c`: the flag takes the first project, the rest arrive as
	// positionals after <url> <path>.
	bind := fv.GetAll("--bind")
	if len(pos) > 2 {
		if len(bind) == 0 {
			fmt.Fprintf(errOut, "%s: unexpected argument %q (projects to bind follow --bind)\n", name, pos[2])
			return cli.ExitUser
		}
		bind = append(bind, pos[2:]...)
	}
	path, err := expandInitPath(pos[1])
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", name, err)
		return cli.ExitUser
	}
	rep, err := storage.CloneVault(context.Background(), storage.CloneRequest{
		URL: pos[0], Path: path, Bind: bind, DryRun: fv.Bool("--dry-run"), Expect: fv.Get("--expect"),
	})
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", name, err)
		return cli.ExitUser
	}
	if fv.Bool("--json") {
		return printVaultJSON(rep)
	}
	if rep.DryRun {
		fmt.Fprintln(out, "DRY RUN — nothing was written")
	}
	if rep.Resumed {
		fmt.Fprintln(out, "finishing an earlier run's clone, which stopped before its bind")
	}
	fmt.Fprintf(out, "vault:  %s\nfrom:   %s (%s at %s)\n", rep.Path, rep.URL, rep.Branch, rep.Tip)
	for _, r := range rep.Remotes {
		fmt.Fprintf(out, "remote %s = %s (%s)\n", r.Name, r.URL, r.State)
	}
	if len(rep.Bindings) > 0 {
		fmt.Fprintf(out, "[project_vaults] in %s:\n", rep.ConfigPath)
		for _, l := range rep.Bindings {
			fmt.Fprintf(out, "  %s\n", l)
		}
		if rep.ConfigChange != "" {
			fmt.Fprintf(out, "config change:\n%s\n", rep.ConfigChange)
		}
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
	fmt.Fprintf(out, "digest: %s\n", rep.Digest)
	if rep.DryRun {
		fmt.Fprintf(out, "\nTo run it:\n  %s\n", rep.Command)
		return cli.ExitOK
	}
	if len(rep.Undo) > 0 {
		fmt.Fprintln(out, "\nUndo:")
		for _, l := range rep.Undo {
			fmt.Fprintf(out, "  %s\n", l)
		}
	}
	return cli.ExitOK
}
