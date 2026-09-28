// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var vaultInitFlags = []cli.FlagDef{
	{Name: "--remote", Arg: "NAME=URL", Help: "A remote of the new vault, in publish order; repeat it. The first is the upstream. Each must exist and be empty"},
	{Name: "--dry-run", Help: "Check the path and every remote, and print what would be created and published, writing nothing"},
	{Name: "--json", Help: "Output the report as JSON"},
}

// cmdVaultInit is `vp vault init`: create a NEW, empty vault and publish it.
// It is not `vp init`, which onboards the current directory as a project of
// a vault and writes host config; this writes neither.
func cmdVaultInit() *cli.Command {
	return &cli.Command{
		Name:     "vault init",
		Synopsis: "vp vault init <path> --remote <name>=<url>... [--dry-run] [--json]",
		Description: "Create a NEW, empty vault at <path> (which must not exist, and must not be inside a vault or a git work tree), " +
			"record its remotes in the tracked .vibe-palace/remotes.toml, make one commit on main, and publish it to every remote " +
			"(each must be reachable and empty); the first --remote becomes the upstream. Nothing is published unless every check passes, " +
			"and if no remote takes the commit the new directory is removed again. " +
			"This is NOT `vp init`: it onboards no project, touches no checkout and writes no host config. " +
			"Bind projects to the new vault with `vp config bind` (or `vp vault clone --bind` on other hosts).",
		Flags: vaultInitFlags,
		Examples: []cli.Example{
			{Cmd: "vp vault init ~/quantum-vibe-palace-vault --remote origin=git@gitlab.example.com:q/vault.git --remote github=git@github.com:u/q-vault.git --dry-run",
				Comment: "Check the path and both remotes, writing nothing"},
			{Cmd: "vp vault init ~/quantum-vibe-palace-vault --remote origin=git@gitlab.example.com:q/vault.git --remote github=git@github.com:u/q-vault.git",
				Comment: "Create the vault and publish main to both remotes"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(vaultInitFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault init: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) != 1 {
				fmt.Fprintln(os.Stderr, "vp vault init: exactly one <path> argument is required")
				return cli.ExitUser
			}
			path, err := expandInitPath(pos[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault init: %v\n", err)
				return cli.ExitUser
			}
			var remotes []storage.RecordedRemote
			for _, spec := range fv.GetAll("--remote") {
				name, url, ok := strings.Cut(spec, "=")
				if !ok {
					fmt.Fprintf(os.Stderr, "vp vault init: --remote %q: want NAME=URL\n", spec)
					return cli.ExitUser
				}
				remotes = append(remotes, storage.RecordedRemote{Name: strings.TrimSpace(name), URL: strings.TrimSpace(url)})
			}
			rep, err := storage.InitVault(context.Background(), storage.InitVaultRequest{
				Path: path, Remotes: remotes, DryRun: fv.Bool("--dry-run"), Scaffold: reconcile.ScaffoldNewVault,
			})
			if fv.Bool("--json") && rep != nil {
				if code := printVaultJSON(rep); code != cli.ExitOK {
					return code
				}
			} else if rep != nil {
				printVaultInitReport(rep)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault init: %v\n", err)
				if isInitRefusal(err) {
					return cli.ExitUser
				}
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

func isInitRefusal(err error) bool {
	if errors.Is(err, storage.ErrGitDisabled) {
		return true
	}
	for _, e := range []error{storage.ErrInitPathExists, storage.ErrInitNested, storage.ErrInitRemoteNotEmpty,
		storage.ErrInitRemoteBad, storage.ErrInitNoRemote, storage.ErrInitRemoteUnreached} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// expandInitPath expands a leading ~ and makes the path absolute.
func expandInitPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %s: %w", p, err)
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Abs(p)
}

func printVaultInitReport(rep *storage.InitVaultReport) {
	if rep.DryRun {
		fmt.Printf("dry run — nothing was written\n")
		fmt.Printf("would create vault %s on branch %s, with:\n", rep.Path, rep.Branch)
	} else {
		fmt.Printf("created vault %s on branch %s (commit %s), with:\n", rep.Path, rep.Branch, rep.Commit)
	}
	for _, f := range rep.Files {
		fmt.Printf("  %s\n", f)
	}
	if rep.DryRun {
		fmt.Printf("would publish to (each checked reachable and empty):\n")
		for _, r := range rep.Remotes {
			up := ""
			if r.Name == rep.Upstream {
				up = "  (upstream)"
			}
			fmt.Printf("  %s  %s%s\n", r.Name, r.URL, up)
		}
	} else {
		// Only the remotes whose live tip holds the commit: a partial publish
		// must not read as a full one.
		fmt.Printf("published to:\n")
		if len(rep.PublishedTo) == 0 {
			fmt.Printf("  (none)\n")
		}
		for _, name := range rep.PublishedTo {
			up := ""
			if name == rep.Upstream {
				up = "  (upstream)"
			}
			fmt.Printf("  %s%s\n", name, up)
		}
		for _, r := range rep.Remotes {
			if !slices.Contains(rep.PublishedTo, r.Name) {
				fmt.Printf("NOT published to: %s\n", r.Name)
			}
		}
	}
	if len(rep.Undo) > 0 {
		fmt.Printf("undo:\n")
		for _, u := range rep.Undo {
			fmt.Printf("  %s\n", u)
		}
	}
}
