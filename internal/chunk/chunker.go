// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package chunk

import (
	"strings"
	"unicode"
)

// ChunkConfig controls the sliding-window chunker.
type ChunkConfig struct {
	MaxChars int // target maximum characters per chunk (default 800)
	Overlap  int // overlap characters between consecutive chunks (default 100)
}

// DefaultChunkConfig returns the PRD-specified defaults: 800 chars, 100 overlap.
func DefaultChunkConfig() ChunkConfig {
	return ChunkConfig{MaxChars: 800, Overlap: 100}
}

// Chunk splits text into semantic units suitable for embedding.
// It respects sentence boundaries and exchange-pair boundaries when possible.
func Chunk(text string, cfg ChunkConfig) []string {
	if cfg.MaxChars <= 0 {
		cfg.MaxChars = 800
	}
	if cfg.Overlap < 0 {
		cfg.Overlap = 0
	}
	if cfg.Overlap >= cfg.MaxChars {
		cfg.Overlap = cfg.MaxChars / 4
	}

	text = normalizeLineEndings(text)
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	format := DetectFormat(text)
	segments := splitSegments(text, format)

	return buildChunks(segments, cfg)
}

// normalizeLineEndings replaces \r\n and \r with \n.
func normalizeLineEndings(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

// splitSegments divides text into logical segments based on format.
func splitSegments(text string, format Format) []string {
	switch format {
	case FormatJSONRPC:
		return splitJSONRPCExchanges(text)
	case FormatMarkdown:
		return splitMarkdownTurns(text)
	default:
		return splitParagraphs(text)
	}
}

// splitParagraphs splits on double newlines, preserving non-empty segments.
func splitParagraphs(text string) []string {
	raw := strings.Split(text, "\n\n")
	var segments []string
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s != "" {
			segments = append(segments, s)
		}
	}
	return segments
}

// splitMarkdownTurns splits on markdown turn markers (## Human, ## Assistant, etc.).
func splitMarkdownTurns(text string) []string {
	lines := strings.Split(text, "\n")
	var segments []string
	var current strings.Builder

	for _, line := range lines {
		if isMarkdownTurnMarker(line) {
			if current.Len() > 0 {
				s := strings.TrimSpace(current.String())
				if s != "" {
					segments = append(segments, s)
				}
				current.Reset()
			}
		}
		current.WriteString(line)
		current.WriteByte('\n')
	}
	if current.Len() > 0 {
		s := strings.TrimSpace(current.String())
		if s != "" {
			segments = append(segments, s)
		}
	}

	if len(segments) == 0 {
		return splitParagraphs(text)
	}
	return segments
}

// isMarkdownTurnMarker detects lines like "## Human", "## Assistant",
// "### User", "### Assistant", "**Human:**", "**Assistant:**".
func isMarkdownTurnMarker(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)

	prefixes := []string{
		"## human", "## assistant", "## user",
		"### human", "### assistant", "### user",
		"**human:**", "**assistant:**", "**user:**",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// splitJSONRPCExchanges splits JSON-RPC / chat-format transcripts on role boundaries.
// It looks for lines containing "role" markers and groups them.
func splitJSONRPCExchanges(text string) []string {
	lines := strings.Split(text, "\n")
	var segments []string
	var current strings.Builder

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isRoleBoundary(trimmed) && current.Len() > 0 {
			s := strings.TrimSpace(current.String())
			if s != "" {
				segments = append(segments, s)
			}
			current.Reset()
		}
		current.WriteString(line)
		current.WriteByte('\n')
	}
	if current.Len() > 0 {
		s := strings.TrimSpace(current.String())
		if s != "" {
			segments = append(segments, s)
		}
	}

	if len(segments) == 0 {
		return splitParagraphs(text)
	}
	return segments
}

// isRoleBoundary detects JSON lines with "role":"user" or "role":"assistant".
func isRoleBoundary(line string) bool {
	return strings.Contains(line, `"role"`) &&
		(strings.Contains(line, `"user"`) || strings.Contains(line, `"assistant"`))
}

