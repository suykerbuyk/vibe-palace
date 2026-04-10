// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestTaskLifecycle exercises create → list → get → update_status → retire → verify in done.
func TestTaskLifecycle(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	// Create a task.
	raw := h.callTool(t, "vp_manage_task", map[string]any{
		"project":  "test-proj",
		"action":   "create",
		"task":     "lifecycle-task",
		"title":    "Lifecycle Test Task",
		"content":  "This is test content.",
		"priority": "high",
	})
	if !strings.Contains(raw, "created") {
		t.Fatalf("create: %s", raw)
	}

	// List active tasks.
	raw = h.callTool(t, "vp_list_tasks", map[string]any{
		"project": "test-proj",
	})
	if !strings.Contains(raw, "lifecycle-task") {
		t.Fatalf("list: missing task: %s", raw)
	}

	// Get task detail.
	raw = h.callTool(t, "vp_get_task", map[string]any{
		"project": "test-proj",
		"task":    "lifecycle-task",
	})
	if !strings.Contains(raw, "Lifecycle Test Task") {
		t.Fatalf("get: missing title: %s", raw)
	}

	// Update status.
	raw = h.callTool(t, "vp_manage_task", map[string]any{
		"project": "test-proj",
		"action":  "update_status",
		"task":    "lifecycle-task",
		"status":  "in_progress",
	})
	if !strings.Contains(raw, "in_progress") {
		t.Fatalf("update_status: %s", raw)
	}

	// Retire.
	raw = h.callTool(t, "vp_manage_task", map[string]any{
		"project": "test-proj",
		"action":  "retire",
		"task":    "lifecycle-task",
	})
	if !strings.Contains(raw, "retired") {
		t.Fatalf("retire: %s", raw)
	}

	// Verify it's in done via list with include_done.
	raw = h.callTool(t, "vp_list_tasks", map[string]any{
		"project":      "test-proj",
		"include_done": true,
	})
	if !strings.Contains(raw, "lifecycle-task") {
		t.Fatalf("list with done: missing retired task: %s", raw)
	}

	// Verify it's NOT in active-only list.
	raw = h.callTool(t, "vp_list_tasks", map[string]any{
		"project": "test-proj",
	})
	if strings.Contains(raw, "lifecycle-task") {
		t.Fatalf("list active: retired task should not appear: %s", raw)
	}
}

// TestContextRoundtrip tests get_workflow, get_resume, update_resume, get_resume (verify write).
func TestContextRoundtrip(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	// Get workflow (should return embedded default).
	raw := h.callTool(t, "vp_get_workflow", map[string]any{
		"project": "test-proj",
	})
	if raw == "" {
		t.Fatal("get_workflow: empty")
	}

	// Get default resume.
	raw = h.callTool(t, "vp_get_resume", map[string]any{
		"project": "test-proj",
	})
	if raw == "" {
		t.Fatal("get_resume default: empty")
	}

	// Update resume.
	h.callTool(t, "vp_update_resume", map[string]any{
		"project": "test-proj",
		"content": "# Updated Resume\nNew content here.",
	})

	// Get resume again — should reflect the write.
	raw = h.callTool(t, "vp_get_resume", map[string]any{
		"project": "test-proj",
	})
	if !strings.Contains(raw, "Updated Resume") {
		t.Fatalf("get_resume after update: %s", raw)
	}
}

// TestProjectDiscovery creates palace dirs and verifies list_projects finds them.
func TestProjectDiscovery(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	// Create some drawers to establish project palace dirs.
	h.addDrawer(t, "alpha", "memory", "notes", "alpha content", "long-term", "2026-01-01")
	h.addDrawer(t, "beta", "memory", "notes", "beta content", "long-term", "2026-01-01")

	raw := h.callTool(t, "vp_list_projects", map[string]any{})
	if !strings.Contains(raw, "alpha") || !strings.Contains(raw, "beta") {
		t.Fatalf("list_projects: %s", raw)
	}
}

// TestIterationAppend appends two iterations and verifies both are in the file.
func TestIterationAppend(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	h.callTool(t, "vp_append_iteration", map[string]any{
		"project": "test-proj",
		"content": "## Iteration 25\nAdded integration tests.",
	})
	h.callTool(t, "vp_append_iteration", map[string]any{
		"project": "test-proj",
		"content": "## Iteration 26\nAdded system tools.",
	})

	// Read the file directly to verify.
	path, err := h.Vault.IterationsFile("test-proj")
	if err != nil {
		t.Fatalf("IterationsFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Iteration 25") || !strings.Contains(content, "Iteration 26") {
		t.Fatalf("iterations: %s", content)
	}
	if strings.Count(content, "---") < 2 {
		t.Fatalf("expected 2+ separators: %s", content)
	}
}

// TestKnowledgeSnapshot adds triples and verifies vp_get_knowledge returns stats + triples.
func TestKnowledgeSnapshot(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	// Add some KG data via the MCP tool.
	h.callTool(t, "vp_kg_add", map[string]any{
		"project":   "test-proj",
		"subject":   "Go",
		"predicate": "uses",
		"object":    "modules",
	})
	h.callTool(t, "vp_kg_add", map[string]any{
		"project":   "test-proj",
		"subject":   "Go",
		"predicate": "has_feature",
		"object":    "concurrency",
	})

	raw := h.callTool(t, "vp_get_knowledge", map[string]any{
		"project": "test-proj",
	})

	var result struct {
		Stats   struct{ TripleCount int `json:"triple_count"` } `json:"stats"`
		Triples []json.RawMessage                               `json:"triples"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if result.Stats.TripleCount != 2 {
		t.Errorf("triple count = %d, want 2", result.Stats.TripleCount)
	}
	if len(result.Triples) != 2 {
		t.Errorf("triples = %d, want 2", len(result.Triples))
	}
}

// TestRefreshIndex adds drawers, rebuilds the index, and verifies search finds them.
func TestRefreshIndex(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	// Add drawers (these bypass the index).
	h.addDrawer(t, "test-proj", "memory", "notes", "golang concurrency patterns", "long-term", "2026-01-01")
	h.addDrawer(t, "test-proj", "memory", "notes", "rust ownership model", "long-term", "2026-01-02")

	// Refresh index.
	raw := h.callTool(t, "vp_refresh_index", map[string]any{
		"project": "test-proj",
	})
	if !strings.Contains(raw, "rebuilt") {
		t.Fatalf("refresh: %s", raw)
	}

	// Search should now find results.
	raw = h.callTool(t, "vp_search", map[string]any{
		"project": "test-proj",
		"query":   "concurrency",
	})
	// With mock embedder, results may not be semantically relevant,
	// but the search should return something since drawers exist.
	if raw == "" {
		t.Fatal("search after rebuild returned empty")
	}
}
