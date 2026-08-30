// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// fixedNow is a stable reference point so windowing tests never call time.Now().
var fixedNow = time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)

// dayBefore returns the YYYY-MM-DD string for n days before fixedNow.
func dayBefore(n int) string {
	return fixedNow.AddDate(0, 0, -n).Format("2006-01-02")
}

// ---------------------------------------------------------------------------
// GetFrictionWindows
// ---------------------------------------------------------------------------

func TestGetFrictionWindows_Empty(t *testing.T) {
	got := GetFrictionWindows(nil, fixedNow, time.UTC, []int{7, 30, 90})
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for _, w := range got {
		if w.SessionCount != 0 || w.AvgFriction != 0 {
			t.Errorf("window %dd = %+v, want zeroed", w.Days, w)
		}
	}
}

func TestGetFrictionWindows_OrderAndBuckets(t *testing.T) {
	sessions := []storage.SessionMeta{
		{Date: dayBefore(1), FrictionScore: 20},  // in 7/30/90
		{Date: dayBefore(5), FrictionScore: 40},  // in 7/30/90
		{Date: dayBefore(20), FrictionScore: 60}, // in 30/90
		{Date: dayBefore(80), FrictionScore: 80}, // in 90 only
	}
	got := GetFrictionWindows(sessions, fixedNow, time.UTC, []int{7, 30, 90})

	want := []struct {
		days  int
		count int
		avg   float64
	}{
		{7, 2, 30.0},  // (20+40)/2
		{30, 3, 40.0}, // (20+40+60)/3
		{90, 4, 50.0}, // (20+40+60+80)/4
	}
	for i, w := range want {
		if got[i].Days != w.days || got[i].SessionCount != w.count || got[i].AvgFriction != w.avg {
			t.Errorf("window[%d] = %+v, want days=%d count=%d avg=%.1f",
				i, got[i], w.days, w.count, w.avg)
		}
	}
}

func TestGetFrictionWindows_BoundaryInclusive(t *testing.T) {
	// Exactly 7 days before noon-now: now.Sub(date) = 7d + 12h > 7d*24h, so it
	// falls OUTSIDE the 7-day window but inside 30/90.
	sessions := []storage.SessionMeta{{Date: dayBefore(7), FrictionScore: 50}}
	got := GetFrictionWindows(sessions, fixedNow, time.UTC, []int{7, 30})
	if got[0].SessionCount != 0 {
		t.Errorf("7d count = %d, want 0 (7-days-ago noon is >7*24h before now)", got[0].SessionCount)
	}
	if got[1].SessionCount != 1 {
		t.Errorf("30d count = %d, want 1", got[1].SessionCount)
	}

	// A session dated today is delta>=0 and well within all windows.
	today := []storage.SessionMeta{{Date: dayBefore(0), FrictionScore: 10}}
	g2 := GetFrictionWindows(today, fixedNow, time.UTC, []int{7})
	if g2[0].SessionCount != 1 {
		t.Errorf("today 7d count = %d, want 1", g2[0].SessionCount)
	}
}

func TestGetFrictionWindows_SkipsUnparseable(t *testing.T) {
	sessions := []storage.SessionMeta{
		{Date: "not-a-date", FrictionScore: 99},
		{Date: dayBefore(1), FrictionScore: 30},
	}
	got := GetFrictionWindows(sessions, fixedNow, time.UTC, []int{7})
	if got[0].SessionCount != 1 || got[0].AvgFriction != 30.0 {
		t.Errorf("got %+v, want count=1 avg=30.0", got[0])
	}
}

func TestGetFrictionWindows_Rounding(t *testing.T) {
	sessions := []storage.SessionMeta{
		{Date: dayBefore(1), FrictionScore: 10},
		{Date: dayBefore(2), FrictionScore: 11},
		{Date: dayBefore(3), FrictionScore: 11},
	}
	got := GetFrictionWindows(sessions, fixedNow, time.UTC, []int{7})
	// (10+11+11)/3 = 10.666... -> 10.7
	if got[0].AvgFriction != 10.7 {
		t.Errorf("avg = %v, want 10.7", got[0].AvgFriction)
	}
}