// buildChunks accumulates segments into chunks respecting MaxChars and Overlap.
func buildChunks(segments []string, cfg ChunkConfig) []string {
	if len(segments) == 0 {
		return nil
	}

	var chunks []string
	var current strings.Builder

	for _, seg := range segments {
		// If the segment alone exceeds MaxChars, split it at sentence boundaries.
		if len(seg) > cfg.MaxChars {
			// Flush current buffer first.
			if current.Len() > 0 {
				chunks = append(chunks, strings.TrimSpace(current.String()))
				current.Reset()
			}
			chunks = append(chunks, splitLargeSegment(seg, cfg.MaxChars)...)
			continue
		}

		// Would adding this segment exceed the limit?
		if current.Len() > 0 && current.Len()+1+len(seg) > cfg.MaxChars {
			chunks = append(chunks, strings.TrimSpace(current.String()))
			current.Reset()
		}

		if current.Len() > 0 {
			current.WriteByte('\n')
		}
		current.WriteString(seg)
	}

	if current.Len() > 0 {
		chunks = append(chunks, strings.TrimSpace(current.String()))
	}

	if len(chunks) <= 1 || cfg.Overlap == 0 {
		return chunks
	}

	return applyOverlap(chunks, cfg.Overlap)
}

// splitLargeSegment breaks a segment that exceeds maxChars at sentence boundaries.
func splitLargeSegment(text string, maxChars int) []string {
	var parts []string
	for len(text) > maxChars {
		pos := lastSentenceBoundary(text, maxChars)
		parts = append(parts, strings.TrimSpace(text[:pos]))
		text = strings.TrimSpace(text[pos:])
	}
	if text != "" {
		parts = append(parts, strings.TrimSpace(text))
	}
	return parts
}

// lastSentenceBoundary scans backward from maxPos looking for a sentence-ending
// punctuation mark (.!?) followed by whitespace. Falls back to last word boundary,
// then to maxPos as absolute last resort.
func lastSentenceBoundary(text string, maxPos int) int {
	if maxPos >= len(text) {
		return len(text)
	}

	// Scan backward for sentence-ending punctuation followed by whitespace.
	for i := maxPos - 1; i > maxPos/4; i-- {
		if pos, ok := isSentenceEnd(text, i); ok {
			return pos
		}
	}

	// Fall back to last word boundary (space).
	for i := maxPos; i > maxPos/4; i-- {
		if text[i] == ' ' || text[i] == '\n' {
			return i
		}
	}

	return maxPos
}

// isSentenceEnd checks if position i holds a sentence-ending character (.!?)
// followed by whitespace or end of string. Returns the position AFTER the
// punctuation (i+1) so the caller can use it directly as a cut point.
func isSentenceEnd(text string, i int) (int, bool) {
	ch := text[i]
	if ch != '.' && ch != '!' && ch != '?' {
		return 0, false
	}
	next := i + 1
	if next >= len(text) {
		return next, true
	}
	if unicode.IsSpace(rune(text[next])) {
		return next, true
	}
	return 0, false
}

// applyOverlap inserts overlap text from the end of each chunk into the start
// of the next chunk, snapped to a word boundary.
func applyOverlap(chunks []string, overlap int) []string {
	result := make([]string, len(chunks))
	result[0] = chunks[0]

	for i := 1; i < len(chunks); i++ {
		prev := chunks[i-1]
		prefix := overlapPrefix(prev, overlap)
		if prefix != "" {
			result[i] = prefix + "\n" + chunks[i]
		} else {
			result[i] = chunks[i]
		}
	}
	return result
}

// overlapPrefix extracts the last ~n characters of text, snapped to a word boundary.
func overlapPrefix(text string, n int) string {
	if len(text) <= n {
		return text
	}
	start := len(text) - n
	// Snap forward to word boundary.
	for start < len(text) && !unicode.IsSpace(rune(text[start])) {
		start++
	}
	// Skip the whitespace itself.
	for start < len(text) && unicode.IsSpace(rune(text[start])) {
		start++
	}
	if start >= len(text) {
		return ""
	}
	return strings.TrimSpace(text[start:])
}
