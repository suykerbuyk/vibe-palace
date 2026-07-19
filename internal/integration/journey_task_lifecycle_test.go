// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJourney_Task_Lifecycle exercises the task-management flow a wrapping
// agent follows: create a task, list to confirm, update its status, retire
// it, and append an iteration capturing the work — all through the MCP
// tool surface.
//
// Cross-tool consistency assertions:
//   - After create, vp_list_tasks returns the new slug in the active set.
//   - After retire, the task no longer appears in the default (active)
//     list, but is still reachable with include_done=true AND its file
//     lives under tasks/done/<slug>.md on disk.
//   - vp_append_iteration persists to the same iterations.md the vault
//     exposes via Vault.IterationsFile — proving the tool and storage
//     agree on the path.
func TestJourney_Task_Lifecycle(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	const (
		project  = "journey-task"
		taskSlug = "ship-journey-tests"
	)

	// 1. create.
	raw := h.callTool(t, "vp_manage_task", map[string]any{
		"project":  project,
		"action":   "create",
		"task":     taskSlug,
		"title":    "Ship Journey Tests",
		"content":  taskBody("Write multi-tool user-journey integration tests."),
		"priority": "high",
	})
	if !strings.Contains(raw, "created") {
		t.Fatalf("create: %s", raw)
	}

	// 2. list_tasks: slug in active set.
	raw = h.callTool(t, "vp_list_tasks", map[string]any{"project": project})
	var listed struct {
		Tasks []struct {
			Slug string `json:"slug"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(raw), &listed); err != nil {
		t.Fatalf("list parse: %v (%s)", err, raw)
	}
	found := false
	for _, t := range listed.Tasks {
		if t.Slug == taskSlug {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("list_tasks missing active slug %q: %s", taskSlug, raw)
	}

	// 3. update_status -> in_progress. (Plan calls this "update".)
	raw = h.callTool(t, "vp_manage_task", map[string]any{
		"project": project,
		"action":  "update_status",
		"task":    taskSlug,
		"status":  "in_progress",
	})
	if !strings.Contains(raw, "in_progress") {
		t.Fatalf("update_status: %s", raw)
	}

	// 4. retire. Requires the agent's own approved_by_human attestation —
	// friction on the default path, not an authorization check.
	raw = h.callTool(t, "vp_manage_task", map[string]any{
		"project":           project,
		"action":            "retire",
		"task":              taskSlug,
		"approved_by_human": true,
	})
	if !strings.Contains(raw, "retired") {
		t.Fatalf("retire: %s", raw)
	}

	// Active list must no longer include the slug.
	raw = h.callTool(t, "vp_list_tasks", map[string]any{"project": project})
	if strings.Contains(raw, taskSlug) {
		t.Errorf("list_tasks (active) still contains retired slug: %s", raw)
	}
	// With include_done, it must reappear.
	raw = h.callTool(t, "vp_list_tasks", map[string]any{
		"project":      project,
		"include_done": true,
	})
	if !strings.Contains(raw, taskSlug) {
		t.Errorf("list_tasks (include_done) missing retired slug: %s", raw)
	}

	// Filesystem consistency: tasks/done/<slug>.md exists.
	doneDir, err := h.Vault.TaskDoneDir(project)
	if err != nil {
		t.Fatalf("TaskDoneDir: %v", err)
	}
	donePath := filepath.Join(doneDir, taskSlug+".md")
	if _, err := os.Stat(donePath); err != nil {
		t.Errorf("retired task file not at %s: %v", donePath, err)
	}

	// 5. vp_append_iteration: record the wrap-up narrative. The SERVER mints the
	// number under the file lock and composes the canonical "## Iteration N —
	// title" header itself (invisible-header drift is impossible now that no
	// caller supplies the number); the caller passes only title and narrative.
	raw = h.callTool(t, "vp_append_iteration", map[string]any{
		"project":   project,
		"title":     "journey",
		"narrative": "Retired task " + taskSlug + " after shipping journey tests.",
	})
	if !strings.Contains(raw, "appended") {
		t.Fatalf("append_iteration: %s", raw)
	}

	// Verify iterations.md on disk matches what the tool wrote.
	iterPath, err := h.Vault.IterationsFile(project)
	if err != nil {
		t.Fatalf("IterationsFile: %v", err)
	}
	data, err := os.ReadFile(iterPath)
	if err != nil {
		t.Fatalf("read iterations: %v", err)
	}
	if !strings.Contains(string(data), "Retired task "+taskSlug) {
		t.Errorf("iterations.md missing expected content:\n%s", data)
	}
}
