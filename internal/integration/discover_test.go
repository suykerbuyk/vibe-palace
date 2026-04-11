// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/llm"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

type discoverMock struct {
	responses []string
	callIdx   int
}

func (m *discoverMock) ChatCompletion(_ context.Context, _ []llm.Message) (*llm.Response, error) {
	if m.callIdx >= len(m.responses) {
		return &llm.Response{Choices: []llm.Choice{{Message: llm.Message{Content: "[]"}}}}, nil
	}
	resp := m.responses[m.callIdx]
	m.callIdx++
	return &llm.Response{
		Choices: []llm.Choice{{Message: llm.Message{Role: "assistant", Content: resp}}},
		Usage:   llm.Usage{PromptTokens: 100, CompletionTokens: 80, TotalTokens: 180},
	}, nil
}

// TestIntegrationDiscoverDetectsAndProposes verifies the full discovery pipeline:
// seed general drawers → LLM suggests keywords → cross-validate → proposals generated.
func TestIntegrationDiscoverDetectsAndProposes(t *testing.T) {
	h := newHarness(t, false)

	// 3 "general" drawers with ML/neural network content.
	// No "ml" room exists in defaults, so these stay in general.
	h.addDrawer(t, "proj", "proj", "general",
		"Train the neural network transformer model with gradient descent fine-tuning",
		"facts", "2026-04-10")
	h.addDrawer(t, "proj", "proj", "general",
		"Implement a deep learning neural network for inference and training pipeline",
		"facts", "2026-04-10")
	h.addDrawer(t, "proj", "proj", "general",
		"Neural network training with backpropagation and gradient descent optimization",
		"facts", "2026-04-10")

	// 1 correctly classified drawer (should not regress).
	h.addDrawer(t, "proj", "proj", "testing",
		"Run the test spec with mock fixtures for coverage",
		"facts", "2026-04-10")

	rc := buildClassifier(h.Config)
	opts := palace.DiscoverOptions{Project: "proj", MaxSamples: 50}

	// Verify candidates are collected.
	candidates, _, err := palace.CollectDiscoveryCandidates(h.Vault, rc, opts)
	if err != nil {
		t.Fatalf("CollectDiscoveryCandidates: %v", err)
	}
	if len(candidates) < 3 {
		t.Fatalf("expected at least 3 candidates, got %d", len(candidates))
	}

	// Mock LLM suggests "neural network" (multi-word phrase) for devops room.
	// We use devops since it's a known room in the classifier.
	// "neural network" is a multi-word phrase, so it uses strings.Contains matching.
	mockResp := []palace.LLMDiscoveryResponse{
		{DrawerID: "any", Room: "devops", Keywords: []palace.LLMKeywordSuggestion{
			{Keyword: "neural network", Weight: "high"},
		}},
	}
	respJSON, _ := json.Marshal(mockResp)
	mock := &discoverMock{responses: []string{string(respJSON)}}

	report, err := palace.RunDiscover(context.Background(), h.Vault, rc, mock, opts, nil)
	if err != nil {
		t.Fatalf("RunDiscover: %v", err)
	}

	if report.CandidatesTotal < 3 {
		t.Errorf("expected at least 3 candidates, got %d", report.CandidatesTotal)
	}
	if report.LLMCalls == 0 {
		t.Error("expected at least 1 LLM call")
	}

	// "neural network" should match 3 general drawers, 0 regressions.
	// Score: 3 - 0 = 3 → accepted.
	if len(report.Proposals) == 0 {
		t.Error("expected at least 1 proposal")
	}

	found := false
	for _, p := range report.Proposals {
		if p.Keyword == "neural network" {
			found = true
			if p.Captures < 3 {
				t.Errorf("neural network captures = %d, want >= 3", p.Captures)
			}
			if p.Score <= 0 {
				t.Errorf("neural network score = %d, want > 0", p.Score)
			}
		}
	}
	if !found {
		t.Error("expected 'neural network' in proposals")
	}

	// TOML diff should be non-empty.
	diff := palace.FormatDiscoveryTOML(report.Proposals)
	if diff == "" {
		t.Error("expected non-empty TOML diff")
	}
}

