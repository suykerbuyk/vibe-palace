// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package enrichment derives structured prompt inputs from Claude Code
// session transcripts for downstream LLM summarization.
package enrichment

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// maxUserChars caps the UserText excerpt fed to the LLM.
	maxUserChars = 12000
	// maxAssistantChars caps the AssistantText excerpt fed to the LLM.
	maxAssistantChars = 12000
)

// PromptInput holds the transcript data needed to build the LLM prompt.
type PromptInput struct {
	UserText      string
	AssistantText string
	FilesChanged  []string
	ToolCounts    map[string]int
	Duration      int // minutes
	UserMessages  int
	AsstMessages  int

	// Narrative context (optional, from heuristic extraction)
	NarrativeSummary string   // Heuristic summary for LLM to refine
	NarrativeTag     string   // Heuristic tag
	Activities       []string // Activity descriptions for context
}

// truncate trims text to at most maxChars characters. When a cut is
// required it prefers to break at the last newline past the half-way
// point so excerpts end on a message boundary, then appends a marker.
func truncate(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}

	// Try to break at a newline before the limit.
	truncated := text[:maxChars]
	if idx := strings.LastIndex(truncated, "\n"); idx > maxChars/2 {
		truncated = truncated[:idx]
	} else {
		// No newline to break on: the byte slice may have split a multi-byte
		// rune. Drop the partial trailing rune so the excerpt stays valid UTF-8
		// (otherwise json.Marshal silently rewrites it to U+FFFD).
		for len(truncated) > 0 {
			if r, size := utf8.DecodeLastRuneInString(truncated); r == utf8.RuneError && size <= 1 {
				truncated = truncated[:len(truncated)-1]
			} else {
				break
			}
		}
	}

	return truncated + "\n[...truncated]"
}

// defaultSystemPrompt is the built-in instruction set for session
// enrichment. Step 5 will allow a vault-editable template to override this
// via the systemPrompt argument threaded through generate/Enrich, falling
// back to this constant when no override is supplied. The allowed-tag line
// MUST stay in sync with validateTag's allow-map (the 7-tag set).
const defaultSystemPrompt = `You analyze Claude Code session transcripts and produce structured JSON summaries.

Respond with valid JSON only. No markdown, no explanation. Schema:
{
  "summary": "1-3 sentences. Past tense. Outcome-focused. What was accomplished.",
  "decisions": ["Decision — rationale", ...],
  "open_threads": ["Actionable next step", ...],
  "tag": "one of: implementation, debugging, refactor, exploration, review, docs, planning"
}

Rules:
- summary: Past tense, focus on outcomes and what changed. 1-3 sentences max.
- decisions: 0-5 key technical decisions made during the session. Format: "Decision — rationale". Omit if none.
- open_threads: 0-3 unfinished items or natural next steps. Actionable, specific. Omit if none.
- tag: Classify the session's primary activity. Exactly one tag.`

// buildUserPrompt renders the per-session user message from a PromptInput.
// Output ordering is deterministic so prompts (and recorded fixtures) are
// stable across runs.
func buildUserPrompt(input PromptInput) string {
	var b strings.Builder

	// Metadata section
	b.WriteString("## Session Metadata\n")
	fmt.Fprintf(&b, "- Duration: %d minutes\n", input.Duration)
	fmt.Fprintf(&b, "- User messages: %d\n", input.UserMessages)
	fmt.Fprintf(&b, "- Assistant messages: %d\n", input.AsstMessages)

	// Tool usage
	if len(input.ToolCounts) > 0 {
		b.WriteString("\n## Tool Usage\n")
		// Sort keys for deterministic output
		keys := make([]string, 0, len(input.ToolCounts))
		for k := range input.ToolCounts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "- %s: %d\n", k, input.ToolCounts[k])
		}
	}

	// Files changed
	if len(input.FilesChanged) > 0 {
		b.WriteString("\n## Files Changed\n")
		for _, f := range input.FilesChanged {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}

	// Heuristic analysis (from narrative extraction)
	if input.NarrativeSummary != "" || input.NarrativeTag != "" || len(input.Activities) > 0 {
		b.WriteString("\n## Heuristic Analysis\n")
		b.WriteString("The following was extracted heuristically. Refine rather than replace.\n")
		if input.NarrativeSummary != "" {
			fmt.Fprintf(&b, "- Summary: %s\n", input.NarrativeSummary)
		}
		if input.NarrativeTag != "" {
			fmt.Fprintf(&b, "- Tag: %s\n", input.NarrativeTag)
		}
		if len(input.Activities) > 0 {
			b.WriteString("- Activities:\n")
			limit := min(len(input.Activities), 20)
			for _, a := range input.Activities[:limit] {
				fmt.Fprintf(&b, "  - %s\n", a)
			}
			if len(input.Activities) > 20 {
				fmt.Fprintf(&b, "  - ... and %d more\n", len(input.Activities)-20)
			}
		}
	}

	// Transcript text (already truncated at extraction time; truncate
	// again defensively in case callers populate these fields directly).
	userText := truncate(input.UserText, maxUserChars)
	asstText := truncate(input.AssistantText, maxAssistantChars)

	b.WriteString("\n## User Messages\n")
	b.WriteString(userText)
	b.WriteString("\n\n## Assistant Messages\n")
	b.WriteString(asstText)

	return b.String()
}
