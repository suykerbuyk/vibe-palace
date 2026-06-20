// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// WeeklyMetric aggregates friction data for one calendar week.
type WeeklyMetric struct {
	WeekStart    string  `json:"week_start"` // Monday of the week, YYYY-MM-DD
	SessionCount int     `json:"session_count"`
	AvgFriction  float64 `json:"avg_friction"`
	MaxFriction  int     `json:"max_friction"`
}

// AnalyzeFriction scores a transcript for friction on a 0-100 scale.
// Four signals are detected, each contributing a sub-score capped at 25.
func AnalyzeFriction(transcript string) (int, error) {
	trimmed := strings.TrimSpace(transcript)
	if trimmed == "" {
		return 0, nil
	}

	lower := strings.ToLower(trimmed)
	tokens := countTokens(trimmed)

	corrections := countCorrections(lower)
	retries := countRetries(lower)
	errorDensity := countErrorDensity(lower, tokens)
	rework := countReworkSignals(lower)

	score := corrections + retries + errorDensity + rework
	if score > 100 {
		score = 100
	}
	return score, nil
}

// correctionPhrases are multi-word phrases checked first (order matters).
var correctionPhrases = []string{
	"no not that",
	"no, not that",
}

// correctionWords are single-word correction indicators.
var correctionWords = []string{
	"wrong",
	"undo",
	"revert",
}

// countCorrections counts correction signals in the lowercased transcript.
func countCorrections(lower string) int {
	count := 0
	remaining := lower

	// Count multi-word phrases first to avoid double-counting.
	for _, phrase := range correctionPhrases {
		n := strings.Count(remaining, phrase)
		count += n
		if n > 0 {
			remaining = strings.ReplaceAll(remaining, phrase, "")
		}
	}

	for _, word := range correctionWords {
		count += strings.Count(remaining, word)
	}

	sub := count * 8
	if sub > 25 {
		sub = 25
	}
	return sub
}

// toolCallPattern matches common transcript patterns for tool invocations.
var toolCallPattern = regexp.MustCompile(`(?im)(?:tool_use|calling tool|tool_name|tool_call)[:\s]+["']?(\w+)`)

// countRetries detects repeated tool calls (same tool 3+ times).
func countRetries(lower string) int {
	matches := toolCallPattern.FindAllStringSubmatch(lower, -1)
	if len(matches) == 0 {
		return 0
	}

	freq := make(map[string]int)
	for _, m := range matches {
		freq[m[1]]++
	}

	groups := 0
	for _, count := range freq {
		if count >= 3 {
			groups++
		}
	}

	sub := groups * 10
	if sub > 25 {
		sub = 25
	}
	return sub
}

// errorKeywords are language-agnostic terms indicating errors in transcripts.
var errorKeywords = []string{
	"error",
	"exception",
	"failed",
	"failure",
	"traceback",
	"stack trace",
	"segfault",
	"core dump",
	"unhandled",
	"fatal",
}

// countErrorDensity measures error keyword density per 1000 tokens.
func countErrorDensity(lower string, tokenCount int) int {
	if tokenCount == 0 {
		return 0
	}

	count := 0
	for _, kw := range errorKeywords {
		count += strings.Count(lower, kw)
	}

	density := (float64(count) / float64(tokenCount)) * 1000
	sub := int(density * 5)
	if sub > 25 {
		sub = 25
	}
	return sub
}

// reworkPhrases are multi-word signals of rework.
var reworkPhrases = []string{
	"go back",
	"start over",
	"try again",
	"let's redo",
	"scratch that",
	"never mind",
	"nevermind",
}

// countReworkSignals counts rework indicators in the lowercased transcript.
func countReworkSignals(lower string) int {
	count := 0
	for _, phrase := range reworkPhrases {
		count += strings.Count(lower, phrase)
	}

	sub := count * 10
	if sub > 25 {
		sub = 25
	}
	return sub
}

// countTokens returns an approximate token count by splitting on whitespace.
func countTokens(text string) int {
	return len(strings.Fields(text))
}

// GetFrictionTrends returns weekly friction aggregations for a project.
// The weeks parameter controls how many weeks back to look (default 8, max 52).
func GetFrictionTrends(vault *storage.Vault, project string, weeks int) ([]WeeklyMetric, error) {
	if weeks <= 0 {
		weeks = 8
	}
	if weeks > 52 {
		weeks = 52
	}

	now := time.Now().UTC()
	// Find the Monday of the current week.
	weekday := now.Weekday()
	if weekday == time.Sunday {
		weekday = 7
	}
	monday := now.AddDate(0, 0, -int(weekday-time.Monday))
	dateFrom := monday.AddDate(0, 0, -7*(weeks-1)).Format("2006-01-02")

	sessions, err := vault.ListSessions(project, dateFrom, "", 0)
	if err != nil {
		return nil, err
	}

	if len(sessions) == 0 {
		return []WeeklyMetric{}, nil
	}

	// Group sessions by ISO week.
	type bucket struct {
		weekStart string
		scores    []int
		max       int
	}
	buckets := make(map[string]*bucket)

	for _, s := range sessions {
		t, err := time.Parse("2006-01-02", s.Date)
		if err != nil {
			slog.Warn("friction: unparseable session date", "date", s.Date, "err", err)
			continue
		}

		// Compute the Monday of this session's week.
		wd := t.Weekday()
		if wd == time.Sunday {
			wd = 7
		}
		mon := t.AddDate(0, 0, -int(wd-time.Monday))
		key := mon.Format("2006-01-02")

		b, ok := buckets[key]
		if !ok {
			b = &bucket{weekStart: key}
			buckets[key] = b
		}
		b.scores = append(b.scores, s.FrictionScore)
		if s.FrictionScore > b.max {
			b.max = s.FrictionScore
		}
	}

	result := make([]WeeklyMetric, 0, len(buckets))
	for _, b := range buckets {
		sum := 0
		for _, s := range b.scores {
			sum += s
		}
		avg := float64(sum) / float64(len(b.scores))
		result = append(result, WeeklyMetric{
			WeekStart:    b.weekStart,
			SessionCount: len(b.scores),
			AvgFriction:  avg,
			MaxFriction:  b.max,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].WeekStart < result[j].WeekStart
	})

	return result, nil
}
