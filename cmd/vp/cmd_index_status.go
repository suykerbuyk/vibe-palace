// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var indexStatusFlags = []cli.FlagDef{
	{Name: "--json", Help: "Output the coverage struct as JSON"},
}

func cmdIndexStatus() *cli.Command {
	return &cli.Command{
		Name:     "index status",
		Synopsis: "vp index status [project] [--json]",
		Description: "Report this host's search-index coverage for a project: which of the seven " +
			"ordered states it is in (absent, stale, legacy, unbuilt, notes, partial, current) " +
			"and why, with the session counts behind it — n of m sessions ingested, and the " +
			"pending, backlog and failing archives separately. It also reports an index run in " +
			"progress (read from the run lock's advisory holder record while its pid is alive; " +
			"it takes no lock). It opens no archive and loads no embedder.",
		Flags: indexStatusFlags,
		Examples: []cli.Example{
			{Cmd: "vp index status myproj", Comment: "Coverage state and session counts for myproj"},
			{Cmd: "vp index status myproj --json", Comment: "The coverage struct as JSON"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(indexStatusFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp index status: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) > 1 {
				fmt.Fprintf(os.Stderr, "vp index status: at most one project may be named, got %v\n", pos)
				return cli.ExitUser
			}
			proj := ""
			if len(pos) == 1 {
				proj = pos[0]
			}
			return runIndexStatus(proj, fv.Bool("--json"), os.Stdout, os.Stderr)
		},
	}
}

// runIndexStatus resolves the project (positional, else from the working
// directory), opens the vault, builds a NO-EMBEDDER engine (a lazy embedder
// whose constructor the coverage path never invokes), and renders the coverage.
func runIndexStatus(proj string, asJSON bool, out, errOut io.Writer) int {
	if proj == "" {
		proj, _ = project.DetectProject(".")
	}
	if proj == "" {
		fmt.Fprintln(errOut, "vp index status: could not detect project (name one)")
		return cli.ExitUser
	}

	vault, err := openProjectVault()
	if err != nil {
		fmt.Fprintf(errOut, "vp index status: open vault: %v\n", err)
		return cli.ExitSystem
	}
	cfg, err := vault.LoadConfig(proj)
	if err != nil {
		fmt.Fprintf(errOut, "vp index status: load config: %v\n", err)
		return cli.ExitSystem
	}

	cov, err := indexCoverage(vault, cfg, proj)
	if err != nil {
		fmt.Fprintf(errOut, "vp index status %q: %v\n", proj, err)
		return cli.ExitSystem
	}

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(cov)
		return cli.ExitOK
	}
	printCoverage(out, cov)
	return cli.ExitOK
}

// indexCoverage builds a no-embedder engine and derives coverage. The lazy
// embedder's constructor is never called on the coverage path (Stale() and
// TrulyEmpty() load no model), so no ONNX model is loaded.
func indexCoverage(vault *storage.Vault, cfg storage.Config, proj string) (search.Coverage, error) {
	emb := embedder.NewLazy(func() (embedder.Embedder, error) { return newVaultEmbedder(vault, cfg) })
	eng := search.NewEngine(emb, vault, cfg)
	defer eng.Close()
	return eng.IndexCoverage(proj)
}

func printCoverage(out io.Writer, cov search.Coverage) {
	fmt.Fprintf(out, "%s: %s\n", cov.Project, cov.State)
	fmt.Fprintf(out, "  %s\n", cov.Reason)
	fmt.Fprintf(out, "  sessions: %d of %d ingested; pending %d, backlog %d, failing %d\n",
		cov.N, cov.M, cov.Pending, cov.Backlog, cov.Failing)
	if cov.Run != nil {
		r := cov.Run
		if r.Progress != nil {
			fmt.Fprintf(out, "  run in progress: pid %d, %s, project %q, %d/%d archives\n",
				r.PID, r.Kind, r.Project, r.Progress.Done, r.Progress.Total)
		} else {
			fmt.Fprintf(out, "  run in progress: pid %d, %s, project %q\n", r.PID, r.Kind, r.Project)
		}
	}
}
