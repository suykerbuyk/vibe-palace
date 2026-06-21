// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package enrichment

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/llm"
)

// defaultEnrichTimeout bounds an Enrich call when the Enricher was
// constructed with a non-positive timeout.
const defaultEnrichTimeout = 10 * time.Second

// allowedTags is the canonical 7-tag set (kept in sync with the
// allowed-tag line in defaultSystemPrompt). Any other tag is normalized to
// the empty string by validateTag.
var allowedTags = map[string]bool{
	"implementation": true,
	"debugging":      true,
	"refactor":       true,
	"exploration":    true,
	"review":         true,
	"docs":           true,
	"planning":       true,
}

// Generate calls the LLM to enrich a session note using the built-in
// default system prompt. Returns (nil, nil) when c is nil (enrichment
// disabled/unavailable).
func Generate(ctx context.Context, c llm.Completer, in PromptInput) (*Result, error) {
	return generate(ctx, c, defaultSystemPrompt, in)
}

// generate is the internal worker shared by Generate and Enricher.Enrich.
// It threads an arbitrary system prompt so Step 5 can supply a
// vault-editable template without reworking this path. On a parse failure
// it performs exactly one corrective reprompt before giving up.
func generate(ctx context.Context, c llm.Completer, systemPrompt string, in PromptInput) (*Result, error) {
	if c == nil {
		return nil, nil
	}

	userPrompt := buildUserPrompt(in)

	content, err := c.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("enrichment: %w", err)
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
		return nil, fmt.Errorf("enrichment reprompt: %w", err)
	}

	result, parseErr = parseResponse(retryContent)
	if parseErr != nil {
		return nil, fmt.Errorf("enrichment: %w", parseErr)
	}
	return result, nil
}

// parseResponse strips any surrounding markdown code fences and decodes the
// enrichment JSON. The tag is normalized through validateTag.
func parseResponse(content string) (*Result, error) {
	cleaned := stripCodeFences(content)

	var ej enrichmentJSON
	if err := json.Unmarshal([]byte(cleaned), &ej); err != nil {
		return nil, fmt.Errorf("unmarshal enrichment JSON: %w", err)
	}

	return &Result{
		Summary:     ej.Summary,
		Decisions:   ej.Decisions,
		OpenThreads: ej.OpenThreads,
		Tag:         validateTag(ej.Tag),
	}, nil
}

// stripCodeFences removes a leading ```json / ``` fence and its matching
// trailing fence when the response is wrapped in a markdown code block.
// Plain (unfenced) content is returned trimmed but otherwise untouched.
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

// validateTag lowercases and trims a tag and returns it only if it is one
// of the canonical 7 tags; otherwise it returns the empty string.
func validateTag(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if allowedTags[tag] {
		return tag
	}
	return ""
}

// Enricher binds a Completer with the model, per-call timeout, and system
// prompt used for session enrichment. A nil *Enricher is treated as
// "disabled" and Enrich returns (nil, nil).
type Enricher struct {
	completer    llm.Completer
	model        string
	timeout      time.Duration
	systemPrompt string
}

// NewEnricher constructs an Enricher. An empty systemPrompt falls back to
// the built-in defaultSystemPrompt.
func NewEnricher(c llm.Completer, model string, timeout time.Duration, systemPrompt string) *Enricher {
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt
	}
	return &Enricher{
		completer:    c,
		model:        model,
		timeout:      timeout,
		systemPrompt: systemPrompt,
	}
}

// Enrich runs enrichment for a single session, bounded by the Enricher's
// timeout. A nil receiver or nil completer returns (nil, nil) so callers
// may hold a nil enricher to mean "enrichment disabled".
func (e *Enricher) Enrich(ctx context.Context, in PromptInput) (*Result, error) {
	if e == nil || e.completer == nil {
		return nil, nil
	}

	timeout := e.timeout
	if timeout <= 0 {
		timeout = defaultEnrichTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return generate(ctx, e.completer, e.systemPrompt, in)
}

// Model returns the configured model identifier for this Enricher.
func (e *Enricher) Model() string {
	return e.model
}
