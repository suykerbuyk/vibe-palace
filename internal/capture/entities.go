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
		// Skip dependency manifests and lock files across languages —
		// these are rarely meaningful entities in session transcripts.
		if dependencyFile(path) {
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

// dependencyFiles are manifest and lock files that produce false-positive
// entity matches. Language-agnostic: covers Go, Node, Python, Rust, Ruby,
// Java, PHP, .NET, Dart, Elixir, and Swift.
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

// dependencyFile reports whether path is a known dependency manifest or lock file.
func dependencyFile(path string) bool {
	base := strings.ToLower(path)
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	return dependencyFiles[base]
}
