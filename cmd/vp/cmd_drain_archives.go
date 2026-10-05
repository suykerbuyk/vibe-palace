// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/ingest"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var drainArchivesFlags = []cli.FlagDef{
	{Name: "--vault-root", Arg: "PATH", Help: "Absolute path to the vault root (required; never inferred)"},
	{Name: "--project", Arg: "SLUG", Help: "The project the trigger resolved (required; never inferred)"},
	{Name: "--first", Arg: "SHA256", Help: "source_sha256 of the archive the trigger just created (optional)"},
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func cmdDrainArchives() *cli.Command {
	return &cli.Command{
		Name:     "drain archives",
		Synopsis: "vp drain archives --vault-root PATH --project SLUG [--first SHA256]",
		Description: "Ingest the pending transcript archives of a vault into this host's " +
			"local index (palace/.local/), the triggering project first: one bounded, " +
			"idempotent pass, then exit. It is launched detached by the session hook, " +
			"vp_capture_session, the pull paths and `vp mcp` startup; nothing else runs " +
			"the automatic pass. --vault-root and --project are required and never " +
			"inferred from the working directory. --first names the archive the trigger " +
			"just created, by its source_sha256. It always exits 0: what happened goes " +
			"to palace/.local/vp.log. It writes nothing tracked.",
		Flags: drainArchivesFlags,
		Examples: []cli.Example{
			{Cmd: "vp drain archives --vault-root /path/to/vault --project myproj", Comment: "Ingest pending archives, myproj first"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(drainArchivesFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp drain archives: %v\n", err)
				return cli.ExitOK
			}
			return runDrainArchives(context.Background(), fv.Get("--vault-root"), fv.Get("--project"), fv.Get("--first"), os.Stderr)
		},
	}
}

// runDrainArchives is the testable body of `vp drain archives` (Scope 5). It
// resolves nothing: an empty or relative vault root, or a missing or invalid
// project, is one line on errOut and exit 0, with no archive opened and no
// lock taken. Otherwise it logs to the vault's palace/.local/vp.log (plan
// revision D12: a detached run's working directory is not the vault), and
// runs ingest.Run with an engine over a lazy embedder, so the model loads
// only on the first cache miss. It exits 0 whatever happened.
func runDrainArchives(ctx context.Context, vaultRoot, project, first string, errOut io.Writer) int {
	if vaultRoot == "" || project == "" {
		fmt.Fprintln(errOut, "vp drain archives: --vault-root and --project are required; nothing ingested")
		return cli.ExitOK
	}
	if !filepath.IsAbs(vaultRoot) {
		fmt.Fprintf(errOut, "vp drain archives: --vault-root must be absolute, got %q; nothing ingested\n", vaultRoot)
		return cli.ExitOK
	}
	if err := slug.Validate(project); err != nil {
		fmt.Fprintf(errOut, "vp drain archives: --project: %v; nothing ingested\n", err)
		return cli.ExitOK
	}
	v := storage.NewVault(vaultRoot)
	initLoggingForVault(v)
	if first != "" && !sha256Pattern.MatchString(first) {
		slog.Warn("vp drain archives: --first is not a source_sha256; ignored", "first", first, "project", project)
		first = ""
	}
	cfg, err := v.LoadConfig(project)
	if err != nil {
		slog.Warn("vp drain archives: cannot load the config; nothing ingested", "project", project, "error", err)
		return cli.ExitOK
	}
	emb := embedder.NewLazy(func() (embedder.Embedder, error) { return newVaultEmbedder(v, cfg) })
	eng := search.NewEngine(emb, v, cfg)
	defer eng.Close()
	res, err := ingest.Run(ctx, ingest.Deps{Vault: v, Engine: eng, Embedder: emb}, ingest.RunOptions{
		VaultRoot: vaultRoot, Project: project, First: first,
	})
	if err != nil {
		slog.Warn("vp drain archives: the run stopped", "project", project, "error", err)
		return cli.ExitOK
	}
	if res.LockHeld {
		slog.Info("vp drain archives: another index run holds the lock; it serves this trigger", "project", project)
		return cli.ExitOK
	}
	slog.Info("vp drain archives: done", "project", project, "committed", res.Committed, "failed", res.Failed,
		"changed", res.Changed, "repaired", res.Repaired, "stopped", res.Stopped, "passes", res.Passes)
	return cli.ExitOK
}