// TestGetFrictionWindows_ExcludesAutoCapture pins the average: stub scores
// must not dilute the window. Unscored non-auto notes still count as 0 — that
// residual is not this unit (and is asserted here so a future change that
// "fixes" it by also dropping zeros is noticed).
func TestGetFrictionWindows_ExcludesAutoCapture(t *testing.T) {
	sessions := []storage.SessionMeta{
		{Date: dayBefore(1), FrictionScore: 20, Tag: "review"},
		{Date: dayBefore(2), FrictionScore: 80, Tag: storage.TagAutoCapture},
		{Date: dayBefore(3), FrictionScore: 40, Tag: "implementation"},
		{Date: dayBefore(4), FrictionScore: 0, Tag: "review"}, // unscored non-auto → still in the average as 0
	}
	got := GetFrictionWindows(sessions, fixedNow, time.UTC, []int{7})
	// auto-capture excluded: (20+40+0)/3 = 20.0 — not (20+80+40+0)/4 = 35.0
	if got[0].SessionCount != 3 {
		t.Fatalf("7d count = %d, want 3 (auto-capture excluded)", got[0].SessionCount)
	}
	if got[0].AvgFriction != 20.0 {
		t.Fatalf("7d avg = %v, want 20.0 (stub excluded; unscored non-auto still zero)", got[0].AvgFriction)
	}
}

// ---------------------------------------------------------------------------
// ComputeFrictionTrend
// ---------------------------------------------------------------------------

func TestComputeFrictionTrend_Unknown(t *testing.T) {
	// Only sessions older than 7 days -> 7-day window empty -> unknown.
	sessions := []storage.SessionMeta{{Date: dayBefore(20), FrictionScore: 40}}
	tr := ComputeFrictionTrend(sessions, fixedNow, time.UTC)
	if tr.Direction != "unknown" {
		t.Errorf("direction = %q, want unknown", tr.Direction)
	}
	if tr.Warn {
		t.Error("warn should be false for unknown")
	}
}

func TestComputeFrictionTrend_Improving(t *testing.T) {
	// Recent (7d) low, older part of 30d high -> recent well below baseline.
	sessions := []storage.SessionMeta{
		{Date: dayBefore(1), FrictionScore: 10},
		{Date: dayBefore(2), FrictionScore: 10},
		{Date: dayBefore(20), FrictionScore: 90},
		{Date: dayBefore(25), FrictionScore: 90},
	}
	tr := ComputeFrictionTrend(sessions, fixedNow, time.UTC)
	if tr.Direction != "improving" {
		t.Errorf("direction = %q, want improving", tr.Direction)
	}
	if tr.RecentAvg != 10.0 {
		t.Errorf("recent avg = %v, want 10.0", tr.RecentAvg)
	}
	if tr.Warn {
		t.Error("improving should not warn")
	}
}

func TestComputeFrictionTrend_WorseningAndWarn(t *testing.T) {
	// Recent high (>=50) and well above the 30-day baseline -> worsening + warn.
	sessions := []storage.SessionMeta{
		{Date: dayBefore(1), FrictionScore: 62},
		{Date: dayBefore(2), FrictionScore: 62},
		{Date: dayBefore(20), FrictionScore: 30},
		{Date: dayBefore(25), FrictionScore: 30},
	}
	tr := ComputeFrictionTrend(sessions, fixedNow, time.UTC)
	if tr.Direction != "worsening" {
		t.Errorf("direction = %q, want worsening", tr.Direction)
	}
	if !tr.Warn {
		t.Fatal("expected warn=true")
	}
	want := "Recent friction is rising (7d avg 62.0 vs 30d 46.0). Consider narrowing scope or using plan mode."
	if tr.Message != want {
		t.Errorf("message = %q, want %q", tr.Message, want)
	}
}

func TestComputeFrictionTrend_WorseningNoWarnBelowFloor(t *testing.T) {
	// Worsening direction but recent avg below the warn floor (50).
	sessions := []storage.SessionMeta{
		{Date: dayBefore(1), FrictionScore: 30},
		{Date: dayBefore(2), FrictionScore: 30},
		{Date: dayBefore(20), FrictionScore: 5},
		{Date: dayBefore(25), FrictionScore: 5},
	}
	tr := ComputeFrictionTrend(sessions, fixedNow, time.UTC)
	if tr.Direction != "worsening" {
		t.Errorf("direction = %q, want worsening", tr.Direction)
	}
	if tr.Warn || tr.Message != "" {
		t.Errorf("should not warn below floor: warn=%v msg=%q", tr.Warn, tr.Message)
	}
}

func TestComputeFrictionTrend_Stable(t *testing.T) {
	// Recent and baseline within the dead-band.
	sessions := []storage.SessionMeta{
		{Date: dayBefore(1), FrictionScore: 40},
		{Date: dayBefore(2), FrictionScore: 42},
		{Date: dayBefore(20), FrictionScore: 41},
		{Date: dayBefore(25), FrictionScore: 39},
	}
	tr := ComputeFrictionTrend(sessions, fixedNow, time.UTC)
	if tr.Direction != "stable" {
		t.Errorf("direction = %q, want stable", tr.Direction)
	}
	if tr.Warn {
		t.Error("stable should not warn")
	}
}

