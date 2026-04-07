// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testSetup(t *testing.T) (*storage.Vault, *vpctx.Resolver) {
	t.Helper()
	root := t.TempDir()
	vault := storage.NewVault(root)
	resolver := vpctx.NewResolver(root)
	return vault, resolver
}

func TestBootstrapEmptyVault(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)

	params := json.RawMessage(`{"project":"test-proj"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	br, ok := result.(BootstrapResult)
	if !ok {
		t.Fatalf("result type = %T, want BootstrapResult", result)
	}

	if br.Project != "test-proj" {
		t.Errorf("Project = %q, want %q", br.Project, "test-proj")
	}
	// Workflow should come from embedded defaults.
	if !strings.Contains(br.Workflow, "Pair Programming") {
		t.Error("expected embedded workflow content")
	}
	// Resume should come from embedded defaults.
	if !strings.Contains(br.Resume, "test-proj") {
		t.Error("expected resume with expanded project name")
	}
	// Empty vault = no tasks, no sessions, no KG.
	if len(br.ActiveTasks) != 0 {
		t.Errorf("ActiveTasks = %d, want 0", len(br.ActiveTasks))
	}
	if len(br.RecentSessions) != 0 {
		t.Errorf("RecentSessions = %d, want 0", len(br.RecentSessions))
	}
}

func TestBootstrapWithTasks(t *testing.T) {
	vault, resolver := testSetup(t)

	// Create two tasks.
	if err := vault.CreateTask("test-proj", "fix-bug", "Fix the bug", "", "high"); err != nil {
		t.Fatal(err)
	}
	if err := vault.CreateTask("test-proj", "add-feature", "Add feature", "", "medium"); err != nil {
		t.Fatal(err)
	}

	tool := BootstrapContextTool(resolver, vault)
	params := json.RawMessage(`{"project":"test-proj"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	br := result.(BootstrapResult)
	if len(br.ActiveTasks) != 2 {
		t.Fatalf("ActiveTasks = %d, want 2", len(br.ActiveTasks))
	}
}

func TestBootstrapWithSessions(t *testing.T) {
	vault, resolver := testSetup(t)

	// Create 7 sessions across two dates.
	for i := 0; i < 4; i++ {
		_, err := vault.WriteSession("test-proj", storage.SessionMeta{
			Date:    "2026-04-06",
			Title:   "session",
			Summary: "work",
			Tag:     "implementation",
		}, "body")
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		_, err := vault.WriteSession("test-proj", storage.SessionMeta{
			Date:    "2026-04-07",
			Title:   "session",
			Summary: "more work",
			Tag:     "implementation",
		}, "body")
		if err != nil {
			t.Fatal(err)
		}
	}

	tool := BootstrapContextTool(resolver, vault)
	params := json.RawMessage(`{"project":"test-proj"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	br := result.(BootstrapResult)
	if len(br.RecentSessions) != 5 {
		t.Fatalf("RecentSessions = %d, want 5", len(br.RecentSessions))
	}
	// Most recent first.
	if br.RecentSessions[0].Date != "2026-04-07" {
		t.Errorf("first session date = %q, want 2026-04-07", br.RecentSessions[0].Date)
	}
}

func TestBootstrapTokenBudget(t *testing.T) {
	vault, resolver := testSetup(t)

	// Create sessions so the response is non-trivial.
	for i := 0; i < 5; i++ {
		_, err := vault.WriteSession("test-proj", storage.SessionMeta{
			Date:    "2026-04-07",
			Title:   "session with a long title to inflate size",
			Summary: strings.Repeat("detail ", 50),
			Tag:     "implementation",
		}, "body")
		if err != nil {
			t.Fatal(err)
		}
	}

	tool := BootstrapContextTool(resolver, vault)
	// Very small token budget to force truncation.
	params := json.RawMessage(`{"project":"test-proj","max_tokens":200}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	br := result.(BootstrapResult)
	// Should have truncated some sessions.
	if len(br.RecentSessions) >= 5 {
		t.Errorf("expected truncated sessions, got %d", len(br.RecentSessions))
	}
}

func TestBootstrapDefaultTokenBudget(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)

	// Omit max_tokens — should default to 8000.
	params := json.RawMessage(`{"project":"test-proj"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	br := result.(BootstrapResult)
	if br.Project != "test-proj" {
		t.Errorf("Project = %q, want %q", br.Project, "test-proj")
	}
}

func TestBootstrapToolSchema(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)

	if tool.Name != "vp_bootstrap_context" {
		t.Errorf("Name = %q, want %q", tool.Name, "vp_bootstrap_context")
	}
	if tool.Description == "" {
		t.Error("Description should not be empty")
	}

	// Schema should be valid JSON.
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Fatalf("invalid schema JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
}
