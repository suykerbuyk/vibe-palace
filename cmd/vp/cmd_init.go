// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var initFlags = []cli.FlagDef{
	{Name: "--name", Short: "-n", Arg: "NAME", Help: "Project name (default: auto-detect)"},
	{Name: "--domain", Short: "-d", Arg: "DOMAIN", Help: "Domain (e.g. work, personal, opensource)"},
	{Name: "--tags", Short: "-t", Arg: "TAGS", Help: "Comma-separated tags"},
}

func cmdInit() *cli.Command {
	return &cli.Command{
		Name:        "init",
		Synopsis:    "vp init [path] [flags]",
		Description: "Initialize a new vibe-palace project in the current or given directory.",
		Flags:       initFlags,
		Examples: []cli.Example{
			{Cmd: "vp init", Comment: "Initialize in current directory"},
			{Cmd: "vp init ~/code/myapp --name myapp --domain work"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(initFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp init: %v\n", err)
				return cli.ExitUser
			}

			dir := "."
			if positional := fv.Args(); len(positional) > 0 {
				dir = positional[0]
			}
			dir, err = filepath.Abs(dir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp init: %v\n", err)
				return cli.ExitSystem
			}

			// Check for existing config.
			configPath := filepath.Join(dir, project.ConfigFileName)
			if _, err := os.Stat(configPath); err == nil {
				fmt.Fprintf(os.Stderr, "vp init: %s already exists\n", configPath)
				return cli.ExitUser
			}

			// Determine project name.
			name := fv.Get("--name")
			if name == "" {
				name, _ = project.DetectProject(dir)
			}
			if name == "" {
				name = filepath.Base(dir)
			}
			if err := storage.ValidateSlug(name); err != nil {
				fmt.Fprintf(os.Stderr, "vp init: invalid project name %q: %v\n", name, err)
				return cli.ExitUser
			}

			// Write .vibe-palace.toml.
			content := fmt.Sprintf("[project]\nname = %q\n", name)
			if d := fv.Get("--domain"); d != "" {
				content += fmt.Sprintf("domain = %q\n", d)
			}
			if t := fv.Get("--tags"); t != "" {
				content += fmt.Sprintf("tags = [%q]\n", t)
			}

			if err := os.MkdirAll(dir, 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "vp init: %v\n", err)
				return cli.ExitSystem
			}
			if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "vp init: %v\n", err)
				return cli.ExitSystem
			}

			// Create vault directories if vault is available.
			vault, err := storage.OpenVault("")
			if err == nil {
				tasksDir, err := vault.TasksDir(name)
				if err == nil {
					os.MkdirAll(filepath.Join(tasksDir, "done"), 0o755)
					os.MkdirAll(filepath.Join(tasksDir, "cancelled"), 0o755)
				}
			}

			fmt.Fprintf(os.Stderr, "Initialized project %q in %s\n", name, dir)
			return cli.ExitOK
		},
	}
}
