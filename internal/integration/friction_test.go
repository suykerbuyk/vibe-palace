// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestIntegrationFrictionScoringOnCapture verifies that capturing a session
// with a transcript computes and persists the friction score.
func TestIntegrationFrictionScoringOnCapture(t *testing.T) {
	h := newHarness(t, false) // mock embedder is fine for friction scoring
	h.registerAllTools(t)

	// High-friction transcript: corrections, rework, errors.
	highFriction := `
User: No not that, wrong file entirely.
Assistant: Let me try again.
User: No not that, read the other one. Undo that change.
User: Wrong, revert it. Go back to the previous version.
User: Start over, that approach is broken.
Error: compilation failed. Error: nil pointer. Exception in handler.
tool_use: read_file
tool_use: read_file
tool_use: read_file
tool_use: read_file
User: Try again with a different strategy. Scratch that.
`
	result := h.callTool(t, "vp_capture_session", map[string]any{
		"project":    "friction-test",
		"summary":    "High friction session",
		"transcript": highFriction,
	})

	var highResult struct {
		Status        string `json:"status"`
		SessionID     string `json:"session_id"`
		FrictionScore int    `json:"friction_score"`
	}
	if err := json.Unmarshal([]byte(result), &highResult); err != nil {
		t.Fatalf("unmarshal high-friction result: %v", err)
	}
	if highResult.FrictionScore < 50 {
		t.Errorf("high-friction transcript should score >= 50, got %d", highResult.FrictionScore)
	}

	// Verify score is persisted in session YAML frontmatter.
	sessions, err := h.Vault.ListSessions("friction-test", "", "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].FrictionScore != highResult.FrictionScore {
		t.Errorf("persisted score %d != returned score %d",
			sessions[0].FrictionScore, highResult.FrictionScore)
	}

	// Smooth transcript: no friction signals.
	smooth := `
User: Please add a helper function to parse config.
Assistant: I'll add parseConfig to config.go.
User: Looks good, thanks.
Assistant: Done. It reads TOML and returns a Config struct.
User: Perfect, let's continue.
`
	smoothResult := h.callTool(t, "vp_capture_session", map[string]any{
		"project":    "friction-test",
		"summary":    "Smooth session",
		"transcript": smooth,
	})

	var lowResult struct {
		FrictionScore int `json:"friction_score"`
	}
	if err := json.Unmarshal([]byte(smoothResult), &lowResult); err != nil {
		t.Fatalf("unmarshal smooth result: %v", err)
	}
	if lowResult.FrictionScore >= 20 {
		t.Errorf("smooth transcript should score < 20, got %d", lowResult.FrictionScore)
	}
}

