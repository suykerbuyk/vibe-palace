// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"path/filepath"
	"strings"
)

// Hall type constants.
const (
	HallDecisions   = "decisions"
	HallDiscoveries = "discoveries"
	HallPreferences = "preferences"
	HallAdvice      = "advice"
	HallEvents      = "events"
	HallFacts       = "facts"
)

// DetectWing returns the wing slug for a given project and source path.
// Returns project if non-empty, otherwise derives from sourcePath's first
// directory component, falling back to "unknown".
func DetectWing(project, sourcePath string) string {
	if project != "" {
		return project
	}
	if sourcePath != "" {
		// Use first directory component as wing slug.
		clean := filepath.ToSlash(filepath.Clean(sourcePath))
		clean = strings.TrimPrefix(clean, "/")
		if idx := strings.Index(clean, "/"); idx > 0 {
			return clean[:idx]
		}
		return clean
	}
	return "unknown"
}

// roomEntry maps a room slug to its keywords, preserving order for
// deterministic first-match-wins.
type roomEntry struct {
	room     string
	keywords []string
}

// defaultRoomKeywords is the built-in room classification table.
// Checked in order; first keyword match wins.
// Keywords are language-agnostic — no single programming language is favored.
var defaultRoomKeywords = []roomEntry{
	{"testing", []string{"test", "assert", "coverage", "spec", "fixture", "mock"}},
	{"devops", []string{"deploy", "ci", "pipeline", "github actions", "docker", "kubernetes", "terraform", "ansible"}},
	{"api", []string{"api", "endpoint", "handler", "route", "http", "grpc", "restful", "graphql"}},
	{"data", []string{"database", "sql", "migration", "schema", "table", "query", "orm"}},
	{"config", []string{"config", "toml", "yaml", "environment", "settings", "dotenv"}},
	{"debugging", []string{"bug", "fix", "error", "crash", "stack trace", "traceback", "segfault", "core dump"}},
	{"refactoring", []string{"refactor", "rename", "cleanup", "reorganize", "restructure"}},
	{"architecture", []string{"design", "architecture", "pattern", "interface", "abstraction"}},
	{"performance", []string{"performance", "benchmark", "latency", "throughput", "profile", "optimization"}},
	{"security", []string{"security", "auth", "token", "permission", "credential", "vulnerability", "cve"}},
}

// filenameRule maps a filename pattern to a room.
type filenameRule struct {
	suffix   string // checked with HasSuffix
	prefix   string // checked with HasPrefix
	exact    string // checked with equality (case-insensitive)
	contains string // checked with Contains
	room     string
}

var filenameRules = []filenameRule{
	// Testing — language-agnostic test file patterns.
	{suffix: "_test.go", room: "testing"},    // Go
	{suffix: "_test.py", room: "testing"},    // Python (suffix convention)
	{prefix: "test_", room: "testing"},       // Python (prefix convention)
	{suffix: ".test.ts", room: "testing"},    // TypeScript
	{suffix: ".test.js", room: "testing"},    // JavaScript
	{suffix: ".test.tsx", room: "testing"},   // React/TSX
	{suffix: ".test.jsx", room: "testing"},   // React/JSX
	{suffix: ".spec.ts", room: "testing"},    // TypeScript (spec convention)
	{suffix: ".spec.js", room: "testing"},    // JavaScript (spec convention)
	{suffix: "test.java", room: "testing"},   // Java (FooTest.java)
	{suffix: "_test.rs", room: "testing"},    // Rust
	{suffix: "_test.rb", room: "testing"},    // Ruby
	{suffix: "_spec.rb", room: "testing"},    // Ruby (RSpec)
	{suffix: "_test.exs", room: "testing"},   // Elixir
	{suffix: "_test.dart", room: "testing"},  // Dart
	{contains: "conftest", room: "testing"},  // Python (pytest fixtures)
	// DevOps — CI/CD, containerization, IaC.
	{exact: "dockerfile", room: "devops"},
	{exact: "docker-compose.yml", room: "devops"},
	{exact: "docker-compose.yaml", room: "devops"},
	{exact: ".github", room: "devops"},
	{exact: ".gitlab-ci.yml", room: "devops"},
	{exact: ".circleci", room: "devops"},
	{exact: ".travis.yml", room: "devops"},
	{exact: "azure-pipelines.yml", room: "devops"},
	{exact: "jenkinsfile", room: "devops"},
	{exact: "makefile", room: "devops"},
	{exact: "cmakelists.txt", room: "devops"},
	{exact: "meson.build", room: "devops"},
	{exact: "configure.ac", room: "devops"},
	{exact: "configure.in", room: "devops"},
	// Config — language-agnostic config files.
	{exact: ".env", room: "config"},
	{exact: "config.toml", room: "config"},
	{exact: "config.yaml", room: "config"},
	{exact: "config.yml", room: "config"},
	{exact: "config.json", room: "config"},
	{exact: ".editorconfig", room: "config"},
	{exact: ".prettierrc", room: "config"},
	{exact: ".eslintrc.json", room: "config"},
	{exact: "tsconfig.json", room: "config"},
	{exact: "pyproject.toml", room: "config"},
	{exact: "tox.ini", room: "config"},
	{exact: "conanfile.txt", room: "config"},
	{exact: "conanfile.py", room: "config"},
	{exact: "vcpkg.json", room: "config"},
}

