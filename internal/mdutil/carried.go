// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mdutil

import (
	"fmt"
	"log"
	"strings"
)

// CarriedBullet represents one bullet entry in the ### Carried forward section.
type CarriedBullet struct {
	// Slug is the canonical identifier extracted from bold text.
	//
	// For bold variants: text inside ** ** up to the first " — " separator
	// (or the full bold span if no separator present). Multiline bold text
	// is joined with a single space.
	//
	// For plain text: first significant phrase up to first sentence-ending
	// punctuation or first 6 words.
	Slug string

	// Body is the prose that follows the slug (on the same line after " — "
	// for em-dash form, or the full line for others), plus any continuation
	// lines, trimmed of surrounding whitespace.
	Body string

	// RawForm records which liberal-read variant was matched:
	//   "canonical"  — - **slug**
	//   "bold-colon" — - **slug:**
	//   "bold-paren" — - **slug (note)**
	//   "em-dash"    — - **slug** — body…
	//   "plain"      — - text (slug derived from plain text)
	RawForm string

	// rawText is the original verbatim bullet text (first line + all
	// continuation lines, joined with "\n"). Used for round-trip preservation:
	// when a bullet is re-emitted without modification it is written verbatim.
	rawText string
}

// RawText returns the verbatim source text of this bullet (first line plus
// continuation lines, newline-separated, without trailing newline).
func (b CarriedBullet) RawText() string {
	return b.rawText
}

// ParseCarriedForward parses the body of a ### Carried forward sub-section into
// a typed list of bullets. The input is the raw text between the ### heading
// line and the next ### or ## heading (or end-of-doc).
//
// Liberal-on-read: accepts multiple bold-list variants and plain `- text`
// lines. A warning is logged when the plain-text path is taken.
func ParseCarriedForward(body string) []CarriedBullet {
	lines := strings.Split(body, "\n")
	var bullets []CarriedBullet

	i := 0
	for i < len(lines) {
		line := lines[i]
		if !strings.HasPrefix(line, "- ") {
			i++
			continue
		}

		// Collect this bullet and its continuation lines.
		raw := []string{line}
		i++
		for i < len(lines) {
			next := lines[i]
			// Continuation: line indented with space/tab.
			if strings.HasPrefix(next, "  ") || strings.HasPrefix(next, "\t") {
				raw = append(raw, next)
				i++
				continue
			}
			// Blank line followed by an indented line = paragraph break within bullet.
			if strings.TrimSpace(next) == "" && i+1 < len(lines) &&
				(strings.HasPrefix(lines[i+1], "  ") || strings.HasPrefix(lines[i+1], "\t")) {
				raw = append(raw, next)
				i++
				continue
			}
			break
		}

		bullets = append(bullets, parseSingleBullet(raw))
	}

	return bullets
}

// parseSingleBullet parses one bullet (first line + continuation lines) into a
// CarriedBullet.
func parseSingleBullet(rawLines []string) CarriedBullet {
	if len(rawLines) == 0 {
		return CarriedBullet{}
	}

	rawText := strings.Join(rawLines, "\n")
	first := rawLines[0]
	// Strip leading "- "
	content := strings.TrimPrefix(first, "- ")

	// Collect the continuation lines stripped of indent, for body composition.
	var contParts []string
	for _, cl := range rawLines[1:] {
		contParts = append(contParts, strings.TrimLeft(cl, " \t"))
	}
	contText := strings.TrimSpace(strings.Join(contParts, " "))

	// joinBody appends continuation text to a remainder from the first line.
	joinBody := func(rem string) string {
		rem = strings.TrimSpace(rem)
		if contText == "" {
			return rem
		}
		if rem == "" {
			return contText
		}
		return rem + " " + contText
	}

	// Bold-text variants: bullet content starts with "**".
	if strings.HasPrefix(content, "**") {
		rest := content[2:] // after opening **

		// Find the closing **.
		closeIdx := strings.Index(rest, "**")
		if closeIdx >= 0 {
			boldText := rest[:closeIdx]
			after := strings.TrimSpace(rest[closeIdx+2:]) // text after closing **

			// Normalise multiline bold text (continuation might have carried it).
			// For single-line bullets this is a no-op.
			boldText = strings.Join(strings.Fields(boldText), " ")

			// em-dash variant: - **slug** — body…
			// Note: after has already been TrimSpaced so leading "— " starts it.
			if strings.HasPrefix(after, "— ") || after == "—" {
				bodyRem := strings.TrimPrefix(after, "— ")
				slug := deriveSlugFromBold(boldText)
				return CarriedBullet{Slug: slug, Body: joinBody(bodyRem), RawForm: "em-dash", rawText: rawText}
			}

			// bold-colon variant: - **slug:**
			if strings.HasSuffix(boldText, ":") {
				slug := strings.TrimSuffix(boldText, ":")
				return CarriedBullet{Slug: slug, Body: joinBody(after), RawForm: "bold-colon", rawText: rawText}
			}

			// canonical / bold-paren: - **slug** or - **slug (note)**
			slug := deriveSlugFromBold(boldText)
			rawForm := "canonical"
			if strings.Contains(boldText, "(") {
				rawForm = "bold-paren"
			}
			return CarriedBullet{Slug: slug, Body: joinBody(after), RawForm: rawForm, rawText: rawText}
		}

		// No closing ** found — treat remainder as bold spanning multiple lines.
		// Join all lines and re-attempt (handles "- **text\n  cont**").
		allContent := content
		for _, cl := range rawLines[1:] {
			allContent += " " + strings.TrimLeft(cl, " \t")
		}
		if joined := parseSingleBullet([]string{"- " + allContent}); joined.Slug != "" {
			joined.rawText = rawText
			return joined
		}
	}

	// Plain "- text" path: derive slug from plain text.
	log.Printf("mdutil: warning: non-conforming carried bullet, deriving slug from plain text: %q", first)
	fullContent := content
	for _, cl := range rawLines[1:] {
		fullContent += " " + strings.TrimLeft(cl, " \t")
	}
	slug := derivePlainSlug(fullContent)
	bodyRem := strings.TrimSpace(strings.TrimPrefix(fullContent, slug))
	return CarriedBullet{Slug: slug, Body: bodyRem, RawForm: "plain", rawText: rawText}
}

