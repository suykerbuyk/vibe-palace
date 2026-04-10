// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/migrate"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func cmdMigrate() *cli.Command {
	return &cli.Command{
		Name:        "migrate",
		Synopsis:    "vp migrate <command> [flags]",
		Description: "Import data into vibe-palace from external sources.",
		Run: func(args []string) int {
			// Parent command: show help.
			fmt.Fprint(os.Stderr, "Usage: vp migrate <command> [flags]\n\nCommands:\n  vibevault   Import sessions from a VibeVault directory\n  mempalace   Import data from a MemPalace JSON export\n\nRun 'vp help migrate vibevault' or 'vp help migrate mempalace' for details.\n")
			return cli.ExitOK
		},
	}
}

var migrateVibeVaultFlags = []cli.FlagDef{
	{Name: "--vault-path", Arg: "PATH", Help: "VibeVault root (default: from config)"},
	{Name: "--dry-run", Help: "Show what would be imported without writing"},
}

func cmdMigrateVibeVault() *cli.Command {
	return &cli.Command{
		Name:        "migrate vibevault",
		Synopsis:    "vp migrate vibevault [--vault-path PATH] [--dry-run]",
		Description: "Import sessions from a VibeVault directory into the palace.",
		Flags:       migrateVibeVaultFlags,
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateVibeVaultFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate vibevault: %v\n", err)
				return cli.ExitUser
			}
			vaultPath := fv.Get("--vault-path")
			dryRun := fv.Bool("--dry-run")

			vault, cfg, err := openMigrateVault(vaultPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
				return cli.ExitUser
			}

			emb, eng, cleanup, err := setupEmbedder(vault, cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
				return cli.ExitSystem
			}
			defer cleanup()

			if dryRun {
				fmt.Fprintln(os.Stderr, "Dry run — no data will be written.")
			}
			fmt.Fprintln(os.Stderr, "Importing VibeVault sessions...")

			result, err := migrate.ImportVibeVault(
				context.Background(), vault, eng, emb, cfg,
				migrate.ImportOptions{
					DryRun:   dryRun,
					Progress: migrateProgressFunc(),
				},
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
				return cli.ExitSystem
			}

			printMigrateResult(result, dryRun)
			return cli.ExitOK
		},
	}
}

var migrateMemPalaceFlags = []cli.FlagDef{
	{Name: "--export-path", Arg: "PATH", Help: "Path to MemPalace JSON export file"},
	{Name: "--dry-run", Help: "Show what would be imported without writing"},
}

func cmdMigrateMemPalace() *cli.Command {
	return &cli.Command{
		Name:        "migrate mempalace",
		Synopsis:    "vp migrate mempalace --export-path PATH [--dry-run]",
		Description: "Import data from a MemPalace JSON export into the palace.",
		Flags:       migrateMemPalaceFlags,
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateMemPalaceFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate mempalace: %v\n", err)
				return cli.ExitUser
			}
			exportPath := fv.Get("--export-path")
			dryRun := fv.Bool("--dry-run")

			if exportPath == "" {
				fmt.Fprintln(os.Stderr, "vp migrate mempalace: --export-path is required")
				return cli.ExitUser
			}

			vault, cfg, err := openMigrateVault("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
				return cli.ExitUser
			}

			emb, eng, cleanup, err := setupEmbedder(vault, cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
				return cli.ExitSystem
			}
			defer cleanup()

			if dryRun {
				fmt.Fprintln(os.Stderr, "Dry run — no data will be written.")
			}
			fmt.Fprintln(os.Stderr, "Importing MemPalace data...")

			result, err := migrate.ImportMemPalace(
				context.Background(), vault, eng, emb, exportPath,
				migrate.ImportOptions{
					DryRun:   dryRun,
					Progress: migrateProgressFunc(),
				},
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
				return cli.ExitSystem
			}

			printMigrateResult(result, dryRun)
			return cli.ExitOK
		},
	}
}

// openMigrateVault opens the vault, using the given path or falling back to config.
func openMigrateVault(vaultPath string) (*storage.Vault, storage.Config, error) {
	var vault *storage.Vault
	var err error

	if vaultPath != "" {
		vault = storage.NewVault(vaultPath)
	} else {
		vault, err = storage.OpenVault("")
		if err != nil {
			return nil, storage.Config{}, fmt.Errorf("open vault: %w", err)
		}
	}

	cfg, err := vault.LoadConfig("")
	if err != nil {
		return nil, storage.Config{}, fmt.Errorf("load config: %w", err)
	}

	return vault, cfg, nil
}

// setupEmbedder creates an ONNX embedder and search engine for migration.
func setupEmbedder(vault *storage.Vault, cfg storage.Config) (embedder.Embedder, *search.Engine, func(), error) {
	modelDir := vault.VaultLocalDir() + "/models"
	emb, err := embedder.NewONNX(
		cfg.EmbedderModel, modelDir,
		cfg.EmbedderMaxSeqLen, cfg.EmbedderBatchSize,
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("embedder: %w", err)
	}

	eng := search.NewEngine(emb, vault, cfg)

	cleanup := func() {
		eng.Close()
		emb.Close()
	}
	return emb, eng, cleanup, nil
}

// migrateProgressFunc returns a progress callback that prints to stderr.
func migrateProgressFunc() migrate.ProgressFunc {
	var lastProject string
	return func(evt migrate.ProgressEvent) {
		switch evt.Type {
		case migrate.ProgressProjectStart:
			lastProject = evt.Project
			fmt.Fprintf(os.Stderr, "  %s:\n", evt.Project)
		case migrate.ProgressSessionDone:
			fmt.Fprintf(os.Stderr, "    %s [%d/%d]\n", evt.SessionID, evt.Current, evt.Total)
		case migrate.ProgressSessionSkip:
			fmt.Fprintf(os.Stderr, "    %s (skipped) [%d/%d]\n", evt.SessionID, evt.Current, evt.Total)
		case migrate.ProgressProjectDone:
			// newline between projects handled by next ProjectStart
		case migrate.ProgressError:
			fmt.Fprintf(os.Stderr, "    ERROR (%s): %s\n", lastProject, evt.Message)
		}
	}
}

// printMigrateResult prints the migration summary to stderr.
func printMigrateResult(result migrate.ImportResult, dryRun bool) {
	prefix := "Done"
	if dryRun {
		prefix = "Would import"
	}
	fmt.Fprintf(os.Stderr, "\n%s: %d projects, %d sessions imported, %d skipped, %d drawers, %d entities, %d triples",
		prefix,
		result.ProjectsScanned,
		result.SessionsImported,
		result.SessionsSkipped,
		result.DrawersCreated,
		result.EntitiesCreated,
		result.TriplesCreated,
	)
	if len(result.Errors) > 0 {
		fmt.Fprintf(os.Stderr, " (%d errors)", len(result.Errors))
	}
	fmt.Fprintln(os.Stderr)

	if !dryRun && (result.SessionsImported > 0 || result.DrawersCreated > 0) {
		fmt.Fprintln(os.Stderr, "\nRestart the MCP server to rebuild search indexes.")
	}
}
