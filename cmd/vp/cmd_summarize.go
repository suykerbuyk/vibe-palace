// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/itersummary"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/summarize"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

func cmdSummarize() *cli.Command {
	return &cli.Command{
		Name:        "summarize",
		Synopsis:    "vp summarize <command> [flags]",
		Description: "Operator-triggered LLM summarization of vault content for search.",
	}
}

var summarizeIterationsFlags = []cli.FlagDef{
	{Name: "--project-path", Arg: "PATH", Help: "Absolute path to the project repo root (required)"},
	{Name: "--force", Help: "Regenerate every entry's summary, even ones already cached"},
}

func cmdSummarizeIterations() *cli.Command {
	return &cli.Command{
		Name:     "summarize iterations",
		Synopsis: "vp summarize iterations --project-path PATH [--force]",
		Description: "Summarize every iterations.md entry for one project, once, and cache the " +
			"result vault-committed. Without --force, an entry already cached (and not superseded " +
			"by a newer same-N entry appended since) is skipped — this is the additive-by-default " +
			"regeneration policy. With --force, every entry is regenerated regardless of cache " +
			"state, overwriting any existing cache file. This is a synchronous, one-shot CLI " +
			"command for OPERATOR-TRIGGERED backfill/regeneration — the normal, automatic path " +
			"(one entry summarized per wrap) goes through the host-local queue " +
			"(vp_enqueue_iteration_summary + vp_trigger_summarization_drain / `vp drain summaries`), " +
			"not this command. Unlike `vp drain summaries` (where a disabled/unresolvable " +
			"[summarization] config is a silent, documented no-op), this command's whole purpose is " +
			"to summarize — a disabled or unresolvable summarizer is reported as a real error here.",
		Flags: summarizeIterationsFlags,
		Examples: []cli.Example{
			{Cmd: "vp summarize iterations --project-path /path/to/project", Comment: "Summarize every not-yet-cached (or superseded) entry"},
			{Cmd: "vp summarize iterations --project-path /path/to/project --force", Comment: "Regenerate every entry's summary unconditionally"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(summarizeIterationsFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp summarize iterations: %v\n", err)
				return cli.ExitUser
			}

			projectPath := fv.Get("--project-path")
			if projectPath == "" {
				fmt.Fprintln(os.Stderr, "vp summarize iterations: --project-path is required")
				return cli.ExitUser
			}
			if !filepath.IsAbs(projectPath) {
				// Fail fast here, exactly like `vp drain summaries` — see
				// cmd_drain.go's identical check for why this must be
				// validated at the argument-parsing boundary rather than
				// surfacing later as an opaque error deeper in the stack.
				fmt.Fprintf(os.Stderr, "vp summarize iterations: --project-path must be absolute, got %q\n", projectPath)
				return cli.ExitUser
			}

			force := fv.Bool("--force")

			return runSummarizeIterations(projectPath, force, os.Stdout)
		},
	}
}

// runSummarizeIterations is the testable body of `vp summarize iterations`.
// out receives progress lines and the final one-line summary (and any error
// text), mirroring cmd_drain.go's runDrainSummaries in shape: resolve the
// vault and slug EXPLICITLY from the given projectPath (never os.Getwd()),
// load the project's own [summarization] config, and build a real,
// config-driven itersummary.IterationSummarizer.
//
// Unlike runDrainSummaries, an unresolvable or disabled summarizer here is a
// real, reported failure (cli.ExitUser) rather than a silent no-op: this
// command's whole purpose is to summarize, so silently doing nothing would
// leave an operator with no idea why `vp summarize iterations` produced zero
// output.
//
// Every entry that is the current last file-order match for its iteration
// number N (per wrapstate's own "more than one entry can share N" model —
// the same convention internal/search/iterations.go's collectIterationCorpus
// and internal/itersummary's own IterationSummarizer.Summarize already use)
// is a candidate. Without force, a candidate whose cached
// storage.IterationSummary.MatchIndex still equals its own current
// matchIndex is skipped WITHOUT ever calling the summarizer — this is the
// additive-by-default policy: only never-summarized or superseded (a newer
// same-N entry appended since the cache was written) entries do real work.
// With force, every candidate is summarized regardless of cache state,
// unconditionally overwriting any existing cache file (WriteIterationSummary
// itself always overwrites — see its own doc comment).
//
// This command does NOT go through internal/summarize's
// queue/jobqueue machinery at all: it calls the Summarizer synchronously,
// in-process, one entry at a time, directly against a
// summarize.SummaryItem{Kind: KindIteration} built for each candidate.
func runSummarizeIterations(projectPath string, force bool, out io.Writer) int {
	vault, err := OpenProjectVaultAt(projectPath)
	if err != nil {
		fmt.Fprintf(out, "vp summarize iterations: open vault: %v\n", err)
		return cli.ExitSystem
	}
	slug, err := project.DetectProject(projectPath)
	if err != nil {
		fmt.Fprintf(out, "vp summarize iterations: detect project: %v\n", err)
		return cli.ExitSystem
	}

	cfg, err := vault.LoadConfig(slug)
	if err != nil {
		fmt.Fprintf(out, "vp summarize iterations: load config: %v\n", err)
		return cli.ExitSystem
	}

	summarizer, err := itersummary.NewIterationSummarizerFromConfig(cfg.Summarization, vault)
	if err != nil {
		// Enabled but unresolvable (e.g. missing API key env). Unlike
		// runDrainSummaries's warn-and-proceed-as-no-op contract, this
		// command's whole reason for existing is to summarize, so this is a
		// real, reported failure, not a silent no-op.
		fmt.Fprintf(out, "vp summarize iterations: summarizer unavailable: %v\n", err)
		return cli.ExitUser
	}
	if summarizer == nil {
		fmt.Fprintf(out, "vp summarize iterations: project=%s: [summarization] is not enabled in the host config (%s) — it is a host-level setting with no per-project tier; nothing to do\n", slug, hostConfigForMessage())
		return cli.ExitUser
	}

	path, err := vault.IterationsFile(slug)
	if err != nil {
		fmt.Fprintf(out, "vp summarize iterations: iterations file: %v\n", err)
		return cli.ExitSystem
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No iterations.md at all yet — nothing to summarize, and that
			// is a legitimate (if unusual) state, not an error.
			fmt.Fprintf(out, "vp summarize iterations: project=%s summarized=0 skipped=0\n", slug)
			return cli.ExitOK
		}
		fmt.Fprintf(out, "vp summarize iterations: read iterations.md: %v\n", err)
		return cli.ExitSystem
	}

	entries := wrapstate.ParseEntries(string(data))

	// totalCount[n] / seenN mirror internal/search/iterations.go's
	// collectIterationCorpus exactly: matchIndex is 0-based among entries
	// sharing the same N in file order, and isLastMatch marks the single
	// entry per N eligible for a summary. Recomputed here directly rather
	// than via a shared helper — see this task's own review note that
	// extracting one was left to the implementer's judgment; the
	// computation is cheap and small enough that duplicating it does not
	// risk drift in practice, and keeping it here avoids touching
	// internal/search or internal/wrapstate for this change.
	totalCount := make(map[int]int, len(entries))
	for _, e := range entries {
		totalCount[e.N]++
	}

	ctx := context.Background()
	summarized, skipped := 0, 0
	seenN := make(map[int]int, len(entries))
	for _, e := range entries {
		matchIndex := seenN[e.N]
		seenN[e.N] = matchIndex + 1
		isLastMatch := matchIndex == totalCount[e.N]-1
		if !isLastMatch {
			// A superseded-in-file-order entry sharing N with a later one:
			// never eligible for its own summary, exactly as
			// collectIterationCorpus and IterationSummarizer.Summarize both
			// already treat it (they resolve to the LAST match for N).
			continue
		}

		if !force {
			cached, ok, rerr := vault.ReadIterationSummary(slug, e.N)
			if rerr != nil {
				fmt.Fprintf(out, "vp summarize iterations: read cached summary n=%d: %v\n", e.N, rerr)
				return cli.ExitSystem
			}
			if ok && cached.MatchIndex == matchIndex {
				// Fresh, non-stale cache already covers this exact
				// matchIndex — additive policy: skip WITHOUT calling the
				// summarizer at all.
				skipped++
				continue
			}
		}

		item := summarize.SummaryItem{
			Kind:      summarize.KindIteration,
			Project:   slug,
			Iteration: e.N,
		}
		if _, serr := summarizer.Summarize(ctx, item); serr != nil {
			fmt.Fprintf(out, "vp summarize iterations: summarize n=%d: %v\n", e.N, serr)
			return cli.ExitSystem
		}
		summarized++
	}

	fmt.Fprintf(out, "vp summarize iterations: project=%s summarized=%d skipped=%d\n", slug, summarized, skipped)
	return cli.ExitOK
}

// hostConfigForMessage names the host config file [summarization] is read from,
// for an operator-facing message: it is the only tier that can set it. A path
// that cannot be resolved is named generically rather than guessed.
func hostConfigForMessage() string {
	if p, err := storage.VaultConfigFilePath(); err == nil {
		return p
	}
	return "<config dir>/vibe-palace/config.toml"
}
