// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/search"
)

var searchFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--wing", Short: "-w", Arg: "WING", Help: "Filter by wing"},
	{Name: "--room", Short: "-r", Arg: "ROOM", Help: "Filter by room"},
	{Name: "--limit", Short: "-n", Arg: "N", Help: "Max results", Default: "10"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdSearch() *cli.Command {
	return &cli.Command{
		Name:        "search",
		Synopsis:    "vp search <query> [flags]",
		Description: "Semantic search across palace content.",
		Flags:       searchFlags,
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
			if proj == "" {
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

			modelDir := vault.VaultLocalDir() + "/models"
			emb, err := embedder.NewONNX(
				cfg.EmbedderModel, modelDir,
				cfg.EmbedderMaxSeqLen, cfg.EmbedderBatchSize,
			)
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

			return runSearch(eng, proj, query, fv.Get("--wing"), fv.Get("--room"), limit, fv.Bool("--json"), os.Stdout)
		},
	}
}

func runSearch(eng *search.Engine, proj, query, wing, room string, limit int, asJSON bool, out io.Writer) int {
	ctx := context.Background()

	// Synchronous rebuild for the target project.
	if err := eng.Rebuild(ctx, proj); err != nil {
		fmt.Fprintf(os.Stderr, "vp search: rebuild index: %v\n", err)
		return cli.ExitSystem
	}

	results, err := eng.Search(ctx, query, search.SearchFilters{
		Project: proj,
		Wing:    wing,
		Room:    room,
		Limit:   limit,
	})
	if err != nil {
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
