// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package kg

import (
	"regexp"
	"strings"
)

// ExtractAllResult is the flat, deduplicated product of a single pass over a
// transcript. Consumers iterate Entities once to populate a KG store, and
// iterate Triples once to record relationships. No extractor pass is exposed;
// callers see a unified, deduped view keyed by (Type, Name) for entities and
// (Subject, Predicate, Object) for triples.
type ExtractAllResult struct {
	Entities []ExtractedEntityRef
	Triples  []ExtractedTriple
}

// ExtractedEntityRef is a single deduplicated entity ready for KG ingest.
// Source identifies which extractor originally produced it (for log context
// when a write fails); it is purely informational.
type ExtractedEntityRef struct {
	Source       string  // "file", "url", "kg-detector" — origin extractor for log context
	Name         string  // canonical display name (original casing preserved)
	Type         string  // storage entity type: "file", "url", "person", "project", "concept", "tool"
	Confidence   float64 // 0.0–1.0; 1.0 for regex-certain matches (files, urls)
	MentionCount int     // number of times the underlying pattern matched in text
}

// ExtractAllOptions tunes the unified extraction pass.
type ExtractAllOptions struct {
	// MinMentions is the frequency-of-mention threshold. Entities with
	// MentionCount < MinMentions are dropped. Default (zero value) is
	// treated as 2 by ExtractAll — see DefaultMinMentions.
	//
	// Rationale for a default of 2: a one-off token (frequent transcript
	// typos, passing references, auto-complete stubs) should not graduate
	// to a permanent KG node. Requiring a second mention is the cheapest
	// signal that a speaker/writer actually engaged with the concept.
	MinMentions int
}

// DefaultMinMentions is the default frequency-of-mention threshold for
// promoting an extracted entity into the permanent KG. Two mentions is the
// minimum that distinguishes intentional reference from accidental token.
const DefaultMinMentions = 2

// Capture-layer regex patterns, re-homed here so ExtractAll owns orchestration
// end-to-end. These are intentionally verbatim copies of the originals in
// internal/capture/entities.go; the goal of this refactor is to dedupe
// orchestration, not retune detection. The capture package delegates to
// ExtractFilesAndURLs below to preserve its public API shape for tests.
var (
	filePathRe = regexp.MustCompile(`(?:^|[^a-zA-Z0-9_/.-])([a-zA-Z_./][a-zA-Z0-9_/.~-]*\.(?:go|ts|tsx|js|jsx|py|rs|c|cpp|cc|cxx|h|hpp|hxx|java|kt|rb|lua|zig|nim|toml|yaml|yml|json|md|sh|sql|proto|css|html))(?:[^a-zA-Z0-9_/-]|$)`)
	urlRe      = regexp.MustCompile(`https?://[^\s)"'>` + "`" + `]+`)
)

// dependencyFiles mirrors the capture-package set; re-homed here to keep
// the file/url extractor self-contained.
var dependencyFiles = map[string]bool{
	"go.mod": true, "go.sum": true,
	"package.json": true, "package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"requirements.txt": true, "pipfile": true, "pipfile.lock": true, "poetry.lock": true,
	"cargo.toml": true, "cargo.lock": true,
	"gemfile": true, "gemfile.lock": true,
	"pom.xml": true, "build.gradle": true, "build.gradle.kts": true,
	"composer.json": true, "composer.lock": true,
	"pubspec.yaml": true, "pubspec.lock": true,
	"mix.exs": true, "mix.lock": true,
	"package.swift": true, "package.resolved": true,
	"nuget.config": true, "packages.config": true,
	"conanfile.txt": true, "conanfile.py": true, "conan.lock": true,
	"vcpkg.json": true,
}

func dependencyFile(path string) bool {
	base := strings.ToLower(path)
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	return dependencyFiles[base]
}

// FileOrURL is a simple bag returned by ExtractFilesAndURLs.
type FileOrURL struct {
	Name string
	Type string // "file" or "url"
}

