// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package itersummary

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/llm"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/summarize"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// IterationSummarizer implements summarize.Summarizer for SummaryJobKind
// KindIteration. It has no Kind guard of its own — DispatchSummarizer
// (internal/summarize) only ever calls it for KindIteration items.
type IterationSummarizer struct {
	client *Summarizer // the LLM client above
	vault  *storage.Vault
}

// Summarize reads item.Project's iterations.md, resolves the entry for
// item.Iteration (the LAST file-order match for that N, per
// wrapstate.LastEntryByN's established convention), asks the LLM client to
// summarize it, and writes the result as that iteration's vault-committed
// search summary.
func (is *IterationSummarizer) Summarize(ctx context.Context, item summarize.SummaryItem) (*summarize.SummaryResult, error) {
	path, err := is.vault.IterationsFile(item.Project)
	if err != nil {
		return nil, fmt.Errorf("itersummary: iterations file: %w", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("itersummary: read iterations.md: %w", err)
	}

	entry, ok := wrapstate.LastEntryByN(string(content), item.Iteration)
	if !ok {
		return nil, fmt.Errorf("itersummary: iteration %d not found in project %q", item.Iteration, item.Project)
	}
	matchIndex := len(wrapstate.EntriesByN(string(content), item.Iteration)) - 1

	if is.client == nil {
		// A disabled/nil client should never have reached this code path —
		// NewIterationSummarizerFromConfig returns (nil, nil) for the WHOLE
		// Summarizer when summarization is disabled, so this indicates a
		// programming-contract violation, not a normal disabled-feature
		// no-op.
		return nil, fmt.Errorf("itersummary: Summarize called with a nil LLM client")
	}

	res, err := is.client.Generate(ctx, PromptInput{Header: entry.Header, Body: entry.Body})
	if err != nil {
		return nil, fmt.Errorf("itersummary: generate: %w", err)
	}
	if res == nil {
		// Same reasoning as the nil-client check above: a nil result with a
		// nil error means the client considered itself disabled, which
		// should never be reachable here.
		return nil, fmt.Errorf("itersummary: LLM client returned no result (disabled client reached Summarize)")
	}

	summary := storage.IterationSummary{
		N:           item.Iteration,
		MatchIndex:  matchIndex,
		Summary:     res.Summary,
		Decisions:   res.Decisions,
		Unblocks:    res.Unblocks,
		Model:       is.client.Model(),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := is.vault.WriteIterationSummary(item.Project, summary); err != nil {
		return nil, fmt.Errorf("itersummary: write iteration summary: %w", err)
	}

	return &summarize.SummaryResult{Kind: summarize.KindIteration}, nil
}

// NewIterationSummarizerFromConfig resolves an IterationSummarizer from the
// project's resolved [summarization] configuration, mirroring
// internal/capture/enricher_config.go's NewEnricherFromConfig return
// contract exactly:
//
//   - Disabled config (!cfg.Enabled) returns (nil, nil): not an error, just
//     a signal that summarization is off.
//   - Enabled but unresolvable (missing api-key env, bad provider) returns
//     (nil, err).
//   - Enabled and resolvable returns (summarizer, nil).
func NewIterationSummarizerFromConfig(cfg storage.SummarizationConfig, vault *storage.Vault) (*IterationSummarizer, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	key := ""
	if cfg.APIKeyEnv != "" {
		key = os.Getenv(cfg.APIKeyEnv)
	}
	if cfg.APIKeyEnv == "" || key == "" {
		return nil, fmt.Errorf("summarization enabled but api key env %q is unset", cfg.APIKeyEnv)
	}

	llmCfg := llm.Config{
		Endpoint:  cfg.BaseURL,
		Model:     cfg.Model,
		APIKey:    key,
		MaxTokens: cfg.MaxTokens,
	}
	completer, err := llm.NewCompleter(cfg.Provider, llmCfg)
	if err != nil {
		return nil, fmt.Errorf("summarization: build completer: %w", err)
	}

	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	client := NewSummarizer(completer, cfg.Model, timeout, "")
	return &IterationSummarizer{client: client, vault: vault}, nil
}
