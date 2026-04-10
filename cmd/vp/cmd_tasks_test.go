// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestRunTasksEmpty(t *testing.T) {
	v := testVault(t)
	var buf bytes.Buffer
	code := runTasks(v, "test-proj", false, false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(buf.String(), "No tasks found") {
		t.Errorf("expected no tasks message: %s", buf.String())
	}
}

func TestRunTasksWithData(t *testing.T) {
	v := testVault(t)
	v.CreateTask("test-proj", "fix-bug", "Fix the login bug", "Details here.", "high")
	v.CreateTask("test-proj", "add-feature", "Add search feature", "More details.", "low")

	var buf bytes.Buffer
	code := runTasks(v, "test-proj", false, false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "PRIORITY") {
		t.Error("missing header")
	}
	if !strings.Contains(out, "fix-bug") {
		t.Error("missing task 1")
	}
	if !strings.Contains(out, "add-feature") {
		t.Error("missing task 2")
	}
	if !strings.Contains(out, "high") {
		t.Error("missing priority")
	}
}

func TestRunTasksJSON(t *testing.T) {
	v := testVault(t)
	v.CreateTask("test-proj", "my-task", "My Task", "content", "P1")

	var buf bytes.Buffer
	code := runTasks(v, "test-proj", false, true, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var tasks []storage.TaskMeta
	if err := json.Unmarshal(buf.Bytes(), &tasks); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Slug != "my-task" {
		t.Errorf("slug = %q", tasks[0].Slug)
	}
}

func TestRunTasksIncludeDone(t *testing.T) {
	v := testVault(t)
	v.CreateTask("test-proj", "active-task", "Active", "content", "high")
	v.CreateTask("test-proj", "done-task", "Done", "content", "low")
	v.RetireTask("test-proj", "done-task")

	// Without --done: only active tasks.
	var buf bytes.Buffer
	runTasks(v, "test-proj", false, false, &buf)
	if strings.Contains(buf.String(), "done-task") {
		t.Error("retired task should not appear without --done")
	}

	// With --done: both.
	buf.Reset()
	runTasks(v, "test-proj", true, false, &buf)
	if !strings.Contains(buf.String(), "active-task") {
		t.Error("active task should appear")
	}
	if !strings.Contains(buf.String(), "done-task") {
		t.Error("done task should appear with --done")
	}
}

func TestRunTasksSlugTruncation(t *testing.T) {
	v := testVault(t)
	longSlug := "a-very-long-task-slug-that-exceeds-the-column-width-limit"
	v.CreateTask("test-proj", longSlug, "Long Slug Task", "content", "")

	var buf bytes.Buffer
	runTasks(v, "test-proj", false, false, &buf)
	if !strings.Contains(buf.String(), "...") {
		t.Error("expected slug truncation with ellipsis")
	}
}
