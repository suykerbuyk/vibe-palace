// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func cmdConfig() *cli.Command {
	return &cli.Command{
		Name:        "config",
		Synopsis:    "vp config <command>",
		Description: "Manage the global vibe-palace configuration.",
		Subcommands: []string{"config upgrade"},
		Run: func(args []string) int {
			fmt.Fprintln(os.Stderr, "Usage: vp config <command>\n\nRun 'vp config --help' for details.")
			return cli.ExitOK
		},
	}
}

var configUpgradeFlags = []cli.FlagDef{
	{Name: "--dry-run", Help: "Show what would be added without writing"},
}

func cmdConfigUpgrade() *cli.Command {
	return &cli.Command{
		Name:        "config upgrade",
		Synopsis:    "vp config upgrade [--dry-run]",
		Description: "Add missing settings to the global config as commented-out defaults. Existing values are never changed.",
		Flags:       configUpgradeFlags,
		Examples: []cli.Example{
			{Cmd: "vp config upgrade", Comment: "Add missing settings to config"},
			{Cmd: "vp config upgrade --dry-run", Comment: "Preview changes without writing"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(configUpgradeFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
				return cli.ExitUser
			}
			dryRun := fv.Bool("--dry-run")

			configPath, err := storage.VaultConfigFilePath()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
				return cli.ExitSystem
			}

			data, err := os.ReadFile(configPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
				fmt.Fprintln(os.Stderr, "Run 'vp init' to create a config file first.")
				return cli.ExitUser
			}
			userText := string(data)

			canonical, err := storage.CanonicalKeys()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
				return cli.ExitSystem
			}

			present := storage.PresentKeys(userText)
			missing := storage.MissingKeys(canonical, present)

			if len(missing) == 0 {
				fmt.Fprintln(os.Stderr, "Config is up to date.")
				return cli.ExitOK
			}

			// Count total missing keys.
			total := 0
			for _, keys := range missing {
				total += len(keys)
			}

			templateBlocks := storage.ParseTemplateBlocks(storage.TemplateTomlContent())
			upgraded := storage.UpgradeConfig(userText, missing, templateBlocks)

			if dryRun {
				fmt.Fprintf(os.Stderr, "%d setting(s) would be added:\n", total)
				for section, keys := range missing {
					for _, k := range keys {
						if section != "" {
							fmt.Fprintf(os.Stderr, "  %s\n", k)
						} else {
							fmt.Fprintf(os.Stderr, "  %s\n", k)
						}
					}
				}
				return cli.ExitOK
			}

			// Create backup only when actual changes are being written.
			backupPath := configPath + ".bak"
			if err := os.WriteFile(backupPath, data, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: create backup: %v\n", err)
				return cli.ExitSystem
			}

			// Atomic write: temp file + rename.
			tmpPath := configPath + ".tmp"
			if err := os.WriteFile(tmpPath, []byte(upgraded), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: write temp: %v\n", err)
				if rmErr := os.Remove(tmpPath); rmErr != nil {
					slog.Error("cleanup temp config file", "path", tmpPath, "err", rmErr)
				}
				return cli.ExitSystem
			}
			if err := os.Rename(tmpPath, configPath); err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: rename: %v\n", err)
				if rmErr := os.Remove(tmpPath); rmErr != nil {
					slog.Error("cleanup temp config file", "path", tmpPath, "err", rmErr)
				}
				return cli.ExitSystem
			}

			fmt.Fprintf(os.Stderr, "Added %d setting(s). Backup at %s\n", total, backupPath)
			return cli.ExitOK
		},
	}
}
