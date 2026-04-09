// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func meta(hall string) palace.DrawerMetadata {
	return palace.DrawerMetadata{
		Wing:       "test-wing",
		Room:       "test-room",
		Hall:       hall,
		SourceType: "session",
		SourceRef:  "abc123",
	}
}

func TestCompressDecisionHeavy(t *testing.T) {
	text := "We decided to use Go for the backend. The team agreed on PostgreSQL for storage. John Smith chose the MIT license after reviewing Apache."
	r := palace.Compress(text, meta("decisions"), nil)

	if r.Weight < 3 {
		t.Errorf("Weight = %d, want >= 3 for decision-heavy text", r.Weight)
	}
	if !containsFlag(r.Flags, "DECISION") {
		t.Errorf("Flags = %v, want DECISION flag", r.Flags)
	}
	if len(r.Topics) == 0 {
		t.Error("expected at least one topic")
	}
	if r.KeyQuote == "" {
		t.Error("expected a key quote")
	}
	if r.Raw == "" {
		t.Error("expected formatted raw output")
	}
	// Should have entity codes for "John Smith"
	if len(r.Entities) == 0 {
		t.Error("expected entity codes for 'John Smith'")
	}
}

func TestCompressEmotionalText(t *testing.T) {
	text := "I'm frustrated that the build keeps failing. It's confusing and makes no sense. But I'm determined to fix it and feeling optimistic about the new approach."
	r := palace.Compress(text, meta("facts"), nil)

	if len(r.Emotions) < 2 {
		t.Errorf("Emotions = %v, want at least 2 emotions", r.Emotions)
	}
	found := make(map[string]bool)
	for _, e := range r.Emotions {
		found[e] = true
	}
	if !found["frustration"] {
		t.Errorf("expected frustration in %v", r.Emotions)
	}
	if !found["confusion"] {
		t.Errorf("expected confusion in %v", r.Emotions)
	}
}

func TestCompressTechnicalText(t *testing.T) {
	text := "The function handleRequest() in /internal/server/handler.go was causing a panic. Fixed by adding nil check. ```go\nif req == nil { return }\n```"
	r := palace.Compress(text, meta("facts"), nil)

	if !containsFlag(r.Flags, "TECHNICAL") {
		t.Errorf("Flags = %v, want TECHNICAL flag", r.Flags)
	}
}

func TestCompressMinimalText(t *testing.T) {
	r := palace.Compress("ok", meta("facts"), nil)

	if r.Weight < 1 {
		t.Errorf("Weight = %d, want >= 1", r.Weight)
	}
	// Should not panic on minimal input.
	if r.Raw == "" {
		t.Error("expected non-empty raw output")
	}
}

func TestCompressEmptyText(t *testing.T) {
	r := palace.Compress("", meta("facts"), nil)
	if r.Weight < 1 {
		t.Errorf("Weight = %d, want >= 1", r.Weight)
	}
	if r.Raw == "" {
		t.Error("expected non-empty raw output even for empty text")
	}
}

func TestCompressMultiEntity(t *testing.T) {
	text := "John Smith and Jane Doe discussed the architecture with Bob Johnson. Alice Williams joined later."
	r := palace.Compress(text, meta("facts"), nil)

	if len(r.Entities) < 3 {
		t.Errorf("Entities = %v, want at least 3 entity codes", r.Entities)
	}
	// All codes should be unique.
	seen := make(map[string]bool)
	for _, e := range r.Entities {
		if seen[e] {
			t.Errorf("duplicate entity code: %s", e)
		}
		seen[e] = true
		if len(e) != 3 {
			t.Errorf("entity code %q should be 3 chars", e)
		}
		if e != strings.ToUpper(e) {
			t.Errorf("entity code %q should be uppercase", e)
		}
	}
}

func TestCompressAllFlags(t *testing.T) {
	tests := []struct {
		name string
		text string
		flag string
	}{
		{"ORIGIN", "We started the project and initialized the repo.", "ORIGIN"},
		{"CORE", "This is a fundamental component, essential to the system.", "CORE"},
		{"SENSITIVE", "Store the secret credential and password securely.", "SENSITIVE"},
		{"PIVOT", "We pivoted and changed direction on the approach.", "PIVOT"},
		{"GENESIS", "This is a new project built from scratch.", "GENESIS"},
		{"DECISION", "We decided to use Go and agreed on the architecture.", "DECISION"},
		{"TECHNICAL", "The function main() starts the server.", "TECHNICAL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := palace.Compress(tt.text, meta("facts"), nil)
			if !containsFlag(r.Flags, tt.flag) {
				t.Errorf("Flags = %v, want %s", r.Flags, tt.flag)
			}
		})
	}
}

func TestCompressWeightRange(t *testing.T) {
	// Minimal text: weight should be 1.
	r := palace.Compress("hello world", meta("facts"), nil)
	if r.Weight != 1 {
		t.Errorf("minimal Weight = %d, want 1", r.Weight)
	}

	// Maximum signals: decision + entity + decisions hall + flag
	text := "John Smith decided to start the new project from scratch."
	r = palace.Compress(text, meta("decisions"), nil)
	if r.Weight != 5 {
		t.Errorf("max Weight = %d, want 5", r.Weight)
	}
}

