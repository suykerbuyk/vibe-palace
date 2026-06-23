// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestComputeEffectiveness_ContextDelta(t *testing.T) {
	// Same week (all Mondays-of fall on 2026-01-05).
	sessions := []storage.SessionMeta{
		// With context: decisions present, friction 15 -> outcome 85.
		{Date: "2026-01-06", Iteration: 1, FrictionScore: 15, Decisions: []string{"x"}},
		// With context: files changed, friction 65 -> outcome 35.
		{Date: "2026-01-07", Iteration: 1, FrictionScore: 65, FilesChanged: []string{"a.go"}},
		// No context: friction 5 -> outcome 95.
		{Date: "2026-01-08", Iteration: 1, FrictionScore: 5},
	}
	res := ComputeEffectiveness("test-proj", sessions)

	if res.Project != "test-proj" {
		t.Errorf("project = %q", res.Project)
	}
	if res.Overall.TotalSessions != 3 {
		t.Errorf("total = %d, want 3", res.Overall.TotalSessions)
	}
	if res.Overall.WithContext != 2 || res.Overall.WithoutContext != 1 {
		t.Errorf("split = %d/%d, want 2/1", res.Overall.WithContext, res.Overall.WithoutContext)
	}
	// Avg ctx outcome (85+35)/2 = 60, no-ctx 95.
	if res.Overall.AvgCtxOutcome != 60 {
		t.Errorf("avg ctx = %v, want 60", res.Overall.AvgCtxOutcome)
	}
	if res.Overall.AvgNoCtxOutcome != 95 {
		t.Errorf("avg no-ctx = %v, want 95", res.Overall.AvgNoCtxOutcome)
	}
	// Delta = 60 - 95 = -35.
	if res.Overall.Delta != -35 {
		t.Errorf("delta = %v, want -35", res.Overall.Delta)
	}
}

func TestComputeEffectiveness_Empty(t *testing.T) {
	res := ComputeEffectiveness("p", nil)
	if res.Overall.TotalSessions != 0 {
		t.Errorf("total = %d, want 0", res.Overall.TotalSessions)
	}
	if len(res.Weeks) != 0 {
		t.Errorf("weeks = %d, want 0", len(res.Weeks))
	}
}

func TestComputeEffectiveness_WeekBucketingAndSkipBadDate(t *testing.T) {
	sessions := []storage.SessionMeta{
		{Date: "2026-01-06", Iteration: 1, FrictionScore: 10, Decisions: []string{"d"}},
		{Date: "2026-01-13", Iteration: 1, FrictionScore: 20, Decisions: []string{"d"}},
		{Date: "garbage", Iteration: 1, FrictionScore: 99},
	}
	res := ComputeEffectiveness("p", sessions)
	if len(res.Weeks) != 2 {
		t.Fatalf("weeks = %d, want 2 (bad date skipped)", len(res.Weeks))
	}
	// Sorted ascending by Monday key.
	if res.Weeks[0].WeekStart != "2026-01-05" || res.Weeks[1].WeekStart != "2026-01-12" {
		t.Errorf("week starts = %s,%s", res.Weeks[0].WeekStart, res.Weeks[1].WeekStart)
	}
	if res.Overall.TotalSessions != 2 {
		t.Errorf("total = %d, want 2", res.Overall.TotalSessions)
	}
}
