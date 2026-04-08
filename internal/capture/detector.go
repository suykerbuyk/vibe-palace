// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"strings"
)

// Format represents a detected transcript format.
type Format int

const (
	// FormatPlainText is unstructured text (default).
	FormatPlainText Format = iota
	// FormatJSONRPC is JSON with "role" fields (chat completion format).
	FormatJSONRPC
	// FormatMarkdown is markdown with turn markers (## Human / ## Assistant).
	FormatMarkdown
)

// DetectFormat inspects the first ~500 bytes of text to determine its format.
func DetectFormat(text string) Format {
	sample := text
	if len(sample) > 500 {
		sample = sample[:500]
	}
	sample = strings.TrimSpace(sample)

	// JSON-RPC / chat format: starts with [ or { and contains "role".
	if len(sample) > 0 && (sample[0] == '[' || sample[0] == '{') {
		if strings.Contains(sample, `"role"`) {
			return FormatJSONRPC
		}
	}

	// Markdown transcript: contains turn markers.
	lower := strings.ToLower(sample)
	markdownMarkers := []string{
		"## human", "## assistant", "## user",
		"### human", "### assistant", "### user",
		"**human:**", "**assistant:**", "**user:**",
	}
	for _, m := range markdownMarkers {
		if strings.Contains(lower, m) {
			return FormatMarkdown
		}
	}

	return FormatPlainText
}

// DetectWing returns the wing slug for a given project.
// Currently the wing defaults to the project slug.
func DetectWing(project string) string {
	return project
}

// roomRule maps a set of keywords to a room slug.
type roomRule struct {
	keywords []string
	room     string
}

// roomRules are checked in order; first match wins.
var roomRules = []roomRule{
	{[]string{"test", "_test.go", "assert", "coverage", "benchmark"}, "testing"},
	{[]string{"deploy", "ci", "pipeline", "github actions", "docker", "kubernetes"}, "devops"},
	{[]string{"api", "endpoint", "handler", "route", "http", "grpc"}, "api"},
	{[]string{"database", "sql", "migration", "schema", "table", "query"}, "data"},
	{[]string{"config", "toml", "yaml", "environment", "settings", "dotenv"}, "config"},
	{[]string{"bug", "fix", "error", "panic", "crash", "stack trace"}, "debugging"},
	{[]string{"refactor", "rename", "cleanup", "reorganize", "restructure"}, "refactoring"},
	{[]string{"design", "architecture", "pattern", "interface", "abstraction"}, "architecture"},
	{[]string{"performance", "benchmark", "latency", "throughput", "profile"}, "performance"},
	{[]string{"security", "auth", "token", "permission", "credential"}, "security"},
}

// DetectRoom inspects chunk content and returns a room slug.
// Uses keyword heuristics with first-match-wins ordering.
func DetectRoom(content string) string {
	lower := strings.ToLower(content)
	for _, rule := range roomRules {
		for _, kw := range rule.keywords {
			if strings.Contains(lower, kw) {
				return rule.room
			}
		}
	}
	return "general"
}

// hallRule maps keywords to a hall (memory type).
type hallRule struct {
	keywords []string
	hall     string
}

// hallRules are checked in order; first match wins.
var hallRules = []hallRule{
	{[]string{"decided", "decision", "chose", "will use", "agreed", "we should"}, "decisions"},
	{[]string{"discovered", "found that", "realized", "til", "learned", "turns out"}, "discoveries"},
	{[]string{"prefer", "preference", "like to", "rather", "style"}, "preferences"},
	{[]string{"should", "recommend", "advice", "best practice", "tip", "warning"}, "advice"},
	{[]string{"happened", "occurred", "event", "released", "shipped", "deployed"}, "events"},
}

// DetectHall classifies a chunk into a memory-type hall.
// Returns one of: decisions, discoveries, preferences, advice, events, facts.
func DetectHall(content string) string {
	lower := strings.ToLower(content)
	for _, rule := range hallRules {
		for _, kw := range rule.keywords {
			if strings.Contains(lower, kw) {
				return rule.hall
			}
		}
	}
	return "facts"
}
