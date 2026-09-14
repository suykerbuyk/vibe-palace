// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package itersummary

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/llm"
)

// defaultGenerateTimeout bounds a Generate call when the Summarizer was
// constructed with a non-positive timeout. Mirrors
// internal/enrichment/client.go's defaultEnrichTimeout.
const defaultGenerateTimeout = 10 * time.Second

// generate is the internal worker behind Summarizer.Generate. Mirrors
// internal/enrichment/client.go's generate exactly: returns (nil, nil) when
// c is nil (summarization disabled/unavailable), and on a parse failure
// performs exactly one corrective reprompt before giving up.
func generate(ctx context.Context, c llm.Completer, systemPrompt string, in PromptInput) (*Result, error) {
	if c == nil {
		return nil, nil
	}

	userPrompt := buildUserPrompt(in)

	content, err := c.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("itersummary: %w", err)
	}

	result, parseErr := parseResponse(content)
	if parseErr == nil {
		return result, nil
	}

	// One corrective reprompt: Anthropic has no JSON mode, so a model may
	// wrap or prose-pad the payload. Remind it to emit bare JSON.
	retrySystem := systemPrompt + "\n\nIMPORTANT: Respond with valid JSON only, no markdown, no commentary."
	retryContent, err := c.Complete(ctx, retrySystem, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("itersummary reprompt: %w", err)
	}

	result, parseErr = parseResponse(retryContent)
	if parseErr != nil {
		return nil, fmt.Errorf("itersummary: %w", parseErr)
	}
	return result, nil
}

// parseResponse strips any surrounding markdown code fences and decodes the
// summary JSON.
func parseResponse(content string) (*Result, error) {
	cleaned := stripCodeFences(content)

	var rj resultJSON
	if err := json.Unmarshal([]byte(cleaned), &rj); err != nil {
		return nil, fmt.Errorf("unmarshal summary JSON: %w", err)
	}

	return &Result{
		Summary:   rj.Summary,
		Decisions: rj.Decisions,
		Unblocks:  rj.Unblocks,
	}, nil
}

// stripCodeFences removes a leading ```json / ``` fence and its matching
// trailing fence when the response is wrapped in a markdown code block.
// Plain (unfenced) content is returned trimmed but otherwise untouched.
// Mirrors internal/enrichment/client.go's unexported helper of the same
// name (there is no shared/exported version to reuse across packages).
func stripCodeFences(content string) string {
	s := strings.TrimSpace(content)
	if !strings.HasPrefix(s, "```") {
		return s
	}

	// Drop the opening fence line (```json, ```JSON, ``` , etc.).
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		s = strings.TrimPrefix(s, "```")
	}

	// Drop the trailing fence if present.
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = s[:idx]
	}

	return strings.TrimSpace(s)
}

// Summarizer is the LLM client for iteration-entry summarization — NOT to
// be confused with internal/summarize.Summarizer, the queue-processing
// interface this package's IterationSummarizer implements. Both are named
// "Summarizer" in their own packages; that is fine since they live in
// different packages, but the two must not be conflated.
//
// A nil *Summarizer is treated as "disabled" and Generate returns (nil, nil).
type Summarizer struct {
	completer    llm.Completer
	model        string
	timeout      time.Duration
	systemPrompt string
}

// NewSummarizer constructs a Summarizer. An empty systemPrompt falls back to
// the built-in defaultSystemPrompt.
func NewSummarizer(c llm.Completer, model string, timeout time.Duration, systemPrompt string) *Summarizer {
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt
	}
	return &Summarizer{
		completer:    c,
		model:        model,
		timeout:      timeout,
		systemPrompt: systemPrompt,
	}
}

// Generate runs summarization for a single iteration entry, bounded by the
// Summarizer's timeout. A nil receiver or nil completer returns (nil, nil)
// so callers may hold a nil Summarizer to mean "summarization disabled".
func (s *Summarizer) Generate(ctx context.Context, in PromptInput) (*Result, error) {
	if s == nil || s.completer == nil {
		return nil, nil
	}

	timeout := s.timeout
	if timeout <= 0 {
		timeout = defaultGenerateTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return generate(ctx, s.completer, s.systemPrompt, in)
}

// Model returns the configured model identifier for this Summarizer.
func (s *Summarizer) Model() string {
	return s.model
}
