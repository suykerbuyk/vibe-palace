// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var searchFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect from the current directory). Must name a project in the vault; an unknown one exits 1."},
	{Name: "--wing", Short: "-w", Arg: "WING", Help: "Filter by wing"},
	{Name: "--room", Short: "-r", Arg: "ROOM", Help: "Filter by room"},
	{Name: "--limit", Short: "-n", Arg: "N", Help: "Max results", Default: "10"},
	{Name: "--json", Help: "Output JSON"},
	{Name: "--raw", Help: "Also show raw source-text hits alongside summary hits (default: raw hits are hidden when a summary exists)."},
}

func cmdSearch() *cli.Command {
	return &cli.Command{
		Name:     "search",
		Synopsis: "vp search <query> [flags]",
		Description: "Semantic search across palace content. The project — named by --project or " +
			"detected from the current directory — must exist in the vault (a Projects/<slug>/ " +
			"directory or a palace store); an unknown or malformed project exits 1 before the " +
			"embedding model loads.",
		Flags: searchFlags,
		Examples: []cli.Example{
			{Cmd: "vp search \"database migrations\"", Comment: "Search current project"},
			{Cmd: "vp search \"auth\" -p myapp -n 5", Comment: "Search specific project"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(searchFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp search: %v\n", err)
				return cli.ExitUser
			}

			positional := fv.Args()
			if len(positional) == 0 {
				fmt.Fprintln(os.Stderr, "vp search: query argument required")
				return cli.ExitUser
			}
			query := positional[0]

			proj := fv.Get("--project")
			detected := proj == ""
			if detected {
				proj, _ = project.DetectProject(".")
			}
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp search: could not detect project (use --project)")
				return cli.ExitUser
			}

			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp search: %v\n", err)
				return cli.ExitUser
			}

			cfg, err := vault.LoadConfig("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp search: %v\n", err)
				return cli.ExitUser
			}

			// Validate the project before the model loads: a typo must not
			// cost a ~90 MB download to be answered "No results found.".
			if code := requireSearchProject(vault, proj, detected); code != cli.ExitOK {
				return code
			}

			emb, err := newVaultEmbedder(vault, cfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp search: embedder: %v\n", err)
				return cli.ExitSystem
			}
			defer emb.Close()

			eng := search.NewEngine(emb, vault, cfg)
			defer eng.Close()

			limit := fv.Int("--limit")
			if limit == 0 {
				limit = 10
			}

			return runSearch(eng, proj, query, fv.Get("--wing"), fv.Get("--room"), limit, fv.Bool("--raw"), fv.Bool("--json"), os.Stdout)
		},
	}
}

// requireSearchProject refuses a search scoped to a project the vault does not
// hold, printing why and returning the exit code (cli.ExitOK when proj
// exists). A search of a project that does not exist is an error whichever way
// the project was named; answering it with empty results is a silent skip
// dressed as success.
//
// It is a thin wrapper over search.ProjectExists — the "exists" predicate,
// including exactly which trees and which oddities (a notes-only project, a
// .local-only palace husk, a symlinked Projects/<slug> or palace/<slug>) count,
// lives on that function's doc comment now, because it is the layer both this
// CLI check and (*search.Engine).Search's own inline check share. This wrapper
// owns only the CLI-specific parts: syntax-checking a flagged slug, and naming
// how the project was resolved (flag vs. detected) in the refusal.
//
// A flagged slug is syntax-checked first. A detected one already was, by
// DetectProject, so only membership applies to it — and its refusal names the
// detection, because the operator never typed the slug that failed.
func requireSearchProject(vault *storage.Vault, proj string, detected bool) int {
	if !detected {
		if err := slug.Validate(proj); err != nil {
			fmt.Fprintf(os.Stderr, "vp search: --project: %v\n", err)
			return cli.ExitUser
		}
	}
	exists, err := search.ProjectExists(vault, proj)
	if err != nil {
		// "I could not look" is not "absent".
		fmt.Fprintf(os.Stderr, "vp search: list projects: %v\n", err)
		return cli.ExitSystem
	}
	if exists {
		return cli.ExitOK
	}
	if detected {
		fmt.Fprintf(os.Stderr, "vp search: no project %q in vault %s (detected from the current directory); run 'vp init' here or pass --project\n", proj, vault.Root)
	} else {
		fmt.Fprintf(os.Stderr, "vp search: --project %q: no such project in vault %s\n", proj, vault.Root)
	}
	return cli.ExitUser
}

func runSearch(eng *search.Engine, proj, query, wing, room string, limit int, includeRaw, asJSON bool, out io.Writer) int {
	ctx := context.Background()

	// Synchronous rebuild for the target project.
	if _, err := eng.Rebuild(ctx, proj); err != nil {
		fmt.Fprintf(os.Stderr, "vp search: rebuild index: %v\n", err)
		return cli.ExitSystem
	}

	results, err := eng.Search(ctx, query, search.SearchFilters{
		Project:    proj,
		Wing:       wing,
		Room:       room,
		Limit:      limit,
		IncludeRaw: includeRaw,
	})
	if err != nil {
		// An unknown project is a user error (ExitUser), never "I could not
		// search" (ExitSystem) — mirroring searchHandler's own
		// errors.As(*search.UnknownProjectError) special-case. Unreachable
		// from cmdSearch today, since requireSearchProject already refuses an
		// unknown project before runSearch is ever called; kept here as
		// defense-in-depth for any other caller of runSearch, and so a future
		// reordering can't silently regress the exit code.
		var unk *search.UnknownProjectError
		if errors.As(err, &unk) {
			fmt.Fprintf(os.Stderr, "vp search: %v\n", err)
			return cli.ExitUser
		}
		fmt.Fprintf(os.Stderr, "vp search: %v\n", err)
		return cli.ExitSystem
	}

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.Encode(results)
		return cli.ExitOK
	}

	if len(results) == 0 {
		fmt.Fprintln(out, "No results found.")
		return cli.ExitOK
	}

	for i, r := range results {
		content := r.Content
		if len(content) > 80 {
			content = content[:77] + "..."
		}
		fmt.Fprintf(out, "%d. [%.3f] %s/%s  %s\n", i+1, r.Score, r.Wing, r.Room, content)
	}
	return cli.ExitOK
}
