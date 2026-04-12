// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/llm"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func cmdTune() *cli.Command {
	return &cli.Command{
		Name:        "tune",
		Synopsis:    "vp tune <command> [flags]",
		Description: "LLM-assisted classification tuning.",
		Subcommands: []string{"tune rooms"},
		Run: func(args []string) int {
			fmt.Fprintln(os.Stderr, "Usage: vp tune <command> [flags]\n\nRun 'vp help tune' for details.")
			return cli.ExitOK
		},
	}
}

var tuneRoomsFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--max-samples", Short: "-n", Arg: "N", Help: "Maximum samples to evaluate (default: 50)"},
	{Name: "--export", Arg: "FILE", Help: "Export tune report as JSON to FILE"},
	{Name: "--apply", Help: "Write proposed scoring changes to project config"},
	{Name: "--estimate", Help: "Estimate token cost without calling LLM"},
	{Name: "--verbose", Short: "-v", Help: "Show detailed judgment results"},
}

func cmdTuneRooms() *cli.Command {
	return &cli.Command{
		Name:        "tune rooms",
		Synopsis:    "vp tune rooms [--project P] [--max-samples N] [--export FILE] [--apply] [--estimate] [--verbose]",
		Description: "Use an LLM offline to evaluate classification quality and propose weight adjustments.",
		Flags:       tuneRoomsFlags,
		Examples: []cli.Example{
			{Cmd: "vp tune rooms --estimate", Comment: "Estimate cost without calling LLM"},
			{Cmd: "vp tune rooms", Comment: "Run tuning and show proposed changes"},
			{Cmd: "vp tune rooms --apply", Comment: "Apply proposed changes to config"},
			{Cmd: "vp tune rooms --export report.json", Comment: "Export full report as JSON"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(tuneRoomsFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp tune rooms: %v\n", err)
				return cli.ExitUser
			}

			proj := fv.Get("--project")
			if proj == "" {
				proj, _ = project.DetectProject(".")
			}
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp tune rooms: could not detect project (use --project)")
				return cli.ExitUser
			}

			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp tune rooms: %v\n", err)
				return cli.ExitSystem
			}

			cfg, err := vault.LoadConfig(proj)
			if err != nil {
				slog.Warn("tune: config load failed, using defaults", "err", err)
			}

			return runTuneRooms(vault, proj, cfg,
				fv.Int("--max-samples"),
				fv.Bool("--apply"), fv.Bool("--estimate"), fv.Bool("--verbose"),
				fv.Get("--export"), os.Stdout)
		},
	}
}

