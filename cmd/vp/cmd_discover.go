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

func cmdDiscover() *cli.Command {
	return &cli.Command{
		Name:        "discover",
		Synopsis:    "vp discover <command> [flags]",
		Description: "LLM-assisted keyword discovery for room classification.",
	}
}

var discoverRoomsFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect)"},
	{Name: "--max-samples", Short: "-n", Arg: "N", Help: "Maximum samples to evaluate (default: 50)"},
	{Name: "--export", Arg: "FILE", Help: "Export discovery report as JSON to FILE"},
	{Name: allowInsideVaultFlag, Help: allowInsideVaultHelp},
	{Name: "--apply", Help: "Write proposed keywords to project config"},
	{Name: "--estimate", Help: "Estimate token cost without calling LLM"},
}

func cmdDiscoverRooms() *cli.Command {
	return &cli.Command{
		Name:        "discover rooms",
		Synopsis:    "vp discover rooms [--project P] [--max-samples N] [--export FILE] [--apply] [--estimate]",
		Description: "Use an LLM to discover new keywords from unclassified content and cross-validate them against all drawers.",
		Flags:       discoverRoomsFlags,
		Examples: []cli.Example{
			{Cmd: "vp discover rooms --estimate", Comment: "Estimate cost without calling LLM"},
			{Cmd: "vp discover rooms", Comment: "Run discovery and show proposed keywords"},
			{Cmd: "vp discover rooms --apply", Comment: "Apply proposed keywords to config"},
			{Cmd: "vp discover rooms --export report.json", Comment: "Export full report as JSON"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(discoverRoomsFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp discover rooms: %v\n", err)
				return cli.ExitUser
			}

			proj := fv.Get("--project")
			if proj == "" {
				proj, _ = project.DetectProject(".")
			}
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp discover rooms: could not detect project (use --project)")
				return cli.ExitUser
			}

			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp discover rooms: %v\n", err)
				return cli.ExitSystem
			}

			cfg, err := vault.LoadConfig(proj)
			if err != nil {
				slog.Warn("discover: config load failed, using defaults", "err", err)
			}

			return runDiscoverRooms(vault, proj, cfg,
				fv.Int("--max-samples"),
				fv.Bool("--apply"), fv.Bool("--estimate"),
				fv.Get("--export"), fv.Bool(allowInsideVaultFlag), os.Stdout)
		},
	}
}

func runDiscoverRooms(vault *storage.Vault, proj string, cfg storage.Config,
	maxSamples int, apply, estimate bool, exportPath string, allowInsideVault bool, out io.Writer) int {
	// Guard the export destination FIRST. It is operator input, checking it is
	// cheap, and this runner does LLM work below — a late guard would burn
	// tokens and then refuse.
	if exportPath != "" {
		if code := guardExportDestination(vault.Root, exportPath, allowInsideVault, out); code != cli.ExitOK {
			return code
		}
	}

	classifier := palace.BuildClassifierFromConfig(cfg)

	opts := palace.DiscoverOptions{
		Project:    proj,
		MaxSamples: maxSamples,
		Keywords:   cfg.PalaceRoomKeywords,
	}

	// Collect candidates to check if there's anything to discover.
	candidates, _, err := palace.CollectDiscoveryCandidates(vault, classifier, opts)
	if err != nil {
		fmt.Fprintf(out, "Error: %v\n", err)
		return cli.ExitSystem
	}
	if len(candidates) == 0 {
		fmt.Fprintln(out, "No unclassified content found.")
		return cli.ExitOK
	}

	// Handle --estimate.
	if estimate {
		est := palace.EstimateDiscoveryCost(candidates, 0)
		fmt.Fprintf(out, "Token Estimate (%d candidates, %d batches):\n", len(candidates), est.BatchCount)
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

	// Run discovery.
	progress := func(msg string) {
		fmt.Fprintf(out, "%s\n", msg)
	}
	report, err := palace.RunDiscover(context.Background(), vault, classifier, client, opts, progress)
	if err != nil {
		fmt.Fprintf(out, "Error: %v\n", err)
		return cli.ExitSystem
	}

	// Summary.
	fmt.Fprintf(out, "\nResults: %d candidates, %d LLM calls, %d proposals, %d rejected\n",
		report.CandidatesTotal, report.LLMCalls, len(report.Proposals), len(report.Rejected))

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
		fmt.Fprintf(out, "Exported discovery report to %s\n", exportPath)
	}

	if len(report.Proposals) == 0 {
		fmt.Fprintln(out, "No viable keyword proposals found.")
		if len(report.Rejected) > 0 {
			fmt.Fprintf(out, "(%d proposals rejected due to regression risk)\n", len(report.Rejected))
		}
		return cli.ExitOK
	}

	// Print TOML diff.
	diff := palace.FormatDiscoveryTOML(report.Proposals)
	if diff != "" {
		fmt.Fprintf(out, "\n%s", diff)
	}

	// Handle --apply.
	if apply {
		rooms := discoveryProposalsToOverrides(report.Proposals)
		if err := vault.WriteScoringConfig(proj, rooms, 0); err != nil {
			fmt.Fprintf(out, "Error writing config: %v\n", err)
			return cli.ExitSystem
		}

		cfgPath, _ := vault.ProjectConfigFile(proj)
		fmt.Fprintf(out, "Applied %d new keywords to %s\n", len(report.Proposals), cfgPath)
	}

	return cli.ExitOK
}

// discoveryProposalsToOverrides converts KeywordProposals to the storage config format.
func discoveryProposalsToOverrides(proposals []palace.KeywordProposal) map[string]storage.ScoringRoomOverride {
	rooms := make(map[string]storage.ScoringRoomOverride)
	for _, p := range proposals {
		ov := rooms[p.Room]
		switch p.Weight {
		case "high":
			ov.High = append(ov.High, p.Keyword)
		case "low":
			ov.Low = append(ov.Low, p.Keyword)
		default:
			ov.Medium = append(ov.Medium, p.Keyword)
		}
		rooms[p.Room] = ov
	}
	return rooms
}
