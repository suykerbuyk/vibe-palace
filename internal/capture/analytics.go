// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// FrictionWindow is the rolling-window friction average over the last Days days.
type FrictionWindow struct {
	Days         int     `json:"days"`
	SessionCount int     `json:"session_count"`
	AvgFriction  float64 `json:"avg_friction"`
}

// FrictionTrend summarizes recent friction direction for the proactive
// session-start warning. Computed from the full session slice.
type FrictionTrend struct {
	Windows   []FrictionWindow `json:"windows"`    // 7/30/90-day
	RecentAvg float64          `json:"recent_avg"` // the 7-day window avg
	Direction string           `json:"direction"`  // "improving" | "worsening" | "stable" | "unknown"
	Warn      bool             `json:"warn"`
	Message   string           `json:"message,omitempty"`
}

// ModelRegression is the friction change across one model-change boundary in
// the time-ordered session history.
type ModelRegression struct {
	FromModel       string  `json:"from_model"`
	ToModel         string  `json:"to_model"`
	FromAvgFriction float64 `json:"from_avg_friction"`
	ToAvgFriction   float64 `json:"to_avg_friction"`
	Delta           float64 `json:"delta"` // ToAvg - FromAvg; positive = friction worsened
	FromSessions    int     `json:"from_sessions"`
	ToSessions      int     `json:"to_sessions"`
}

// ModelRegressionReport carries the regressions plus coverage labeling: how
// many sessions had a non-empty Model vs were skipped (per the plan's "label
// coverage" requirement; old sessions have empty Model).
type ModelRegressionReport struct {
	Regressions    []ModelRegression `json:"regressions"`
	ModeledCount   int               `json:"modeled_count"`
	UnmodeledCount int               `json:"unmodeled_count"`
}

// FrictionSession is a triage row: a session ranked by friction.
type FrictionSession struct {
	ID            string `json:"id"`
	Date          string `json:"date"`
	Iteration     int    `json:"iteration"`
	Title         string `json:"title"`
	FrictionScore int    `json:"friction_score"`
}

// CorrectionPoint is one session's correction sub-score over time.
type CorrectionPoint struct {
	Date        string `json:"date"`
	Iteration   int    `json:"iteration"`
	Corrections int    `json:"corrections"`
}

// CorrectionDensitySeries is the time-ordered correction sub-score series plus
// a count of sessions skipped because they predate breakdown capture (nil
// Breakdown) — surfaced explicitly per review finding HIGH-2 (never conflate
// "no data" with a measured zero).
type CorrectionDensitySeries struct {
	Points       []CorrectionPoint `json:"points"`
	MissingCount int               `json:"missing_count"` // sessions with nil Breakdown
}

// Direction thresholds for ComputeFrictionTrend. Kept as named consts so the
// trend sensitivity is tunable without hunting through the logic.
const (
	// trendDeadBand is the friction-point band around the baseline within which
	// the trend is treated as "stable" — noise rather than a real move.
	trendDeadBand = 5.0
	// trendWarnFloor is the recent-average friction at or above which a
	// worsening trend earns a proactive warning (the rough half of 0–100).
	trendWarnFloor = 50.0
)

// parsedSession is a session reduced to the fields the windowing math needs,
// with its date already parsed once (so unparseable dates are warned about a
// single time rather than per window).
type parsedSession struct {
	date     time.Time
	friction int
}

// parseSessionDates parses each session's YYYY-MM-DD Date, skipping (and
// warn-logging) any that fail to parse, mirroring GetFrictionTrends.
func parseSessionDates(sessions []storage.SessionMeta) []parsedSession {
	out := make([]parsedSession, 0, len(sessions))
	for _, s := range sessions {
		d, err := time.Parse("2006-01-02", s.Date)
		if err != nil {
			slog.Warn("analytics: unparseable session date", "date", s.Date, "err", err)
			continue
		}
		out = append(out, parsedSession{date: d, friction: s.FrictionScore})
	}
	return out
}

