// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var trendsFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--json", Help: "Output JSON"},
}

// trendsResult is the combined --json payload: rolling friction windows,
// correction-density series, and model-regression report.
type trendsResult struct {
	Windows           []capture.FrictionWindow        `json:"windows"`
	CorrectionDensity capture.CorrectionDensitySeries `json:"correction_density"`
	ModelRegressions  capture.ModelRegressionReport   `json:"model_regressions"`
}

func cmdTrends() *cli.Command {
	return &cli.Command{
		Name:        "trends",
		Synopsis:    "vp trends [--project P] [--json]",
		Description: "Show friction trends for a project: rolling 7/30/90-day friction windows, the correction-density series over time, and friction deltas across model-change boundaries.",
		Flags:       trendsFlags,
		Examples: []cli.Example{
			{Cmd: "vp trends", Comment: "Friction windows, correction density, and model regressions"},
			{Cmd: "vp trends -p myapp --json", Comment: "Trend data as JSON"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(trendsFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp trends: %v\n", err)
				return cli.ExitUser
			}

			proj := fv.Get("--project")
			if proj == "" {
				proj, _ = project.DetectProject(".")
			}
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp trends: could not detect project (use --project)")
				return cli.ExitUser
			}

			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp trends: %v\n", err)
				return cli.ExitUser
			}

			return runTrends(vault, proj, fv.Bool("--json"), os.Stdout)
		},
	}
}

func runTrends(vault *storage.Vault, proj string, asJSON bool, out io.Writer) int {
	sessions, err := vault.ListSessions(proj, "", "", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp trends: %v\n", err)
		return cli.ExitSystem
	}

	windows := capture.GetFrictionWindows(sessions, time.Now(), []int{7, 30, 90})
	density := capture.GetCorrectionDensitySeries(sessions)
	regressions := capture.DetectModelRegressions(sessions)

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.Encode(trendsResult{
			Windows:           windows,
			CorrectionDensity: density,
			ModelRegressions:  regressions,
		})
		return cli.ExitOK
	}

	// Rolling windows.
	fmt.Fprintln(out, "Friction windows:")
	fmt.Fprintf(out, "%-6s %-9s %s\n", "WINDOW", "SESSIONS", "AVG_FRIC")
	for _, w := range windows {
		fmt.Fprintf(out, "%-6s %-9d %.1f\n", fmt.Sprintf("%dd", w.Days), w.SessionCount, w.AvgFriction)
	}

	// Correction-density series.
	fmt.Fprintln(out, "\nCorrection density:")
	if len(density.Points) == 0 {
		fmt.Fprintln(out, "  (no sessions with breakdown data)")
	} else {
		for _, p := range density.Points {
			fmt.Fprintf(out, "  %-12s iter %-3d corrections %d\n", p.Date, p.Iteration, p.Corrections)
		}
	}
	if density.MissingCount > 0 {
		fmt.Fprintf(out, "  (%d sessions predate breakdown capture — excluded)\n", density.MissingCount)
	}

	// Model regressions.
	fmt.Fprintln(out, "\nModel regressions:")
	if regressions.ModeledCount == 0 {
		fmt.Fprintln(out, "  no model data yet")
	} else {
		for _, r := range regressions.Regressions {
			fmt.Fprintf(out, "  %s → %s: %.1f → %.1f (delta %+.1f, %d→%d sessions)\n",
				r.FromModel, r.ToModel, r.FromAvgFriction, r.ToAvgFriction,
				r.Delta, r.FromSessions, r.ToSessions)
		}
		if len(regressions.Regressions) == 0 {
			fmt.Fprintln(out, "  no model-change boundaries")
		}
		fmt.Fprintf(out, "  model coverage: %d of %d sessions\n",
			regressions.ModeledCount, regressions.ModeledCount+regressions.UnmodeledCount)
	}
	return cli.ExitOK
}
