// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package notesummary produces vault-committed, search-oriented LLM
// summaries of individual captured session notes. It implements
// internal/summarize's Summarizer interface for SummaryJobKind
// KindSessionNote, mirroring internal/itersummary's shape for the
// iterations.md corpus.
package notesummary

import (
	"fmt"
	"strings"
)

// LengthGateBytes is the point at which a session note's raw body stops
// fitting in chunk.DefaultChunkConfig's <=2 raw chunks (measured empirically
// at 67.1% of the vault's ~2076 session notes at or under this length as of
// 2026-09-13 — re-derive rather than trust this number) and starts
// fragmenting across 3+ chunks. Below this, a note is already a compact,
// easily-retrievable unit and an LLM summarization pass buys little; above
// it, fragmentation across chunks is exactly where a single dense summary
// row helps.
//
// This is the ONE place this threshold is defined. internal/capture's
// enqueue-time gate (session.go) and SessionNoteSummarizer's own
// belt-and-suspenders check both import it rather than re-deriving it.
const LengthGateBytes = 1600

// PromptInput holds the session-note data needed to build the summary
// prompt. Unlike internal/itersummary's Header/Body split (which is
// iteration-entry-shaped), this carries what is actually available for a
// captured session note: the note's structured frontmatter fields plus its
// free-form narrative Body (internal/storage.Vault.ReadSession's second
// return value).
type PromptInput struct {
	Summary      string
	Decisions    []string
	FilesChanged []string
	OpenThreads  []string
	Body         string
}

// Result is the structured summary a Summarizer call produces. Named Summary
// inside this package's own Result type — distinct from, and not to be
// confused with, storage.SessionMeta.SearchSummary, the field it is
// ultimately written into.
type Result struct {
	Summary string
}

// resultJSON is the wire shape parseResponse decodes into, mirroring
// internal/enrichment and internal/itersummary's own *JSON/Result split. A
// session note's LLM output is a single dense summary string — unlike
// itersummary's decisions/unblocks split, there is no separate structured
// output here, because storage.SessionMeta.SearchSummary is a single string
// field and the note's own Decisions/OpenThreads/FilesChanged are already
// available structurally (folded into the INPUT prompt below, not
// regenerated as output).
type resultJSON struct {
	SearchSummary string `json:"search_summary"`
}

// buildUserPrompt renders the per-note user message from a PromptInput.
// Sections are omitted when empty and rendered in a fixed order so prompts
// (and any recorded fixtures) stay deterministic across runs.
func buildUserPrompt(in PromptInput) string {
	var b strings.Builder

	if in.Summary != "" {
		fmt.Fprintf(&b, "## Existing Summary\n%s\n\n", in.Summary)
	}
	if len(in.Decisions) > 0 {
		b.WriteString("## Decisions\n")
		for _, d := range in.Decisions {
			fmt.Fprintf(&b, "- %s\n", d)
		}
		b.WriteString("\n")
	}
	if len(in.FilesChanged) > 0 {
		b.WriteString("## Files Changed\n")
		for _, f := range in.FilesChanged {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}
	if len(in.OpenThreads) > 0 {
		b.WriteString("## Open Threads\n")
		for _, ot := range in.OpenThreads {
			fmt.Fprintf(&b, "- %s\n", ot)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Session Note Body\n")
	b.WriteString(in.Body)

	return b.String()
}

// defaultSystemPrompt is the built-in instruction set for session-note
// summarization. The JSON field name is a content-quality decision made
// during planning and must not be altered.
const defaultSystemPrompt = `You summarize a captured development-session note for LATER SEMANTIC SEARCH RETRIEVAL, not
for a human reading it directly (the original note remains available in full via a separate
lookup). Respond with valid JSON only. No markdown, no explanation. Schema:
{
  "search_summary": "1-3 sentences, dense and keyword-forward, of what this session did and any decisions made"
}

Rules:
- search_summary: Past tense, outcome-focused. Preserve what changed, why, and any explicit decisions made. 1-3 sentences max.
- Favor concrete nouns and technical keywords over generic phrasing — this text is retrieved by embedding similarity, not read for comfort.
- Do NOT include verbatim code snippets, file paths, or line numbers — those are recoverable separately via the full session note.`
