// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

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
	{Name: "--cwd", Arg: "DIR", Help: "Upgrade the cwd project config (default: current directory). Mutually exclusive with --project."},
	{Name: "--project", Arg: "SLUG", Help: "Upgrade the vault-project config for SLUG. Mutually exclusive with --cwd."},
}

// upgradeTarget captures the inputs needed to upgrade a specific config
// file against its canonical schema template.
type upgradeTarget struct {
	configPath    string
	canonicalText string
	templateText  string
	notFoundHint  string
}

func cmdConfigUpgrade() *cli.Command {
	return &cli.Command{
		Name:        "config upgrade",
		Synopsis:    "vp config upgrade [--dry-run] [--cwd [DIR] | --project SLUG]",
		Description: "Add missing settings to a config file as commented-out defaults. Existing values are never changed. Default target is the global config; use --cwd for a project directory's .vibe-palace.toml or --project SLUG for a vault-project config.",
		Flags:       configUpgradeFlags,
		Examples: []cli.Example{
			{Cmd: "vp config upgrade", Comment: "Upgrade the global config"},
			{Cmd: "vp config upgrade --dry-run", Comment: "Preview changes without writing"},
			{Cmd: "vp config upgrade --cwd", Comment: "Upgrade .vibe-palace.toml in the current directory"},
			{Cmd: "vp config upgrade --cwd ~/code/myapp", Comment: "Upgrade .vibe-palace.toml in a specific directory"},
			{Cmd: "vp config upgrade --project myapp", Comment: "Upgrade vault-project config for myapp"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(configUpgradeFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
				return cli.ExitUser
			}
			dryRun := fv.Bool("--dry-run")

			// --cwd and --project are mutually exclusive.
			cwdFlag := fv.Get("--cwd")
			projectFlag := fv.Get("--project")
			cwdSet := fv.IsSet("--cwd")
			if cwdSet && projectFlag != "" {
				fmt.Fprintln(os.Stderr, "vp config upgrade: --cwd and --project are mutually exclusive")
				return cli.ExitUser
			}

			target, code := resolveUpgradeTarget(cwdSet, cwdFlag, projectFlag)
			if code != cli.ExitOK {
				return code
			}

			data, err := os.ReadFile(target.configPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
				if target.notFoundHint != "" {
					fmt.Fprintln(os.Stderr, target.notFoundHint)
				}
				return cli.ExitUser
			}
			userText := string(data)

			canonical, err := storage.CanonicalKeysFrom(target.canonicalText)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: parse schema: %v\n", err)
				return cli.ExitSystem
			}
			present := storage.PresentKeys(userText)
			missing := storage.MissingKeys(canonical, present)

			if len(missing) == 0 {
				fmt.Fprintln(os.Stderr, "Config is up to date.")
				return cli.ExitOK
			}

			total := 0
			for _, keys := range missing {
				total += len(keys)
			}

			templateBlocks := storage.ParseTemplateBlocks(target.templateText)
			upgraded := storage.UpgradeConfig(userText, missing, templateBlocks)

			if dryRun {
				fmt.Fprintf(os.Stderr, "%d setting(s) would be added:\n", total)
				for _, keys := range missing {
					for _, k := range keys {
						fmt.Fprintf(os.Stderr, "  %s\n", k)
					}
				}
				return cli.ExitOK
			}

			backupPath := target.configPath + ".bak"
			if err := os.WriteFile(backupPath, data, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: create backup: %v\n", err)
				return cli.ExitSystem
			}

			tmpPath := target.configPath + ".tmp"
			if err := os.WriteFile(tmpPath, []byte(upgraded), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: write temp: %v\n", err)
				if rmErr := os.Remove(tmpPath); rmErr != nil {
					slog.Error("cleanup temp config file", "path", tmpPath, "err", rmErr)
				}
				return cli.ExitSystem
			}
			if err := os.Rename(tmpPath, target.configPath); err != nil {
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

// resolveUpgradeTarget selects the config file, canonical schema source,
// and template based on the --cwd / --project flags.
func resolveUpgradeTarget(cwdSet bool, cwdFlag, projectFlag string) (upgradeTarget, int) {
	switch {
	case projectFlag != "":
		if err := storage.ValidateSlug(projectFlag); err != nil {
			fmt.Fprintf(os.Stderr, "vp config upgrade: invalid --project slug %q: %v\n", projectFlag, err)
			return upgradeTarget{}, cli.ExitUser
		}
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
			return upgradeTarget{}, cli.ExitSystem
		}
		vault, err := storage.OpenVaultFromCwd(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp config upgrade: open vault: %v\n", err)
			return upgradeTarget{}, cli.ExitUser
		}
		cfgPath, err := vault.ProjectConfigFile(projectFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
			return upgradeTarget{}, cli.ExitUser
		}
		// Canonical schema for vault-project = active keys in the
		// vault-project template ([meta] only today). Snippets are
		// drawn from the same template.
		return upgradeTarget{
			configPath:    cfgPath,
			canonicalText: storage.VaultProjectTemplateContent(),
			templateText:  storage.VaultProjectTemplateContent(),
			notFoundHint:  fmt.Sprintf("Run 'vp migrate' or initialize the project to create %s first.", cfgPath),
		}, cli.ExitOK

	case cwdSet:
		dir := cwdFlag
		if dir == "" {
			var err error
			dir, err = os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
				return upgradeTarget{}, cli.ExitSystem
			}
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
			return upgradeTarget{}, cli.ExitSystem
		}
		cfgPath := filepath.Join(abs, ".vibe-palace.toml")
		return upgradeTarget{
			configPath:    cfgPath,
			canonicalText: storage.CwdProjectTemplateContent(),
			templateText:  storage.CwdProjectTemplateContent(),
			notFoundHint:  fmt.Sprintf("Run 'vp init' in %s to create a project config first.", abs),
		}, cli.ExitOK

	default:
		cfgPath, err := storage.VaultConfigFilePath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp config upgrade: %v\n", err)
			return upgradeTarget{}, cli.ExitSystem
		}
		// Global config retains the original behavior: defaults.toml as
		// canonical schema, template.toml for snippets.
		defaultsText, _ := storage.DefaultsTomlContent()
		return upgradeTarget{
			configPath:    cfgPath,
			canonicalText: defaultsText,
			templateText:  storage.TemplateTomlContent(),
			notFoundHint:  "Run 'vp init' to create a config file first.",
		}, cli.ExitOK
	}
}
