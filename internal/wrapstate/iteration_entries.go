// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package wrapstate

import (
	"strings"
)

// IterHeading is one iteration header found outside a code fence.
// Line is 1-indexed within the source content.
type IterHeading struct {
	Line   int    // 1-indexed line number
	Hashes string // "##" or "###"
	N      int    // iteration number
	Text   string // trimmed full header line
}

// Entry is one narrative in iterations.md: the header line plus the body the
// writer stored under it. Body is the trimmed narrative only — the leading
// blank line after the header and a trailing "---" separator before the next
// entry are stripped so a round-trip through AppendIterationOwned compares
// equal to the stored body.
type Entry struct {
	N      int    // iteration number
	Title  string // text after the em dash in the header
	Header string // full trimmed header line
	Body   string // narrative body (trimmed; no trailing ---)
	Bytes  int    // len(Body) in bytes — the budget unit for recent fill
	Line   int    // 1-indexed header line
}

// ScanIterHeadings returns every iteration header outside a fenced code block.
// It is the exported form of the scanner NextIterFromIterationsMD already uses.
func ScanIterHeadings(content string) []IterHeading {
	raw := scanIterHeadings(content)
	out := make([]IterHeading, len(raw))
	for i, h := range raw {
		out[i] = IterHeading{Line: h.line, Hashes: h.hashes, N: h.n, Text: h.text}
	}
	return out
}

// ParseEntries returns every iteration entry in file order (append-at-EOF =
// chronological ascending). The file preamble claiming "newest first" is false.
func ParseEntries(content string) []Entry {
	heads := scanIterHeadings(content)
	if len(heads) == 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	out := make([]Entry, 0, len(heads))
	for i, h := range heads {
		// Header is at 1-indexed line h.line → index h.line-1.
		// Body runs from the next line up to (but not including) the next header.
		start := h.line // 0-indexed first body line
		end := len(lines)
		if i+1 < len(heads) {
			end = heads[i+1].line - 1 // 0-indexed exclusive = next header's index
		}
		if start > end {
			start = end
		}
		if start > len(lines) {
			start = len(lines)
		}
		if end > len(lines) {
			end = len(lines)
		}
		rawBody := strings.Join(lines[start:end], "\n")
		body := stripEntryBody(rawBody)
		out = append(out, Entry{
			N:      h.n,
			Title:  titleFromHeader(h.text),
			Header: h.text,
			Body:   body,
			Bytes:  len(body),
			Line:   h.line,
		})
	}
	return out
}

// EntriesByN returns every entry whose iteration number is n, in file order.
// An empty result means not-found (gap), not success-with-no-body.
func EntriesByN(content string, n int) []Entry {
	var out []Entry
	for _, e := range ParseEntries(content) {
		if e.N == n {
			out = append(out, e)
		}
	}
	return out
}

// LastEntryByN returns the last file-order match for n, if any.
// Used by the bare per-iteration resource URI (iteration/{project}/{n}).
func LastEntryByN(content string, n int) (Entry, bool) {
	all := EntriesByN(content, n)
	if len(all) == 0 {
		return Entry{}, false
	}
	return all[len(all)-1], true
}

// EntryByNMatch returns the matchIndex-th file-order match for n (0-based).
// Used by iteration/{project}/{n}/{match} so content_uri is byte-identical to
// the tool row that advertised it.
func EntryByNMatch(content string, n, matchIndex int) (Entry, bool) {
	all := EntriesByN(content, n)
	if matchIndex < 0 || matchIndex >= len(all) {
		return Entry{}, false
	}
	return all[matchIndex], true
}

// EntriesNewestFirst returns entries in reverse file order (newest first).
// Selection for a byte budget walks this order; results are still returned to
// callers in file order after the window is chosen.
func EntriesNewestFirst(content string) []Entry {
	fwd := ParseEntries(content)
	if len(fwd) == 0 {
		return nil
	}
	out := make([]Entry, len(fwd))
	for i := range fwd {
		out[i] = fwd[len(fwd)-1-i]
	}
	return out
}

// stripEntryBody removes the writer's framing from the raw text between one
// header and the next: leading/trailing blank lines and a trailing "---"
// separator line that precedes the next entry's "\n---\n" frame.
func stripEntryBody(raw string) string {
	// Writer frame ends the previous entry with "\n---\n" before the next
	// header. After joining body lines that trailing separator appears as a
	// final "\n---" (or bare "---" if the body was empty). Strip only that
	// framing — a body's own thematic break is preserved when it is not the
	// sole trailing separator pattern the writer emits between entries.
	s := strings.TrimSpace(raw)
	if rest, ok := strings.CutSuffix(s, "\n---"); ok {
		return strings.TrimSpace(rest)
	}
	if s == "---" {
		return ""
	}
	return s
}

// titleFromHeader extracts the title after "Iteration N —" / "Iteration N -".
func titleFromHeader(header string) string {
	// Strip leading hashes and space.
	s := strings.TrimSpace(header)
	for strings.HasPrefix(s, "#") {
		s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
	}
	const mark = "Iteration "
	i := strings.Index(s, mark)
	if i < 0 {
		return ""
	}
	s = s[i+len(mark):]
	// Skip the number.
	j := 0
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	s = strings.TrimSpace(s[j:])
	if rest, ok := strings.CutPrefix(s, "—"); ok {
		return strings.TrimSpace(rest)
	}
	if rest, ok := strings.CutPrefix(s, "-"); ok {
		return strings.TrimSpace(rest)
	}
	return s
}
