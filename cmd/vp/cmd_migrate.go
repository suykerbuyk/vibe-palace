// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	{Name: "--vault-path", Arg: "PATH", Help: "SOURCE VibeVault root to read sessions from (default: the configured vault). Writes always land in the configured vault_path, never here."},
	{Name: "--dry-run", Help: "Preview import; prompts for conflict resolution like a real run. Use --yes to auto-accept defaults."},
	{Name: "--strict", Help: "Abort on the first frontmatter parse error (default: log file path, skip the session, continue)"},
	{Name: "--yes", Short: "-y", Help: "Accept default slug-rename suggestions without prompting"},
	{Name: "--slug-map", Arg: "OLD=NEW[,OLD=NEW...]", Help: "Pre-specify slug renames; uncovered collisions fall back to interactive or auto"},
}

func cmdMigrateVibeVault() *cli.Command {
	return &cli.Command{
		Name:        "migrate vibevault",
		Synopsis:    "vp migrate vibevault [--vault-path PATH] [--dry-run]",
		Description: "Import sessions from a VibeVault directory into the palace. " +
			"--vault-path names the SOURCE vault to read sessions from; the DESTINATION " +
			"is always the configured vault_path (~/.config/vibe-palace/config.toml). All " +
			"writes — palace data, idempotency markers, embed and model caches, and per-project " +
			"config scaffolds — land in the destination. When --vault-path is omitted, source " +
			"and destination are the same configured vault. Every run prints a Source/Destination/" +
			"Same-vault banner to stderr before scanning; a real (non-dry-run) cross-vault import " +
			"requires confirmation (--yes, or an interactive [y/N] prompt on a TTY).",
		Flags: migrateVibeVaultFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate vibevault", Comment: "Import in place: source and destination are the configured vault"},
			{Cmd: "vp migrate vibevault --vault-path ~/old-vault --dry-run", Comment: "Preview import reading from another vault (writes nothing)"},
			{Cmd: "vp migrate vibevault --vault-path /home/johns/obsidian/VibeVault --yes", Comment: "Cross-vault import: read from VibeVault, write to the configured vault (--yes confirms)"},
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

			dest, cfg, err := openMigrateDestination()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
				return cli.ExitUser
			}

			source, err := openMigrateSource(vaultPath, dest)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
				return cli.ExitUser
			}

			// Resolve both roots to absolute paths for an accurate
			// "Same vault" comparison without mutating the vaults.
			resolvedSource, err := expandAndAbsPath(source.Root)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
				return cli.ExitUser
			}
			resolvedDest, err := expandAndAbsPath(dest.Root)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
				return cli.ExitUser
			}
			sameVault := resolvedSource == resolvedDest

			// Banner on every run (real and dry), before any other output.
			fmt.Fprint(os.Stderr, migrateBanner(resolvedSource, resolvedDest))

			// Real-run cross-vault confirmation gate (before the embedder
			// loads, so an abort costs nothing).
			proceed, abortMsg := confirmCrossVaultWrite(
				sameVault, dryRun, yes, isStdinTTY(),
				resolvedSource, resolvedDest, os.Stdin, os.Stderr,
			)
			if !proceed {
				if abortMsg != "" {
					fmt.Fprintln(os.Stderr, abortMsg)
				}
				return cli.ExitUser
			}

			// Orphan-marker preflight warning for cross-vault runs.
			if sourceHasOrphanMarkers(resolvedSource, resolvedDest) {
				fmt.Fprintf(os.Stderr,
					"WARNING: prior runs left idempotency markers in the source vault, but after\n"+
						"this fix markers are read from the destination. Those sessions will be\n"+
						"re-processed (slow; re-embeds). Palace artifacts are deduped by ID, so no\n"+
						"corruption results.\n"+
						"One-time remediation: copy\n"+
						"  %s/palace/*/.local/imported-sessions.jsonl\n"+
						"into the matching\n"+
						"  %s/palace/*/.local/\n"+
						"before running.\n",
					resolvedSource, resolvedDest,
				)
			}

			// Resolver scans the SOURCE for sibling-collision avoidance.
			resolver, err := buildSlugResolver(source.Root, yes, slugMapArg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
				return cli.ExitUser
			}

			// Engine and model cache bind to the DESTINATION.
			emb, eng, cleanup, err := setupEmbedder(dest, cfg)
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
				context.Background(), source, dest, eng, emb, cfg,
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

			dest, cfg, err := openMigrateDestination()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate: %v\n", err)
				return cli.ExitUser
			}

			emb, eng, cleanup, err := setupEmbedder(dest, cfg)
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
				context.Background(), dest, eng, emb, exportPath,
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

