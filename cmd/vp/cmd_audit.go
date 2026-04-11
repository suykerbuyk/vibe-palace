// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func cmdAudit() *cli.Command {
	return &cli.Command{
		Name:        "audit",
		Synopsis:    "vp audit <command> [flags]",
		Description: "Audit palace classification quality.",
		Subcommands: []string{"audit rooms"},
		Run: func(args []string) int {
			fmt.Fprintln(os.Stderr, "Usage: vp audit <command> [flags]\n\nRun 'vp help audit' for details.")
			return cli.ExitOK
		},
	}
}

var auditRoomsFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--export", Arg: "FILE", Help: "Export report as JSON to FILE"},
	{Name: "--dry-run", Help: "Preview reclassification without changes"},
	{Name: "--apply", Help: "Apply reclassification to mismatched drawers"},
	{Name: "--verbose", Short: "-v", Help: "Show detailed scoring for every drawer"},
}

func cmdAuditRooms() *cli.Command {
	return &cli.Command{
		Name:        "audit rooms",
		Synopsis:    "vp audit rooms [--project P] [--export FILE] [--dry-run] [--apply] [--verbose]",
		Description: "Scan all drawers and report on classification quality. Detects mismatches, borderline classifications, and dead keywords.",
		Flags:       auditRoomsFlags,
		Examples: []cli.Example{
			{Cmd: "vp audit rooms", Comment: "Audit current project"},
			{Cmd: "vp audit rooms --dry-run", Comment: "Preview reclassification"},
			{Cmd: "vp audit rooms --apply", Comment: "Reclassify mismatched drawers"},
			{Cmd: "vp audit rooms --export report.json", Comment: "Export audit as JSON"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(auditRoomsFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp audit rooms: %v\n", err)
				return cli.ExitUser
			}

			proj := fv.Get("--project")
			if proj == "" {
				proj, _ = project.DetectProject(".")
			}
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp audit rooms: could not detect project (use --project)")
				return cli.ExitUser
			}

			vault, err := storage.OpenVault("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp audit rooms: %v\n", err)
				return cli.ExitSystem
			}

			cfg, err := vault.LoadConfig(proj)
			if err != nil {
				slog.Warn("audit: config load failed, using defaults", "err", err)
			}

			return runAuditRooms(vault, proj, cfg,
				fv.Bool("--apply"), fv.Bool("--dry-run"), fv.Bool("--verbose"),
				fv.Get("--export"), os.Stdout)
		},
	}
}

func runAuditRooms(vault *storage.Vault, proj string, cfg storage.Config, apply, dryRun, verbose bool, exportPath string, out io.Writer) int {
	if apply && dryRun {
		fmt.Fprintln(out, "Error: --apply and --dry-run are mutually exclusive.")
		return cli.ExitUser
	}

	classifier := palace.BuildClassifierFromConfig(cfg)

	opts := palace.AuditOptions{
		Project:  proj,
		Keywords: cfg.PalaceRoomKeywords,
	}
	report, err := palace.RunAudit(vault, classifier, opts)
	if err != nil {
		fmt.Fprintf(out, "Error: %v\n", err)
		return cli.ExitSystem
	}

	// Handle --export.
	if exportPath != "" {
		f, err := os.Create(exportPath)
		if err != nil {
			fmt.Fprintf(out, "Error: create export file: %v\n", err)
			return cli.ExitSystem
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(out, "Error: write export: %v\n", err)
			return cli.ExitSystem
		}
		fmt.Fprintf(out, "Exported audit report to %s\n", exportPath)
	}

	// Handle --dry-run.
	if dryRun {
		candidates := report.Candidates()
		if len(candidates) == 0 {
			fmt.Fprintln(out, "No reclassification candidates found.")
			return cli.ExitOK
		}
		fmt.Fprintf(out, "Reclassification candidates (%d):\n", len(candidates))
		for _, c := range candidates {
			fmt.Fprintf(out, "  %-8s %-12s %-12s → %-12s (%.2f → %.2f)\n",
				c.DrawerID, c.Wing, c.FromRoom, c.ToRoom, c.OldScore, c.NewScore)
		}
		return cli.ExitOK
	}

	// Handle --apply.
	if apply {
		candidates := report.Candidates()
		if len(candidates) == 0 {
			fmt.Fprintln(out, "No reclassification candidates found.")
			return cli.ExitOK
		}
		moved, errors := applyMoves(vault, proj, candidates, out)
		fmt.Fprintf(out, "\nMoved %d drawers", moved)
		if errors > 0 {
			fmt.Fprintf(out, " (%d errors)", errors)
		}
		fmt.Fprintln(out, ".")
		if errors > 0 {
			return cli.ExitSystem
		}
		return cli.ExitOK
	}

	// Default: human-readable report.
	formatReport(report, verbose, out)
	return cli.ExitOK
}

