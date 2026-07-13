// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package mdfence is the ONE definition of a markdown code fence in this
// codebase.
//
// It exists because there were two, and they were both wrong in the same way.
// internal/storage (since iteration 184) and internal/wrapstate (since 191)
// each decided a fence delimiter was "the trimmed line starts with ``` or ~~~".
// That rule is not CommonMark, and iterations.md line 698 is the counterexample
// — an indented bullet continuation whose first non-space characters are an
// INLINE code run:
//
//	```bash tutorial``` extraction from `doc/TUTORIAL.md` — deferred
//
// The naive rule reads that as a lone OPENING fence, inverts its fence state,
// and never recovers. In wrapstate it swallowed 187 of 191 iteration headings
// and reported iteration 77 on a project at 190. In storage.validateTaskBody it
// failed OPEN: everything after such a line was treated as fenced and skipped,
// so a duplicate "**Status:**" sailed past the check that iteration 184 added
// specifically to stop it.
//
// The actual CommonMark rule, and the one implemented here: an opening backtick
// fence's info string MAY NOT CONTAIN A BACKTICK — precisely so that a line like
// the one above is prose, not a fence. A closing delimiter is a bare run of the
// same character, at least as long as the opener, with no info string. An indent
// of four or more spaces is an indented code block, not a delimiter.
//
// This is the same shape as the status-line defect of 184 and the heading defect
// of 191: two private definitions of one concept, disagreeing where nobody
// looked. The remedy is the same. Do not add a third copy — call this.
package mdfence

import "strings"

// Line is a source line lying outside any fenced code block.
type Line struct {
	// Num is the 1-indexed line number within the original content.
	Num int
	// Text is the raw line, exactly as it appeared. Callers that want
	// leading whitespace removed should trim it themselves.
	Text string
}

// Delim reports whether line is a code-fence delimiter, returning the fence
// character, the length of its run, and its info string.
//
// It does NOT decide whether the delimiter opens or closes — that depends on
// scanner state. See OutsideFences.
func Delim(line string) (ch byte, run int, info string, ok bool) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	// Four or more leading spaces is an indented code block, not a fence.
	if indent > 3 || indent >= len(line) {
		return 0, 0, "", false
	}
	c := line[indent]
	if c != '`' && c != '~' {
		return 0, 0, "", false
	}
	end := indent
	for end < len(line) && line[end] == c {
		end++
	}
	if end-indent < 3 {
		return 0, 0, "", false
	}
	return c, end - indent, strings.TrimSpace(line[end:]), true
}

// OpensFence reports whether a delimiter with the given fence character and
// info string can OPEN a fence.
//
// A backtick fence's info string may not contain a backtick. This single rule
// is what distinguishes a real opening fence from a line of prose that merely
// begins with an inline code run — and getting it wrong is the entire bug this
// package exists to prevent. Tilde fences have no such restriction.
func OpensFence(ch byte, info string) bool {
	if ch == '`' && strings.Contains(info, "`") {
		return false
	}
	return true
}

// Kind classifies a line relative to fenced code blocks.
type Kind int

const (
	// Text is a line outside any fence. Only these lines are structurally
	// significant: a heading, a table row, or a metadata line is only real here.
	Text Kind = iota
	// Delimiter is a fence delimiter line — it opens or closes a fence, and is
	// itself neither content nor structure. Callers tracking contiguous runs
	// (a markdown table, say) should treat it as a break.
	Delimiter
	// Fenced is a line inside a fence: sample text, never structure.
	Fenced
)

// Scanner tracks fence state across a document, one line at a time.
//
// Use it when a caller needs to distinguish a Delimiter from Fenced content —
// for example to break a contiguous table run at a fence boundary. When all a
// caller wants is "the lines that are structurally real", use OutsideFences.
//
// The zero value is ready to use.
type Scanner struct {
	ch  byte
	run int
	in  bool
}

// Step advances the scanner by one line and classifies it.
func (s *Scanner) Step(line string) Kind {
	if ch, run, info, ok := Delim(line); ok {
		switch {
		case !s.in:
			// A backtick delimiter whose info string carries a backtick cannot
			// open a fence: this is prose containing an inline code run. Fall
			// through and classify it as Text — misreading it as a Delimiter is
			// the bug this package exists to prevent.
			if !OpensFence(ch, info) {
				return Text
			}
			s.in, s.ch, s.run = true, ch, run
			return Delimiter
		case ch == s.ch && run >= s.run && info == "":
			s.in = false
			return Delimiter
		default:
			// A non-matching delimiter inside a fence is just content.
			return Fenced
		}
	}
	if s.in {
		return Fenced
	}
	return Text
}

// OutsideFences returns every line of content that lies outside a fenced code
// block, with its 1-indexed line number. Fence delimiter lines themselves are
// never returned.
//
// An unterminated fence means the remainder of the content is treated as fenced.
// That is deliberate: a heading-shaped or metadata-shaped line inside a half-open
// fence is far more likely to be sample text than the real thing, and callers of
// this package are deciding whether such a line is structurally significant.
func OutsideFences(content string) []Line {
	var out []Line
	var s Scanner
	for i, line := range strings.Split(content, "\n") {
		if s.Step(line) == Text {
			out = append(out, Line{Num: i + 1, Text: line})
		}
	}
	return out
}
