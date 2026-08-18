// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

var injectFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
}

func cmdInject() *cli.Command {
	return &cli.Command{
		Name:        "inject",
		Synopsis:    "vp inject [--project P]",
		Description: "Output bootstrap context as JSON for AI consumption. Provides sessions, tasks, decisions, and friction data in a structured format.",
		Flags:       injectFlags,
		Examples: []cli.Example{
			{Cmd: "vp inject", Comment: "Output context for the current project"},
			{Cmd: "vp inject -p myapp", Comment: "Output context for a named project"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(injectFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp inject: %v\n", err)
				return cli.ExitUser
			}

			proj := fv.Get("--project")
			if proj == "" {
				proj, _ = project.DetectProject(".")
			}
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp inject: could not detect project (use --project)")
				return cli.ExitUser
			}

			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp inject: %v\n", err)
				return cli.ExitUser
			}

			return runInject(vault, proj, os.Stdout)
		},
	}
}

func runInject(vault *storage.Vault, proj string, out io.Writer) int {
	resolver := vpctx.NewResolver(vault.Root)
	result := tools.AssembleBootstrap(resolver, vault, proj, "", "")

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "vp inject: %v\n", err)
		return cli.ExitSystem
	}
	return cli.ExitOK
}
