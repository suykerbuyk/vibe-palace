// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/planscan"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Root `vp plans` is a read-only container that prints usage. The real work
// lives in `plans scan`. Registered UNWRAPPED (no mutates()): the scanner only
// reads ~/.claude/plans and the vault; it never promotes, deletes, or writes.
func cmdPlans() *cli.Command {
	return &cli.Command{
		Name:        "plans",
		Synopsis:    "vp plans <command> [flags]",
		Description: "Read-only reporting over Claude Code's flat ~/.claude/plans directory.",
		Subcommands: []string{"plans scan"},
	}
}

var plansScanFlags = []cli.FlagDef{
	{Name: "--json", Help: "Output JSON"},
}

func cmdPlansScan() *cli.Command {
	return &cli.Command{
		Name:     "plans scan",
		Synopsis: "vp plans scan [--json]",
		Description: "Scan ~/.claude/plans/*.md (honors CLAUDE_HOME) and report, for each " +
			"stray plan, the absolute paths in its body and an attribution: managed " +
			"(a vibe-palace-managed project, with slug), unmanaged (a real dir with no " +
			".vibe-palace.toml), or none (no absolute path, unattributable). Strictly " +
			"read-only — never promotes, deletes, or writes. Claude-only: an absent " +
			"plans dir (Grok/Zed) reports empty.",
		Flags: plansScanFlags,
		Examples: []cli.Example{
			{Cmd: "vp plans scan", Comment: "Report orphaned Claude plans"},
			{Cmd: "vp plans scan --json", Comment: "Emit JSON for scripting"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(plansScanFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp plans scan: %v\n", err)
				return cli.ExitUser
			}

			claudeHome, err := archive.ClaudeHome()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp plans scan: %v\n", err)
				return cli.ExitSystem
			}

			// Vault root is best-effort: a plan referencing a marked project can
			// still only be confirmed "managed" against a real vault, but an
			// absent vault must not fail the read-only scan — it just degrades
			// every marked candidate to "unmanaged".
			vaultRoot := ""
			cwd, cerr := os.Getwd()
			if cerr == nil {
				if vp, _, perr := storage.ResolveVaultPath(cwd); perr == nil {
					vaultRoot = vp
				}
			}

			rep, err := planscan.Scan(claudeHome, vaultRoot)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp plans scan: %v\n", err)
				return cli.ExitSystem
			}

			if fv.Bool("--json") {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(rep)
				return cli.ExitOK
			}

			if len(rep.Strays) == 0 {
				fmt.Fprintf(os.Stdout, "No stray plans under %s\n", rep.PlansDir)
				return cli.ExitOK
			}
			fmt.Fprintf(os.Stdout, "Stray plans under %s:\n", rep.PlansDir)
			for _, s := range rep.Strays {
				r := s.Resolution
				line := r.Kind
				switch r.Kind {
				case "managed":
					line = fmt.Sprintf("managed (%s)", r.Project)
				case "unmanaged":
					if r.CandidateDir != "" {
						line = fmt.Sprintf("unmanaged (%s)", r.CandidateDir)
					}
				}
				if r.Ambiguous {
					line += " [ambiguous: multi-root]"
				}
				fmt.Fprintf(os.Stdout, "  %s\n    -> %s\n", s.File, line)
			}
			return cli.ExitOK
		},
	}
}
