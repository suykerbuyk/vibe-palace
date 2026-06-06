// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const carriedFixture = `# Resume

## Open Threads

### Carried forward

- **existing-item** — already here

### alpha

alpha body

## Project History

history
`

func TestCarriedAdd_Basic(t *testing.T) {
	vault := newVaultRoot(t)
	abs := writeResumeFixture(t, vault, "proj", carriedFixture)
	tool := CarriedAddTool(vault)
	if tool.Name != "vp_carried_add" {
		t.Fatalf("name = %q", tool.Name)
	}
	p, _ := json.Marshal(map[string]any{
		"project": "proj",
		"slug":    "new-item",
		"title":   "a new carried item",
		"body":    "detail",
	})
	if _, err := tool.Handler(context.Background(), p); err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := readFile(t, abs)
	if !strings.Contains(got, "**new-item**") {
		t.Error("new bullet missing")
	}
	if !strings.Contains(got, "**existing-item**") {
		t.Error("existing bullet clobbered")
	}
}

func TestCarriedAdd_DuplicateSlug(t *testing.T) {
	vault := newVaultRoot(t)
	writeResumeFixture(t, vault, "proj", carriedFixture)
	tool := CarriedAddTool(vault)
	p, _ := json.Marshal(map[string]any{
		"project": "proj",
		"slug":    "EXISTING-ITEM",
		"title":   "dup",
	})
	if _, err := tool.Handler(context.Background(), p); err == nil {
		t.Fatal("expected already-exists error (case-insensitive)")
	}
}

func TestCarriedAdd_NoCarriedSection(t *testing.T) {
	vault := newVaultRoot(t)
	writeResumeFixture(t, vault, "proj", threadFixture) // no ### Carried forward
	tool := CarriedAddTool(vault)
	p, _ := json.Marshal(map[string]any{"project": "proj", "slug": "x", "title": "y"})
	if _, err := tool.Handler(context.Background(), p); err == nil {
		t.Fatal("expected carried-forward-not-found error")
	}
}

func TestCarriedRemove_Basic(t *testing.T) {
	vault := newVaultRoot(t)
	abs := writeResumeFixture(t, vault, "proj", carriedFixture)
	tool := CarriedRemoveTool(vault)
	p, _ := json.Marshal(map[string]any{"project": "proj", "slug": "existing-item"})
	if _, err := tool.Handler(context.Background(), p); err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := readFile(t, abs)
	if strings.Contains(got, "**existing-item**") {
		t.Error("bullet not removed")
	}
	if !strings.Contains(got, "### Carried forward") {
		t.Error("Carried forward heading should survive")
	}
}

func TestCarriedRemove_NotFound(t *testing.T) {
	vault := newVaultRoot(t)
	writeResumeFixture(t, vault, "proj", carriedFixture)
	tool := CarriedRemoveTool(vault)
	p, _ := json.Marshal(map[string]any{"project": "proj", "slug": "ghost"})
	if _, err := tool.Handler(context.Background(), p); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestCarriedPromoteToTask_Basic(t *testing.T) {
	vault := newVaultRoot(t)
	abs := writeResumeFixture(t, vault, "proj", carriedFixture)
	tool := CarriedPromoteToTaskTool(vault)
	if tool.Name != "vp_carried_promote_to_task" {
		t.Fatalf("name = %q", tool.Name)
	}
	p, _ := json.Marshal(map[string]any{
		"project":       "proj",
		"slug":          "existing-item",
		"new_task_slug": "promoted-task",
	})
	res, err := tool.Handler(context.Background(), p)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// Bullet removed from resume.
	got := readFile(t, abs)
	if strings.Contains(got, "**existing-item**") {
		t.Error("carried bullet should be removed after promotion")
	}

	// Task created via the shared backend and readable through GetTask.
	meta, content, err := vault.GetTask("proj", "promoted-task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if meta.Status != "pending" {
		t.Errorf("status = %q, want pending", meta.Status)
	}
	if meta.Title != "existing-item" {
		t.Errorf("title = %q, want carried slug", meta.Title)
	}
	if !strings.Contains(content, "already here") {
		t.Errorf("task body missing carried bullet body: %q", content)
	}

	m := res.(map[string]any)
	if m["new_task_slug"] != "promoted-task" {
		t.Errorf("result new_task_slug = %v", m["new_task_slug"])
	}
}

func TestCarriedPromoteToTask_TaskAlreadyExists(t *testing.T) {
	vault := newVaultRoot(t)
	abs := writeResumeFixture(t, vault, "proj", carriedFixture)
	if err := vault.CreateTask("proj", "promoted-task", "t", "", "medium"); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	tool := CarriedPromoteToTaskTool(vault)
	p, _ := json.Marshal(map[string]any{
		"project":       "proj",
		"slug":          "existing-item",
		"new_task_slug": "promoted-task",
	})
	if _, err := tool.Handler(context.Background(), p); err == nil {
		t.Fatal("expected task-already-exists error")
	}
	// Carried bullet must NOT have been removed since task creation failed.
	if got := readFile(t, abs); !strings.Contains(got, "**existing-item**") {
		t.Error("carried bullet should remain when task creation fails")
	}
}

func TestCarriedPromoteToTask_SlugNotFound(t *testing.T) {
	vault := newVaultRoot(t)
	writeResumeFixture(t, vault, "proj", carriedFixture)
	tool := CarriedPromoteToTaskTool(vault)
	p, _ := json.Marshal(map[string]any{
		"project":       "proj",
		"slug":          "ghost",
		"new_task_slug": "t",
	})
	if _, err := tool.Handler(context.Background(), p); err == nil {
		t.Fatal("expected carried-slug-not-found error")
	}
}