func TestGetFrictionWindows_ParsesInVaultLocation(t *testing.T) {
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skipf("no tzdata for America/Denver on this host: %v", err)
	}
	// 2026-08-19 04:00 UTC = 2026-08-18 22:00 MDT.
	now := time.Date(2026, 8, 19, 4, 0, 0, 0, time.UTC)
	sessions := []storage.SessionMeta{{Date: "2026-08-12", FrictionScore: 10}}

	utc := GetFrictionWindows(sessions, now, time.UTC, []int{7})
	if utc[0].SessionCount != 0 {
		t.Errorf("UTC parse: 7d count = %d, want 0 (Aug 12 00:00 UTC is >7*24h before Aug 19 04:00 UTC)", utc[0].SessionCount)
	}
	den := GetFrictionWindows(sessions, now, denver, []int{7})
	if den[0].SessionCount != 1 {
		t.Errorf("Denver parse: 7d count = %d, want 1 (Aug 12 00:00 MDT is inside 7d of Aug 18 22:00 MDT)", den[0].SessionCount)
	}
}

// ---------------------------------------------------------------------------
// DetectModelRegressions
// ---------------------------------------------------------------------------

func TestDetectModelRegressions_Basic(t *testing.T) {
	sessions := []storage.SessionMeta{
		{Date: "2026-01-01", Iteration: 1, Model: "opus", FrictionScore: 20},
		{Date: "2026-01-02", Iteration: 1, Model: "opus", FrictionScore: 30},
		{Date: "2026-01-03", Iteration: 1, Model: "sonnet", FrictionScore: 60},
		{Date: "2026-01-04", Iteration: 1, Model: "sonnet", FrictionScore: 70},
	}
	rep := DetectModelRegressions(sessions)
	if rep.ModeledCount != 4 || rep.UnmodeledCount != 0 {
		t.Errorf("coverage = modeled %d / unmodeled %d, want 4/0", rep.ModeledCount, rep.UnmodeledCount)
	}
	if len(rep.Regressions) != 1 {
		t.Fatalf("regressions = %d, want 1", len(rep.Regressions))
	}
	r := rep.Regressions[0]
	if r.FromModel != "opus" || r.ToModel != "sonnet" {
		t.Errorf("boundary = %s->%s, want opus->sonnet", r.FromModel, r.ToModel)
	}
	if r.FromAvgFriction != 25.0 || r.ToAvgFriction != 65.0 || r.Delta != 40.0 {
		t.Errorf("avgs = from %.1f to %.1f delta %.1f, want 25/65/40", r.FromAvgFriction, r.ToAvgFriction, r.Delta)
	}
	if r.FromSessions != 2 || r.ToSessions != 2 {
		t.Errorf("counts = from %d to %d, want 2/2", r.FromSessions, r.ToSessions)
	}
}

func TestDetectModelRegressions_SkipsEmptyModelAndCounts(t *testing.T) {
	sessions := []storage.SessionMeta{
		{Date: "2026-01-01", Iteration: 1, Model: "", FrictionScore: 99},
		{Date: "2026-01-02", Iteration: 1, Model: "opus", FrictionScore: 10},
		{Date: "2026-01-03", Iteration: 1, Model: "", FrictionScore: 99},
		{Date: "2026-01-04", Iteration: 1, Model: "sonnet", FrictionScore: 50},
	}
	rep := DetectModelRegressions(sessions)
	if rep.ModeledCount != 2 || rep.UnmodeledCount != 2 {
		t.Errorf("coverage = %d/%d, want 2/2", rep.ModeledCount, rep.UnmodeledCount)
	}
	if len(rep.Regressions) != 1 {
		t.Fatalf("regressions = %d, want 1", len(rep.Regressions))
	}
	// Empty-model rows are dropped, so opus and sonnet are adjacent runs.
	if rep.Regressions[0].FromModel != "opus" || rep.Regressions[0].ToModel != "sonnet" {
		t.Errorf("boundary = %s->%s", rep.Regressions[0].FromModel, rep.Regressions[0].ToModel)
	}
}

