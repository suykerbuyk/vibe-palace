// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var statusFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdStatus() *cli.Command {
	return &cli.Command{
		Name:        "status",
		Synopsis:    "vp status [--project P] [--json]",
		Description: "Show palace overview for a project. Displays the resolved vault path and its source, session count, task summary, and recent activity.",
		Flags:       statusFlags,
		Examples: []cli.Example{
			{Cmd: "vp status", Comment: "Show status for the current project"},
			{Cmd: "vp status -p myapp --json", Comment: "Show status as JSON for a specific project"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(statusFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp status: %v\n", err)
				return cli.ExitUser
			}

			proj := fv.Get("--project")
			if proj == "" {
				proj, _ = project.DetectProject(".")
			}
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp status: could not detect project (use --project)")
				return cli.ExitUser
			}

			// Resolve ONCE, here, rather than calling openProjectVault and then
			// re-resolving for the source: OpenVaultFromCwd already calls
			// ResolveVaultPath and throws the source away, so asking twice would
			// be two answers that can disagree — and printing a path that is not
			// the vault this command actually read is a worse lie than printing
			// nothing, which is the bug being fixed.
			cwd, err := os.Getwd()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp status: get working directory: %v\n", err)
				return cli.ExitUser
			}
			vaultPath, vaultSource, err := storage.ResolveVaultPath(cwd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp status: %v\n", err)
				return cli.ExitUser
			}

			return runStatus(storage.NewVault(vaultPath), proj, vaultSource, fv.Bool("--json"), os.Stdout)
		},
	}
}

type statusResult struct {
	Project string `json:"project"`
	// VaultPath is the root of the vault this run actually read — vault.Root,
	// never a re-resolution — and VaultPathSource names where that binding came
	// from, in the same cwd:<file> / global:<configpath> vocabulary vp check
	// uses. Both exist so "resolve, don't recall" has a command that answers.
	VaultPath       string              `json:"vault_path"`
	VaultPathSource string              `json:"vault_path_source"`
	Palace          *palace.PalaceStats `json:"palace,omitempty"`
	Tasks           int                 `json:"active_tasks"`
	Sessions        int                 `json:"recent_sessions"`
	KG              *storage.KGStats    `json:"knowledge_graph,omitempty"`
}

// runStatus renders the status report. vaultSource is the resolution source for
// vault, supplied by the caller that resolved it; an empty one is reported as
// "unknown" rather than guessed at, because a fabricated source is exactly the
// recalled-instead-of-resolved answer this output exists to replace.
func runStatus(vault *storage.Vault, proj string, vaultSource string, asJSON bool, out io.Writer) int {
	if vaultSource == "" {
		vaultSource = "unknown"
	}
	result := statusResult{Project: proj, VaultPath: vault.Root, VaultPathSource: vaultSource}

	if g, err := palace.BuildGraph(vault, proj); err == nil {
		stats := g.Stats()
		result.Palace = &stats
	}

	if tasks, err := vault.ListTasks(proj, false); err == nil {
		result.Tasks = len(tasks)
	}

	if sessions, err := vault.ListSessions(proj, "", "", 0); err == nil {
		result.Sessions = len(sessions)
	}

	if stats, err := vault.KGStats(proj); err == nil {
		result.KG = &stats
	}

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.Encode(result)
		return cli.ExitOK
	}

	fmt.Fprintf(out, "Project: %s\n", result.Project)
	// Byte-identical to CheckConfigAt's detail lines, deliberately: the
	// remediation agents are handed says to grep for vault_path, and one grep
	// must work against either command.
	fmt.Fprintf(out, "vault_path = %s\n", result.VaultPath)
	fmt.Fprintf(out, "vault_path source = %s\n", result.VaultPathSource)
	if result.Palace != nil {
		fmt.Fprintf(out, "Palace:  %d wings, %d rooms, %d drawers\n",
			result.Palace.Wings, result.Palace.Rooms, result.Palace.Drawers)
	}
	fmt.Fprintf(out, "Tasks:   %d active\n", result.Tasks)
	fmt.Fprintf(out, "Sessions: %d total\n", result.Sessions)
	if result.KG != nil {
		fmt.Fprintf(out, "KG:      %d entities, %d triples (%d current)\n",
			result.KG.EntityCount, result.KG.TripleCount, result.KG.CurrentFacts)
	}
	return cli.ExitOK
}
