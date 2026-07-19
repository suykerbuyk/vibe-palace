// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// TestTaskLifecycle exercises create → list → get → update_status → retire → verify in done.
func TestTaskLifecycle(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	// Create a task. The body must clear the handler's minimum-content floor and
	// must not carry its own H1/**Status:** header (CreateTask writes those).
	raw := h.callTool(t, "vp_manage_task", map[string]any{
		"project":  "test-proj",
		"action":   "create",
		"task":     "lifecycle-task",
		"title":    "Lifecycle Test Task",
		"content":  taskBody("Exercise the full create → list → get → update_status → retire path."),
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

	// Retire. approved_by_human is friction, not authorization — the schema
	// requires it to be present and the handler requires it to be true, but
	// nothing verifies the claim behind it.
	raw = h.callTool(t, "vp_manage_task", map[string]any{
		"project":           "test-proj",
		"action":            "retire",
		"task":              "lifecycle-task",
		"approved_by_human": true,
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

	// Update resume. No project resume.md exists yet (the read above fell through
	// to the embedded default), so the compare-and-set guard is the empty string:
	// "assert absent".
	h.callTool(t, "vp_update_resume", map[string]any{
		"project":         "test-proj",
		"content":         "# Updated Resume\nNew content here.",
		"expected_sha256": "",
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

	// The server mints the number under the lock: on a fresh vault these become
	// Iteration 1 and 2, regardless of what the caller thinks the count is.
	h.callTool(t, "vp_append_iteration", map[string]any{
		"project":   "test-proj",
		"title":     "added integration tests",
		"narrative": "Body of the first iteration.",
	})
	h.callTool(t, "vp_append_iteration", map[string]any{
		"project":   "test-proj",
		"title":     "added system tools",
		"narrative": "Body of the second iteration.",
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
	if !strings.Contains(content, "## Iteration 1 — added integration tests") ||
		!strings.Contains(content, "## Iteration 2 — added system tools") {
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
		Stats struct {
			TripleCount int `json:"triple_count"`
		} `json:"stats"`
		Triples []json.RawMessage `json:"triples"`
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

// TestTools_SchemaDriftMatrix asserts every registered MCP tool rejects a
// request that omits its declared required fields. For each tool:
//  1. Parse the tool's JSON Schema to discover "required" field names.
//  2. Send tools/call with an empty arguments object.
//  3. Assert the response is a well-formed JSON-RPC response whose result
//     signals IsError=true with a validation-style error message.
//  4. Assert the error text mentions at least one of the missing required
//     fields by name — this catches schema-drift where a handler's
//     runtime validation stops lining up with its advertised schema.
//
// Tools with no required fields are asserted to succeed (or at least not
// error on the missing-required-fields axis), establishing a baseline.
func TestTools_SchemaDriftMatrix(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)
	h.initMCP(t)

	infos := h.Server.Registry().List()
	if len(infos) == 0 {
		t.Fatal("no tools registered")
	}

	// Count tools with required fields for the summary log line.
	coveredRequired := 0

	for _, info := range infos {
		t.Run(info.Name, func(t *testing.T) {
			// Parse schema to extract required fields.
			var schemaDoc struct {
				Required []string `json:"required"`
			}
			if len(info.Schema) > 0 {
				if err := json.Unmarshal(info.Schema, &schemaDoc); err != nil {
					t.Fatalf("unmarshal schema for %q: %v", info.Name, err)
				}
			}

			msg := json.RawMessage(fmt.Sprintf(
				`{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":%q,"arguments":{}}}`,
				info.Name,
			))
			resp := h.Server.HandleMessage(context.Background(), msg)
			rpc, ok := resp.(mcplib.JSONRPCResponse)
			if !ok {
				t.Fatalf("tool %q: expected JSONRPCResponse, got %T: %+v",
					info.Name, resp, resp)
			}

			rawResult, _ := json.Marshal(rpc.Result)
			var result struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			}
			if err := json.Unmarshal(rawResult, &result); err != nil {
				t.Fatalf("tool %q: unmarshal result: %v (raw=%s)",
					info.Name, err, rawResult)
			}

			if len(schemaDoc.Required) == 0 {
				// Baseline: a tool with no required fields must not
				// fail with a validation error just because args were
				// empty. It may still fail for semantic reasons, but
				// the protocol surface should stay well-formed.
				return
			}

			coveredRequired++

			if !result.IsError {
				t.Fatalf("tool %q with required=%v accepted empty args (result=%s)",
					info.Name, schemaDoc.Required, rawResult)
			}
			if len(result.Content) == 0 {
				t.Fatalf("tool %q error result has no content", info.Name)
			}
			errText := result.Content[0].Text
			// At least one required field name must appear in the
			// error message — this is the schema-drift signal: the
			// schema advertises a field, and the protocol boundary
			// must mention it by name when it's missing.
			hit := false
			for _, field := range schemaDoc.Required {
				if strings.Contains(errText, field) {
					hit = true
					break
				}
			}
			if !hit {
				t.Errorf("tool %q error text does not mention any required field %v: %s",
					info.Name, schemaDoc.Required, errText)
			}
		})
	}

	t.Logf("schema-drift matrix: covered %d tools with required fields (of %d total)",
		coveredRequired, len(infos))
}
