// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var configBindFlags = []cli.FlagDef{
	{Name: "--vault", Arg: "PATH", Help: "The vault the project lives in on this host: absolute, or starting with ~/ (required)"},
	{Name: "--checkout", Arg: "DIR", Help: "A checkout of the project to verify after the write; repeatable"},
	{Name: "--new", Help: "The project was born in that vault; this host's default vault never held it"},
	{Name: "--allow-unlabelled", Help: "Bind although the departure record names no destination to check the vault's remotes against"},
	{Name: "--dry-run", Help: "Check everything and show the change; write nothing"},
	{Name: "--json", Help: "Print the report as JSON"},
}

// cmdConfigBind is the thin CLI over storage.BindProjectVault, the same
// function the MCP tool vp_config_bind calls.
func cmdConfigBind() *cli.Command {
	return &cli.Command{
		Name:     "config bind",
		Synopsis: "vp config bind <slug> --vault <path> [--checkout <dir>...] [--new] [--allow-unlabelled] [--dry-run] [--json]",
		Description: "Bind a project to a vault on THIS host: one [project_vaults].<slug> line in the global config " +
			"(ADR-012), so every checkout of the project on this host resolves that vault. After a vault split the " +
			"host's default vault must record the project as moved away, and one of the target vault's git remotes " +
			"must equal the recorded destination; --new binds a project born in another vault instead. It never " +
			"writes a checkout (a committed .vibe-palace.toml never carries vault_path), never re-points an existing " +
			"binding, and never creates the global config. The write is verified through the resolver from every " +
			"--checkout, and any failure restores the file. Reload your AI host afterwards: a running MCP server " +
			"resolved its vault at startup.",
		Flags: configBindFlags,
		Examples: []cli.Example{
			{Cmd: "vp config bind qa-metabuild-system --vault ~/quantum-vibe-palace-vault --checkout ~/code/qa-metabuild-system", Comment: "Bind a project split into another vault, and verify its checkout"},
			{Cmd: "vp config bind chimera --vault ~/quantum-vibe-palace-vault --new", Comment: "Bind a project born in another vault, before its first session here"},
		},
		Run: func(args []string) int {
			return runConfigBind(args, os.Stdout, os.Stderr)
		},
	}
}

func runConfigBind(args []string, out, errOut io.Writer) int {
	fv, err := cli.ParseFlags(configBindFlags, args)
	if err != nil {
		fmt.Fprintf(errOut, "vp config bind: %v\n", err)
		return cli.ExitUser
	}
	if len(fv.Args()) != 1 {
		fmt.Fprintln(errOut, "vp config bind: exactly one project slug is required")
		return cli.ExitUser
	}
	if fv.Get("--vault") == "" {
		fmt.Fprintln(errOut, "vp config bind: --vault is required")
		return cli.ExitUser
	}
	if n := len(fv.GetAll("--vault")); n > 1 {
		fmt.Fprintf(errOut, "vp config bind: --vault was given %d times; a project binds to exactly one vault\n", n)
		return cli.ExitUser
	}
	var checkouts []string
	for _, c := range fv.GetAll("--checkout") {
		// The shell expands a leading ~ only at the start of a word, so
		// `--checkout=~/code/x` reaches us unexpanded.
		if c == "~" || strings.HasPrefix(c, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				fmt.Fprintf(errOut, "vp config bind: --checkout %s: %v\n", c, err)
				return cli.ExitUser
			}
			c = filepath.Join(home, c[1:])
		}
		abs, err := filepath.Abs(c)
		if err != nil {
			fmt.Fprintf(errOut, "vp config bind: --checkout %s: %v\n", c, err)
			return cli.ExitUser
		}
		checkouts = append(checkouts, abs)
	}
	mode := storage.BindMoved
	if fv.Bool("--new") {
		mode = storage.BindNew
	}
	rep, err := storage.BindProjectVault(storage.BindRequest{
		Slug: fv.Args()[0], VaultPath: fv.Get("--vault"), Mode: mode, Checkouts: checkouts,
		AllowUnlabelled: fv.Bool("--allow-unlabelled"), DryRun: fv.Bool("--dry-run"),
	})
	if err != nil {
		fmt.Fprintf(errOut, "vp config bind: %v\n", err)
		return cli.ExitUser
	}
	if fv.Bool("--json") {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(errOut, "vp config bind: %v\n", err)
			return cli.ExitSystem
		}
		return cli.ExitOK
	}
	if rep.DryRun {
		fmt.Fprintln(out, "DRY RUN — nothing was written")
	}
	fmt.Fprintf(out, "config: %s\nvault:  %s\nchange: %s\n", rep.ConfigPath, rep.Vault, rep.Change)
	if rep.BackupPath != "" {
		fmt.Fprintf(out, "backup: %s\n", rep.BackupPath)
	}
	for _, c := range rep.Checkouts {
		fmt.Fprintf(out, "checkout %s -> %s (%s)\n", c.Checkout, c.Resolved, c.Source)
	}
	if rep.GrepTotal > 0 {
		fmt.Fprintf(out, "%d line(s) in the checkout(s) still name the old vault (reported, not changed):\n", rep.GrepTotal)
		for _, h := range rep.GrepHits {
			fmt.Fprintf(out, "  %s:%s: %s\n", h.File, h.Line, h.Text)
		}
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
	return cli.ExitOK
}
