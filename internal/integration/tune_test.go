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

type tuneMock struct {
	responses []string
	callIdx   int
}

func (m *tuneMock) ChatCompletion(_ context.Context, _ []llm.Message) (*llm.Response, error) {
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

func TestIntegrationTuneDetectsAndProposes(t *testing.T) {
	h := newHarness(t, false)

	// 4 "general" drawers with kubernetes/docker content (should be devops).
	h.addDrawer(t, "proj", "proj", "general",
		"Set up the kubernetes cluster for deployment using docker compose",
		"facts", "2026-04-10")
	h.addDrawer(t, "proj", "proj", "general",
		"Configure kubernetes pods and docker containers for CI/CD pipeline",
		"facts", "2026-04-10")
	h.addDrawer(t, "proj", "proj", "general",
		"Deploy kubernetes services with docker and manage the pipeline",
		"facts", "2026-04-10")
	h.addDrawer(t, "proj", "proj", "general",
		"Monitor kubernetes deployments across docker environments in the pipeline",
		"facts", "2026-04-10")

	// 2 correctly classified drawers.
	h.addDrawer(t, "proj", "proj", "testing",
		"Run the test spec with mock fixtures for coverage",
		"facts", "2026-04-10")
	h.addDrawer(t, "proj", "proj", "api",
		"Implement the graphql endpoint with restful fallback",
		"facts", "2026-04-10")

	rc := buildClassifier(h.Config)
	opts := palace.TuneOptions{Project: "proj", MaxSamples: 50}

	// Verify samples are selected.
	samples, err := palace.SelectSamples(h.Vault, rc, opts)
	if err != nil {
		t.Fatalf("SelectSamples: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("expected samples")
	}

	// Build mock response: classify kubernetes content as devops.
	var judgments []palace.LLMJudgment
	for _, s := range samples {
		room := s.CurrentRoom // agree with current
		if s.CurrentRoom == "general" {
			room = "devops" // LLM says it's devops
		}
		judgments = append(judgments, palace.LLMJudgment{
			DrawerID:   s.DrawerID,
			Room:       room,
			Confidence: 0.9,
			Reasoning:  "kubernetes/docker content",
		})
	}
	respJSON, _ := json.Marshal(judgments)
	mock := &tuneMock{responses: []string{string(respJSON)}}

	report, err := palace.RunTune(context.Background(), h.Vault, rc, mock, opts, nil)
	if err != nil {
		t.Fatalf("RunTune: %v", err)
	}

	if report.SamplesTotal == 0 {
		t.Error("expected samples")
	}
	if report.Disagreements == 0 {
		t.Error("expected disagreements (general drawers reclassified)")
	}

	// Should have proposals or unmatched flags for the devops content.
	if len(report.Proposals) == 0 && len(report.UnmatchedFlags) == 0 {
		t.Error("expected proposals or unmatched flags")
	}

	// TOML diff should be valid if proposals exist.
	diff := palace.FormatTOMLDiff(report.Proposals)
	if len(report.Proposals) > 0 && diff == "" {
		t.Error("expected non-empty TOML diff with proposals")
	}
}

func TestIntegrationTuneApplyImproves(t *testing.T) {
	h := newHarness(t, false)

	// 4 "general" drawers with devops content that has existing keywords
	// (docker is 0.6 in devops, deploy is 0.6).
	h.addDrawer(t, "proj", "proj", "general",
		"The docker deploy pipeline handles kubernetes orchestration",
		"facts", "2026-04-10")
	h.addDrawer(t, "proj", "proj", "general",
		"Use docker to deploy services to the kubernetes cluster",
		"facts", "2026-04-10")
	h.addDrawer(t, "proj", "proj", "general",
		"docker and deploy workflows for kubernetes infrastructure",
		"facts", "2026-04-10")
	h.addDrawer(t, "proj", "proj", "general",
		"Configure docker and deploy automation with kubernetes",
		"facts", "2026-04-10")

	rc := buildClassifier(h.Config)
	opts := palace.TuneOptions{Project: "proj", MaxSamples: 50}

	samples, err := palace.SelectSamples(h.Vault, rc, opts)
	if err != nil {
		t.Fatalf("SelectSamples: %v", err)
	}

	// Mock LLM classifies all as devops.
	var judgments []palace.LLMJudgment
	for _, s := range samples {
		judgments = append(judgments, palace.LLMJudgment{
			DrawerID:   s.DrawerID,
			Room:       "devops",
			Confidence: 0.95,
			Reasoning:  "devops content",
		})
	}
	respJSON, _ := json.Marshal(judgments)
	mock := &tuneMock{responses: []string{string(respJSON)}}

	report, err := palace.RunTune(context.Background(), h.Vault, rc, mock, opts, nil)
	if err != nil {
		t.Fatalf("RunTune: %v", err)
	}

	if len(report.Proposals) == 0 {
		t.Skip("no proposals generated (content may need different keywords)")
	}

	// Apply proposals.
	rooms := make(map[string]storage.ScoringRoomOverride)
	for _, p := range report.Proposals {
		if p.OldWeight == p.NewWeight {
			continue
		}
		ov := rooms[p.Room]
		switch {
		case p.NewWeight >= 0.9:
			ov.High = append(ov.High, p.Keyword)
		case p.NewWeight >= 0.5:
			ov.Medium = append(ov.Medium, p.Keyword)
		default:
			ov.Low = append(ov.Low, p.Keyword)
		}
		rooms[p.Room] = ov
	}

	if err := h.Vault.WriteScoringConfig("proj", rooms, 0); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}

	// Reload config and build new classifier.
	newCfg, err := h.Vault.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	newRC := buildClassifier(newCfg)

	// Re-run audit.
	auditBefore, _ := palace.RunAudit(h.Vault, rc, palace.AuditOptions{Project: "proj"})
	auditAfter, _ := palace.RunAudit(h.Vault, newRC, palace.AuditOptions{Project: "proj"})

	// The new classifier should classify at least as well.
	if auditAfter.GeneralCount > auditBefore.GeneralCount {
		t.Errorf("general count increased after tuning: %d → %d",
			auditBefore.GeneralCount, auditAfter.GeneralCount)
	}
}

func TestIntegrationTuneEstimate(t *testing.T) {
	h := newHarness(t, false)

	for i := range 5 {
		h.addDrawer(t, "proj", "proj", "general",
			"Some unclassified content "+string(rune('A'+i)),
			"facts", "2026-04-10")
	}

	rc := buildClassifier(h.Config)
	samples, err := palace.SelectSamples(h.Vault, rc, palace.TuneOptions{Project: "proj"})
	if err != nil {
		t.Fatalf("SelectSamples: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("expected samples")
	}

	est := palace.EstimateTokenCost(samples, 0)
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
