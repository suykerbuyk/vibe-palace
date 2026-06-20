// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestAnalyzeFriction_EmptyTranscript(t *testing.T) {
	for _, input := range []string{"", "   ", "\n\t\n"} {
		score, err := AnalyzeFriction(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if score != 0 {
			t.Errorf("expected 0 for empty input %q, got %d", input, score)
		}
	}
}

func TestAnalyzeFriction_SmoothSession(t *testing.T) {
	transcript := `
User: Can you add a helper function to parse the config file?
Assistant: Sure, I'll add a parseConfig function to config.go.
User: That looks good, thanks.
Assistant: Done. The function reads the TOML file and returns a Config struct.
User: Perfect. Let's move on to the next task.
Assistant: Ready when you are.
`
	score, err := AnalyzeFriction(transcript)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if score >= 20 {
		t.Errorf("smooth session should score < 20, got %d", score)
	}
}

func TestAnalyzeFriction_HighFrictionCorrections(t *testing.T) {
	transcript := `
User: No not that, I wanted the other file.
Assistant: Let me read the correct file.
User: Wrong, that's still not right.
User: No not that, read vault.go instead.
User: Undo that change.
User: Revert the last edit.
User: No, not that file either. Wrong approach entirely.
`
	score, err := AnalyzeFriction(transcript)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if score < 25 {
		t.Errorf("high-friction corrections should score >= 25, got %d", score)
	}
}

func TestAnalyzeFriction_HighFrictionRetries(t *testing.T) {
	transcript := `
tool_use: read_file
tool_use: read_file
tool_use: read_file
tool_use: read_file
tool_use: edit_file
tool_use: edit_file
tool_use: edit_file
Other conversation text about implementing the feature.
`
	score, err := AnalyzeFriction(transcript)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if score < 20 {
		t.Errorf("high-friction retries should score >= 20, got %d", score)
	}
}

func TestAnalyzeFriction_HighFrictionErrors(t *testing.T) {
	// Dense errors: ~20 error keywords in ~100 tokens → density ~200/1000 → sub = 1000
	words := make([]string, 80)
	for i := range words {
		words[i] = "code"
	}
	errorWords := []string{
		"error", "error", "error", "error", "error",
		"exception", "exception", "failed", "failed", "failure",
		"traceback", "traceback", "fatal", "fatal", "fatal",
		"error", "exception", "failed", "failure", "stack trace",
	}
	transcript := strings.Join(append(words, errorWords...), " ")

	score, err := AnalyzeFriction(transcript)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if score < 25 {
		t.Errorf("high error density should max the error sub-score, got %d", score)
	}
}

func TestAnalyzeFriction_HighFrictionRework(t *testing.T) {
	transcript := `
User: Let's go back to the previous approach.
User: Actually, start over with a different design.
User: Try again, that didn't work.
User: Scratch that, let's redo this from the beginning.
User: Never mind, let's try again from the top.
`
	score, err := AnalyzeFriction(transcript)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if score < 25 {
		t.Errorf("high-friction rework should max the rework sub-score, got %d", score)
	}
}

func TestAnalyzeFriction_CombinedSignals(t *testing.T) {
	transcript := `
User: No not that, wrong file. Undo that.
User: No not that, revert it. Wrong again.
tool_use: read_file
tool_use: read_file
tool_use: read_file
tool_use: read_file
Error: file not found. Error: parse failed. Exception raised. Failed to compile.
Traceback printed. Panic in handler. Error: nil pointer. Stack trace follows.
User: Go back to the old approach. Start over entirely.
User: Try again with a clean slate. Scratch that idea.
User: Never mind, let's redo the whole thing. Try again please.
`
	score, err := AnalyzeFriction(transcript)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if score < 70 {
		t.Errorf("combined signals should score >= 70, got %d", score)
	}
}

func TestAnalyzeFriction_SubScoreCapping(t *testing.T) {
	// 30x "wrong" should still cap corrections at 25.
	transcript := strings.Repeat("wrong wrong wrong ", 10)
	score, err := AnalyzeFriction(transcript)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only corrections signal, should be exactly 25.
	if score > 25 {
		t.Errorf("corrections sub-score should cap at 25, got total %d", score)
	}
}

func TestAnalyzeFriction_MaxScore(t *testing.T) {
	score, err := AnalyzeFriction("test")
	if err != nil {
		t.Fatal(err)
	}
	if score > 100 {
		t.Errorf("score should never exceed 100, got %d", score)
	}
}

// --- Helper function tests ---

