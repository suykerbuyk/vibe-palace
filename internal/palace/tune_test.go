// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/suykerbuyk/vibe-palace/internal/llm"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// --- helpers ---

func tuneVault(t *testing.T) *storage.Vault {
	t.Helper()
	return storage.NewVault(t.TempDir())
}

func addTuneDrawer(t *testing.T, v *storage.Vault, project, wing, room, content string) {
	t.Helper()
	d := storage.Drawer{
		Content:    content,
		Hall:       "facts",
		SourceType: "manual",
		FiledAt:    "2026-04-10T10:00:00Z",
	}
	if err := v.AppendDrawer(project, wing, room, d); err != nil {
		t.Fatalf("AppendDrawer(%s/%s/%s): %v", project, wing, room, err)
	}
}

type mockCaller struct {
	responses []string
	callIdx   int
}

func (m *mockCaller) ChatCompletion(_ context.Context, _ []llm.Message) (*llm.Response, error) {
	if m.callIdx >= len(m.responses) {
		return &llm.Response{Choices: []llm.Choice{{Message: llm.Message{Content: "[]"}}}}, nil
	}
	resp := m.responses[m.callIdx]
	m.callIdx++
	return &llm.Response{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: resp}}},
		Usage:   llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
	}, nil
}

// --- SelectSamples tests ---