func TestDetectModelRegressions_ConsecutiveRunGrouping(t *testing.T) {
	// Pattern A,A,B,A across time: two boundaries (A->B, B->A), three runs.
	sessions := []storage.SessionMeta{
		{Date: "2026-01-04", Iteration: 1, Model: "a", FrictionScore: 40},
		{Date: "2026-01-01", Iteration: 1, Model: "a", FrictionScore: 10},
		{Date: "2026-01-02", Iteration: 1, Model: "a", FrictionScore: 20},
		{Date: "2026-01-03", Iteration: 1, Model: "b", FrictionScore: 30},
	}
	rep := DetectModelRegressions(sessions)
	if len(rep.Regressions) != 2 {
		t.Fatalf("regressions = %d, want 2", len(rep.Regressions))
	}
	if rep.Regressions[0].FromModel != "a" || rep.Regressions[0].ToModel != "b" {
		t.Errorf("first boundary = %s->%s, want a->b", rep.Regressions[0].FromModel, rep.Regressions[0].ToModel)
	}
	if rep.Regressions[1].FromModel != "b" || rep.Regressions[1].ToModel != "a" {
		t.Errorf("second boundary = %s->%s, want b->a", rep.Regressions[1].FromModel, rep.Regressions[1].ToModel)
	}
	// First A-run avg = (10+20)/2 = 15.
	if rep.Regressions[0].FromAvgFriction != 15.0 {
		t.Errorf("first from-avg = %v, want 15.0", rep.Regressions[0].FromAvgFriction)
	}
}

func TestDetectModelRegressions_NoBoundaries(t *testing.T) {
	sessions := []storage.SessionMeta{
		{Date: "2026-01-01", Iteration: 1, Model: "opus", FrictionScore: 10},
		{Date: "2026-01-02", Iteration: 1, Model: "opus", FrictionScore: 20},
	}
	rep := DetectModelRegressions(sessions)
	if len(rep.Regressions) != 0 {
		t.Errorf("regressions = %d, want 0", len(rep.Regressions))
	}
	if rep.ModeledCount != 2 {
		t.Errorf("modeled = %d, want 2", rep.ModeledCount)
	}
}

// ---------------------------------------------------------------------------
// TopFrictionSessions
// ---------------------------------------------------------------------------

func TestTopFrictionSessions_OrderAndLimit(t *testing.T) {
	sessions := []storage.SessionMeta{
		{ID: "a", Date: "2026-01-01", Iteration: 1, FrictionScore: 10},
		{ID: "b", Date: "2026-01-02", Iteration: 1, FrictionScore: 90},
		{ID: "c", Date: "2026-01-03", Iteration: 1, FrictionScore: 50},
	}
	got := TopFrictionSessions(sessions, 2)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "b" || got[1].ID != "c" {
		t.Errorf("order = %s,%s, want b,c", got[0].ID, got[1].ID)
	}
}