func TestCompressTopicExtraction(t *testing.T) {
	text := "database database database migration migration schema query optimization performance performance benchmark"
	r := palace.Compress(text, meta("facts"), nil)

	if len(r.Topics) == 0 {
		t.Fatal("expected topics")
	}
	if r.Topics[0] != "database" {
		t.Errorf("top topic = %q, want database", r.Topics[0])
	}
}

func TestCompressKeyQuoteTruncation(t *testing.T) {
	// A very long sentence should be truncated to 120 chars.
	long := strings.Repeat("word ", 50) + "end"
	r := palace.Compress(long, meta("facts"), nil)
	if len(r.KeyQuote) > 120 {
		t.Errorf("KeyQuote length = %d, want <= 120", len(r.KeyQuote))
	}
}

func TestCompressOutputFormat(t *testing.T) {
	text := "We decided to use Go."
	r := palace.Compress(text, meta("facts"), nil)

	// Format: ZID:ENTITIES|topics|"key_quote"|WEIGHT|EMOTIONS|FLAGS
	if !strings.HasPrefix(r.Raw, "abc123:") {
		t.Errorf("Raw should start with ZID, got: %s", r.Raw)
	}
	// Should have exactly 6 pipe-delimited sections after ZID:
	parts := strings.SplitN(r.Raw, "|", 6)
	if len(parts) != 6 {
		t.Errorf("Raw has %d pipe sections, want 6: %s", len(parts), r.Raw)
	}
}

func TestCompressBatchConsistency(t *testing.T) {
	drawers := []storage.Drawer{
		{ID: "d1", Content: "John Smith created the api.", Hall: "facts", SourceType: "manual"},
		{ID: "d2", Content: "John Smith reviewed the api.", Hall: "facts", SourceType: "manual"},
	}
	metas := []palace.DrawerMetadata{
		{SourceRef: "d1", Hall: "facts"},
		{SourceRef: "d2", Hall: "facts"},
	}

	results := palace.CompressBatch(drawers, metas)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	// Both should use the same entity code for "John Smith".
	if len(results[0].Entities) == 0 || len(results[1].Entities) == 0 {
		t.Skip("no entities detected in batch items")
	}
	// Find the John Smith code in both.
	code0 := results[0].Entities[0]
	code1 := results[1].Entities[0]
	if code0 != code1 {
		t.Errorf("batch entity codes differ: %q vs %q, want same for 'John Smith'", code0, code1)
	}
}

func TestCompressCompressionRatio(t *testing.T) {
	// Generate a reasonably long input.
	var b strings.Builder
	for range 20 {
		b.WriteString("The team discussed the database migration strategy and decided to use a blue-green deployment approach. ")
	}
	text := b.String()

	r := palace.Compress(text, meta("facts"), nil)
	ratio := float64(len(r.Raw)) / float64(len(text))
	if ratio > 0.10 {
		t.Errorf("compression ratio = %.2f, want < 0.10 (raw=%d, input=%d)", ratio, len(r.Raw), len(text))
	}
}

func TestCompressAllEmotions(t *testing.T) {
	// Verify all 24 emotion categories are detectable.
	allEmotions := []string{
		"joy", "trust", "fear", "surprise", "sadness", "disgust", "anger",
		"anticipation", "frustration", "confusion", "satisfaction", "curiosity",
		"relief", "pride", "doubt", "urgency", "excitement", "concern",
		"optimism", "pessimism", "determination", "resignation", "confidence",
		"anxiety",
	}

	texts := map[string]string{
		"joy":           "I'm so happy and delighted with the results.",
		"trust":         "This is a reliable and dependable solution.",
		"fear":          "I'm afraid this might break in production.",
		"surprise":      "That was completely unexpected and surprising.",
		"sadness":       "Unfortunately this is a disappointing outcome.",
		"disgust":       "This code is terrible and awful to maintain.",
		"anger":         "I'm furious about the outraged response.",
		"anticipation":  "Looking forward to the upcoming release.",
		"frustration":   "I'm frustrated and stuck on this issue.",
		"confusion":     "This is unclear and puzzling behavior.",
		"satisfaction":  "I'm satisfied, this works well now.",
		"curiosity":     "That's interesting and intriguing.",
		"relief":        "Finally, at last it works. Relieved.",
		"pride":         "Proud of this achievement, impressive work.",
		"doubt":         "I doubt this approach, uncertain about it.",
		"urgency":       "This is urgent, we need it immediately.",
		"excitement":    "This is exciting and amazing!",
		"concern":       "I'm concerned this is troubling and risky.",
		"optimism":      "Feeling optimistic and hopeful about this.",
		"pessimism":     "This looks hopeless and bleak.",
		"determination": "I'm determined and committed to fixing this.",
		"resignation":   "I'm resigned, it's inevitable at this point.",
		"confidence":    "I'm confident and certain this works.",
		"anxiety":       "Feeling anxious and nervous about the deploy.",
	}

	for _, emotion := range allEmotions {
		t.Run(emotion, func(t *testing.T) {
			text, ok := texts[emotion]
			if !ok {
				t.Fatalf("no test text for emotion %q", emotion)
			}
			r := palace.Compress(text, meta("facts"), nil)
			if !slices.Contains(r.Emotions, emotion) {
				t.Errorf("emotion %q not detected in %v", emotion, r.Emotions)
			}
		})
	}
}

func containsFlag(flags []string, flag string) bool {
	return slices.Contains(flags, flag)
}
