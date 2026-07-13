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
	code := runTasks(v, "test-proj", taskListOpts{flat: true}, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(buf.String(), "No tasks found") {
		t.Errorf("expected no tasks message: %s", buf.String())
	}
}

func TestRunTasksWithData(t *testing.T) {
	v := testVault(t)
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "fix-bug", Title: "Fix the login bug", Content: "Details here.", Priority: "high"})
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "add-feature", Title: "Add search feature", Content: "More details.", Priority: "low"})

	var buf bytes.Buffer
	code := runTasks(v, "test-proj", taskListOpts{flat: true}, &buf)
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
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "my-task", Title: "My Task", Content: "content", Priority: "P1"})

	var buf bytes.Buffer
	code := runTasks(v, "test-proj", taskListOpts{flat: true, asJSON: true}, &buf)
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
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "active-task", Title: "Active", Content: "content", Priority: "high"})
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "done-task", Title: "Done", Content: "content", Priority: "low"})
	v.RetireTask("test-proj", "done-task")

	// Without --done: only active tasks.
	var buf bytes.Buffer
	runTasks(v, "test-proj", taskListOpts{flat: true}, &buf)
	if strings.Contains(buf.String(), "done-task") {
		t.Error("retired task should not appear without --done")
	}

	// With --done: both.
	buf.Reset()
	runTasks(v, "test-proj", taskListOpts{flat: true, includeDone: true}, &buf)
	if !strings.Contains(buf.String(), "active-task") {
		t.Error("active task should appear")
	}
	if !strings.Contains(buf.String(), "done-task") {
		t.Error("done task should appear with --done")
	}
}

// A slug is an IDENTIFIER: it is what you type into vp_get_task. Truncating it
// to "a-very-long-task-slug-that-exce..." makes the one column that has to be
// copy-pasteable useless, and the old byte-based cut could split a rune in half
// besides. Both views measure and pad instead.
func TestRunTasksNeverTruncatesSlugs(t *testing.T) {
	v := testVault(t)
	longSlug := "a-very-long-task-slug-that-exceeds-the-column-width-limit"
	v.CreateTask("test-proj", storage.TaskSpec{Slug: longSlug, Title: "Long Slug Task", Content: "content", Priority: ""})

	for _, tc := range []struct {
		name string
		opts taskListOpts
	}{
		{"flat", taskListOpts{flat: true}},
		{"tree", taskListOpts{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			runTasks(v, "test-proj", tc.opts, &buf)
			out := buf.String()
			if !strings.Contains(out, longSlug) {
				t.Errorf("slug was truncated; want the full %q in:\n%s", longSlug, out)
			}
			if strings.Contains(out, "...") || strings.Contains(out, "…") {
				t.Errorf("output carries an ellipsis — nothing may be truncated:\n%s", out)
			}
		})
	}
}

func TestRunTasksTreeGroupsByEpicAndShowsBlockers(t *testing.T) {
	v := testVault(t)
	mk := func(slug, pri string) {
		v.CreateTask("test-proj", storage.TaskSpec{
			Slug: slug, Title: slug, Content: "body", Priority: pri,
		})
	}
	mk("big-epic", "high")
	mk("first-step", "high")
	mk("second-step", "high")
	mk("unrelated", "low")

	parent := "big-epic"
	deps := []string{"first-step"}
	if err := v.SetTaskRelations("test-proj", "first-step", storage.TaskRelations{Parent: &parent}); err != nil {
		t.Fatal(err)
	}
	if err := v.SetTaskRelations("test-proj", "second-step", storage.TaskRelations{Parent: &parent, Depends: &deps}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if code := runTasks(v, "test-proj", taskListOpts{}, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()

	if !strings.Contains(out, "EPIC  big-epic") {
		t.Errorf("epic header missing:\n%s", out)
	}
	if !strings.Contains(out, "STANDALONE") || !strings.Contains(out, "unrelated") {
		t.Errorf("standalone bucket missing:\n%s", out)
	}
	if !strings.Contains(out, "[blocked by: first-step]") {
		t.Errorf("blocker annotation missing:\n%s", out)
	}
	// A dependency must be listed above the task it blocks.
	if strings.Index(out, "first-step") > strings.Index(out, "second-step") {
		t.Errorf("dependency listed after its dependent:\n%s", out)
	}
}

func TestRunTasksTreeHidesIceboxButSaysHowMany(t *testing.T) {
	v := testVault(t)
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "hot", Title: "Hot", Content: "body", Priority: "high"})
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "cold", Title: "Cold", Content: "body", Priority: "low"})
	if err := v.UpdateTaskStatus("test-proj", "cold", storage.StatusIcebox); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	runTasks(v, "test-proj", taskListOpts{}, &buf)
	out := buf.String()
	if strings.Contains(out, "cold") {
		t.Errorf("iceboxed task must be hidden by default:\n%s", out)
	}
	// Hidden, but NEVER silently: an icebox nobody is told about is a deletion.
	if !strings.Contains(out, "1 iceboxed") {
		t.Errorf("hidden count missing:\n%s", out)
	}

	buf.Reset()
	runTasks(v, "test-proj", taskListOpts{includeIcebox: true}, &buf)
	if !strings.Contains(buf.String(), "cold") {
		t.Errorf("--all must show the icebox:\n%s", buf.String())
	}
}
