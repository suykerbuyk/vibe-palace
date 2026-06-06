// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package mdutil provides pure-string markdown section-editing primitives used
// by vibe-palace's surgical resume.md editors (the vp_thread_* and vp_carried_*
// MCP tools).
//
// Every function in this package operates on markdown text and returns the
// modified document; nothing here touches the filesystem. The tool layer is
// responsible for reading the file off disk and writing the result back through
// internal/atomicfile so the write is atomic and surface-stamped. Keeping the
// write out of mdutil keeps the package free of vault/permission concerns and
// trivially testable.
package mdutil

import (
	"fmt"
	"strings"
)

// NormalizeSubheadingSlug extracts the slug from a ### heading text by
// truncating at the first " — " (space–em-dash–space) occurrence or returning
// the full text when that separator is absent.
//
// The input should be the text after the "### " prefix.
func NormalizeSubheadingSlug(headingText string) string {
	const sep = " — " // U+2014 em dash, space-padded
	if idx := strings.Index(headingText, sep); idx >= 0 {
		return headingText[:idx]
	}
	return headingText
}

// subheadingSlugs returns all ### sub-heading slugs found within the body of a
// ## parent section (from parentStart+1 up to parentEnd, exclusive).
func subheadingSlugs(lines []string, parentStart, parentEnd int) []string {
	var slugs []string
	for i := parentStart + 1; i < parentEnd; i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "### ") {
			slugs = append(slugs, NormalizeSubheadingSlug(strings.TrimPrefix(line, "### ")))
		}
	}
	return slugs
}

// findParentSection locates the start and end line indices (end is exclusive)
// for a ## heading in lines. Returns (-1, -1) when not found.
func findParentSection(lines []string, parentHeading string) (start, end int) {
	target := "## " + parentHeading
	start = -1
	for i, line := range lines {
		if strings.TrimSpace(line) == target {
			start = i
			break
		}
	}
	if start == -1 {
		return -1, -1
	}
	end = len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "## ") {
			end = i
			break
		}
	}
	return start, end
}

// htmlCommentLineRE reports whether a line is purely an HTML comment.
func htmlCommentLineRE(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "<!--") && strings.HasSuffix(t, "-->")
}

// ReplaceSubsectionBody replaces the body of a ### sub-heading inside a ##
// parent section. The sub-heading is matched by its normalized slug (text up to
// the first " — " or end-of-line).
//
// Rules:
//   - Zero matches → error listing all available slugs.
//   - One match → replace silently; return (modifiedDoc, nil).
//   - Multiple matches → hard error (Direction-C D9: ambiguity is a pre-write
//     structural failure, not a post-hoc warning). The returned error lists the
//     candidate slugs to help the operator disambiguate.
//
// The newBody must NOT include the ### heading line. The function re-emits the
// original heading line verbatim.
//
// HTML-comment-only lines inside the replaced body cause a hard error because
// the tool cannot safely preserve them.
func ReplaceSubsectionBody(doc, parentHeading, subHeading, newBody string) (string, error) {
	lines := strings.Split(doc, "\n")

	parentStart, parentEnd := findParentSection(lines, parentHeading)
	if parentStart == -1 {
		return "", fmt.Errorf("parent section %q not found", parentHeading)
	}

	// Collect all sub-heading positions that match the slug.
	type match struct{ headLineIdx, bodyEnd int }
	var matches []match

	for i := parentStart + 1; i < parentEnd; i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "### ") {
			slug := NormalizeSubheadingSlug(strings.TrimPrefix(line, "### "))
			if slug == subHeading {
				bodyEnd := parentEnd
				for j := i + 1; j < parentEnd; j++ {
					if strings.HasPrefix(strings.TrimSpace(lines[j]), "### ") {
						bodyEnd = j
						break
					}
				}
				matches = append(matches, match{i, bodyEnd})
			}
		}
	}

	if len(matches) == 0 {
		available := subheadingSlugs(lines, parentStart, parentEnd)
		return "", fmt.Errorf("slug %q not found in %s; available: %v", subHeading, parentHeading, available)
	}
	if len(matches) > 1 {
		idxs := make([]int, len(matches))
		for i, mm := range matches {
			idxs[i] = mm.headLineIdx
		}
		return "", ambiguousSlugError(lines, parentHeading, subHeading, idxs)
	}

	m := matches[0]

	// Reject if the existing body contains HTML-comment-only lines.
	for k := m.headLineIdx + 1; k < m.bodyEnd; k++ {
		if htmlCommentLineRE(lines[k]) {
			return "", fmt.Errorf("marker preservation not implemented inside replaced subsection body; manual edit required")
		}
	}

	// Build the replacement.
	var result []string
	result = append(result, lines[:m.headLineIdx+1]...) // up to and including ### heading
	result = append(result, "")
	body := strings.TrimRight(newBody, "\n")
	result = append(result, body)
	result = append(result, "")
	result = append(result, lines[m.bodyEnd:]...)

	return strings.Join(result, "\n"), nil
}

