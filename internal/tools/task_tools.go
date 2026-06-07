// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ---------------------------------------------------------------------------
// vp_list_tasks
// ---------------------------------------------------------------------------

type listTasksParams struct {
	Project     string `json:"project"`
	IncludeDone bool   `json:"include_done,omitempty"`
}

var listTasksSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project":      {"type": "string", "description": "Project slug."},
		"include_done": {"type": "boolean", "description": "Include done/cancelled tasks (default false)."}
	},
	"required": ["project"]
}`)

func ListTasksTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_list_tasks",
		Description: "List tasks for a project.",
		Schema:      listTasksSchema,
		Handler:     listTasksHandler(vault),
	}
}

func listTasksHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p listTasksParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		tasks, err := vault.ListTasks(p.Project, p.IncludeDone)
		if err != nil {
			return nil, fmt.Errorf("list tasks: %w", err)
		}
		if tasks == nil {
			tasks = []storage.TaskMeta{}
		}
		return map[string]any{"tasks": tasks}, nil
	}
}

// ---------------------------------------------------------------------------
// vp_get_task
// ---------------------------------------------------------------------------

type getTaskParams struct {
	Project string `json:"project"`
	Task    string `json:"task"`
}

type getTaskResult struct {
	Meta    storage.TaskMeta `json:"meta"`
	Content string           `json:"content"`
}

var getTaskSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"task":    {"type": "string", "description": "Task slug."}
	},
	"required": ["project", "task"]
}`)

func GetTaskTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_get_task",
		Description: "Get full task detail including metadata and content.",
		Schema:      getTaskSchema,
		Handler:     getTaskHandler(vault),
	}
}

func getTaskHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p getTaskParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.Task == "" {
			return nil, fmt.Errorf("task is required")
		}
		meta, content, err := vault.GetTask(p.Project, p.Task)
		if err != nil {
			return nil, fmt.Errorf("get task: %w", err)
		}
		return getTaskResult{Meta: meta, Content: content}, nil
	}
}

// ---------------------------------------------------------------------------
// vp_manage_task
// ---------------------------------------------------------------------------

type manageTaskParams struct {
	Project  string `json:"project"`
	Action   string `json:"action"`
	Task     string `json:"task"`
	Title    string `json:"title,omitempty"`
	Content  string `json:"content,omitempty"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
}

var manageTaskSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project":  {"type": "string", "description": "Project slug."},
		"action":   {"type": "string", "description": "Action: create, update_status, retire, or cancel."},
		"task":     {"type": "string", "description": "Task slug."},
		"title":    {"type": "string", "description": "Task title (for create)."},
		"content":  {"type": "string", "description": "Task content body (for create)."},
		"priority": {"type": "string", "description": "Priority: low, medium, high, critical (for create)."},
		"status":   {"type": "string", "description": "New status (for update_status)."}
	},
	"required": ["project", "action", "task"]
}`)

func ManageTaskTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_manage_task",
		Mutating:    true,
		Description: "Create, update, retire, or cancel a task.",
		Schema:      manageTaskSchema,
		Handler:     manageTaskHandler(vault),
	}
}

func manageTaskHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p manageTaskParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.Task == "" {
			return nil, fmt.Errorf("task is required")
		}

		switch p.Action {
		case "create":
			title := p.Title
			if title == "" {
				title = p.Task
			}
			priority := p.Priority
			if priority == "" {
				priority = "medium"
			}
			if err := vault.CreateTask(p.Project, p.Task, title, p.Content, priority); err != nil {
				return nil, fmt.Errorf("create task: %w", err)
			}
			return map[string]string{"status": "created", "task": p.Task}, nil

		case "update_status":
			if p.Status == "" {
				return nil, fmt.Errorf("status is required for update_status action")
			}
			if err := vault.UpdateTaskStatus(p.Project, p.Task, p.Status); err != nil {
				return nil, fmt.Errorf("update task status: %w", err)
			}
			return map[string]string{"status": p.Status, "task": p.Task}, nil

		case "retire":
			if err := vault.RetireTask(p.Project, p.Task); err != nil {
				return nil, fmt.Errorf("retire task: %w", err)
			}
			return map[string]string{"status": "retired", "task": p.Task}, nil

		case "cancel":
			if err := vault.CancelTask(p.Project, p.Task); err != nil {
				return nil, fmt.Errorf("cancel task: %w", err)
			}
			return map[string]string{"status": "cancelled", "task": p.Task}, nil

		default:
			return nil, fmt.Errorf("invalid action %q: must be create, update_status, retire, or cancel", p.Action)
		}
	}
}
