// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/migrate"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

const migrateUsage = `vp migrate — import data into vibe-palace

Usage:
  vp migrate vibevault [--vault-path PATH] [--dry-run]
  vp migrate mempalace --export-path PATH [--dry-run]

Commands:
  vibevault   Import sessions from a VibeVault directory
  mempalace   Import data from a MemPalace JSON export

Options:
  --vault-path PATH    VibeVault root (default: from config)
  --export-path PATH   Path to MemPalace JSON export file
  --dry-run            Show what would be imported without writing
`

func runMigrate() int {
	if len(os.Args) < 3 {
		fmt.Fprint(os.Stderr, migrateUsage)
		return 1
	}

	switch os.Args[2] {
	case "vibevault":
		return runMigrateVibeVault()
	case "mempalace":
		return runMigrateMemPalace()
	case "help", "--help", "-h":
		fmt.Print(migrateUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "vp migrate: unknown command %q\nRun 'vp migrate help' for usage.\n", os.Args[2])
		return 1
	}
}

func runMigrateVibeVault() int {
	var vaultPath string
	var dryRun bool

	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--vault-path":
			if i+1 >= len(os.Args) {
				fmt.Fprintln(os.Stderr, "vp migrate vibevault: --vault-path requires a value")
				return 1
			}
			i++
			vaultPath = os.Args[i]
		case "--dry-run":
			dryRun = true
		default:
			fmt.Fprintf(os.Stderr, "vp migrate vibevault: unknown flag %q\n", os.Args[i])
			return 1
		}
	}

	vault, cfg, err := openMigrateVault(vaultPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
		return 1
	}

	emb, eng, cleanup, err := setupEmbedder(vault, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
		return 1
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
		return 1
	}

	printResult(result, dryRun)
	return 0
}

func runMigrateMemPalace() int {
	var exportPath string
	var dryRun bool

	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--export-path":
			if i+1 >= len(os.Args) {
				fmt.Fprintln(os.Stderr, "vp migrate mempalace: --export-path requires a value")
				return 1
			}
			i++
			exportPath = os.Args[i]
		case "--dry-run":
			dryRun = true
		default:
			fmt.Fprintf(os.Stderr, "vp migrate mempalace: unknown flag %q\n", os.Args[i])
			return 1
		}
	}

	if exportPath == "" {
		fmt.Fprintln(os.Stderr, "vp migrate mempalace: --export-path is required")
		return 1
	}

	vault, cfg, err := openMigrateVault("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
		return 1
	}

	emb, eng, cleanup, err := setupEmbedder(vault, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
		return 1
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
		return 1
	}

	printResult(result, dryRun)
	return 0
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

// printResult prints the migration summary to stderr.
func printResult(result migrate.ImportResult, dryRun bool) {
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