// TestIntegrationFrictionScoringNoTranscript verifies that sessions without
// a transcript get friction_score = 0.
func TestIntegrationFrictionScoringNoTranscript(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	result := h.callTool(t, "vp_capture_session", map[string]any{
		"project": "no-transcript",
		"summary": "Session without transcript",
	})

	var r struct {
		FrictionScore int `json:"friction_score"`
	}
	if err := json.Unmarshal([]byte(result), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.FrictionScore != 0 {
		t.Errorf("no-transcript session should have friction_score 0, got %d", r.FrictionScore)
	}
}

// TestIntegrationFrictionTrendsEndToEnd verifies the vp_get_friction_trends
// tool returns correctly aggregated weekly data.
func TestIntegrationFrictionTrendsEndToEnd(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	// Write sessions with controlled dates and friction scores directly.
	sessions := []struct {
		date  string
		score int
	}{
		{"2026-03-23", 10}, // Monday, week 13
		{"2026-03-24", 30}, // Tuesday, week 13
		{"2026-03-30", 50}, // Monday, week 14
		{"2026-04-06", 80}, // Monday, week 15
		{"2026-04-07", 40}, // Tuesday, week 15
	}

	for _, s := range sessions {
		meta := storage.SessionMeta{
			Date:          s.date,
			Summary:       "test session",
			FrictionScore: s.score,
		}
		if _, err := h.Vault.WriteSession("trend-proj", meta, "body"); err != nil {
			t.Fatalf("WriteSession(%s): %v", s.date, err)
		}
	}

	result := h.callTool(t, "vp_get_friction_trends", map[string]any{
		"project": "trend-proj",
		"weeks":   52,
	})

	var metrics []capture.WeeklyMetric
	if err := json.Unmarshal([]byte(result), &metrics); err != nil {
		t.Fatalf("unmarshal trends: %v", err)
	}

	if len(metrics) != 3 {
		t.Fatalf("expected 3 weeks, got %d: %+v", len(metrics), metrics)
	}

	// Verify sorted ascending.
	for i := 1; i < len(metrics); i++ {
		if metrics[i].WeekStart <= metrics[i-1].WeekStart {
			t.Errorf("not sorted: %s <= %s", metrics[i].WeekStart, metrics[i-1].WeekStart)
		}
	}

	// Week 13: 2 sessions, avg 20, max 30.
	w13 := metrics[0]
	if w13.SessionCount != 2 {
		t.Errorf("week 13: count = %d, want 2", w13.SessionCount)
	}
	if w13.MaxFriction != 30 {
		t.Errorf("week 13: max = %d, want 30", w13.MaxFriction)
	}
	if w13.AvgFriction != 20.0 {
		t.Errorf("week 13: avg = %.1f, want 20.0", w13.AvgFriction)
	}

	// Week 14: 1 session, avg 50, max 50.
	w14 := metrics[1]
	if w14.SessionCount != 1 {
		t.Errorf("week 14: count = %d, want 1", w14.SessionCount)
	}
	if w14.MaxFriction != 50 {
		t.Errorf("week 14: max = %d, want 50", w14.MaxFriction)
	}

	// Week 15: 2 sessions, avg 60, max 80.
	w15 := metrics[2]
	if w15.SessionCount != 2 {
		t.Errorf("week 15: count = %d, want 2", w15.SessionCount)
	}
	if w15.MaxFriction != 80 {
		t.Errorf("week 15: max = %d, want 80", w15.MaxFriction)
	}
	if w15.AvgFriction != 60.0 {
		t.Errorf("week 15: avg = %.1f, want 60.0", w15.AvgFriction)
	}
}

// TestIntegrationFrictionTrendsEmpty verifies empty result for a project
// with no sessions.
func TestIntegrationFrictionTrendsEmpty(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	result := h.callTool(t, "vp_get_friction_trends", map[string]any{
		"project": "empty-proj",
		"weeks":   4,
	})

	var metrics []capture.WeeklyMetric
	if err := json.Unmarshal([]byte(result), &metrics); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(metrics) != 0 {
		t.Errorf("expected empty metrics, got %d", len(metrics))
	}
}

// TestIntegrationFrictionSearchByMinScore verifies that SearchSessions
// correctly filters by minimum friction score.
func TestIntegrationFrictionSearchByMinScore(t *testing.T) {
	h := newHarness(t, false)

	// Write sessions with different friction scores.
	for _, s := range []struct {
		score int
		title string
	}{
		{10, "smooth"},
		{30, "mild friction"},
		{60, "high friction"},
		{85, "very high friction"},
	} {
		meta := storage.SessionMeta{
			Date:          "2026-04-07",
			Title:         s.title,
			Summary:       s.title,
			FrictionScore: s.score,
		}
		if _, err := h.Vault.WriteSession("search-proj", meta, "body"); err != nil {
			t.Fatalf("WriteSession: %v", err)
		}
	}

	// Search with minFriction = 50.
	results, err := h.Vault.SearchSessions("", "search-proj", 50, 0)
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 sessions with friction >= 50, got %d", len(results))
	}
	for _, r := range results {
		if r.FrictionScore < 50 {
			t.Errorf("session %q has friction %d, expected >= 50", r.Title, r.FrictionScore)
		}
	}
}