// openMigrateDestination opens the canonical write target — always the
// config `vault_path`-rooted vault (honoring any cwd-local override) — and
// loads its config. This is where all migration writes land.
func openMigrateDestination() (*storage.Vault, storage.Config, error) {
	vault, err := openProjectVault()
	if err != nil {
		return nil, storage.Config{}, fmt.Errorf("open vault: %w", err)
	}

	cfg, err := vault.LoadConfig("")
	if err != nil {
		return nil, storage.Config{}, fmt.Errorf("load config: %w", err)
	}

	return vault, cfg, nil
}

// openMigrateSource resolves the read source for a migration. When
// vaultPath is set, it returns a new Vault rooted at the resolved
// (tilde-expanded, absolute) path; otherwise it returns dest, so the
// shared-vault (in-place) case reuses the destination pointer.
func openMigrateSource(vaultPath string, dest *storage.Vault) (*storage.Vault, error) {
	if vaultPath == "" {
		return dest, nil
	}
	resolved, err := expandAndAbsPath(vaultPath)
	if err != nil {
		return nil, fmt.Errorf("resolve --vault-path: %w", err)
	}
	return storage.NewVault(resolved), nil
}

// migrateBanner formats the source/destination summary printed before
// every vibevault migration run. source and dest must be already-resolved
// absolute paths.
func migrateBanner(source, dest string) string {
	same := "no"
	if source == dest {
		same = "yes"
	}
	return fmt.Sprintf("Source:      %s\nDestination: %s\nSame vault:  %s\n", source, dest, same)
}

// confirmCrossVaultWrite gates a real (non-dry-run) migration that writes
// into a destination different from the source.
//
//   - same vault, dry run, or --yes → proceed (no prompt).
//   - TTY → prompt on promptOut, read a line from in, proceed on y/yes.
//   - non-TTY without --yes → refuse, returning an explanatory message.
//
// Returns (proceed, abortMsg); abortMsg is a user-facing reason to print
// when proceed is false (empty if no message is warranted).
func confirmCrossVaultWrite(sameVault, dryRun, yes, tty bool, source, dest string, in io.Reader, promptOut io.Writer) (bool, string) {
	if sameVault || dryRun || yes {
		return true, ""
	}
	if !tty {
		return false, "cross-vault migration into a different destination requires --yes (or a TTY to confirm)"
	}
	fmt.Fprintf(promptOut, "Write into %s (reading from %s)? [y/N]: ", dest, source)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, ""
	default:
		return false, "Aborted."
	}
}

// sourceHasOrphanMarkers reports whether the source vault holds
// per-project idempotency markers (palace/*/.local/imported-sessions.jsonl)
// that the destination lacks. After the source/destination split, markers
// are read from the destination, so orphaned source-side markers would no
// longer suppress re-processing. Returns false when the roots are identical.
func sourceHasOrphanMarkers(srcRoot, dstRoot string) bool {
	if srcRoot == dstRoot {
		return false
	}
	srcMarkers, _ := filepath.Glob(filepath.Join(srcRoot, "palace", "*", ".local", "imported-sessions.jsonl"))
	if len(srcMarkers) == 0 {
		return false
	}
	dstMarkers, _ := filepath.Glob(filepath.Join(dstRoot, "palace", "*", ".local", "imported-sessions.jsonl"))
	return len(dstMarkers) == 0
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
