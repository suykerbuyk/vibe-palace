// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/ingest"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var indexRebuildFlags = []cli.FlagDef{
	{Name: "--all", Help: "Rebuild every project of the vault, in slug order, under one hold of the run lock"},
	{Name: "--skip", Arg: "SLUG", Help: "Leave a project out of the run entirely (repeatable); its ledger, baseline set and chunks stay untouched"},
	{Name: "--max-archives", Arg: "N", Help: "Stop the archive pass after N archives (the run then does not complete)"},
	{Name: "--dry-run", Help: "Report the pending archives, the baseline set, the estimate and the reserve; write nothing and take no lock"},
	{Name: "--no-embed", Help: "Write chunks, the local KG and the ledger but embed nothing (an operator mode; the run does not complete)"},
	{Name: "--vault-root", Arg: "PATH", Help: "Absolute vault root (optional; resolved from the working directory otherwise). Set by the detached MCP launch."},
}

func cmdIndexRebuild() *cli.Command {
	return &cli.Command{
		Name:     "index rebuild",
		Synopsis: "vp index rebuild [project] [--all] [--skip SLUG]... [--max-archives N] [--dry-run] [--no-embed]",
		Description: "Rebuild this host's local search index for one project (or every " +
			"project, with --all) from its tracked transcript archives and every other " +
			"corpus tier. This is the EXPLICIT, resumable path: it ingests the historical " +
			"backlog (the baseline set), retries archives the automatic ingester skips after " +
			"repeated failures, discards on a fingerprint mismatch, and is the only path that " +
			"empties the baseline set and clears a fingerprint-reason stale flag — on " +
			"completion only. A disk-and-inode watchdog refuses to start, and stops a running " +
			"rebuild, before it would cross a free-space reserve. It writes nothing tracked. " +
			"--skip leaves a project wholly untouched (repeatable). --max-archives caps the " +
			"run (which then does not complete). --no-embed writes chunks and the ledger but " +
			"embeds nothing. --dry-run reports the plan and the estimate and writes nothing.",
		Flags: indexRebuildFlags,
		Examples: []cli.Example{
			{Cmd: "vp index rebuild myproj", Comment: "Rebuild myproj's index, backlog included"},
			{Cmd: "vp index rebuild myproj --dry-run", Comment: "Report what a rebuild would do, and its disk estimate"},
			{Cmd: "vp index rebuild --all --skip qa-metabuild-system --skip orchestrator", Comment: "Rebuild every project but the two named"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(indexRebuildFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp index rebuild: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) > 1 {
				fmt.Fprintf(os.Stderr, "vp index rebuild: at most one project may be named, got %v\n", pos)
				return cli.ExitUser
			}
			project := ""
			if len(pos) == 1 {
				project = pos[0]
			}
			return runIndexRebuild(context.Background(), indexRebuildArgs{
				vaultRoot:   fv.Get("--vault-root"),
				project:     project,
				all:         fv.Bool("--all"),
				skip:        fv.GetAll("--skip"),
				maxArchives: fv.Int("--max-archives"),
				dryRun:      fv.Bool("--dry-run"),
				noEmbed:     fv.Bool("--no-embed"),
			}, os.Stdout, os.Stderr)
		},
	}
}

type indexRebuildArgs struct {
	vaultRoot   string
	project     string
	all         bool
	skip        []string
	maxArchives int
	dryRun      bool
	noEmbed     bool
}

// runIndexRebuild is the testable body of `vp index rebuild`. It resolves the
// vault from --vault-root when given (the detached MCP launch sets it, since a
// detached child's cwd is not the vault) and otherwise from the working
// directory, builds an engine over a lazy embedder, and runs the driver
// (ingest.Rebuild) or the preflight (ingest.DryRun).
func runIndexRebuild(ctx context.Context, a indexRebuildArgs, out, errOut io.Writer) int {
	if a.all && a.project != "" {
		fmt.Fprintln(errOut, "vp index rebuild: a project and --all are mutually exclusive")
		return cli.ExitUser
	}
	if !a.all && a.project == "" {
		fmt.Fprintln(errOut, "vp index rebuild: name a project, or pass --all")
		return cli.ExitUser
	}

	var v *storage.Vault
	var err error
	if a.vaultRoot != "" {
		if !filepath.IsAbs(a.vaultRoot) {
			fmt.Fprintf(errOut, "vp index rebuild: --vault-root must be absolute, got %q\n", a.vaultRoot)
			return cli.ExitUser
		}
		v, err = OpenProjectVaultAt(a.vaultRoot)
	} else {
		v, err = openProjectVault()
	}
	if err != nil {
		fmt.Fprintf(errOut, "vp index rebuild: open vault: %v\n", err)
		return cli.ExitSystem
	}
	initLoggingForVault(v)

	cfgProject := a.project
	if a.all {
		cfgProject = ""
	}
	cfg, err := v.LoadConfig(cfgProject)
	if err != nil {
		fmt.Fprintf(errOut, "vp index rebuild: load config: %v\n", err)
		return cli.ExitSystem
	}
	emb := embedder.NewLazy(func() (embedder.Embedder, error) { return newVaultEmbedder(v, cfg) })
	eng := search.NewEngine(emb, v, cfg)
	defer eng.Close()

	d := ingest.Deps{Vault: v, Engine: eng, Embedder: emb}
	opts := ingest.RebuildOptions{
		Project:     a.project,
		All:         a.all,
		Skip:        a.skip,
		MaxArchives: a.maxArchives,
		NoEmbed:     a.noEmbed,
	}

	if a.dryRun {
		rep, err := ingest.DryRun(d, opts)
		if err != nil {
			fmt.Fprintf(errOut, "vp index rebuild --dry-run: %v\n", err)
			return cli.ExitUser
		}
		printDryRun(out, rep)
		if rep.Refusal != "" {
			fmt.Fprintf(errOut, "vp index rebuild --dry-run: %s\n", rep.Refusal)
			return cli.ExitUser
		}
		return cli.ExitOK
	}

	res, err := ingest.Rebuild(ctx, d, opts)
	if err != nil {
		fmt.Fprintf(errOut, "vp index rebuild: %v\n", err)
		if errors.Is(err, ingest.ErrRunLockHeld) || errors.Is(err, ingest.ErrReserve) {
			return cli.ExitUser
		}
		return cli.ExitSystem
	}
	fmt.Fprintf(out, "vp index rebuild: committed=%d failed=%d repaired=%d projects=%v stopped=%q\n",
		res.Committed, res.Failed, res.Repaired, res.Projects, res.Stopped)
	return cli.ExitOK
}

// printDryRun renders a dry-run report: per project the pending archives and
// the baseline set separately with their bytes, then the estimate, the reserve
// and the free space.
func printDryRun(out io.Writer, rep ingest.DryRunReport) {
	for _, p := range rep.Projects {
		fmt.Fprintf(out, "project %s:\n", p.Project)
		fmt.Fprintf(out, "  pending:  %d archives, %d compressed bytes, %d uncompressed bytes\n",
			p.PendingArchives, p.PendingCompressedBytes, p.PendingUncompressedBytes)
		fmt.Fprintf(out, "  baseline: %d archives, %d compressed bytes, %d uncompressed bytes\n",
			p.BaselineArchives, p.BaselineCompressedBytes, p.BaselineUncompressedBytes)
		fmt.Fprintf(out, "  estimate: %d bytes, %d inodes\n", p.Estimate.Bytes, p.Estimate.Inodes)
	}
	fmt.Fprintf(out, "reserve: %d bytes, %d inodes\n", rep.Reserve.Bytes, rep.Reserve.Inodes)
	if rep.Free.HasInodes {
		fmt.Fprintf(out, "free:    %d bytes, %d inodes\n", rep.Free.Bytes, rep.Free.Inodes)
	} else {
		fmt.Fprintf(out, "free:    %d bytes (inodes not reported on this platform)\n", rep.Free.Bytes)
	}
}