// DetectRoom inspects content, source path, and optional custom keywords to
// classify into a room. Uses a single 4-tier cascade (first match wins):
//  1. Custom keywords (if keywords != nil)
//  2. Filename match (if sourcePath != "")
//  3. Content keywords (built-in defaults)
//  4. Fallback → "general"
func DetectRoom(content, sourcePath string, keywords map[string][]string) string {
	lower := strings.ToLower(content)

	// Tier 1: custom keywords.
	for room, kws := range keywords {
		for _, kw := range kws {
			if strings.Contains(lower, strings.ToLower(kw)) {
				return room
			}
		}
	}

	// Tier 2: filename match.
	if sourcePath != "" {
		base := strings.ToLower(filepath.Base(sourcePath))
		for _, rule := range filenameRules {
			if rule.suffix != "" && strings.HasSuffix(base, rule.suffix) {
				return rule.room
			}
			if rule.prefix != "" && strings.HasPrefix(base, rule.prefix) {
				return rule.room
			}
			if rule.contains != "" && strings.Contains(base, rule.contains) {
				return rule.room
			}
			if rule.exact != "" && base == rule.exact {
				return rule.room
			}
		}
	}

	// Tier 3: content keywords (built-in defaults).
	for _, entry := range defaultRoomKeywords {
		for _, kw := range entry.keywords {
			if strings.Contains(lower, kw) {
				return entry.room
			}
		}
	}

	// Tier 4: fallback.
	return "general"
}

// hallEntry maps a hall type to its keywords.
type hallEntry struct {
	hall     string
	keywords []string
}

var defaultHallKeywords = []hallEntry{
	{HallDecisions, []string{"decided", "decision", "chose", "will use", "agreed", "we should"}},
	{HallDiscoveries, []string{"discovered", "found that", "realized", "til", "learned", "turns out"}},
	{HallPreferences, []string{"prefer", "preference", "like to", "rather", "style"}},
	{HallAdvice, []string{"should", "recommend", "advice", "best practice", "tip", "warning"}},
	{HallEvents, []string{"happened", "occurred", "event", "released", "shipped", "deployed"}},
}

// DetectHall classifies content into a memory-type hall.
// Returns one of the Hall* constants; defaults to HallFacts.
func DetectHall(content string) string {
	lower := strings.ToLower(content)
	for _, entry := range defaultHallKeywords {
		for _, kw := range entry.keywords {
			if strings.Contains(lower, kw) {
				return entry.hall
			}
		}
	}
	return HallFacts
}

// DefaultRoomKeywords returns a copy of the built-in room keyword map.
func DefaultRoomKeywords() map[string][]string {
	m := make(map[string][]string, len(defaultRoomKeywords))
	for _, entry := range defaultRoomKeywords {
		kws := make([]string, len(entry.keywords))
		copy(kws, entry.keywords)
		m[entry.room] = kws
	}
	return m
}
