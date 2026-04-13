// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package absorb

import (
	"bufio"
	"regexp"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
)

// SplitStrategy selects how the splitter walks a source file.
type SplitStrategy int

const (
	// SplitHeading treats markdown `#`-headings as section boundaries.
	SplitHeading SplitStrategy = iota
	// SplitWholeFile returns the entire non-managed body as a single section.
	SplitWholeFile
)

// Section is one contiguous slice of a source file with classification
// inputs already extracted.
type Section struct {
	// Heading is the heading text without leading `#` marks. Empty for
	// the non-heading preamble before the first heading, and for
	// whole-file synthetic sections.
	Heading string
	// Level is the markdown heading level (1..6). Zero for preamble /
	// whole-file synthetic sections.
	Level int
	// Body is the section content (excluding the heading line itself).
	Body string
	// StartLine is the 1-indexed source line where the section begins
	// (the heading line, or line 1 for preamble / whole-file).
	StartLine int
	// EndLine is the 1-indexed inclusive last source line of the section.
	EndLine int
}

// headingLine matches a markdown ATX heading at column zero. We do not
// match setext (underlined) headings — they're rare in these files and the
// cost of missing them is minor (content falls into the preceding section
// or preamble, which still routes safely).
var headingLine = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*$`)

// fenceLine matches a fenced-code-block delimiter (``` or ~~~ with
// optional info string). Used to skip over code that happens to contain
// `#` characters at column zero.
var fenceLine = regexp.MustCompile("^(```|~~~)")

// Split returns the ordered section list for data using the requested
// strategy. For SplitHeading, the managed vibe-palace block (if present)
// is excised before splitting so its body never appears as a section.
// For SplitWholeFile, the entire non-managed body becomes a single
// synthetic section with Heading == "" and Level == 0.
func Split(data []byte, strategy SplitStrategy) []Section {
	body := stripManagedBlock(data)
	if strategy == SplitWholeFile {
		trimmed := strings.TrimSpace(body)
		if trimmed == "" {
			return nil
		}
		return []Section{{
			Heading:   "",
			Level:     0,
			Body:      trimmed,
			StartLine: 1,
			EndLine:   1 + strings.Count(body, "\n"),
		}}
	}
	return splitByHeading(body)
}

func stripManagedBlock(data []byte) string {
	start, end := agentfile.FindBlock(data)
	if start < 0 {
		return string(data)
	}
	var b strings.Builder
	b.Grow(len(data))
	b.Write(data[:start])
	// Collapse the block's trailing newline to avoid leaving an extra
	// blank line.
	if end < len(data) && data[end] == '\n' {
		end++
	}
	b.Write(data[end:])
	return b.String()
}

type cursor struct {
	heading string
	level   int
	start   int
	lines   []string
}

// splitByHeading scans body line-by-line, opening a new section whenever a
// heading line appears outside a fenced code block. Content before the
// first heading becomes a single preamble section with Level == 0.
func splitByHeading(body string) []Section {
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var sections []Section
	var cur cursor
	cur.start = 1

	lineno := 0
	inFence := false
	for scanner.Scan() {
		lineno++
		line := scanner.Text()
		if fenceLine.MatchString(line) {
			inFence = !inFence
			cur.lines = append(cur.lines, line)
			continue
		}
		if !inFence {
			if m := headingLine.FindStringSubmatch(line); m != nil {
				// Flush the previous section.
				if cur.heading != "" || cur.hasContent() {
					sections = append(sections, cur.finalize(lineno-1))
				}
				cur = cursor{
					heading: strings.TrimSpace(m[2]),
					level:   len(m[1]),
					start:   lineno,
				}
				continue
			}
		}
		cur.lines = append(cur.lines, line)
	}
	// Flush last section.
	if cur.heading != "" || cur.hasContent() {
		sections = append(sections, cur.finalize(lineno))
	}
	return sections
}

func (c cursor) hasContent() bool {
	for _, l := range c.lines {
		if strings.TrimSpace(l) != "" {
			return true
		}
	}
	return false
}

func (c cursor) finalize(endLine int) Section {
	body := strings.TrimRight(strings.Join(c.lines, "\n"), "\n")
	return Section{
		Heading:   c.heading,
		Level:     c.level,
		Body:      body,
		StartLine: c.start,
		EndLine:   endLine,
	}
}