// InsertPosition describes where inside a ## parent section a new ### block
// should be inserted.
type InsertPosition struct {
	// Mode is one of "top", "bottom", "after", "before".
	Mode string
	// AnchorSlug is the normalized slug of the adjacent ### heading.
	// Required when Mode is "after" or "before".
	AnchorSlug string
}

// InsertSubsection inserts a new ### sub-heading block at the requested position
// inside a ## parent section. The caller supplies the body WITHOUT the heading
// line; the function emits "### subHeading\n\nbody\n".
//
// slug must NOT already exist inside the parent — hard error if it does.
func InsertSubsection(doc, parentHeading string, pos InsertPosition, subHeading, body string) (string, error) {
	lines := strings.Split(doc, "\n")

	parentStart, parentEnd := findParentSection(lines, parentHeading)
	if parentStart == -1 {
		return "", fmt.Errorf("parent section %q not found", parentHeading)
	}

	// Reject if slug already exists.
	for _, s := range subheadingSlugs(lines, parentStart, parentEnd) {
		if s == NormalizeSubheadingSlug(subHeading) {
			return "", fmt.Errorf("slug %q already exists in %s", subHeading, parentHeading)
		}
	}

	block := "### " + subHeading + "\n\n" + strings.TrimRight(body, "\n") + "\n"

	var insertIdx int // lines will be inserted BEFORE this index
	switch pos.Mode {
	case "top":
		// Right after the parent heading line; skip a blank line if present.
		insertIdx = parentStart + 1
		if insertIdx < parentEnd && strings.TrimSpace(lines[insertIdx]) == "" {
			insertIdx++
		}

	case "bottom":
		// Right before the next ## or end-of-doc; back up past trailing blanks.
		insertIdx = parentEnd
		for insertIdx > parentStart+1 && strings.TrimSpace(lines[insertIdx-1]) == "" {
			insertIdx--
		}

	case "after", "before":
		if pos.AnchorSlug == "" {
			return "", fmt.Errorf("anchor_slug required for position mode %q", pos.Mode)
		}
		anchorIdx := -1
		anchorBodyEnd := -1
		for j := parentStart + 1; j < parentEnd; j++ {
			line := strings.TrimSpace(lines[j])
			if strings.HasPrefix(line, "### ") {
				slug := NormalizeSubheadingSlug(strings.TrimPrefix(line, "### "))
				if slug == pos.AnchorSlug {
					anchorIdx = j
					anchorBodyEnd = parentEnd
					for k := j + 1; k < parentEnd; k++ {
						if strings.HasPrefix(strings.TrimSpace(lines[k]), "### ") {
							anchorBodyEnd = k
							break
						}
					}
					break
				}
			}
		}
		if anchorIdx == -1 {
			available := subheadingSlugs(lines, parentStart, parentEnd)
			return "", fmt.Errorf("anchor slug %q not found in %s; available: %v", pos.AnchorSlug, parentHeading, available)
		}
		if pos.Mode == "after" {
			// Insert after anchor body end (back up past trailing blanks).
			insertIdx = anchorBodyEnd
			for insertIdx > anchorIdx+1 && strings.TrimSpace(lines[insertIdx-1]) == "" {
				insertIdx--
			}
		} else { // before
			insertIdx = anchorIdx
		}

	default:
		return "", fmt.Errorf("unknown position mode %q; expected top, bottom, after, or before", pos.Mode)
	}

	// Splice: lines[:insertIdx] + block lines + lines[insertIdx:]
	blockLines := strings.Split(block, "\n")
	var result []string
	result = append(result, lines[:insertIdx]...)
	result = append(result, blockLines...)
	result = append(result, lines[insertIdx:]...)

	return strings.Join(result, "\n"), nil
}