func TestSelectSamples_Empty(t *testing.T) {
	v := tuneVault(t)
	rc := NewRoomClassifier(nil, 0)
	samples, err := SelectSamples(v, rc, TuneOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(samples) != 0 {
		t.Errorf("expected 0 samples, got %d", len(samples))
	}
}

func TestSelectSamples_Priority(t *testing.T) {
	v := tuneVault(t)
	rc := NewRoomClassifier(nil, 0)

	// Mismatched: kubernetes content in api room (should be devops).
	addTuneDrawer(t, v, "proj", "proj", "api", "Set up the kubernetes cluster for deployment with docker")
	// General fallback: unclassifiable content.
	addTuneDrawer(t, v, "proj", "proj", "general", "Some random thoughts about life")

	samples, err := SelectSamples(v, rc, TuneOptions{Project: "proj", MaxSamples: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("expected samples")
	}

	// Verify we got at least the mismatch.
	hasMismatch := false
	for _, s := range samples {
		if s.Reason == "mismatch" {
			hasMismatch = true
		}
	}
	if !hasMismatch {
		t.Error("expected at least one mismatch sample")
	}
}

func TestSelectSamples_Cap(t *testing.T) {
	v := tuneVault(t)
	rc := NewRoomClassifier(nil, 0)

	// Add many general drawers.
	for i := range 20 {
		addTuneDrawer(t, v, "proj", "proj", "general", "Some unclassified content about topic "+string(rune('A'+i)))
	}

	samples, err := SelectSamples(v, rc, TuneOptions{Project: "proj", MaxSamples: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(samples) > 5 {
		t.Errorf("expected at most 5 samples, got %d", len(samples))
	}
}

// --- BatchSamples tests ---

func TestBatchSamples_Deterministic(t *testing.T) {
	samples := make([]TuneSample, 12)
	for i := range samples {
		samples[i] = TuneSample{DrawerID: string(rune('z' - i))} // reverse order
	}

	batches1 := BatchSamples(samples, 5)
	batches2 := BatchSamples(samples, 5)

	if len(batches1) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(batches1))
	}
	if len(batches1[0]) != 5 || len(batches1[1]) != 5 || len(batches1[2]) != 2 {
		t.Errorf("batch sizes = %d, %d, %d; want 5, 5, 2",
			len(batches1[0]), len(batches1[1]), len(batches1[2]))
	}

	// Same input produces same output.
	for i := range batches1 {
		for j := range batches1[i] {
			if batches1[i][j].DrawerID != batches2[i][j].DrawerID {
				t.Errorf("batch[%d][%d] differs: %s vs %s",
					i, j, batches1[i][j].DrawerID, batches2[i][j].DrawerID)
			}
		}
	}

	// Verify sorted order.
	if batches1[0][0].DrawerID >= batches1[0][1].DrawerID {
		t.Error("batches should be sorted by DrawerID")
	}
}

// --- BuildClassificationPrompt tests ---

func TestBuildClassificationPrompt(t *testing.T) {
	batch := []TuneSample{
		{DrawerID: "abc", Content: "short content"},
		{DrawerID: "def", Content: strings.Repeat("x", 600)}, // will be truncated
	}
	defs := []RoomDefinition{
		{Name: "testing", Keywords: []string{"test", "assert"}},
		{Name: "devops", Keywords: []string{"docker", "kubernetes"}},
	}

	msgs := BuildClassificationPrompt(batch, defs)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("first message role = %q, want system", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "testing") {
		t.Error("system message should contain room definitions")
	}
	if !strings.Contains(msgs[1].Content, "abc") {
		t.Error("user message should contain drawer IDs")
	}
	// Second sample should be truncated.
	if strings.Contains(msgs[1].Content, strings.Repeat("x", 600)) {
		t.Error("long content should be truncated in prompt")
	}
	if !strings.Contains(msgs[1].Content, "...") {
		t.Error("truncated content should end with ...")
	}

	// Original sample content should NOT be modified.
	if len(batch[1].Content) != 600 {
		t.Error("original sample content should not be truncated")
	}
}

// --- ParseJudgments tests ---

func TestParseJudgments_Valid(t *testing.T) {
	input := `[
		{"drawer_id": "a1", "room": "testing", "confidence": 0.9, "reasoning": "has test keywords"},
		{"drawer_id": "b2", "room": "devops", "confidence": 0.8, "reasoning": "kubernetes content"}
	]`
	validRooms := map[string]bool{"testing": true, "devops": true}

	judgments, err := ParseJudgments(input, validRooms)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(judgments) != 2 {
		t.Fatalf("expected 2 judgments, got %d", len(judgments))
	}
	if judgments[0].Room != "testing" {
		t.Errorf("first judgment room = %q, want testing", judgments[0].Room)
	}
}

func TestParseJudgments_Partial(t *testing.T) {
	input := `[
		{"drawer_id": "a1", "room": "testing", "confidence": 0.9, "reasoning": "ok"},
		"not a valid object",
		{"drawer_id": "b2", "room": "api", "confidence": 0.7, "reasoning": "api stuff"}
	]`
	validRooms := map[string]bool{"testing": true, "api": true}

	judgments, err := ParseJudgments(input, validRooms)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(judgments) != 2 {
		t.Errorf("expected 2 valid judgments, got %d", len(judgments))
	}
}

func TestParseJudgments_Garbage(t *testing.T) {
	_, err := ParseJudgments("not json at all", nil)
	if err == nil {
		t.Error("expected error for garbage input")
	}
}

func TestParseJudgments_UnknownRoom(t *testing.T) {
	input := `[{"drawer_id": "a1", "room": "unknown_room", "confidence": 0.9, "reasoning": "x"}]`
	validRooms := map[string]bool{"testing": true}

	judgments, _ := ParseJudgments(input, validRooms)
	if len(judgments) != 0 {
		t.Errorf("expected 0 judgments for unknown room, got %d", len(judgments))
	}
}

func TestParseJudgments_CodeFence(t *testing.T) {
	input := "```json\n" + `[{"drawer_id": "a1", "room": "general", "confidence": 0.5, "reasoning": "ok"}]` + "\n```"
	judgments, err := ParseJudgments(input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(judgments) != 1 {
		t.Errorf("expected 1 judgment, got %d", len(judgments))
	}
}

// --- ComputeProposals tests ---

func TestComputeProposals_PromoteLow(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	// "test" is a low-weight keyword (0.3) in the testing room.
	// Simulate 4 general drawers that the LLM says belong in "testing",
	// where the keyword "test" fires.
	samples := make([]TuneSample, 4)
	judgments := make([]LLMJudgment, 4)
	for i := range 4 {
		id := string(rune('a' + i))
		samples[i] = TuneSample{DrawerID: id, CurrentRoom: "general", Content: "this is a test of the system"}
		judgments[i] = LLMJudgment{DrawerID: id, Room: "testing", Confidence: 0.9}
	}

	proposals, _ := ComputeProposals(judgments, samples, rc)
	found := false
	for _, p := range proposals {
		if p.Keyword == "test" && p.Room == "testing" {
			found = true
			if p.OldWeight != 0.3 {
				t.Errorf("old weight = %f, want 0.3", p.OldWeight)
			}
			if p.NewWeight != 0.6 {
				t.Errorf("new weight = %f, want 0.6", p.NewWeight)
			}
		}
	}
	if !found {
		t.Error("expected promotion proposal for 'test' keyword")
	}
}

func TestComputeProposals_PromoteMedium(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	// "assert" is medium (0.6) in testing room.
	samples := make([]TuneSample, 4)
	judgments := make([]LLMJudgment, 4)
	for i := range 4 {
		id := string(rune('a' + i))
		samples[i] = TuneSample{DrawerID: id, CurrentRoom: "general", Content: "use assert to verify correctness"}
		judgments[i] = LLMJudgment{DrawerID: id, Room: "testing", Confidence: 0.9}
	}

	proposals, _ := ComputeProposals(judgments, samples, rc)
	found := false
	for _, p := range proposals {
		if p.Keyword == "assert" && p.Room == "testing" {
			found = true
			if p.NewWeight != 1.0 {
				t.Errorf("new weight = %f, want 1.0 (promoted from 0.6)", p.NewWeight)
			}
		}
	}
	if !found {
		t.Error("expected promotion proposal for 'assert'")
	}
}

func TestComputeProposals_DemoteMedium(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	// "deploy" is medium (0.6) in devops room.
	// Simulate 3 drawers in devops that LLM says belong elsewhere.
	samples := make([]TuneSample, 3)
	judgments := make([]LLMJudgment, 3)
	for i := range 3 {
		id := string(rune('a' + i))
		samples[i] = TuneSample{DrawerID: id, CurrentRoom: "devops", Content: "deploy the application to production"}
		judgments[i] = LLMJudgment{DrawerID: id, Room: "api", Confidence: 0.8}
	}

	proposals, _ := ComputeProposals(judgments, samples, rc)
	found := false
	for _, p := range proposals {
		if p.Keyword == "deploy" && p.Room == "devops" {
			found = true
			if p.NewWeight != 0.3 {
				t.Errorf("new weight = %f, want 0.3 (demoted from 0.6)", p.NewWeight)
			}
		}
	}
	if !found {
		t.Error("expected demotion proposal for 'deploy'")
	}
}

func TestComputeProposals_DemoteHigh(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	// "kubernetes" is high (1.0) in devops room.
	// Simulate 3 drawers in devops that LLM says belong in api.
	samples := make([]TuneSample, 3)
	judgments := make([]LLMJudgment, 3)
	for i := range 3 {
		id := string(rune('a' + i))
		samples[i] = TuneSample{DrawerID: id, CurrentRoom: "devops", Content: "kubernetes service mesh for api routing"}
		judgments[i] = LLMJudgment{DrawerID: id, Room: "api", Confidence: 0.85}
	}

	proposals, _ := ComputeProposals(judgments, samples, rc)
	found := false
	for _, p := range proposals {
		if p.Keyword == "kubernetes" && p.Room == "devops" {
			found = true
			if p.NewWeight != 0.6 {
				t.Errorf("new weight = %f, want 0.6 (demoted from 1.0)", p.NewWeight)
			}
		}
	}
	if !found {
		t.Error("expected demotion proposal for 'kubernetes'")
	}
}

func TestComputeProposals_BelowThreshold(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	// Only 2 disagreements — below threshold of 3.
	samples := []TuneSample{
		{DrawerID: "a", CurrentRoom: "general", Content: "test something"},
		{DrawerID: "b", CurrentRoom: "general", Content: "test something else"},
	}
	judgments := []LLMJudgment{
		{DrawerID: "a", Room: "testing", Confidence: 0.9},
		{DrawerID: "b", Room: "testing", Confidence: 0.9},
	}

	proposals, _ := ComputeProposals(judgments, samples, rc)
	if len(proposals) != 0 {
		t.Errorf("expected 0 proposals below threshold, got %d", len(proposals))
	}
}

func TestComputeProposals_UnmatchedFlag(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	// Content with no testing keywords — LLM says testing anyway.
	samples := []TuneSample{
		{DrawerID: "a", CurrentRoom: "general", Content: "completely unrelated content about cooking"},
	}
	judgments := []LLMJudgment{
		{DrawerID: "a", Room: "testing", Confidence: 0.7},
	}

	_, flags := ComputeProposals(judgments, samples, rc)
	if len(flags) != 1 {
		t.Fatalf("expected 1 unmatched flag, got %d", len(flags))
	}
	if flags[0].LLMRoom != "testing" {
		t.Errorf("flag room = %q, want testing", flags[0].LLMRoom)
	}
}

func TestComputeProposals_AllAgree(t *testing.T) {
	rc := NewRoomClassifier(nil, 0)
	samples := []TuneSample{
		{DrawerID: "a", CurrentRoom: "testing", Content: "test assert mock"},
	}
	judgments := []LLMJudgment{
		{DrawerID: "a", Room: "testing", Confidence: 1.0},
	}

	proposals, flags := ComputeProposals(judgments, samples, rc)
	if len(proposals) != 0 {
		t.Errorf("expected 0 proposals when all agree, got %d", len(proposals))
	}
	if len(flags) != 0 {
		t.Errorf("expected 0 flags when all agree, got %d", len(flags))
	}
}

// --- FormatTOMLDiff tests ---

func TestFormatTOMLDiff_Empty(t *testing.T) {
	result := FormatTOMLDiff(nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestFormatTOMLDiff_ValidTOML(t *testing.T) {
	proposals := []WeightProposal{
		{Room: "testing", Keyword: "test", OldWeight: 0.3, NewWeight: 0.6, Rationale: "promoted"},
		{Room: "api", Keyword: "handler", OldWeight: 0.6, NewWeight: 1.0, Rationale: "promoted"},
	}

	result := FormatTOMLDiff(proposals)
	if !strings.Contains(result, "[palace.scoring.rooms.") {
		t.Error("expected TOML table headers")
	}

	// Verify it parses as valid TOML.
	var parsed map[string]any
	if _, err := toml.Decode(result, &parsed); err != nil {
		t.Errorf("FormatTOMLDiff output is not valid TOML: %v\nOutput:\n%s", err, result)
	}
}

func TestFormatTOMLDiff_ReviewOnly(t *testing.T) {
	// When old == new, it's review-only — should not appear in TOML output.
	proposals := []WeightProposal{
		{Room: "testing", Keyword: "test", OldWeight: 0.3, NewWeight: 0.3, Rationale: "already min"},
	}
	result := FormatTOMLDiff(proposals)
	if result != "" {
		t.Errorf("expected empty string for review-only proposals, got %q", result)
	}
}

// --- EstimateTokenCost tests ---

func TestEstimateTokenCost(t *testing.T) {
	samples := make([]TuneSample, 16)
	for i := range samples {
		samples[i] = TuneSample{DrawerID: string(rune('a' + i)), Content: strings.Repeat("x", 100)}
	}

	est := EstimateTokenCost(samples, 8)
	if est.BatchCount != 2 {
		t.Errorf("batch count = %d, want 2", est.BatchCount)
	}
	if est.PromptTokens <= 0 {
		t.Error("prompt tokens should be positive")
	}
	if est.CompletionTokens != 16*50 {
		t.Errorf("completion tokens = %d, want %d", est.CompletionTokens, 16*50)
	}
	if est.TotalTokens != est.PromptTokens+est.CompletionTokens {
		t.Error("total should equal prompt + completion")
	}
}

// --- RunTune tests ---

func TestRunTune_NoSamples(t *testing.T) {
	v := tuneVault(t)
	rc := NewRoomClassifier(nil, 0)
	mock := &mockCaller{}

	report, err := RunTune(context.Background(), v, rc, mock, TuneOptions{Project: "proj"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.SamplesTotal != 0 {
		t.Errorf("samples = %d, want 0", report.SamplesTotal)
	}
}

func TestRunTune_WithMock(t *testing.T) {
	v := tuneVault(t)
	rc := NewRoomClassifier(nil, 0)

	// Add 4 general drawers with content that has the "test" keyword (unique content for unique IDs).
	for i := range 4 {
		addTuneDrawer(t, v, "proj", "proj", "general", fmt.Sprintf("this is a test of subsystem %d", i))
	}

	// Mock LLM responds: classify all as "testing".
	mockResp := func(ids []string) string {
		var judgments []LLMJudgment
		for _, id := range ids {
			judgments = append(judgments, LLMJudgment{
				DrawerID:   id,
				Room:       "testing",
				Confidence: 0.9,
				Reasoning:  "contains test keywords",
			})
		}
		b, _ := json.Marshal(judgments)
		return string(b)
	}

	// We need to know the drawer IDs. Get them from samples.
	samples, _ := SelectSamples(v, rc, TuneOptions{Project: "proj"})
	var ids []string
	for _, s := range samples {
		ids = append(ids, s.DrawerID)
	}

	mock := &mockCaller{responses: []string{mockResp(ids)}}

	report, err := RunTune(context.Background(), v, rc, mock, TuneOptions{Project: "proj"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.SamplesTotal != 4 {
		t.Errorf("samples = %d, want 4", report.SamplesTotal)
	}
	if report.JudgmentsTotal == 0 {
		t.Error("expected judgments")
	}
	if report.Disagreements == 0 {
		t.Error("expected disagreements (general != testing)")
	}
	// With 4 drawers and "test" at weight 0.3, we should get a promotion proposal.
	if len(report.Proposals) == 0 {
		t.Error("expected proposals")
	}
}
