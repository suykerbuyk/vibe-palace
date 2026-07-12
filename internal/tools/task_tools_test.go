// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
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

// unitTaskBody returns a create body that clears the handler's minimum-content
// floor and carries no metadata header of its own (storage.CreateTask rejects a
// body with its own H1 or **Status:** line).
func unitTaskBody() string {
	return strings.Repeat("A real plan line describing what to do and why it matters.\n", 6)
}

func TestManageTaskCreate(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ManageTaskTool(vault)

	params, _ := json.Marshal(manageTaskParams{
		Project:  "test-proj",
		Action:   "create",
		Task:     "new-task",
		Title:    "New Task",
		Content:  unitTaskBody(),
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
		Project:         "test-proj",
		Action:          "retire",
		Task:            "my-task",
		ApprovedByHuman: true,
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

// ---------------------------------------------------------------------------
// vp_get_task Phase-2 behaviour: include_content tri-state + ContentURI/Size.
// ---------------------------------------------------------------------------

// getTaskCall drives the vp_get_task handler with raw JSON params and returns
// the typed result, mirroring how context_tools_test.go drives bootstrap.
func getTaskCall(t *testing.T, vault *storage.Vault, params string) getTaskResult {
	t.Helper()
	tool := GetTaskTool(vault)
	res, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	gr, ok := res.(getTaskResult)
	if !ok {
		t.Fatalf("result type = %T, want getTaskResult", res)
	}
	return gr
}

// makeBigTask creates a task whose STORED body (header + content, what GetTask
// returns) comfortably exceeds taskExcerptCap so the excerpt path is exercised.
// It returns the canonical task URI and the full stored body.
func makeBigTask(t *testing.T, vault *storage.Vault, project, slug string) (uri, body string) {
	t.Helper()
	content := strings.Repeat("This is a line of task body content for the excerpt test.\n", 80)
	if err := vault.CreateTask(project, slug, "Big Task", content, "high"); err != nil {
		t.Fatal(err)
	}
	_, stored, err := vault.GetTask(project, slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) <= taskExcerptCap {
		t.Fatalf("setup: stored body len %d <= taskExcerptCap %d, excerpt path unreachable",
			len(stored), taskExcerptCap)
	}
	return mcp.TaskURI(project, slug), stored
}

// TestGetTaskURIAndSizeAlwaysSet pins that ContentURI and ContentSize are set on
// every response shape — content included OR dropped — and that they address the
// canonical task URI and the full body length.
func TestGetTaskURIAndSizeAlwaysSet(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	uri, body := makeBigTask(t, vault, "test-proj", "big-task")

	cases := []struct {
		name   string
		params string
	}{
		{"default (no include_content)", `{"project":"test-proj","task":"big-task"}`},
		{"include_content=true", `{"project":"test-proj","task":"big-task","include_content":true}`},
		{"include_content=false", `{"project":"test-proj","task":"big-task","include_content":false}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gr := getTaskCall(t, vault, tc.params)
			if gr.ContentURI != uri {
				t.Errorf("ContentURI = %q, want %q", gr.ContentURI, uri)
			}
			if gr.ContentSize != len(body) {
				t.Errorf("ContentSize = %d, want %d (full body len)", gr.ContentSize, len(body))
			}
		})
	}
}

// TestGetTaskDefaultIncludesFullBody pins the back-compat contract: with
// include_content omitted (nil) or explicitly true, Content is the full body
// byte-for-byte and Excerpt is empty.
func TestGetTaskDefaultIncludesFullBody(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	_, body := makeBigTask(t, vault, "test-proj", "big-task")

	for _, params := range []string{
		`{"project":"test-proj","task":"big-task"}`,
		`{"project":"test-proj","task":"big-task","include_content":true}`,
	} {
		gr := getTaskCall(t, vault, params)
		if gr.Content != body {
			t.Errorf("params %s: Content not byte-identical to full body (len %d, want %d)",
				params, len(gr.Content), len(body))
		}
		if gr.Excerpt != "" {
			t.Errorf("params %s: Excerpt = %q, want empty when content included", params, gr.Excerpt)
		}
	}
}

// TestGetTaskExcludeContentReturnsExcerpt pins the slim path: include_content=false
// drops the inline Content for a bounded, rune-safe Excerpt that is a genuine
// leading slice of the body and never exceeds the excerpt cap.
func TestGetTaskExcludeContentReturnsExcerpt(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	_, body := makeBigTask(t, vault, "test-proj", "big-task")

	gr := getTaskCall(t, vault, `{"project":"test-proj","task":"big-task","include_content":false}`)

	if gr.Content != "" {
		t.Errorf("Content non-empty (len %d), want empty when include_content=false", len(gr.Content))
	}
	if gr.Excerpt == "" {
		t.Fatal("Excerpt should be non-empty when content is dropped")
	}
	if !utf8.ValidString(gr.Excerpt) {
		t.Error("Excerpt is not valid UTF-8")
	}
	// Bound read from the production constant, not a hardcoded guess.
	if len(gr.Excerpt) > taskExcerptCap {
		t.Errorf("Excerpt len %d exceeds taskExcerptCap %d", len(gr.Excerpt), taskExcerptCap)
	}
	// The excerpt is a true leading slice of the body (runeSafeExcerpt may cut
	// back to the last newline within the cap, which keeps it a prefix).
	if !strings.HasPrefix(body, gr.Excerpt) {
		t.Errorf("Excerpt is not a prefix of the body:\n excerpt head %q", gr.Excerpt[:min(60, len(gr.Excerpt))])
	}
	// The body was big enough that the excerpt is genuinely shorter.
	if len(gr.Excerpt) >= len(body) {
		t.Errorf("Excerpt len %d not shorter than body len %d — excerpt path not exercised", len(gr.Excerpt), len(body))
	}
}

// TestGetTaskExcludeContentSmallBodyStaysInline pins the fix for the wasted-fetch
// case: a body that already fits within taskExcerptCap cannot truncate, so even
// with include_content=false it is returned whole in Content (not as a partial
// Excerpt), sparing the agent a vp_read_resource round-trip.
func TestGetTaskExcludeContentSmallBodyStaysInline(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("test-proj", "small-task", "Small", "a tiny body", "low"); err != nil {
		t.Fatal(err)
	}
	_, full, err := vault.GetTask("test-proj", "small-task")
	if err != nil {
		t.Fatal(err)
	}
	if len(full) > taskExcerptCap {
		t.Fatalf("fixture too large: %d > cap %d", len(full), taskExcerptCap)
	}

	gr := getTaskCall(t, vault, `{"project":"test-proj","task":"small-task","include_content":false}`)
	if gr.Content != full {
		t.Errorf("small body not returned inline: got Content len %d, want full len %d", len(gr.Content), len(full))
	}
	if gr.Excerpt != "" {
		t.Errorf("Excerpt should be empty for a complete small body, got %q", gr.Excerpt)
	}
	if gr.ContentURI == "" || gr.ContentSize != len(full) {
		t.Errorf("ContentURI/ContentSize must still be set: uri=%q size=%d want %d", gr.ContentURI, gr.ContentSize, len(full))
	}
}