// GetFrictionWindows returns one FrictionWindow per entry in windowDays
// (e.g. []int{7,30,90}), each the avg FrictionScore over sessions whose Date
// falls within that many days before now. Sessions with unparseable dates are
// skipped. Distinct from the ISO-week buckets of GetFrictionTrends.
//
// tag:auto-capture sessions are excluded from the average — their scores are
// keyword hits on early-Stop transcript (and historically on git-log summaries),
// not interaction. Unscored non-auto notes still count as 0 (FrictionScore
// omitempty); that residual is not this unit.
func GetFrictionWindows(sessions []storage.SessionMeta, now time.Time, windowDays []int) []FrictionWindow {
	filtered := make([]storage.SessionMeta, 0, len(sessions))
	for _, s := range sessions {
		if s.Tag == storage.TagAutoCapture {
			continue
		}
		filtered = append(filtered, s)
	}
	parsed := parseSessionDates(filtered)
	out := make([]FrictionWindow, 0, len(windowDays))
	for _, days := range windowDays {
		cutoff := time.Duration(days) * 24 * time.Hour
		sum, count := 0, 0
		for _, p := range parsed {
			delta := now.Sub(p.date)
			if delta >= 0 && delta <= cutoff {
				sum += p.friction
				count++
			}
		}
		w := FrictionWindow{Days: days, SessionCount: count}
		if count > 0 {
			w.AvgFriction = roundTo(float64(sum)/float64(count), 1)
		}
		out = append(out, w)
	}
	return out
}

// ComputeFrictionTrend builds the 7/30/90-day windows and derives a direction
// by comparing the 7-day average against the 30-day average, plus a warn flag +
// human message when recent friction is both elevated and rising.
func ComputeFrictionTrend(sessions []storage.SessionMeta, now time.Time) FrictionTrend {
	windows := GetFrictionWindows(sessions, now, []int{7, 30, 90})
	recent := windows[0]   // 7-day
	baseline := windows[1] // 30-day

	t := FrictionTrend{
		Windows:   windows,
		RecentAvg: recent.AvgFriction,
		Direction: "unknown",
	}

	// No recent sessions means we cannot speak to direction. The 7-day window
	// is a subset of the 30-day window, so a non-empty 7-day window guarantees a
	// non-empty 30-day baseline (no divide-by-zero below).
	if recent.SessionCount == 0 {
		return t
	}

	delta := recent.AvgFriction - baseline.AvgFriction
	switch {
	case delta < -trendDeadBand:
		t.Direction = "improving"
	case delta > trendDeadBand:
		t.Direction = "worsening"
	default:
		t.Direction = "stable"
	}

	if t.Direction == "worsening" && t.RecentAvg >= trendWarnFloor {
		t.Warn = true
		t.Message = fmt.Sprintf(
			"Recent friction is rising (7d avg %.1f vs 30d %.1f). Consider narrowing scope or using plan mode.",
			recent.AvgFriction, baseline.AvgFriction,
		)
	}
	return t
}

// lessSessionByDateFpIter orders sessions by (Date, Fingerprint, Iteration)
// ascending. Same-date cross-host sessions can share an iteration number, so the
// writer fingerprint is the secondary key for a deterministic total order. The
// descending call sites invert the order by swapping operands.
func lessSessionByDateFpIter(aDate, aFP string, aIter int, bDate, bFP string, bIter int) bool {
	if aDate != bDate {
		return aDate < bDate
	}
	if aFP != bFP {
		return aFP < bFP
	}
	return aIter < bIter
}

// sessionSortKey holds a session's ordering coordinates with the writer
// fingerprint parsed from the ID exactly once. Sorting this decorated slice (a
// decorate-sort) keeps fingerprint parsing O(n) instead of the O(n log n) it
// would cost re-parsing inside every comparison. idx points back at the source
// slice so it can be reordered to match.
type sessionSortKey struct {
	date     string
	fp       string
	iter     int
	friction int
	idx      int
}

// sessionSortKeys decorates sessions with their parsed fingerprints for sorting.
func sessionSortKeys(sessions []storage.SessionMeta) []sessionSortKey {
	keys := make([]sessionSortKey, len(sessions))
	for i, s := range sessions {
		keys[i] = sessionSortKey{
			date:     s.Date,
			fp:       ParseFingerprint(s.ID),
			iter:     s.Iteration,
			friction: s.FrictionScore,
			idx:      i,
		}
	}
	return keys
}

// reorderSessions returns a new slice of sessions in the order described by the
// (already-sorted) decoration keys.
func reorderSessions(sessions []storage.SessionMeta, keys []sessionSortKey) []storage.SessionMeta {
	out := make([]storage.SessionMeta, len(keys))
	for i, k := range keys {
		out[i] = sessions[k.idx]
	}
	return out
}

