// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/migrate"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func cmdMigrate() *cli.Command {
	return &cli.Command{
		Name:        "migrate",
		Synopsis:    "vp migrate <command> [flags]",
		Description: "Import data into vibe-palace from external sources.",
		Subcommands: []string{"migrate vibevault", "migrate mempalace"},
	}
}

var migrateVibeVaultFlags = []cli.FlagDef{
	{Name: "--vault-path", Arg: "PATH", Help: "VibeVault root (default: from config)"},
	{Name: "--dry-run", Help: "Preview import; prompts for conflict resolution like a real run. Use --yes to auto-accept defaults."},
	{Name: "--strict", Help: "Abort on the first frontmatter parse error (default: log file path, skip the session, continue)"},
	{Name: "--yes", Short: "-y", Help: "Accept default slug-rename suggestions without prompting"},
	{Name: "--slug-map", Arg: "OLD=NEW[,OLD=NEW...]", Help: "Pre-specify slug renames; uncovered collisions fall back to interactive or auto"},
}

func cmdMigrateVibeVault() *cli.Command {
	return &cli.Command{
		Name:        "migrate vibevault",
		Synopsis:    "vp migrate vibevault [--vault-path PATH] [--dry-run]",
		Description: "Import sessions from a VibeVault directory into the palace.",
		Flags:       migrateVibeVaultFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate vibevault", Comment: "Import from default vault path"},
			{Cmd: "vp migrate vibevault --vault-path ~/old-vault --dry-run", Comment: "Preview import from a specific directory"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateVibeVaultFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate vibevault: %v\n", err)
				return cli.ExitUser
			}
			vaultPath := fv.Get("--vault-path")
			dryRun := fv.Bool("--dry-run")
			strict := fv.Bool("--strict")
			yes := fv.Bool("--yes")
			slugMapArg := fv.Get("--slug-map")

			vault, cfg, err := openMigrateVault(vaultPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
				return cli.ExitUser
			}

			resolver, err := buildSlugResolver(vault.Root, yes, slugMapArg)
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
			fmt.Fprintln(os.Stderr, "Scanning projects...")

			result, err := migrate.ImportVibeVault(
				context.Background(), vault, eng, emb, cfg,
				migrate.ImportOptions{
					DryRun:   dryRun,
					Strict:   strict,
					Progress: migrateProgressFuncDeferred(),
					Resolver: resolver,
				},
			)
			if err != nil {
				if errors.Is(err, migrate.ErrResolverAbort) {
					fmt.Fprintf(os.Stderr, "\nAborted: %v\n", err)
					return cli.ExitUser
				}
				fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
				return cli.ExitSystem
			}

			printMigrateResult(result, dryRun)
			return cli.ExitOK
		},
	}
}

// buildSlugResolver constructs a SlugResolver from CLI flags.
// Selection rules:
//   - --slug-map (optional) pre-resolves listed collisions.
//   - --yes → AutoResolver for anything not covered.
//   - Else (stdin TTY) → InteractiveResolver; (non-TTY) → AutoResolver.
func buildSlugResolver(vaultRoot string, yes bool, slugMapArg string) (migrate.SlugResolver, error) {
	onDisk, err := scanOnDiskSlugsForResolver(filepath.Join(vaultRoot, "Projects"))
	if err != nil {
		return nil, fmt.Errorf("scan existing slugs: %w", err)
	}

	var base migrate.SlugResolver
	if yes || !isStdinTTY() {
		base = &migrate.AutoResolver{OnDisk: onDisk}
	} else {
		base = &migrate.InteractiveResolver{OnDisk: onDisk}
	}

	parsed, err := migrate.ParseSlugMap(slugMapArg)
	if err != nil {
		return nil, fmt.Errorf("--slug-map: %w", err)
	}
	if len(parsed) == 0 {
		return base, nil
	}
	return &migrate.MapResolver{Map: parsed, Fallback: base}, nil
}

// scanOnDiskSlugsForResolver returns the set of slugs already present
// as directories under {vault}/Projects/ so the resolver can avoid
// proposing rename targets that would collide with prior migrations.
func scanOnDiskSlugsForResolver(projectsDir string) (map[string]bool, error) {
	out := make(map[string]bool)
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s := slug.Slugify(e.Name())
		if s != "" {
			out[s] = true
		}
	}
	return out, nil
}

// isStdinTTY reports whether stdin is a terminal. Non-terminal stdin
// (pipe, file, /dev/null) means interactive prompts would block or
// loop on EOF; the resolver builder falls back to AutoResolver.
func isStdinTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
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
		Examples: []cli.Example{
			{Cmd: "vp migrate mempalace --export-path ~/export.json", Comment: "Import from MemPalace export"},
			{Cmd: "vp migrate mempalace --export-path ~/export.json --dry-run", Comment: "Preview import without writing"},
		},
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
		vault, err = openProjectVault()
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
			if evt.File != "" {
				fmt.Fprintf(os.Stderr, "    ERROR (%s) %s: %s\n", lastProject, evt.File, evt.Message)
			} else {
				fmt.Fprintf(os.Stderr, "    ERROR (%s): %s\n", lastProject, evt.Message)
			}
		}
	}
}

// migrateProgressFuncDeferred wraps migrateProgressFunc with a one-shot
// "Importing VibeVault sessions..." banner emitted on the first
// project-start event — so the scan/prompt phase can complete first.
func migrateProgressFuncDeferred() migrate.ProgressFunc {
	inner := migrateProgressFunc()
	var banner bool
	return func(evt migrate.ProgressEvent) {
		if !banner && evt.Type == migrate.ProgressProjectStart {
			fmt.Fprintln(os.Stderr, "Importing VibeVault sessions...")
			banner = true
		}
		inner(evt)
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

	if len(result.SlugRemap) > 0 {
		fmt.Fprintln(os.Stderr, "\nSlug remap (collisions resolved):")
		keys := make([]string, 0, len(result.SlugRemap))
		for k := range result.SlugRemap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(os.Stderr, "  %s → %s\n", k, result.SlugRemap[k])
		}
	}

	if !dryRun && (result.SessionsImported > 0 || result.DrawersCreated > 0) {
		fmt.Fprintln(os.Stderr, "\nRestart the MCP server to rebuild search indexes.")
	}
}
