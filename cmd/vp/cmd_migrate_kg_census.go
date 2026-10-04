// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var migrateKGCensusFlags = []cli.FlagDef{
	{Name: "--json", Help: "Print the census as JSON"},
}

// cmdMigrateKGCensus classifies a vault copy's tracked KG records by origin,
// the classification the authored-only migration will apply. It is read-only.
func cmdMigrateKGCensus() *cli.Command {
	return &cli.Command{
		Name:     "migrate kg-census",
		Synopsis: "vp migrate kg-census <vault-copy> [--json]",
		Description: "Classify every tracked knowledge-graph triple file and entity line of a vault COPY " +
			"as authored or extracted, per project, with the triples that are authored only by the " +
			"pre-migration invalidation rule (X5) in their own column. Each project's totals are checked " +
			"exactly against an independent count of the copy's HEAD (git ls-tree for triples, " +
			"git cat-file for entities.jsonl); there is no tolerance and no unclassified bucket. " +
			"Read-only. Run it on a remote-stripped copy, never on the live vault. Exits non-zero " +
			"when any project's totals disagree with its HEAD.",
		Flags: migrateKGCensusFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate kg-census ~/vault-copy", Comment: "Census table for every project in the copy"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateKGCensusFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate kg-census: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) != 1 {
				fmt.Fprintln(os.Stderr, "vp migrate kg-census: exactly one argument, the vault copy, is required")
				return cli.ExitUser
			}
			rows, err := storage.KGCensus(pos[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate kg-census: %v\n", err)
				return cli.ExitSystem
			}
			if fv.Bool("--json") {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(rows); err != nil {
					return cli.ExitSystem
				}
			} else {
				printKGCensus(os.Stdout, rows)
			}
			for _, r := range rows {
				if !r.Matches() {
					fmt.Fprintf(os.Stderr, "vp migrate kg-census: %s: classified totals differ from HEAD\n", r.Project)
					return cli.ExitSystem
				}
			}
			return cli.ExitOK
		},
	}
}

func printKGCensus(w io.Writer, rows []storage.KGCensusRow) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tTRIPLES AUTHORED\t(RULE 3)\tTRIPLES EXTRACTED\tIN HEAD\tENTITIES AUTHORED\tENTITIES EXTRACTED\tIN HEAD\tMATCH")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%v\n", r.Project,
			r.TriplesAuthored, r.TriplesRule3, r.TriplesExtracted, r.TriplesInGit,
			r.EntitiesAuthored, r.EntitiesExtracted, r.EntitiesInGit, r.Matches())
	}
	tw.Flush()
}