// deriveSlugFromBold extracts the slug from bold text. When the text contains
// " — " the slug is the portion before; otherwise the full text is returned.
func deriveSlugFromBold(bold string) string {
	const sep = " — " // U+2014 em dash
	if idx := strings.Index(bold, sep); idx >= 0 {
		return strings.TrimSpace(bold[:idx])
	}
	return strings.TrimSpace(bold)
}

// derivePlainSlug extracts a slug from plain bullet text as the first
// significant phrase up to the first sentence-ending punctuation or the first
// 6 words, whichever comes first.
func derivePlainSlug(text string) string {
	text = strings.TrimSpace(text)
	for i, r := range text {
		if r == '.' || r == '!' || r == '?' {
			return strings.TrimSpace(text[:i])
		}
	}
	words := strings.Fields(text)
	if len(words) > 6 {
		words = words[:6]
	}
	return strings.Join(words, " ")
}

// EmitCarriedBullets serialises a slice of CarriedBullet back to markdown.
// Bullets with non-empty rawText are emitted verbatim (round-trip
// preservation); newly synthesised bullets use canonical form. The returned
// string ends with a trailing newline (empty input yields "").
func EmitCarriedBullets(bullets []CarriedBullet) string {
	return existingBulletsText(bullets)
}

// buildCanonicalBulletLine builds a single-line canonical bullet.
func buildCanonicalBulletLine(slug, body string) string {
	if body != "" {
		return fmt.Sprintf("- **%s** — %s\n", slug, body)
	}
	return fmt.Sprintf("- **%s**\n", slug)
}

// BuildCarriedBullet constructs a canonical bullet string for a new entry given
// slug, title (short description), and body (longer prose):
//
//   - **{slug}** — {title}
//     {body continuation}
//
// When body is empty, only the title line is emitted. When body is a single
// paragraph it is appended after the title on the same line; multi-paragraph
// bodies are indented as continuation lines.
func BuildCarriedBullet(slug, title, body string) string {
	if body == "" {
		return fmt.Sprintf("- **%s** — %s\n", slug, title)
	}
	bodyTrimmed := strings.TrimRight(body, "\n")
	if !strings.Contains(bodyTrimmed, "\n") {
		return fmt.Sprintf("- **%s** — %s %s\n", slug, title, bodyTrimmed)
	}
	return fmt.Sprintf("- **%s** — %s\n  %s\n", slug, title, indentContinuation(bodyTrimmed))
}