// RemoveSubsection removes a ### sub-heading block (heading line + body) from
// inside a ## parent section. Matches by normalized slug.
//
//   - Zero matches → error.
//   - One match → remove silently.
//   - Multiple matches → hard error (Direction-C D9: ambiguity is a pre-write
//     structural failure, not a post-hoc warning). The returned error lists the
//     candidate slugs to help the operator disambiguate.
func RemoveSubsection(doc, parentHeading, subHeading string) (string, error) {
	lines := strings.Split(doc, "\n")

	parentStart, parentEnd := findParentSection(lines, parentHeading)
	if parentStart == -1 {
		return "", fmt.Errorf("parent section %q not found", parentHeading)
	}

	type match struct{ headLineIdx, bodyEnd int }
	var matches []match

	for i := parentStart + 1; i < parentEnd; i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "### ") {
			slug := NormalizeSubheadingSlug(strings.TrimPrefix(line, "### "))
			if slug == subHeading {
				bodyEnd := parentEnd
				for j := i + 1; j < parentEnd; j++ {
					if strings.HasPrefix(strings.TrimSpace(lines[j]), "### ") {
						bodyEnd = j
						break
					}
				}
				matches = append(matches, match{i, bodyEnd})
			}
		}
	}

	if len(matches) == 0 {
		available := subheadingSlugs(lines, parentStart, parentEnd)
		return "", fmt.Errorf("slug %q not found in %s; available: %v", subHeading, parentHeading, available)
	}
	if len(matches) > 1 {
		idxs := make([]int, len(matches))
		for i, mm := range matches {
			idxs[i] = mm.headLineIdx
		}
		return "", ambiguousSlugError(lines, parentHeading, subHeading, idxs)
	}

	m := matches[0]

	// Remove the heading + body, trimming trailing blank lines before the
	// heading and preserving a single blank separator when content follows.
	keepBefore := m.headLineIdx
	for keepBefore > parentStart+1 && strings.TrimSpace(lines[keepBefore-1]) == "" {
		keepBefore--
	}

	var result []string
	result = append(result, lines[:keepBefore]...)
	if m.bodyEnd < len(lines) {
		result = append(result, "")
	}
	result = append(result, lines[m.bodyEnd:]...)

	return strings.Join(result, "\n"), nil
}

// ambiguousSlugError builds the Direction-C D9 hard error for a multi-match
// slug, listing the candidate headings to help the operator disambiguate.
func ambiguousSlugError(lines []string, parentHeading, subHeading string, headIdxs []int) error {
	var candidateSlugs []string
	for _, idx := range headIdxs {
		candidateSlugs = append(candidateSlugs, NormalizeSubheadingSlug(
			strings.TrimPrefix(strings.TrimSpace(lines[idx]), "### ")))
	}
	return fmt.Errorf("slug %q ambiguous in %s: %d matches (candidates: %s); resolve duplicates before retrying",
		subHeading, parentHeading, len(headIdxs), strings.Join(candidateSlugs, ","))
}
