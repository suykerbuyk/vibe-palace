// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package itersummary produces vault-committed, search-oriented LLM
// summaries of individual iterations.md entries. It implements
// internal/summarize's Summarizer interface for SummaryJobKind
// KindIteration.
package itersummary

import "fmt"

// PromptInput holds the iteration-entry data needed to build the summary
// prompt. Unlike internal/enrichment's PromptInput (derived from a session
// TRANSCRIPT), this is derived from an already-written iterations.md entry
// — a human/agent-authored wrap narrative, not raw conversational text.
type PromptInput struct {
	Header string
	Body   string
}

// Result is the structured summary a Summarizer call produces.
type Result struct {
	Summary   string
	Decisions []string
	Unblocks  string
}

// resultJSON is the wire shape parseResponse decodes into, mirroring
// internal/enrichment's own enrichmentJSON/Result split.
type resultJSON struct {
	Summary   string   `json:"summary"`
	Decisions []string `json:"decisions"`
	Unblocks  string   `json:"unblocks"`
}

// buildUserPrompt renders the per-entry user message from a PromptInput.
// This input is much simpler than enrichment's — just the entry's header
// and body under clear labels — since none of enrichment's tool-counts/
// files-changed/heuristic-analysis sections apply to an already-written
// iterations.md narrative.
func buildUserPrompt(in PromptInput) string {
	return fmt.Sprintf("## Iteration Header\n%s\n\n## Iteration Narrative\n%s", in.Header, in.Body)
}

// defaultSystemPrompt is the built-in instruction set for iteration-entry
// summarization. The JSON field names and the 0-3 Decisions cap are a
// content-quality decision made during planning and must not be altered.
const defaultSystemPrompt = `You summarize a single development-iteration narrative for LATER SEMANTIC SEARCH RETRIEVAL, not
for a human reading it directly (the original entry remains available in full via a separate
lookup). Respond with valid JSON only. No markdown, no explanation. Schema:
{
  "summary": "1-3 sentences. Past tense. What changed and why.",
  "decisions": ["Decision — rationale", ...],
  "unblocks": "1 sentence: what this iteration enables next, or empty if nothing specific"
}

Rules:
- summary: Past tense, focus on outcomes and what changed. 1-3 sentences max.
- decisions: 0-3 key technical decisions made during this iteration. Format: "Decision — rationale". Omit if none.
- unblocks: 1 sentence on what this iteration enables/unblocks next. Empty string if nothing specific.
- Do NOT include verbatim code snippets, file paths, or line numbers — those are recoverable separately via the full iteration entry.`