// indentContinuation adds 2-space indent to continuation lines.
func indentContinuation(body string) string {
	lines := strings.Split(body, "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i] != "" {
			lines[i] = "  " + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

const carriedForwardSubheading = "Carried forward"

// AddCarriedBullet inserts a new bullet at the bottom of the ### Carried forward
// sub-section inside the given parent ## section. Returns an error if a bullet
// with the same slug already exists (case-insensitive comparison).
func AddCarriedBullet(doc, parentHeading, slug, title, body string) (string, error) {
	bullets, cfStart, cfBodyEnd, err := locateCarriedBullets(doc, parentHeading)
	if err != nil {
		return "", err
	}

	slugLower := strings.ToLower(slug)
	for _, b := range bullets {
		if strings.ToLower(b.Slug) == slugLower {
			return "", fmt.Errorf("carried-forward slug %q already exists", slug)
		}
	}

	newBulletStr := BuildCarriedBullet(slug, title, body)
	return spliceCarriedBody(doc, cfStart, cfBodyEnd, existingBulletsText(bullets)+newBulletStr)
}

// RemoveCarriedBullet removes the bullet matching slug (case-insensitive) from
// ### Carried forward. Returns an error if the slug is not found.
func RemoveCarriedBullet(doc, parentHeading, slug string) (string, error) {
	bullets, cfStart, cfBodyEnd, err := locateCarriedBullets(doc, parentHeading)
	if err != nil {
		return "", err
	}

	slugLower := strings.ToLower(slug)
	idx := -1
	for i, b := range bullets {
		if strings.ToLower(b.Slug) == slugLower {
			idx = i
			break
		}
	}
	if idx == -1 {
		return "", fmt.Errorf("carried-forward slug %q not found; available: %v", slug, carriedSlugList(bullets))
	}

	kept := append(bullets[:idx:idx], bullets[idx+1:]...)
	return spliceCarriedBody(doc, cfStart, cfBodyEnd, existingBulletsText(kept))
}

// GetCarriedBullet returns the CarriedBullet matching slug (case-insensitive).
func GetCarriedBullet(doc, parentHeading, slug string) (CarriedBullet, error) {
	bullets, _, _, err := locateCarriedBullets(doc, parentHeading)
	if err != nil {
		return CarriedBullet{}, err
	}
	slugLower := strings.ToLower(slug)
	for _, b := range bullets {
		if strings.ToLower(b.Slug) == slugLower {
			return b, nil
		}
	}
	return CarriedBullet{}, fmt.Errorf("carried-forward slug %q not found; available: %v", slug, carriedSlugList(bullets))
}

// carriedSlugList returns the slugs of bullets, for error messages.
func carriedSlugList(bullets []CarriedBullet) []string {
	out := make([]string, len(bullets))
	for i, b := range bullets {
		out[i] = b.Slug
	}
	return out
}

// locateCarriedBullets finds and parses the ### Carried forward body within the
// parent section, returning (bullets, cfHeadingLine, cfBodyEnd, error).
// cfHeadingLine is the index of the "### Carried forward" line. cfBodyEnd is
// the index of the first line after the body (next ### or parentEnd).
func locateCarriedBullets(doc, parentHeading string) ([]CarriedBullet, int, int, error) {
	lines := strings.Split(doc, "\n")
	parentStart, parentEnd := findParentSection(lines, parentHeading)
	if parentStart == -1 {
		return nil, 0, 0, fmt.Errorf("parent section %q not found", parentHeading)
	}

	cfStart := -1
	cfBodyEnd := parentEnd
	for i := parentStart + 1; i < parentEnd; i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "### ") {
			s := NormalizeSubheadingSlug(strings.TrimPrefix(line, "### "))
			if s == carriedForwardSubheading {
				cfStart = i
				for j := i + 1; j < parentEnd; j++ {
					if strings.HasPrefix(strings.TrimSpace(lines[j]), "### ") {
						cfBodyEnd = j
						break
					}
				}
				break
			}
		}
	}
	if cfStart == -1 {
		return nil, 0, 0, fmt.Errorf("### Carried forward not found in %q", parentHeading)
	}

	body := strings.Join(lines[cfStart+1:cfBodyEnd], "\n")
	return ParseCarriedForward(body), cfStart, cfBodyEnd, nil
}

// existingBulletsText serialises bullets preserving raw text for unchanged
// bullets and emitting canonical form for tool-generated ones.
func existingBulletsText(bullets []CarriedBullet) string {
	var sb strings.Builder
	for _, b := range bullets {
		if b.rawText != "" {
			sb.WriteString(b.rawText)
			sb.WriteByte('\n')
		} else {
			sb.WriteString(buildCanonicalBulletLine(b.Slug, b.Body))
		}
	}
	return sb.String()
}

// spliceCarriedBody replaces the body between cfStart+1 and cfBodyEnd with
// newBody, returning the updated document.
func spliceCarriedBody(doc string, cfStart, cfBodyEnd int, newBody string) (string, error) {
	lines := strings.Split(doc, "\n")

	var result []string
	result = append(result, lines[:cfStart+1]...) // up to and including ### Carried forward
	if newBody != "" {
		result = append(result, "")
		result = append(result, strings.TrimRight(newBody, "\n"))
	}
	result = append(result, "")
	result = append(result, lines[cfBodyEnd:]...)

	return strings.Join(result, "\n"), nil
}
