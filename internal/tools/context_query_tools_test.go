// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestGetWorkflow(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)

	tool := GetWorkflowTool(resolver)
	if tool.Name != "vp_get_workflow" {
		t.Fatalf("name = %q", tool.Name)
	}

	params, _ := json.Marshal(map[string]string{"project": "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	rr := result.(resolveResult)
	if rr.Project != "test-proj" {
		t.Errorf("project = %q", rr.Project)
	}
	if rr.Content == "" {
		t.Error("expected non-empty workflow from embedded defaults")
	}
}

func TestGetResume(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)

	tool := GetResumeTool(resolver)
	if tool.Name != "vp_get_resume" {
		t.Fatalf("name = %q", tool.Name)
	}

	params, _ := json.Marshal(map[string]string{"project": "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	rr := result.(resolveResult)
	if rr.Content == "" {
		t.Error("expected non-empty resume from embedded defaults")
	}
}

func TestUpdateResumeThenGet(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)

	// Write a resume.
	updateTool := UpdateResumeTool(vault)
	params, _ := json.Marshal(updateResumeParams{
		Project: "test-proj",
		Content: "# My Resume\nUpdated content.",
	})
	result, err := updateTool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("update handler: %v", err)
	}
	m := result.(map[string]string)
	if m["status"] != "updated" {
		t.Errorf("status = %q", m["status"])
	}

	// Read it back via the resolver.
	getTool := GetResumeTool(resolver)
	getParams, _ := json.Marshal(map[string]string{"project": "test-proj"})
	getResult, err := getTool.Handler(context.Background(), getParams)
	if err != nil {
		t.Fatalf("get handler: %v", err)
	}
	rr := getResult.(resolveResult)
	if rr.Content != "# My Resume\nUpdated content." {
		t.Errorf("content = %q", rr.Content)
	}
}

func TestUpdateResumeValidation(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := UpdateResumeTool(vault)

	tests := []struct {
		name   string
		params updateResumeParams
	}{
		{"empty project", updateResumeParams{Project: "", Content: "x"}},
		{"empty content", updateResumeParams{Project: "proj", Content: ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := json.Marshal(tt.params)
			if _, err := tool.Handler(context.Background(), p); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestGetKnowledgeEmpty(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := GetKnowledgeTool(vault)

	params, _ := json.Marshal(map[string]string{"project": "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	kr := result.(knowledgeResult)
	if kr.Stats.EntityCount != 0 {
		t.Errorf("entity count = %d", kr.Stats.EntityCount)
	}
	if len(kr.Triples) != 0 {
		t.Errorf("triples = %d", len(kr.Triples))
	}
}

func TestGetKnowledgePopulated(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	if err := vault.AddTriple("test-proj", storage.Triple{
		Subject: "Go", Predicate: "uses", Object: "modules",
	}); err != nil {
		t.Fatalf("AddTriple: %v", err)
	}

	tool := GetKnowledgeTool(vault)
	params, _ := json.Marshal(map[string]string{"project": "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	kr := result.(knowledgeResult)
	if kr.Stats.TripleCount != 1 {
		t.Errorf("triple count = %d", kr.Stats.TripleCount)
	}
	if len(kr.Triples) != 1 {
		t.Errorf("triples = %d", len(kr.Triples))
	}
}

func TestGetKnowledgeLimit(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	for i := 0; i < 5; i++ {
		if err := vault.AddTriple("test-proj", storage.Triple{
			Subject:   "A",
			Predicate: "rel",
			Object:    string(rune('a' + i)),
		}); err != nil {
			t.Fatalf("AddTriple %d: %v", i, err)
		}
	}

	tool := GetKnowledgeTool(vault)
	params, _ := json.Marshal(getKnowledgeParams{Project: "test-proj", Limit: 2})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	kr := result.(knowledgeResult)
	if len(kr.Triples) != 2 {
		t.Errorf("triples = %d, want 2", len(kr.Triples))
	}
	if kr.Stats.TripleCount != 5 {
		t.Errorf("stats.triple_count = %d, want 5", kr.Stats.TripleCount)
	}
}
