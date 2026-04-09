// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package kg

import (
	"regexp"
	"strings"
)

// ExtractedTriple represents a relationship extracted from text.
type ExtractedTriple struct {
	Subject    string
	Predicate  string
	Object     string
	Confidence float64
	Flags      []string
	ValidFrom  string
}

// triplePattern is a compiled regex + triple template.
type triplePattern struct {
	re         *regexp.Regexp
	predicate  string
	confidence float64
	flags      []string
	validFrom  bool // whether to set ValidFrom from the caller-provided date
}

var triplePatterns = []triplePattern{
	{
		re:         regexp.MustCompile(`(?i)\b([A-Z][a-z]+(?:\s+[A-Z][a-z]+)*)\s+works?\s+on\s+(?:the\s+)?([A-Za-z][A-Za-z0-9_-]+)\b`),
		predicate:  "works_on",
		confidence: 0.7,
	},
	{
		re:         regexp.MustCompile(`(?i)\b([A-Z][a-z]+(?:\s+[A-Z][a-z]+)*)\s+decided\s+to\s+use\s+([A-Za-z][A-Za-z0-9_-]+)\b`),
		predicate:  "decided",
		confidence: 0.8,
		flags:      []string{"DECISION"},
	},
	{
		re:         regexp.MustCompile(`(?i)\b([A-Z][a-z]+(?:\s+[A-Z][a-z]+)*)\s+(?:started|joined)\s+(?:the\s+)?([A-Za-z][A-Za-z0-9_-]+)\b`),
		predicate:  "member_of",
		confidence: 0.7,
		validFrom:  true,
	},
	{
		re:         regexp.MustCompile(`(?i)\b([A-Za-z][A-Za-z0-9_-]+)\s+depends?\s+on\s+([A-Za-z][A-Za-z0-9_-]+)\b`),
		predicate:  "depends_on",
		confidence: 0.6,
	},
	{
		re:         regexp.MustCompile(`(?i)\b([A-Z][a-z]+(?:\s+[A-Z][a-z]+)*)\s+(?:created|built)\s+(?:the\s+)?([A-Za-z][A-Za-z0-9_-]+)\b`),
		predicate:  "created",
		confidence: 0.7,
	},
	{
		re:         regexp.MustCompile(`(?i)\b([A-Za-z][A-Za-z0-9_-]+)\s+uses?\s+([A-Za-z][A-Za-z0-9_-]+)\b`),
		predicate:  "uses",
		confidence: 0.6,
	},
}

// ExtractTriples finds relationship patterns in text, filtered against known entities.
func ExtractTriples(text string, entities []DetectedEntity, validFrom string) []ExtractedTriple {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	entityNames := make(map[string]bool)
	for _, e := range entities {
		entityNames[normalize(e.Name)] = true
	}

	type tripleKey struct{ s, p, o string }
	seen := make(map[tripleKey]int) // key -> index in result
	var result []ExtractedTriple

	for _, pat := range triplePatterns {
		for _, m := range pat.re.FindAllStringSubmatch(text, -1) {
			subject := strings.TrimSpace(m[1])
			object := strings.TrimSpace(m[2])

			// Filter: at least one side should be a known entity.
			subNorm := normalize(subject)
			objNorm := normalize(object)
			if !entityNames[subNorm] && !entityNames[objNorm] {
				continue
			}

			// Skip false positives.
			if isFalsePositive(subject) || isFalsePositive(object) {
				continue
			}

			key := tripleKey{subNorm, pat.predicate, objNorm}
			if idx, ok := seen[key]; ok {
				// Keep highest confidence.
				if pat.confidence > result[idx].Confidence {
					result[idx].Confidence = pat.confidence
				}
				continue
			}

			t := ExtractedTriple{
				Subject:    subject,
				Predicate:  pat.predicate,
				Object:     object,
				Confidence: pat.confidence,
			}
			if pat.flags != nil {
				t.Flags = append([]string{}, pat.flags...)
			}
			if pat.validFrom && validFrom != "" {
				t.ValidFrom = validFrom
			}

			seen[key] = len(result)
			result = append(result, t)
		}
	}

	return result
}
