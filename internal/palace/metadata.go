// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
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

// keyword holds a keyword string and an optional pre-compiled regex for
// word-boundary matching. Single-word keywords use a leading \b to prevent
// substring false positives while preserving stem matching. Multi-word
// phrases use strings.Contains (spaces provide natural boundaries).
type keyword struct {
	raw    string
	re     *regexp.Regexp // nil for multi-word phrases
	weight float64
}

// buildKeyword creates a keyword with an optional pre-compiled leading word
// boundary regex. No (?i) flag — callers lowercase content before matching.
// Default weight is 1.0.
func buildKeyword(s string) keyword {
	if strings.Contains(s, " ") {
		return keyword{raw: s, weight: 1.0}
	}
	return keyword{raw: s, re: regexp.MustCompile(`\b` + regexp.QuoteMeta(s)), weight: 1.0}
}

// buildWeightedKeyword creates a keyword with a specific weight.
func buildWeightedKeyword(s string, w float64) keyword {
	kw := buildKeyword(s)
	kw.weight = w
	return kw
}

// wkws builds a []keyword slice where all words share the same weight.
func wkws(weight float64, words ...string) []keyword {
	out := make([]keyword, len(words))
	for i, w := range words {
		out[i] = buildWeightedKeyword(w, weight)
	}
	return out
}

// mergeKws concatenates multiple keyword slices into one.
func mergeKws(slices ...[]keyword) []keyword {
	var out []keyword
	for _, s := range slices {
		out = append(out, s...)
	}
	return out
}

func (kw keyword) matches(lower string) bool {
	if kw.re != nil {
		return kw.re.MatchString(lower)
	}
	return strings.Contains(lower, kw.raw)
}

// kws is a helper to build a []keyword slice from raw strings.
func kws(words ...string) []keyword {
	out := make([]keyword, len(words))
	for i, w := range words {
		out[i] = buildKeyword(w)
	}
	return out
}

// roomEntry maps a room slug to its keywords, preserving order for
// deterministic first-match-wins.
type roomEntry struct {
	room     string
	keywords []keyword
}

// minRoomScore is the minimum accumulated weight for a room to classify.
// A single medium-weight (0.6) keyword classifies; a single low-weight (0.3)
// keyword does not. Two low-weight hits from the same room (0.6) do classify.
const minRoomScore = 0.6

// scoreAllRooms accumulates weighted keyword scores for every room.
// Each keyword contributes at most once (presence, not count).
func scoreAllRooms(lower string, entries []roomEntry) map[string]float64 {
	scores := make(map[string]float64, len(entries))
	for _, entry := range entries {
		var score float64
		for _, kw := range entry.keywords {
			if kw.matches(lower) {
				score += kw.weight
			}
		}
		scores[entry.room] = score
	}
	return scores
}

// scoreRooms returns the best room and its score. Ties break by room table
// order (first room with the highest score wins, via strict > comparison).
func scoreRooms(lower string, entries []roomEntry) (string, float64) {
	scores := scoreAllRooms(lower, entries)
	var best string
	var bestScore float64
	for _, entry := range entries {
		if s := scores[entry.room]; s > bestScore {
			bestScore = s
			best = entry.room
		}
	}
	return best, bestScore
}