func TestCountCorrections(t *testing.T) {
	tests := []struct {
		input    string
		expected int // sub-score, not count
	}{
		{"nothing here", 0},
		{"wrong answer", 8},
		{"wrong wrong", 16},
		{"wrong wrong wrong wrong", 25}, // capped
		{"no not that, revert it", 16},  // phrase + word
		{"undo the change", 8},
	}
	for _, tt := range tests {
		got := countCorrections(tt.input)
		if got != tt.expected {
			t.Errorf("countCorrections(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestCountRetries(t *testing.T) {
	tests := []struct {
		input    string
		expected int // sub-score
	}{
		{"no tool calls here", 0},
		{"tool_use: read\ntool_use: read", 0},                                                // only 2, not 3
		{"tool_use: read\ntool_use: read\ntool_use: read", 10},                               // 1 group
		{"tool_use: a\ntool_use: a\ntool_use: a\ntool_use: b\ntool_use: b\ntool_use: b", 20}, // 2 groups
		{"tool_use: a\n" + strings.Repeat("tool_use: a\n", 10), 10},                          // still 1 group
	}
	for _, tt := range tests {
		got := countRetries(tt.input)
		if got != tt.expected {
			t.Errorf("countRetries(%q...) = %d, want %d", tt.input[:min(len(tt.input), 40)], got, tt.expected)
		}
	}
}

func TestCountErrorDensity(t *testing.T) {
	tests := []struct {
		input      string
		tokenCount int
		expected   int // sub-score
	}{
		{"no issues here at all", 5, 0},
		{"", 0, 0}, // zero tokens, avoid divide-by-zero
	}
	for _, tt := range tests {
		got := countErrorDensity(tt.input, tt.tokenCount)
		if got != tt.expected {
			t.Errorf("countErrorDensity(%q, %d) = %d, want %d", tt.input, tt.tokenCount, got, tt.expected)
		}
	}

	// 5 errors in 100 tokens → density 50 → sub = 250 → capped at 25
	dense := strings.Repeat("word ", 95) + "error error error error error"
	got := countErrorDensity(dense, 100)
	if got != 25 {
		t.Errorf("dense errors should cap at 25, got %d", got)
	}
}

func TestCountReworkSignals(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"all good", 0},
		{"go back to the start", 10},
		{"start over and try again", 20},
		{"go back, start over, try again", 25}, // capped
	}
	for _, tt := range tests {
		got := countReworkSignals(tt.input)
		if got != tt.expected {
			t.Errorf("countReworkSignals(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestCountTokens(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"", 0},
		{"hello", 1},
		{"hello world", 2},
		{"  spaced  out  ", 2},
		{"line\none\ttwo", 3},
	}
	for _, tt := range tests {
		got := countTokens(tt.input)
		if got != tt.expected {
			t.Errorf("countTokens(%q) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

// --- GetFrictionTrends tests ---

func TestGetFrictionTrends_Empty(t *testing.T) {
	root := t.TempDir()
	vault := storage.NewVault(root)

	metrics, err := GetFrictionTrends(vault, "test-proj", 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(metrics) != 0 {
		t.Errorf("expected empty metrics, got %d", len(metrics))
	}
}

func TestGetFrictionTrends_SingleWeek(t *testing.T) {
	root := t.TempDir()
	vault := storage.NewVault(root)

	// Write 3 sessions on the same day (today) with known friction scores.
	today := nowDate()
	for _, score := range []int{20, 40, 60} {
		meta := storage.SessionMeta{
			Date:          today,
			Summary:       fmt.Sprintf("session with score %d", score),
			FrictionScore: score,
		}
		_, err := vault.WriteSession("test-proj", meta, "body")
		if err != nil {
			t.Fatalf("WriteSession: %v", err)
		}
	}

	metrics, err := GetFrictionTrends(vault, "test-proj", 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("expected 1 week, got %d", len(metrics))
	}

	m := metrics[0]
	if m.SessionCount != 3 {
		t.Errorf("expected 3 sessions, got %d", m.SessionCount)
	}
	if m.MaxFriction != 60 {
		t.Errorf("expected max 60, got %d", m.MaxFriction)
	}
	expectedAvg := 40.0
	if m.AvgFriction != expectedAvg {
		t.Errorf("expected avg %.1f, got %.1f", expectedAvg, m.AvgFriction)
	}
}

func TestGetFrictionTrends_MultipleWeeks(t *testing.T) {
	root := t.TempDir()
	vault := storage.NewVault(root)

	// Write sessions across 3 different weeks.
	// Use specific dates that are in different ISO weeks.
	dates := []struct {
		date  string
		score int
	}{
		{"2026-03-23", 10}, // Monday week 13
		{"2026-03-24", 30}, // Tuesday week 13
		{"2026-03-30", 50}, // Monday week 14
		{"2026-04-06", 70}, // Monday week 15
	}

	for _, d := range dates {
		meta := storage.SessionMeta{
			Date:          d.date,
			Summary:       fmt.Sprintf("session %s", d.date),
			FrictionScore: d.score,
		}
		_, err := vault.WriteSession("test-proj", meta, "body")
		if err != nil {
			t.Fatalf("WriteSession(%s): %v", d.date, err)
		}
	}

	metrics, err := GetFrictionTrends(vault, "test-proj", 52)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(metrics) != 3 {
		t.Fatalf("expected 3 weeks, got %d: %+v", len(metrics), metrics)
	}

	// Verify sorted by week_start ascending.
	for i := 1; i < len(metrics); i++ {
		if metrics[i].WeekStart <= metrics[i-1].WeekStart {
			t.Errorf("metrics not sorted: %s <= %s", metrics[i].WeekStart, metrics[i-1].WeekStart)
		}
	}

	// Week 13 has 2 sessions: avg 20, max 30.
	w13 := metrics[0]
	if w13.SessionCount != 2 {
		t.Errorf("week 13: expected 2 sessions, got %d", w13.SessionCount)
	}
	if w13.MaxFriction != 30 {
		t.Errorf("week 13: expected max 30, got %d", w13.MaxFriction)
	}
}

func TestGetFrictionTrends_DefaultWeeks(t *testing.T) {
	root := t.TempDir()
	vault := storage.NewVault(root)

	// Passing 0 should default to 8 weeks (not error).
	metrics, err := GetFrictionTrends(vault, "test-proj", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if metrics == nil {
		t.Error("expected non-nil slice")
	}
}

func TestGetFrictionTrends_MaxWeeks(t *testing.T) {
	root := t.TempDir()
	vault := storage.NewVault(root)

	// Passing > 52 should cap at 52 (not error).
	metrics, err := GetFrictionTrends(vault, "test-proj", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if metrics == nil {
		t.Error("expected non-nil slice")
	}
}

// nowDate returns today's date in YYYY-MM-DD format.
func nowDate() string {
	return time.Now().UTC().Format("2006-01-02")
}