// ExtractFilesAndURLs runs the regex extractors for file paths and URLs.
// Returns deduplicated (Name,Type) pairs. The capture package delegates here
// so the regexes live in exactly one place.
func ExtractFilesAndURLs(text string) []FileOrURL {
	seen := make(map[string]bool)
	var out []FileOrURL

	add := func(name, typ string) {
		key := typ + ":" + name
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, FileOrURL{Name: name, Type: typ})
	}

	for _, m := range filePathRe.FindAllStringSubmatch(text, -1) {
		path := m[1]
		if len(path) < 5 {
			continue
		}
		if dependencyFile(path) {
			continue
		}
		add(path, "file")
	}

	for _, m := range urlRe.FindAllString(text, -1) {
		m = strings.TrimRight(m, ".,;:!?")
		add(m, "url")
	}

	return out
}

// ExtractAll performs a single, deduplicated extraction pass over text.
//
// It runs all three entity/relationship extractors (file+URL regexes,
// DetectEntities, ExtractTriples), merges their entity outputs across
// passes (so a file path found by two extractors yields ONE result), and
// applies a frequency-of-mention filter so that singletons do not graduate
// to permanent KG nodes.
//
// validFrom is forwarded to ExtractTriples for triples that carry a
// temporal bound (e.g. "X joined Y" → member_of with ValidFrom=today).
// Pass "" if no temporal bound is desired.
func ExtractAll(text, validFrom string, opts ExtractAllOptions) ExtractAllResult {
	if strings.TrimSpace(text) == "" {
		return ExtractAllResult{}
	}
	minMentions := opts.MinMentions
	if minMentions <= 0 {
		minMentions = DefaultMinMentions
	}

	// Dedup key: (type, lowercased name). Case-sensitive names with the same
	// lower-form collapse to one result (first writer wins on display casing).
	type entKey struct{ typ, name string }
	bucket := make(map[entKey]*ExtractedEntityRef)

	upsert := func(source, name, typ string, confidence float64, mentions int) {
		k := entKey{typ: typ, name: strings.ToLower(name)}
		if cur, ok := bucket[k]; ok {
			// Prefer the higher-confidence source label; sum mention counts so
			// an entity seen by two extractors accumulates evidence.
			if confidence > cur.Confidence {
				cur.Confidence = confidence
				cur.Source = source
			}
			cur.MentionCount += mentions
			return
		}
		bucket[k] = &ExtractedEntityRef{
			Source:       source,
			Name:         name,
			Type:         typ,
			Confidence:   confidence,
			MentionCount: mentions,
		}
	}

	// Pass 1: file + URL regex extractors.
	for _, fu := range ExtractFilesAndURLs(text) {
		mentions := countMentions(text, fu.Name)
		// Regex extractors are treated as high-confidence (1.0): a regex
		// match is deterministic evidence.
		upsert("file-url-regex", fu.Name, fu.Type, 1.0, mentions)
	}

	// Pass 2: person/project/concept/tool detector.
	detected := DetectEntities(text)
	for _, d := range detected {
		mentions := countMentions(text, d.Name)
		upsert("kg-detector", d.Name, string(d.Type), d.Confidence, mentions)
	}

	// Apply frequency filter and flatten.
	entities := make([]ExtractedEntityRef, 0, len(bucket))
	for _, e := range bucket {
		if e.MentionCount < minMentions {
			continue
		}
		entities = append(entities, *e)
	}

	// Pass 3: triples (relationships). Triples have their own dedup inside
	// ExtractTriples and are filtered against the *full* detected-entity set
	// (not the frequency-filtered set) so that a relationship anchored on a
	// low-frequency mention still surfaces — triples carry their own
	// confidence signal via pattern strength.
	triples := ExtractTriples(text, detected, validFrom)

	return ExtractAllResult{
		Entities: entities,
		Triples:  triples,
	}
}

// countMentions counts non-overlapping occurrences of name in text
// (case-insensitive). Used to build MentionCount for the frequency filter.
func countMentions(text, name string) int {
	if name == "" {
		return 0
	}
	return strings.Count(strings.ToLower(text), strings.ToLower(name))
}