func runTuneRooms(vault *storage.Vault, proj string, cfg storage.Config,
	maxSamples int, apply, estimate, verbose bool, exportPath string, out io.Writer) int {

	classifier := palace.BuildClassifierFromConfig(cfg)

	opts := palace.TuneOptions{
		Project:    proj,
		MaxSamples: maxSamples,
		Keywords:   cfg.PalaceRoomKeywords,
	}

	// Select samples.
	samples, err := palace.SelectSamples(vault, classifier, opts)
	if err != nil {
		fmt.Fprintf(out, "Error: %v\n", err)
		return cli.ExitSystem
	}
	if len(samples) == 0 {
		fmt.Fprintln(out, "No samples found.")
		return cli.ExitOK
	}

	// Handle --estimate.
	if estimate {
		est := palace.EstimateTokenCost(samples, 0)
		fmt.Fprintf(out, "Token Estimate (%d samples, %d batches):\n", len(samples), est.BatchCount)
		fmt.Fprintf(out, "  Prompt tokens:     ~%d\n", est.PromptTokens)
		fmt.Fprintf(out, "  Completion tokens: ~%d\n", est.CompletionTokens)
		fmt.Fprintf(out, "  Total tokens:      ~%d\n", est.TotalTokens)
		return cli.ExitOK
	}

	// Validate LLM config.
	if cfg.PalaceLLM.Endpoint == "" || cfg.PalaceLLM.Model == "" || cfg.PalaceLLM.APIKeyEnv == "" {
		fmt.Fprintln(out, "Error: LLM not configured. Add [palace.llm] to your config with endpoint, model, and api_key_env.")
		return cli.ExitUser
	}
	apiKey := os.Getenv(cfg.PalaceLLM.APIKeyEnv)
	if apiKey == "" {
		fmt.Fprintf(out, "Error: API key environment variable %q is not set.\n", cfg.PalaceLLM.APIKeyEnv)
		return cli.ExitUser
	}

	client, err := llm.NewClient(llm.Config{
		Endpoint:  cfg.PalaceLLM.Endpoint,
		Model:     cfg.PalaceLLM.Model,
		APIKey:    apiKey,
		MaxTokens: cfg.PalaceLLM.MaxTokens,
	})
	if err != nil {
		fmt.Fprintf(out, "Error: %v\n", err)
		return cli.ExitSystem
	}

	// Run tuning.
	progress := func(msg string) {
		fmt.Fprintf(out, "%s\n", msg)
	}
	report, err := palace.RunTune(context.Background(), vault, classifier, client, opts, progress)
	if err != nil {
		fmt.Fprintf(out, "Error: %v\n", err)
		return cli.ExitSystem
	}

	// Summary.
	fmt.Fprintf(out, "\nResults: %d samples, %d judgments, %d agreements, %d disagreements\n",
		report.SamplesTotal, report.JudgmentsTotal, report.Agreements, report.Disagreements)

	// Handle --export (always, regardless of proposals).
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
		fmt.Fprintf(out, "Exported tune report to %s\n", exportPath)
	}

	if len(report.Proposals) == 0 && len(report.UnmatchedFlags) == 0 {
		fmt.Fprintln(out, "No weight changes proposed.")
		return cli.ExitOK
	}

	// Print TOML diff.
	diff := palace.FormatTOMLDiff(report.Proposals)
	if diff != "" {
		fmt.Fprintf(out, "\n%s", diff)
	}

	// Unmatched flags summary.
	if len(report.UnmatchedFlags) > 0 {
		fmt.Fprintf(out, "\nUnmatched content (%d drawers, candidates for keyword discovery):\n", len(report.UnmatchedFlags))
		for _, f := range report.UnmatchedFlags {
			fmt.Fprintf(out, "  %-8s → %-12s  %s\n", f.DrawerID, f.LLMRoom, f.ContentSnip)
		}
	}

	// Verbose: per-judgment details.
	if verbose && report.JudgmentsTotal > 0 {
		fmt.Fprintln(out, "\nJudgment Details:")
		// RunTune doesn't export raw judgments in the report, so verbose
		// is limited to the report summary. Future: add Judgments field.
	}

	// Handle --apply.
	if apply {
		if len(report.Proposals) == 0 {
			fmt.Fprintln(out, "No proposals to apply.")
			return cli.ExitOK
		}

		rooms := proposalsToOverrides(report.Proposals)
		if err := vault.WriteScoringConfig(proj, rooms, 0); err != nil {
			fmt.Fprintf(out, "Error writing config: %v\n", err)
			return cli.ExitSystem
		}

		cfgPath, _ := vault.ProjectConfigFile(proj)
		fmt.Fprintf(out, "Applied %d weight changes to %s\n", len(report.Proposals), cfgPath)
		fmt.Fprintln(out, "Note: TOML comments in the config file are not preserved.")
	}

	return cli.ExitOK
}

// proposalsToOverrides converts WeightProposals to the storage config format.
func proposalsToOverrides(proposals []palace.WeightProposal) map[string]storage.ScoringRoomOverride {
	rooms := make(map[string]storage.ScoringRoomOverride)
	for _, p := range proposals {
		if p.OldWeight == p.NewWeight {
			continue // review-only
		}
		ov := rooms[p.Room]
		switch {
		case p.NewWeight >= 0.9:
			ov.High = append(ov.High, p.Keyword)
		case p.NewWeight >= 0.5:
			ov.Medium = append(ov.Medium, p.Keyword)
		default:
			ov.Low = append(ov.Low, p.Keyword)
		}
		rooms[p.Room] = ov
	}
	return rooms
}
