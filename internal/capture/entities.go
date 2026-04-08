// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"regexp"
	"strings"
)

// ExtractedEntity represents an entity found via regex in transcript text.
type ExtractedEntity struct {
	Name string
	Type string // "file", "url", "tool"
}

var (
	// filePathRe matches paths ending in common source file extensions.
	// The trailing boundary allows period (sentence-ending), whitespace, or end-of-string.
	filePathRe = regexp.MustCompile(`(?:^|[^a-zA-Z0-9_/.-])([a-zA-Z_./][a-zA-Z0-9_/.~-]*\.(?:go|ts|tsx|js|jsx|py|rs|c|cpp|cc|cxx|h|hpp|hxx|java|kt|rb|lua|zig|nim|toml|yaml|yml|json|md|sh|sql|proto|css|html))(?:[^a-zA-Z0-9_/-]|$)`)

	// urlRe matches HTTP(S) URLs.
	urlRe = regexp.MustCompile(`https?://[^\s)"'>` + "`" + `]+`)
)

// ExtractEntities returns deduplicated entities found in text via regex patterns.
func ExtractEntities(text string) []ExtractedEntity {
	seen := make(map[string]bool)
	var entities []ExtractedEntity

	add := func(name, typ string) {
		key := typ + ":" + name
		if seen[key] {
			return
		}
		seen[key] = true
		entities = append(entities, ExtractedEntity{Name: name, Type: typ})
	}

	// Extract file paths.
	for _, match := range filePathRe.FindAllStringSubmatch(text, -1) {
		path := match[1]
		// Skip very short matches that are likely false positives.
		if len(path) < 5 {
			continue
		}
		// Skip common false positives.
		if path == "go.mod" || path == "go.sum" {
			continue
		}
		add(path, "file")
	}

	// Extract URLs.
	for _, match := range urlRe.FindAllString(text, -1) {
		// Clean trailing punctuation.
		match = strings.TrimRight(match, ".,;:!?")
		add(match, "url")
	}

	return entities
}
