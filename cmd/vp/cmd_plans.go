// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"errors"
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
			//
			// A REJECTED config is not the absent case and does not get that
			// silence: the scan would label every candidate "unmanaged" on the
			// strength of a vault_path that was written, ignored, and never
			// reported. The scan still runs — it is read-only and the listing is
			// useful — but the reason the labels are degraded is stated.
			vaultRoot := ""
			degraded := ""
			cwd, cerr := os.Getwd()
			if cerr == nil {
				vp, _, perr := storage.ResolveVaultPath(cwd)
				switch {
				case perr == nil:
					vaultRoot = vp
				case errors.Is(perr, storage.ErrSwallowedVaultPath):
					degraded = perr.Error()
					fmt.Fprintf(os.Stderr, "vp plans scan: %v\n", perr)
					fmt.Fprintln(os.Stderr, "vp plans scan: every marked candidate below is reported \"unmanaged\" because of the above.")
				}
			}

			rep, err := planscan.Scan(claudeHome, vaultRoot)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp plans scan: %v\n", err)
				return cli.ExitSystem
			}
			// stderr does not survive a pipe into jq. Without this a --json
			// consumer cannot tell a genuinely unmanaged tree from an
			// unevaluated one.
			rep.VaultUnresolved = degraded

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
