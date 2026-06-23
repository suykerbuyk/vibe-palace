// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"log/slog"
	"sort"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// WeeklyEffectiveness summarizes context availability vs outcomes for one week.
type WeeklyEffectiveness struct {
	WeekStart       string  `json:"week_start"`
	SessionCount    int     `json:"session_count"`
	AvgOutcome      float64 `json:"avg_outcome"`
	WithContext     int     `json:"with_context"`
	WithoutContext  int     `json:"without_context"`
	AvgCtxOutcome   float64 `json:"avg_context_outcome"`
	AvgNoCtxOutcome float64 `json:"avg_no_context_outcome"`
}

// EffectivenessResult is the response for vp_get_effectiveness.
type EffectivenessResult struct {
	Project string                `json:"project"`
	Weeks   []WeeklyEffectiveness `json:"weeks"`
	Overall OverallEffectiveness  `json:"overall"`
}

// OverallEffectiveness aggregates across all weeks.
type OverallEffectiveness struct {
	TotalSessions   int     `json:"total_sessions"`
	AvgOutcome      float64 `json:"avg_outcome"`
	WithContext     int     `json:"with_context"`
	WithoutContext  int     `json:"without_context"`
	AvgCtxOutcome   float64 `json:"avg_context_outcome"`
	AvgNoCtxOutcome float64 `json:"avg_no_context_outcome"`
	Delta           float64 `json:"delta"`
}

// ComputeEffectiveness buckets sessions by ISO week (Monday key) and aggregates
// outcome (100 - FrictionScore) split by whether the session carried rich
// context (any decisions or files changed). The caller is responsible for
// loading and date-filtering the session slice; this function is pure.
func ComputeEffectiveness(project string, sessions []storage.SessionMeta) EffectivenessResult {
	// Group sessions by ISO week.
	type weekBucket struct {
		weekStart  string
		ctxSum     float64
		ctxCount   int
		noCtxSum   float64
		noCtxCount int
	}
	buckets := make(map[string]*weekBucket)

	for _, s := range sessions {
		t, err := time.Parse("2006-01-02", s.Date)
		if err != nil {
			slog.Warn("effectiveness: unparseable session date", "date", s.Date, "err", err)
			continue
		}
		// ISO week start (Monday).
		weekday := int(t.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		monday := t.AddDate(0, 0, -(weekday - 1))
		key := monday.Format("2006-01-02")

		b, ok := buckets[key]
		if !ok {
			b = &weekBucket{weekStart: key}
			buckets[key] = b
		}

		outcome := float64(100 - s.FrictionScore)
		hasContext := len(s.Decisions) > 0 || len(s.FilesChanged) > 0
		if hasContext {
			b.ctxSum += outcome
			b.ctxCount++
		} else {
			b.noCtxSum += outcome
			b.noCtxCount++
		}
	}

	// Build sorted weekly results.
	weekKeys := make([]string, 0, len(buckets))
	for k := range buckets {
		weekKeys = append(weekKeys, k)
	}
	sort.Strings(weekKeys)

	var weeklyResults []WeeklyEffectiveness
	var totalCtxSum, totalNoCtxSum float64
	var totalCtxCount, totalNoCtxCount int

	for _, k := range weekKeys {
		b := buckets[k]
		total := b.ctxCount + b.noCtxCount
		we := WeeklyEffectiveness{
			WeekStart:      b.weekStart,
			SessionCount:   total,
			WithContext:    b.ctxCount,
			WithoutContext: b.noCtxCount,
		}
		if total > 0 {
			we.AvgOutcome = roundTo((b.ctxSum+b.noCtxSum)/float64(total), 1)
		}
		if b.ctxCount > 0 {
			we.AvgCtxOutcome = roundTo(b.ctxSum/float64(b.ctxCount), 1)
		}
		if b.noCtxCount > 0 {
			we.AvgNoCtxOutcome = roundTo(b.noCtxSum/float64(b.noCtxCount), 1)
		}
		weeklyResults = append(weeklyResults, we)

		totalCtxSum += b.ctxSum
		totalCtxCount += b.ctxCount
		totalNoCtxSum += b.noCtxSum
		totalNoCtxCount += b.noCtxCount
	}

	overall := OverallEffectiveness{
		TotalSessions:  totalCtxCount + totalNoCtxCount,
		WithContext:    totalCtxCount,
		WithoutContext: totalNoCtxCount,
	}
	if overall.TotalSessions > 0 {
		overall.AvgOutcome = roundTo((totalCtxSum+totalNoCtxSum)/float64(overall.TotalSessions), 1)
	}
	if totalCtxCount > 0 {
		overall.AvgCtxOutcome = roundTo(totalCtxSum/float64(totalCtxCount), 1)
	}
	if totalNoCtxCount > 0 {
		overall.AvgNoCtxOutcome = roundTo(totalNoCtxSum/float64(totalNoCtxCount), 1)
	}
	overall.Delta = roundTo(overall.AvgCtxOutcome-overall.AvgNoCtxOutcome, 1)

	return EffectivenessResult{
		Project: project,
		Weeks:   weeklyResults,
		Overall: overall,
	}
}
