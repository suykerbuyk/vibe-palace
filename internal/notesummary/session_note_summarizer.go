// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package notesummary

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/llm"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/summarize"
)

// SessionNoteSummarizer implements summarize.Summarizer for SummaryJobKind
// KindSessionNote. It has no Kind guard of its own — DispatchSummarizer
// (internal/summarize) only ever calls it for KindSessionNote items, exactly
// like internal/itersummary.IterationSummarizer's own assumption for
// KindIteration.
type SessionNoteSummarizer struct {
	client *Summarizer // the LLM client above
	vault  *storage.Vault
}

// Summarize reads the session note identified by item's coordinates, asks
// the LLM client for a search-oriented summary, and writes the result back
// onto that note's frontmatter (SearchSummary/SearchSummaryAt/
// SearchSummaryModel), leaving the note's body untouched.
func (is *SessionNoteSummarizer) Summarize(ctx context.Context, item summarize.SummaryItem) (*summarize.SummaryResult, error) {
	meta, body, err := is.vault.ReadSession(item.Project, item.Date, item.Fingerprint, item.Iteration)
	if err != nil {
		// A real error here (note genuinely missing) is a real, actionable
		// failure: return it so the caller can retry — the queue file and
		// the note are independently mutable, so this consumes a retry
		// correctly.
		return nil, fmt.Errorf("notesummary: read session: %w", err)
	}

	// Belt-and-suspenders length check. This branch should be unreachable in
	// practice: the PRIMARY gate is at enqueue time
	// (internal/capture/session.go, guarded by notesummary.LengthGateBytes),
	// so a note short enough to fail this check should never have been
	// enqueued at all. This only protects against a job enqueued before that
	// gate existed, or before a length-gate config change takes effect. It is
	// a benign no-op success, not an error — it must not consume a retry.
	if len(body) <= LengthGateBytes {
		return &summarize.SummaryResult{Kind: summarize.KindSessionNote}, nil
	}

	if is.client == nil {
		// A disabled/nil client should never have reached this code path —
		// NewSessionNoteSummarizerFromConfig returns (nil, nil) for the WHOLE
		// Summarizer when summarization is disabled, so this indicates a
		// programming-contract violation, not a normal disabled-feature
		// no-op.
		return nil, fmt.Errorf("notesummary: Summarize called with a nil LLM client")
	}

	res, err := is.client.Generate(ctx, PromptInput{
		Summary:      meta.Summary,
		Decisions:    meta.Decisions,
		FilesChanged: meta.FilesChanged,
		OpenThreads:  meta.OpenThreads,
		Body:         body,
	})
	if err != nil {
		return nil, fmt.Errorf("notesummary: generate: %w", err)
	}
	if res == nil {
		// Same reasoning as the nil-client check above: a nil result with a
		// nil error means the client considered itself disabled, which
		// should never be reachable here.
		return nil, fmt.Errorf("notesummary: LLM client returned no result (disabled client reached Summarize)")
	}

	meta.SearchSummary = res.Summary
	meta.SearchSummaryAt = time.Now().UTC().Format(time.RFC3339)
	meta.SearchSummaryModel = is.client.Model()

	if err := is.vault.RewriteSession(item.Project, item.Date, item.Fingerprint, item.Iteration, meta, body); err != nil {
		return nil, fmt.Errorf("notesummary: rewrite session: %w", err)
	}

	return &summarize.SummaryResult{Kind: summarize.KindSessionNote}, nil
}

// NewSessionNoteSummarizerFromConfig resolves a SessionNoteSummarizer from
// the project's resolved [summarization] configuration, mirroring
// internal/itersummary.NewIterationSummarizerFromConfig's return contract
// exactly:
//
//   - Disabled config (!cfg.Enabled) returns (nil, nil): not an error, just
//     a signal that summarization is off.
//   - Enabled but unresolvable (missing api-key env, bad provider) returns
//     (nil, err).
//   - Enabled and resolvable returns (summarizer, nil).
func NewSessionNoteSummarizerFromConfig(cfg storage.SummarizationConfig, vault *storage.Vault) (*SessionNoteSummarizer, error) {
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
	return &SessionNoteSummarizer{client: client, vault: vault}, nil
}
