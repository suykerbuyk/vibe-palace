// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

var vaultGCFlags = []cli.FlagDef{
	{Name: "--apply", Help: "Actually remove provably-unheld lock sidecars (default is a dry run that removes nothing)"},
	{Name: "--dry-run", Help: "Report what would be removed without removing anything (the default)"},
	{Name: "--json", Help: "Output the sweep report as JSON"},
	vaultRootFlag,
}

// vaultGCReportJSON is the versioned JSON shape for `vp vault gc --json`.
type vaultGCReportJSON struct {
	Version   int                `json:"version"`
	Vault     string             `json:"vault"`
	Quiescent bool               `json:"quiescent"`
	DryRun    bool               `json:"dry_run"`
	Entries   []vaultGCEntryJSON `json:"entries"`
}

type vaultGCEntryJSON struct {
	Sidecar string `json:"sidecar"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// cmdVaultGC garbage-collects stale .vp-locks sidecar litter. It is registered
// WITHOUT mutates(): it runs no git, writes no vault content, and reaches no
// vault-write funnel sink (it only os.Removes empty advisory-lock markers), so it
// derives non-mutating and must stay declared ungated (ADR-003 amendment
// 2026-10-27; the derived-gate check in internal/sourceaudit).
func cmdVaultGC() *cli.Command {
	return &cli.Command{
		Name:     "vault gc",
		Synopsis: "vp vault gc [--apply] [--json] [--vault PATH]",
		Description: "Garbage-collect stale .vp-locks sidecar litter: the empty 0-byte advisory-lock " +
			"marker files that accumulate one per distinct vault path ever written. It removes ONLY " +
			"sidecars whose advisory lock no process holds, and only at a quiescent instant — it takes " +
			"an exclusive coordination lock via a bounded non-blocking poll and ABORTS (removing " +
			"nothing, reporting 'busy') if the vault is actively being written. It runs no git and " +
			"removes no vault content. Dry-run is the DEFAULT; pass --apply to actually remove.",
		Flags: vaultGCFlags,
		Examples: []cli.Example{
			{Cmd: "vp vault gc", Comment: "Preview which unheld lock sidecars would be removed (dry run)"},
			{Cmd: "vp vault gc --apply", Comment: "Remove the provably-unheld lock sidecars"},
			{Cmd: "vp vault gc --json", Comment: "Emit the sweep report as JSON"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(vaultGCFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp vault gc: %v\n", err)
				return cli.ExitUser
			}

			// Resolve the vault the cwd-aware way (an explicit --vault overrides and
			// must name the top level of its own repo, so a subfolder cannot redirect
			// the sweep). No git_enabled gate: gc runs no git.
			root, source, rerr := resolveVaultRootFlag(fv.Get("--vault"), vaultBindingRootWithSource)
			if rerr == nil && source == vaultFlagSource {
				rerr = refuseUnlessOwnGitRepo(root)
			}
			if rerr != nil {
				fmt.Fprintf(os.Stderr, "vp vault gc: %v\n", rerr)
				return cli.ExitUser
			}

			// Dry-run is default-on; --apply turns it off. --dry-run is accepted as
			// the explicit (default) form, and --dry-run=false is an alias for --apply.
			// Passing both --apply and --dry-run is contradictory and refused rather
			// than silently letting one win.
			if fv.Bool("--apply") && fv.IsSet("--dry-run") && fv.Bool("--dry-run") {
				fmt.Fprintln(os.Stderr, "vp vault gc: --apply and --dry-run are mutually exclusive")
				return cli.ExitUser
			}
			dryRun := true
			if fv.Bool("--apply") {
				dryRun = false
			}
			if fv.IsSet("--dry-run") {
				dryRun = fv.Bool("--dry-run")
			}

			rep, serr := storage.NewVault(root).GCLocks(dryRun)
			if serr != nil {
				fmt.Fprintf(os.Stderr, "vp vault gc: %v\n", serr)
				return cli.ExitSystem
			}

			if fv.Bool("--json") {
				return printVaultGCJSON(root, rep)
			}
			printVaultRoot(os.Stdout, root)
			printVaultGC(rep)
			return cli.ExitOK
		},
	}
}

// printVaultGC renders the human-readable sweep report.
func printVaultGC(rep vaultlock.SweepReport) {
	if !rep.Quiescent {
		fmt.Println("busy: the vault is being written; nothing was removed — retry when idle")
		return
	}
	fmt.Println("quiescent")
	if len(rep.Entries) == 0 {
		fmt.Println("no lock sidecars to sweep")
		return
	}
	for _, e := range rep.Entries {
		fmt.Printf("  %-13s %s (%s)\n", e.Verdict, e.Name, e.Reason)
	}
	verb, target := "removed", "removed"
	if rep.DryRun {
		verb, target = "would remove", "would-remove"
	}
	n := 0
	for _, e := range rep.Entries {
		if e.Verdict == target {
			n++
		}
	}
	fmt.Printf("%s %d sidecar(s)\n", verb, n)
}

func printVaultGCJSON(root string, rep vaultlock.SweepReport) int {
	out := vaultGCReportJSON{
		Version:   1,
		Vault:     root,
		Quiescent: rep.Quiescent,
		DryRun:    rep.DryRun,
	}
	for _, e := range rep.Entries {
		out.Entries = append(out.Entries, vaultGCEntryJSON{Sidecar: e.Name, Verdict: e.Verdict, Reason: e.Reason})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp vault gc: marshal json: %v\n", err)
		return cli.ExitSystem
	}
	fmt.Println(string(b))
	return cli.ExitOK
}