func applyMoves(vault *storage.Vault, proj string, candidates []palace.MoveCandidate, out io.Writer) (int, int) {
	moved, errors := 0, 0
	for _, c := range candidates {
		if err := vault.MoveDrawer(proj, c.Wing, c.FromRoom, c.ToRoom, c.DrawerID); err != nil {
			slog.Error("audit apply: move failed",
				"drawer", c.DrawerID, "from", c.FromRoom, "to", c.ToRoom, "err", err)
			fmt.Fprintf(out, "  ERROR %-8s %s → %s: %v\n", c.DrawerID, c.FromRoom, c.ToRoom, err)
			errors++
			continue
		}
		fmt.Fprintf(out, "  MOVED %-8s %s → %s\n", c.DrawerID, c.FromRoom, c.ToRoom)
		moved++
	}
	return moved, errors
}

func formatReport(report *palace.AuditReport, verbose bool, out io.Writer) {
	fmt.Fprintf(out, "Audit: %s (%d drawers)\n\n", report.Project, report.TotalDrawers)

	if report.TotalDrawers == 0 {
		fmt.Fprintln(out, "No drawers found.")
		return
	}

	// Room distribution.
	fmt.Fprintln(out, "Room Distribution:")
	for _, d := range report.Distributions {
		tag := ""
		if d.Room == "general" {
			tag = "  [fallback]"
		}
		fmt.Fprintf(out, "  %-14s %3d  (%5.1f%%)%s\n", d.Room, d.Count, d.Percent, tag)
	}
	fmt.Fprintf(out, "\nGeneral fallback rate: %.1f%% (%d/%d)\n",
		report.GeneralPercent, report.GeneralCount, report.TotalDrawers)

	// Mismatches.
	if len(report.Mismatches) > 0 {
		fmt.Fprintf(out, "\nReclassification Candidates (%d):\n", len(report.Mismatches))
		fmt.Fprintf(out, "  %-8s %-12s %-12s %-12s %s\n", "ID", "Wing", "Current", "Proposed", "Score")
		for _, m := range report.Mismatches {
			fmt.Fprintf(out, "  %-8s %-12s %-12s %-12s %.2f → %.2f\n",
				m.ID, m.Wing, m.CurrentRoom, m.BestRoom, m.CurrentScore, m.BestScore)
		}
	}

	// Borderlines.
	if len(report.Borderlines) > 0 {
		fmt.Fprintf(out, "\nBorderline Classifications (%d):\n", len(report.Borderlines))
		fmt.Fprintf(out, "  %-8s %-12s %-12s %s  (threshold: %.2f)\n",
			"ID", "Wing", "Room", "Score", report.MinScore)
		for _, b := range report.Borderlines {
			fmt.Fprintf(out, "  %-8s %-12s %-12s %.2f\n",
				b.ID, b.Wing, b.CurrentRoom, b.BestScore)
		}
	}

	// Keyword coverage.
	if verbose && len(report.Coverage) > 0 {
		fmt.Fprintln(out, "\nKeyword Coverage:")
		for _, c := range report.Coverage {
			total := len(c.Fired) + len(c.Dead)
			fmt.Fprintf(out, "  %-14s %d/%d fired", c.Room, len(c.Fired), total)
			if len(c.Dead) > 0 && len(c.Dead) <= 5 {
				fmt.Fprintf(out, "  (dead: %s)", joinMax(c.Dead, 5))
			} else if len(c.Dead) > 5 {
				fmt.Fprintf(out, "  (dead: %s, +%d more)", joinMax(c.Dead, 5), len(c.Dead)-5)
			}
			fmt.Fprintln(out)
		}
	}
}

func joinMax(items []string, max int) string {
	if len(items) <= max {
		return join(items)
	}
	return join(items[:max])
}

func join(items []string) string {
	result := ""
	for i, item := range items {
		if i > 0 {
			result += ", "
		}
		result += item
	}
	return result
}
