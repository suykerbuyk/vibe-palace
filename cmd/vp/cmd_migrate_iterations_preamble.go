// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// Deleting the false "newest first" preamble.
//
// This is a SEPARATE command from `vp migrate iteration-headings` on purpose,
// and the separation is structural rather than stylistic. The heading migration
// claims to change heading LINES and nothing else, and that claim is verified by
// reading its diff. Folding an unrelated preamble edit into the same run would
// put non-heading lines in that diff and destroy the only cheap proof the
// heading migration has. Two commands, two diffs, two things that can each be
// checked by looking.
//
// See wrapstate.RepairIterationsPreamble for why the match is an exact literal
// and which three TRUE "newest first" statements elsewhere in the vault that
// choice protects.

var migrateIterationsPreambleFlags = []cli.FlagDef{
	{Name: "--vault", Arg: "PATH", Help: "Vault root to scan and repair (default: the configured vault_path)"},
	{Name: "--apply", Help: "WRITE the repairs. Without this the command only reports."},
}

// cmdMigrateIterationsPreamble replaces the false "newest first" HTML comment at
// the top of an iterations.md with an accurate one.
func cmdMigrateIterationsPreamble() *cli.Command {
	return &cli.Command{
		Name:     "migrate iterations-preamble",
		Synopsis: "vp migrate iterations-preamble [--vault PATH] [--apply]",
		Description: "Replace the FALSE \"newest first\" HTML comment at the top of an iterations.md " +
			"with an accurate one. The writer appends every narrative at the END of the file, so the " +
			"order is oldest first; the comment is not stale but backwards, and a reader who trusts " +
			"it takes the OLDEST narrative while believing it took the newest.\n\n" +
			"PLAN-FIRST: the bare command REPORTS and writes nothing; pass --apply to write.\n\n" +
			"The match is the known comment block BYTE-FOR-BYTE, and only where it sits ahead of the " +
			"first iteration heading. Anything else — a hand-edited variant, a second copy, a copy " +
			"quoted inside a narrative — is reported and left alone. Other, TRUE \"newest first\" " +
			"statements in the vault (rezbldr's preserved pre-rename block, and the narratives " +
			"describing `git log`) are untouched by construction.\n\n" +
			"Deliberately separate from `vp migrate iteration-headings` so that command's diff stays " +
			"provably heading-lines-only.",
		Flags: migrateIterationsPreambleFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate iterations-preamble", Comment: "Report which archives carry the false comment"},
			{Cmd: "vp migrate iterations-preamble --apply", Comment: "Replace it in the configured vault"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateIterationsPreambleFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate iterations-preamble: %v\n", err)
				return cli.ExitUser
			}
			root, err := resolveMigrationVaultRoot(fv.Get("--vault"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate iterations-preamble: %v\n", err)
				return cli.ExitUser
			}
			if _, err := runIterationsPreambleMigration(root, fv.Bool("--apply"), os.Stderr); err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate iterations-preamble: %v\n", err)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

// iterationsPreambleSummary is the roll-up of one preamble migration run.
type iterationsPreambleSummary struct {
	Projects int // archives scanned
	Repaired int // archives carrying the exact false block
	Refused  int // archives where the block was found in a shape not reasoned about
	Written  int // files actually written (0 unless --apply)
}

// runIterationsPreambleMigration scans every archive under root, prints what it
// found, and — with apply — writes the repaired files through the locked,
// surface-stamping vault write path under a compare-and-set guard against the
// bytes it planned from. The clean-tree gate is the same one the heading
// migration takes, and for the same reason: this edits the one file in the vault
// that has no second copy.
func runIterationsPreambleMigration(root string, apply bool, out io.Writer) (iterationsPreambleSummary, error) {
	var sum iterationsPreambleSummary

	projects, err := iterationArchiveProjects(root)
	if err != nil {
		return sum, err
	}
	if apply {
		if err := requireCleanVaultTree(root); err != nil {
			return sum, err
		}
	}

	printVaultRoot(out, root)
	if apply {
		fmt.Fprintln(out, "Mode:  APPLY — the false preamble comment will be replaced.")
	} else {
		fmt.Fprintln(out, "Mode:  REPORT ONLY — nothing is written. Pass --apply to write.")
	}
	fmt.Fprintln(out)

	for _, slug := range projects {
		rel := filepath.Join("Projects", slug, "iterations.md")
		data, rerr := os.ReadFile(filepath.Join(root, rel))
		if rerr != nil {
			continue
		}
		sum.Projects++
		updated, state := wrapstate.RepairIterationsPreamble(string(data))
		switch state {
		case wrapstate.PreambleAbsent:
			continue
		case wrapstate.PreambleRepaired:
			sum.Repaired++
			fmt.Fprintf(out, "%s — false \"newest first\" comment found; replacing with the append-only sentence\n", rel)
		default:
			sum.Refused++
			fmt.Fprintf(out, "%s — REFUSED (%s): the block is not in the one shape this repair was reasoned about; left unchanged\n", rel, state)
			continue
		}

		if !apply {
			continue
		}
		digest := sha256.Sum256(data)
		if _, werr := vaultfs.Write(root, rel, updated, hex.EncodeToString(digest[:])); werr != nil {
			return sum, fmt.Errorf("write %s: %w", rel, werr)
		}
		sum.Written++
	}

	verb := "would replace"
	if apply {
		verb = "replaced"
	}
	fmt.Fprintf(out, "\nScanned %d archives; %s the false preamble in %d, refused %d.\n",
		sum.Projects, verb, sum.Repaired, sum.Refused)
	if apply {
		fmt.Fprintf(out, "Files written: %d\n", sum.Written)
	}
	return sum, nil
}