func TestTopFrictionSessions_TieBreak(t *testing.T) {
	// Equal friction: most recent Date wins, then higher Iteration.
	sessions := []storage.SessionMeta{
		{ID: "old", Date: "2026-01-01", Iteration: 5, FrictionScore: 50},
		{ID: "new-it1", Date: "2026-01-02", Iteration: 1, FrictionScore: 50},
		{ID: "new-it2", Date: "2026-01-02", Iteration: 2, FrictionScore: 50},
	}
	got := TopFrictionSessions(sessions, 3)
	if got[0].ID != "new-it2" || got[1].ID != "new-it1" || got[2].ID != "old" {
		t.Errorf("order = %s,%s,%s, want new-it2,new-it1,old", got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestTopFrictionSessions_NonPositiveN(t *testing.T) {
	sessions := []storage.SessionMeta{{ID: "a", FrictionScore: 10}}
	if got := TopFrictionSessions(sessions, 0); got != nil {
		t.Errorf("n=0 -> %v, want nil", got)
	}
	if got := TopFrictionSessions(sessions, -3); got != nil {
		t.Errorf("n=-3 -> %v, want nil", got)
	}
}

func TestTopFrictionSessions_NLargerThanInput(t *testing.T) {
	sessions := []storage.SessionMeta{{ID: "a", FrictionScore: 10}}
	got := TopFrictionSessions(sessions, 10)
	if len(got) != 1 {
		t.Errorf("len = %d, want 1", len(got))
	}
}

// ---------------------------------------------------------------------------
// GetCorrectionDensitySeries
// ---------------------------------------------------------------------------

func TestGetCorrectionDensitySeries_MissingAndOrder(t *testing.T) {
	sessions := []storage.SessionMeta{
		{Date: "2026-01-03", Iteration: 1, Breakdown: &storage.FrictionBreakdown{Corrections: 16}},
		{Date: "2026-01-01", Iteration: 2, Breakdown: &storage.FrictionBreakdown{Corrections: 8}},
		{Date: "2026-01-01", Iteration: 1, Breakdown: &storage.FrictionBreakdown{Corrections: 0}},
		{Date: "2026-01-02", Iteration: 1, Breakdown: nil}, // predates breakdown capture
	}
	series := GetCorrectionDensitySeries(sessions)
	if series.MissingCount != 1 {
		t.Errorf("missing = %d, want 1", series.MissingCount)
	}
	if len(series.Points) != 3 {
		t.Fatalf("points = %d, want 3", len(series.Points))
	}
	// Chronological by Date then Iteration.
	wantOrder := []struct {
		date string
		iter int
		corr int
	}{
		{"2026-01-01", 1, 0},
		{"2026-01-01", 2, 8},
		{"2026-01-03", 1, 16},
	}
	for i, w := range wantOrder {
		p := series.Points[i]
		if p.Date != w.date || p.Iteration != w.iter || p.Corrections != w.corr {
			t.Errorf("point[%d] = %+v, want %+v", i, p, w)
		}
	}
}

func TestGetCorrectionDensitySeries_AllMissing(t *testing.T) {
	sessions := []storage.SessionMeta{
		{Date: "2026-01-01", Iteration: 1, Breakdown: nil},
		{Date: "2026-01-02", Iteration: 1, Breakdown: nil},
	}
	series := GetCorrectionDensitySeries(sessions)
	if series.MissingCount != 2 {
		t.Errorf("missing = %d, want 2", series.MissingCount)
	}
	if len(series.Points) != 0 {
		t.Errorf("points = %d, want 0", len(series.Points))
	}
}

func TestGetCorrectionDensitySeries_MeasuredZeroNotMissing(t *testing.T) {
	// A non-nil all-zero breakdown is a measured zero, not missing.
	sessions := []storage.SessionMeta{
		{Date: "2026-01-01", Iteration: 1, Breakdown: &storage.FrictionBreakdown{}},
	}
	series := GetCorrectionDensitySeries(sessions)
	if series.MissingCount != 0 {
		t.Errorf("missing = %d, want 0 (measured zero is not missing)", series.MissingCount)
	}
	if len(series.Points) != 1 || series.Points[0].Corrections != 0 {
		t.Errorf("points = %+v, want one zero-corrections point", series.Points)
	}
}

// TestSortTiebreakByFingerprint pins that same-date, same-iteration sessions
// from different writer fingerprints (which can now legitimately collide on
// (date, iteration)) get a deterministic total order via the fingerprint
// tiebreak parsed from meta.ID — independent of input order.
func TestSortTiebreakByFingerprint(t *testing.T) {
	// Two sessions, same date+iteration, distinct fingerprints in the ID.
	a := storage.SessionMeta{
		ID: "2026-06-23-aaaaaaaa-01", Date: "2026-06-23", Iteration: 1,
		FrictionScore: 40, Breakdown: &storage.FrictionBreakdown{Corrections: 9},
	}
	b := storage.SessionMeta{
		ID: "2026-06-23-bbbbbbbb-01", Date: "2026-06-23", Iteration: 1,
		FrictionScore: 40, Breakdown: &storage.FrictionBreakdown{Corrections: 5},
	}

	// GetCorrectionDensitySeries sorts ascending by (date, fp, iteration).
	for _, order := range [][]storage.SessionMeta{{a, b}, {b, a}} {
		series := GetCorrectionDensitySeries(order)
		if len(series.Points) != 2 {
			t.Fatalf("points = %d, want 2", len(series.Points))
		}
		// aaaaaaaa sorts before bbbbbbbb regardless of input order.
		if series.Points[0].Corrections != 9 || series.Points[1].Corrections != 5 {
			t.Errorf("correction-series order not deterministic: %+v", series.Points)
		}
	}

	// TopFrictionSessions breaks the equal-friction, equal-date tie by fp
	// (descending), also deterministic across input orders.
	for _, order := range [][]storage.SessionMeta{{a, b}, {b, a}} {
		top := TopFrictionSessions(order, 2)
		if len(top) != 2 {
			t.Fatalf("top = %d, want 2", len(top))
		}
		if top[0].ID != "2026-06-23-bbbbbbbb-01" || top[1].ID != "2026-06-23-aaaaaaaa-01" {
			t.Errorf("top-friction tiebreak not deterministic: %q, %q", top[0].ID, top[1].ID)
		}
	}
}
