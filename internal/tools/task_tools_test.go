// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestListTasksEmpty(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ListTasksTool(vault)

	params, _ := json.Marshal(listTasksParams{Project: "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]any)
	tasks := m["tasks"].([]storage.TaskMeta)
	if len(tasks) != 0 {
		t.Errorf("tasks = %v", tasks)
	}
}

func TestListTasksPopulated(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("test-proj", "my-task", "My Task", "content", "high"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	tool := ListTasksTool(vault)
	params, _ := json.Marshal(listTasksParams{Project: "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]any)
	tasks := m["tasks"].([]storage.TaskMeta)
	if len(tasks) != 1 {
		t.Fatalf("tasks count = %d", len(tasks))
	}
	if tasks[0].Slug != "my-task" {
		t.Errorf("slug = %q", tasks[0].Slug)
	}
}

func TestListTasksIncludeDone(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("test-proj", "active-task", "Active", "", "medium"); err != nil {
		t.Fatalf("CreateTask active: %v", err)
	}
	if err := vault.CreateTask("test-proj", "done-task", "Done", "", "low"); err != nil {
		t.Fatalf("CreateTask done: %v", err)
	}
	if err := vault.RetireTask("test-proj", "done-task"); err != nil {
		t.Fatalf("RetireTask: %v", err)
	}

	tool := ListTasksTool(vault)

	// Without include_done.
	params, _ := json.Marshal(listTasksParams{Project: "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	tasks := result.(map[string]any)["tasks"].([]storage.TaskMeta)
	if len(tasks) != 1 {
		t.Errorf("without done: got %d tasks", len(tasks))
	}

	// With include_done.
	params, _ = json.Marshal(listTasksParams{Project: "test-proj", IncludeDone: true})
	result, err = tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	tasks = result.(map[string]any)["tasks"].([]storage.TaskMeta)
	if len(tasks) != 2 {
		t.Errorf("with done: got %d tasks", len(tasks))
	}
}

func TestGetTaskFound(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("test-proj", "my-task", "My Task", "body", "high"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	tool := GetTaskTool(vault)
	params, _ := json.Marshal(getTaskParams{Project: "test-proj", Task: "my-task"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	tr := result.(getTaskResult)
	if tr.Meta.Slug != "my-task" {
		t.Errorf("slug = %q", tr.Meta.Slug)
	}
	if tr.Meta.Title != "My Task" {
		t.Errorf("title = %q", tr.Meta.Title)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := GetTaskTool(vault)

	params, _ := json.Marshal(getTaskParams{Project: "test-proj", Task: "nonexistent"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for missing task")
	}
}

func TestManageTaskCreate(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ManageTaskTool(vault)

	params, _ := json.Marshal(manageTaskParams{
		Project:  "test-proj",
		Action:   "create",
		Task:     "new-task",
		Title:    "New Task",
		Content:  "Task body.",
		Priority: "high",
	})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]string)
	if m["status"] != "created" {
		t.Errorf("status = %q", m["status"])
	}

	// Verify it exists.
	meta, _, err := vault.GetTask("test-proj", "new-task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if meta.Title != "New Task" {
		t.Errorf("title = %q", meta.Title)
	}
}

func TestManageTaskUpdateStatus(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("test-proj", "my-task", "My Task", "", "medium"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	tool := ManageTaskTool(vault)
	params, _ := json.Marshal(manageTaskParams{
		Project: "test-proj",
		Action:  "update_status",
		Task:    "my-task",
		Status:  "in_progress",
	})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]string)
	if m["status"] != "in_progress" {
		t.Errorf("status = %q", m["status"])
	}
}

func TestManageTaskRetire(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("test-proj", "my-task", "My Task", "", "medium"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	tool := ManageTaskTool(vault)
	params, _ := json.Marshal(manageTaskParams{
		Project: "test-proj",
		Action:  "retire",
		Task:    "my-task",
	})
	if _, err := tool.Handler(context.Background(), params); err != nil {
		t.Fatalf("handler: %v", err)
	}

	// Verify it's in done.
	meta, _, err := vault.GetTask("test-proj", "my-task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !meta.Done {
		t.Error("expected Done=true")
	}
}

func TestManageTaskCancel(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("test-proj", "my-task", "My Task", "", "medium"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	tool := ManageTaskTool(vault)
	params, _ := json.Marshal(manageTaskParams{
		Project: "test-proj",
		Action:  "cancel",
		Task:    "my-task",
	})
	if _, err := tool.Handler(context.Background(), params); err != nil {
		t.Fatalf("handler: %v", err)
	}

	meta, _, err := vault.GetTask("test-proj", "my-task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if meta.Status != "cancelled" {
		t.Errorf("status = %q", meta.Status)
	}
}

func TestManageTaskInvalidAction(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ManageTaskTool(vault)

	params, _ := json.Marshal(manageTaskParams{
		Project: "test-proj",
		Action:  "nope",
		Task:    "my-task",
	})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for invalid action")
	}
}

func TestManageTaskUpdateStatusMissing(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ManageTaskTool(vault)

	params, _ := json.Marshal(manageTaskParams{
		Project: "test-proj",
		Action:  "update_status",
		Task:    "my-task",
	})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for missing status")
	}
}