// defaultRoomKeywords is the built-in room classification table.
// Uses weighted scoring: high (1.0) = domain-specific, medium (0.6) =
// moderately specific, low (0.3) = common/ambiguous.
// Multi-word phrases automatically get 1.0 via buildKeyword.
// Keywords are language-agnostic — no single programming language is favored.
var defaultRoomKeywords = []roomEntry{
	{"testing", mergeKws(
		wkws(1.0, "test spec", "fixture"),
		wkws(0.6, "assert", "coverage", "mock"),
		wkws(0.3, "test"),
	)},
	{"devops", mergeKws(
		wkws(1.0, "kubernetes", "terraform", "ansible", "github actions"),
		wkws(0.6, "deploy", "ci/cd", "pipeline", "docker"),
	)},
	{"api", mergeKws(
		wkws(1.0, "grpc", "graphql", "restful", "endpoint"),
		wkws(0.6, "api", "handler", "route"),
	)},
	{"data", mergeKws(
		wkws(1.0, "sql", "orm", "database table", "migration"),
		wkws(0.6, "database", "schema", "query"),
	)},
	{"config", mergeKws(
		wkws(1.0, "toml", "yaml", "dotenv", "environment"),
		wkws(0.6, "config", "settings"),
	)},
	{"debugging", mergeKws(
		wkws(1.0, "segfault", "core dump", "stack trace", "traceback"),
		wkws(0.6, "crash", "bug"),
		wkws(0.3, "fix", "error"),
	)},
	{"refactoring", mergeKws(
		wkws(1.0, "reorganize", "restructure"),
		wkws(0.6, "refactor", "rename", "cleanup"),
	)},
	{"architecture", mergeKws(
		wkws(1.0, "system design", "software design", "design pattern", "interface design", "abstraction layer"),
		wkws(0.6, "architecture", "abstraction"),
	)},
	{"performance", mergeKws(
		wkws(1.0, "cpu profile", "memory profile", "latency", "throughput", "profiling"),
		wkws(0.6, "benchmark", "performance", "optimization"),
	)},
	{"security", mergeKws(
		wkws(1.0, "vulnerability", "cve", "auth token", "access token", "credential"),
		wkws(0.6, "security", "permission"),
		wkws(0.3, "auth"),
	)},
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

// WeightedOverride holds keyword overrides at three weight tiers for a room.
type WeightedOverride struct {
	High   []string // weight 1.0
	Medium []string // weight 0.6
	Low    []string // weight 0.3
}

// RoomClassifier holds a merged room keyword table and scoring threshold.
// Use NewRoomClassifier to create one with custom overrides; the zero value
// uses compiled defaults.
type RoomClassifier struct {
	entries  []roomEntry
	minScore float64
}

// NewRoomClassifier creates a classifier by deep-copying the built-in keyword
// table and merging overrides. For existing rooms, override keywords are
// appended. New rooms are added at the end (preserving tie-break order for
// existing rooms). If minScore <= 0, the compiled default (0.6) is used.
func NewRoomClassifier(overrides map[string]WeightedOverride, minScore float64) *RoomClassifier {
	if minScore <= 0 {
		minScore = minRoomScore
	}

	// Deep-copy defaults.
	entries := make([]roomEntry, len(defaultRoomKeywords))
	for i, e := range defaultRoomKeywords {
		kws := make([]keyword, len(e.keywords))
		copy(kws, e.keywords)
		entries[i] = roomEntry{room: e.room, keywords: kws}
	}

	// Merge overrides.
	for room, ov := range overrides {
		extra := buildOverrideKeywords(ov)
		if len(extra) == 0 {
			continue
		}
		found := false
		for i := range entries {
			if entries[i].room == room {
				entries[i].keywords = append(entries[i].keywords, extra...)
				found = true
				break
			}
		}
		if !found {
			entries = append(entries, roomEntry{room: room, keywords: extra})
		}
	}

	return &RoomClassifier{entries: entries, minScore: minScore}
}

// BuildClassifierFromConfig creates a RoomClassifier from a storage.Config.
// This is the canonical conversion from config to classifier, eliminating
// the repeated ScoringRoomOverride → WeightedOverride boilerplate.
func BuildClassifierFromConfig(cfg storage.Config) *RoomClassifier {
	var overrides map[string]WeightedOverride
	if len(cfg.PalaceScoringOverrides) > 0 {
		overrides = make(map[string]WeightedOverride, len(cfg.PalaceScoringOverrides))
		for room, ov := range cfg.PalaceScoringOverrides {
			overrides[room] = WeightedOverride{
				High:   ov.High,
				Medium: ov.Medium,
				Low:    ov.Low,
			}
		}
	}
	return NewRoomClassifier(overrides, cfg.PalaceMinScore)
}

// buildOverrideKeywords converts a WeightedOverride into keyword structs.
func buildOverrideKeywords(ov WeightedOverride) []keyword {
	var out []keyword
	for _, s := range ov.High {
		out = append(out, buildWeightedKeyword(s, 1.0))
	}
	for _, s := range ov.Medium {
		out = append(out, buildWeightedKeyword(s, 0.6))
	}
	for _, s := range ov.Low {
		out = append(out, buildWeightedKeyword(s, 0.3))
	}
	return out
}

// Classify inspects content, source path, and optional custom keywords to
// classify into a room. Uses a single 4-tier cascade (first match wins):
//  1. Custom keywords (if keywords != nil, sorted by room for determinism)
//  2. Filename match (if sourcePath != "")
//  3. Content keywords (merged defaults + overrides)
//  4. Fallback → "general"
func (rc *RoomClassifier) Classify(content, sourcePath string, keywords map[string][]string) string {
	lower := strings.ToLower(content)

	// Tier 1: custom keywords — sorted for deterministic ordering.
	if len(keywords) > 0 {
		rooms := make([]string, 0, len(keywords))
		for room := range keywords {
			rooms = append(rooms, room)
		}
		sort.Strings(rooms)
		for _, room := range rooms {
			for _, kw := range keywords[room] {
				if buildKeyword(strings.ToLower(kw)).matches(lower) {
					return room
				}
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

	// Tier 3: weighted content scoring.
	entries := rc.entries
	threshold := rc.minScore
	if len(entries) == 0 {
		entries = defaultRoomKeywords
		threshold = minRoomScore
	}
	if best, score := scoreRooms(lower, entries); score >= threshold {
		return best
	}

	// Tier 4: fallback.
	return "general"
}

// defaultClassifier is the package-level classifier using compiled defaults.
var defaultClassifier = NewRoomClassifier(nil, 0)

// DetectRoom inspects content, source path, and optional custom keywords to
// classify into a room. Delegates to the default RoomClassifier.
func DetectRoom(content, sourcePath string, keywords map[string][]string) string {
	return defaultClassifier.Classify(content, sourcePath, keywords)
}

// hallEntry maps a hall type to its keywords.
type hallEntry struct {
	hall     string
	keywords []keyword
}

var defaultHallKeywords = []hallEntry{
	{HallDecisions, kws("decided", "decision", "chose", "will use", "agreed", "we should")},
	{HallDiscoveries, kws("discovered", "found that", "realized", "til", "learned", "turns out")},
	{HallPreferences, kws("prefer", "preference", "like to", "rather", "style")},
	{HallAdvice, kws("should", "recommend", "advice", "best practice", "tip", "warning")},
	{HallEvents, kws("happened", "occurred", "release event", "launch event", "released", "shipped", "deployed")},
}

// DetectHall classifies content into a memory-type hall.
// Returns one of the Hall* constants; defaults to HallFacts.
func DetectHall(content string) string {
	lower := strings.ToLower(content)
	for _, entry := range defaultHallKeywords {
		for _, kw := range entry.keywords {
			if kw.matches(lower) {
				return entry.hall
			}
		}
	}
	return HallFacts
}

// ClassifyResult holds the full scoring breakdown for a drawer classification.
type ClassifyResult struct {
	Room       string             `json:"room"`
	Score      float64            `json:"score"`
	Scores     map[string]float64 `json:"scores"`
	MinScore   float64            `json:"min_score"`
	Tier       string             `json:"tier"`       // "custom-keyword", "filename", "content", "fallback"
	Borderline bool               `json:"borderline"` // true only when Tier=="content" and score within 0.2 of MinScore
}

// ClassifyWithScores performs classification like Classify but returns the
// full scoring breakdown for audit purposes. Content scores (tier 3) are
// always computed regardless of which tier wins the classification.
func (rc *RoomClassifier) ClassifyWithScores(content, sourcePath string, keywords map[string][]string) ClassifyResult {
	lower := strings.ToLower(content)

	entries := rc.entries
	threshold := rc.minScore
	if len(entries) == 0 {
		entries = defaultRoomKeywords
		threshold = minRoomScore
	}

	// Always compute tier-3 content scores for audit visibility.
	allScores := scoreAllRooms(lower, entries)
	bestRoom, bestScore := scoreRooms(lower, entries)

	// Tier 1: custom keywords.
	if len(keywords) > 0 {
		rooms := make([]string, 0, len(keywords))
		for room := range keywords {
			rooms = append(rooms, room)
		}
		sort.Strings(rooms)
		for _, room := range rooms {
			for _, kw := range keywords[room] {
				if buildKeyword(strings.ToLower(kw)).matches(lower) {
					return ClassifyResult{
						Room:     room,
						Score:    bestScore,
						Scores:   allScores,
						MinScore: threshold,
						Tier:     "custom-keyword",
					}
				}
			}
		}
	}

	// Tier 2: filename match.
	if sourcePath != "" {
		base := strings.ToLower(filepath.Base(sourcePath))
		for _, rule := range filenameRules {
			matched := (rule.suffix != "" && strings.HasSuffix(base, rule.suffix)) ||
				(rule.prefix != "" && strings.HasPrefix(base, rule.prefix)) ||
				(rule.contains != "" && strings.Contains(base, rule.contains)) ||
				(rule.exact != "" && base == rule.exact)
			if matched {
				return ClassifyResult{
					Room:     rule.room,
					Score:    allScores[rule.room],
					Scores:   allScores,
					MinScore: threshold,
					Tier:     "filename",
				}
			}
		}
	}

	// Tier 3: weighted content scoring.
	if bestScore >= threshold {
		borderline := bestScore < threshold+0.2
		return ClassifyResult{
			Room:       bestRoom,
			Score:      bestScore,
			Scores:     allScores,
			MinScore:   threshold,
			Tier:       "content",
			Borderline: borderline,
		}
	}

	// Tier 4: fallback.
	return ClassifyResult{
		Room:     "general",
		Score:    0,
		Scores:   allScores,
		MinScore: threshold,
		Tier:     "fallback",
	}
}

// RoomKeywordReport describes keyword firing for one room.
type RoomKeywordReport struct {
	Room  string   `json:"room"`
	Fired []string `json:"fired"`
	Dead  []string `json:"dead"`
}

// KeywordCoverage returns per-room keyword firing analysis for the given content.
// Callers should join multiple drawer contents with "\n\n" separators to reduce
// false positives from multi-word phrases spanning content boundaries.
func (rc *RoomClassifier) KeywordCoverage(content string) []RoomKeywordReport {
	lower := strings.ToLower(content)
	entries := rc.entries
	if len(entries) == 0 {
		entries = defaultRoomKeywords
	}

	reports := make([]RoomKeywordReport, 0, len(entries))
	for _, entry := range entries {
		r := RoomKeywordReport{Room: entry.room}
		for _, kw := range entry.keywords {
			if kw.matches(lower) {
				r.Fired = append(r.Fired, kw.raw)
			} else {
				r.Dead = append(r.Dead, kw.raw)
			}
		}
		reports = append(reports, r)
	}
	return reports
}

// RoomDefinition describes a room for LLM prompt construction.
type RoomDefinition struct {
	Name     string   `json:"name"`
	Keywords []string `json:"keywords"`
}

// RoomDefinitions returns a summary of all rooms and their keywords.
// Includes both built-in and override rooms.
func (rc *RoomClassifier) RoomDefinitions() []RoomDefinition {
	entries := rc.entries
	if len(entries) == 0 {
		entries = defaultRoomKeywords
	}
	defs := make([]RoomDefinition, 0, len(entries))
	for _, entry := range entries {
		kws := make([]string, len(entry.keywords))
		for i, kw := range entry.keywords {
			kws[i] = kw.raw
		}
		defs = append(defs, RoomDefinition{Name: entry.room, Keywords: kws})
	}
	return defs
}

// KeywordMatch describes a keyword that matched content.
type KeywordMatch struct {
	Raw    string
	Weight float64
}

// MatchingKeywords returns keywords from the specified room that fire on content.
// Used by the tuning engine to identify which keywords contribute to classification.
func (rc *RoomClassifier) MatchingKeywords(content, room string) []KeywordMatch {
	lower := strings.ToLower(content)
	entries := rc.entries
	if len(entries) == 0 {
		entries = defaultRoomKeywords
	}
	for _, entry := range entries {
		if entry.room != room {
			continue
		}
		var matches []KeywordMatch
		for _, kw := range entry.keywords {
			if kw.matches(lower) {
				matches = append(matches, KeywordMatch{Raw: kw.raw, Weight: kw.weight})
			}
		}
		return matches
	}
	return nil
}

// DefaultRoomKeywords returns a copy of the built-in room keyword map.
func DefaultRoomKeywords() map[string][]string {
	m := make(map[string][]string, len(defaultRoomKeywords))
	for _, entry := range defaultRoomKeywords {
		raw := make([]string, len(entry.keywords))
		for i, kw := range entry.keywords {
			raw[i] = kw.raw
		}
		m[entry.room] = raw
	}
	return m
}