// TestIntegrationDiscoverApplyReducesGeneral verifies that applying discovery
// results actually improves classification on a subsequent audit.
func TestIntegrationDiscoverApplyReducesGeneral(t *testing.T) {
	h := newHarness(t, false)

	// 4 "general" drawers that contain "orchestration" — a keyword not in defaults.
	h.addDrawer(t, "proj", "proj", "general",
		"Container orchestration with kubernetes cluster management",
		"facts", "2026-04-10")
	h.addDrawer(t, "proj", "proj", "general",
		"Service orchestration and deployment pipeline automation",
		"facts", "2026-04-10")
	h.addDrawer(t, "proj", "proj", "general",
		"Orchestration of microservices using kubernetes and docker",
		"facts", "2026-04-10")
	h.addDrawer(t, "proj", "proj", "general",
		"Automated orchestration for cloud infrastructure",
		"facts", "2026-04-10")

	// 1 correctly classified drawer.
	h.addDrawer(t, "proj", "proj", "devops",
		"Deploy kubernetes pods to production cluster",
		"facts", "2026-04-10")

	rc := buildClassifier(h.Config)

	// Mock LLM suggests "orchestration" as medium-weight keyword for devops.
	mockResp := []palace.LLMDiscoveryResponse{
		{DrawerID: "any", Room: "devops", Keywords: []palace.LLMKeywordSuggestion{
			{Keyword: "orchestration", Weight: "medium"},
		}},
	}
	respJSON, _ := json.Marshal(mockResp)
	mock := &discoverMock{responses: []string{string(respJSON)}}

	opts := palace.DiscoverOptions{Project: "proj", MaxSamples: 50}
	report, err := palace.RunDiscover(context.Background(), h.Vault, rc, mock, opts, nil)
	if err != nil {
		t.Fatalf("RunDiscover: %v", err)
	}

	if len(report.Proposals) == 0 {
		t.Fatal("expected proposals")
	}

	// Apply: write scoring config.
	rooms := make(map[string]storage.ScoringRoomOverride)
	for _, p := range report.Proposals {
		ov := rooms[p.Room]
		switch p.Weight {
		case "high":
			ov.High = append(ov.High, p.Keyword)
		case "low":
			ov.Low = append(ov.Low, p.Keyword)
		default:
			ov.Medium = append(ov.Medium, p.Keyword)
		}
		rooms[p.Room] = ov
	}
	if err := h.Vault.WriteScoringConfig("proj", rooms, 0); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}

	// Reload config and rebuild classifier.
	newCfg, err := h.Vault.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	newRC := buildClassifier(newCfg)

	// Re-audit with new classifier: the general drawers should now show as
	// mismatches (the classifier would put them in devops, but they're still
	// physically in general). This proves the scoring config was applied.
	auditAfter, err := palace.RunAudit(h.Vault, newRC, palace.AuditOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("audit after: %v", err)
	}

	// Count mismatches where general drawers would now classify as devops.
	reclassified := 0
	for _, m := range auditAfter.Mismatches {
		if m.CurrentRoom == "general" && m.BestRoom != "general" {
			reclassified++
		}
	}
	if reclassified == 0 {
		t.Errorf("expected general drawers to be flagged as mismatches after config change, got 0")
	}

	// The old classifier had no mismatches for these drawers (they were general
	// and scored as general). The new one should flag them.
	auditOld, _ := palace.RunAudit(h.Vault, rc, palace.AuditOptions{Project: "proj"})
	oldMismatches := 0
	for _, m := range auditOld.Mismatches {
		if m.CurrentRoom == "general" {
			oldMismatches++
		}
	}
	if reclassified <= oldMismatches {
		t.Errorf("new classifier should flag more general mismatches: old=%d, new=%d",
			oldMismatches, reclassified)
	}
}

// TestIntegrationDiscoverEstimate verifies token estimation without LLM calls.
func TestIntegrationDiscoverEstimate(t *testing.T) {
	h := newHarness(t, false)

	for i := 0; i < 5; i++ {
		h.addDrawer(t, "proj", "proj", "general",
			"Some unclassified content "+string(rune('A'+i)),
			"facts", "2026-04-10")
	}

	rc := buildClassifier(h.Config)
	candidates, _, err := palace.CollectDiscoveryCandidates(h.Vault, rc,
		palace.DiscoverOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("CollectDiscoveryCandidates: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("expected candidates")
	}

	est := palace.EstimateDiscoveryCost(candidates, 0)
	if est.BatchCount <= 0 {
		t.Error("expected positive batch count")
	}
	if est.TotalTokens <= 0 {
		t.Error("expected positive total tokens")
	}
	if est.PromptTokens <= 0 {
		t.Error("expected positive prompt tokens")
	}
}

// TestIntegrationDiscoverRejectsRegressions verifies that proposals causing
// regressions to correctly-classified drawers are rejected.
func TestIntegrationDiscoverRejectsRegressions(t *testing.T) {
	h := newHarness(t, false)

	// 1 general drawer with "frobulate" keyword.
	h.addDrawer(t, "proj", "proj", "general",
		"Frobulate the data ingestion pipeline for faster processing",
		"facts", "2026-04-10")

	// 3 correctly classified drawers in different rooms that also contain "frobulate".
	h.addDrawer(t, "proj", "proj", "testing",
		"Test the frobulate function with mock fixtures and frobulate edge cases",
		"facts", "2026-04-10")
	h.addDrawer(t, "proj", "proj", "api",
		"Frobulate graphql endpoint for the restful frobulate service",
		"facts", "2026-04-10")
	h.addDrawer(t, "proj", "proj", "data",
		"Frobulate the sql database migration and frobulate schema changes",
		"facts", "2026-04-10")

	rc := buildClassifier(h.Config)
	opts := palace.DiscoverOptions{Project: "proj", MaxSamples: 50}

	// Mock LLM suggests "frobulate" as a keyword for data room.
	// This should cause regressions: testing and api drawers also contain "frobulate".
	mockResp := []palace.LLMDiscoveryResponse{
		{DrawerID: "any", Room: "data", Keywords: []palace.LLMKeywordSuggestion{
			{Keyword: "frobulate", Weight: "medium"},
		}},
	}
	respJSON, _ := json.Marshal(mockResp)
	mock := &discoverMock{responses: []string{string(respJSON)}}

	report, err := palace.RunDiscover(context.Background(), h.Vault, rc, mock, opts, nil)
	if err != nil {
		t.Fatalf("RunDiscover: %v", err)
	}

	// "frobulate" should be rejected: 1 capture (general), 2+ regressions (testing, api).
	// Score: 1 - (2 * 3) = -5 → rejected.
	for _, p := range report.Proposals {
		if p.Keyword == "frobulate" {
			t.Errorf("'frobulate' should be rejected but was accepted with score %d", p.Score)
		}
	}

	found := false
	for _, r := range report.Rejected {
		if r.Keyword == "frobulate" {
			found = true
			if r.Regressions < 2 {
				t.Errorf("frobulate regressions = %d, want >= 2", r.Regressions)
			}
			if r.Score > 0 {
				t.Errorf("frobulate score = %d, want <= 0", r.Score)
			}
		}
	}
	if !found {
		t.Error("expected 'frobulate' in rejected proposals")
	}
}