// DetectModelRegressions sorts sessions chronologically (by Date then
// Iteration), drops those with an empty Model, groups consecutive runs of the
// same model, and reports the avg-friction delta at each model->model boundary.
func DetectModelRegressions(sessions []storage.SessionMeta) ModelRegressionReport {
	var report ModelRegressionReport

	modeled := make([]storage.SessionMeta, 0, len(sessions))
	for _, s := range sessions {
		if s.Model == "" {
			report.UnmodeledCount++
			continue
		}
		modeled = append(modeled, s)
	}
	report.ModeledCount = len(modeled)

	keys := sessionSortKeys(modeled)
	sort.SliceStable(keys, func(i, j int) bool {
		return lessSessionByDateFpIter(
			keys[i].date, keys[i].fp, keys[i].iter,
			keys[j].date, keys[j].fp, keys[j].iter,
		)
	})
	modeled = reorderSessions(modeled, keys)

	// Collapse into maximal consecutive runs sharing a Model.
	type run struct {
		model string
		sum   int
		count int
	}
	var runs []run
	for _, s := range modeled {
		if n := len(runs); n > 0 && runs[n-1].model == s.Model {
			runs[n-1].sum += s.FrictionScore
			runs[n-1].count++
			continue
		}
		runs = append(runs, run{model: s.Model, sum: s.FrictionScore, count: 1})
	}

	for i := 1; i < len(runs); i++ {
		from, to := runs[i-1], runs[i]
		fromAvg := roundTo(float64(from.sum)/float64(from.count), 1)
		toAvg := roundTo(float64(to.sum)/float64(to.count), 1)
		report.Regressions = append(report.Regressions, ModelRegression{
			FromModel:       from.model,
			ToModel:         to.model,
			FromAvgFriction: fromAvg,
			ToAvgFriction:   toAvg,
			Delta:           roundTo(toAvg-fromAvg, 1),
			FromSessions:    from.count,
			ToSessions:      to.count,
		})
	}
	return report
}

// TopFrictionSessions returns the n highest-friction sessions, descending by
// FrictionScore (ties broken by most-recent Date then Iteration). n<=0 returns nil.
func TopFrictionSessions(sessions []storage.SessionMeta, n int) []FrictionSession {
	if n <= 0 {
		return nil
	}
	keys := sessionSortKeys(sessions)
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].friction != keys[j].friction {
			return keys[i].friction > keys[j].friction
		}
		// Descending (Date, Fingerprint, Iteration): invert by swapping operands
		// so most-recent / higher-fp / higher-iteration sorts first.
		return lessSessionByDateFpIter(
			keys[j].date, keys[j].fp, keys[j].iter,
			keys[i].date, keys[i].fp, keys[i].iter,
		)
	})
	sorted := reorderSessions(sessions, keys)

	limit := min(n, len(sorted))
	out := make([]FrictionSession, 0, limit)
	for i := range limit {
		s := sorted[i]
		out = append(out, FrictionSession{
			ID:            s.ID,
			Date:          s.Date,
			Iteration:     s.Iteration,
			Title:         s.Title,
			FrictionScore: s.FrictionScore,
		})
	}
	return out
}

// GetCorrectionDensitySeries builds the chronological correction-density series
// from sessions whose Breakdown is non-nil, counting nil-Breakdown sessions as
// missing.
func GetCorrectionDensitySeries(sessions []storage.SessionMeta) CorrectionDensitySeries {
	var series CorrectionDensitySeries

	withData := make([]storage.SessionMeta, 0, len(sessions))
	for _, s := range sessions {
		if s.Breakdown == nil {
			series.MissingCount++
			continue
		}
		withData = append(withData, s)
	}

	keys := sessionSortKeys(withData)
	sort.SliceStable(keys, func(i, j int) bool {
		return lessSessionByDateFpIter(
			keys[i].date, keys[i].fp, keys[i].iter,
			keys[j].date, keys[j].fp, keys[j].iter,
		)
	})
	withData = reorderSessions(withData, keys)

	for _, s := range withData {
		series.Points = append(series.Points, CorrectionPoint{
			Date:        s.Date,
			Iteration:   s.Iteration,
			Corrections: s.Breakdown.Corrections,
		})
	}
	return series
}

// roundTo rounds f to n decimal places. Local to capture so the analytics and
// effectiveness math need not reach into the tools package's copy.
func roundTo(f float64, n int) float64 {
	pow := math.Pow(10, float64(n))
	return math.Round(f*pow) / pow
}
