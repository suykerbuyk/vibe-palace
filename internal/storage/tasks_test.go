// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCreateAndGetTask(t *testing.T) {
	v := testVault(t)
	err := v.CreateTask("proj", "my-task", "My Task Title", "Some content here.", "P1")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	meta, body, err := v.GetTask("proj", "my-task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if meta.Slug != "my-task" {
		t.Errorf("Slug = %q, want %q", meta.Slug, "my-task")
	}
	if meta.Title != "My Task Title" {
		t.Errorf("Title = %q, want %q", meta.Title, "My Task Title")
	}
	if meta.Status != "pending" {
		t.Errorf("Status = %q, want %q", meta.Status, "pending")
	}
	if meta.Priority != "P1" {
		t.Errorf("Priority = %q, want %q", meta.Priority, "P1")
	}
	if meta.Done {
		t.Error("Done should be false for active task")
	}
	if body == "" {
		t.Error("body should not be empty")
	}
}

func TestCreateTaskDuplicate(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", "my-task", "Title", "", "P1"); err != nil {
		t.Fatal(err)
	}
	err := v.CreateTask("proj", "my-task", "Title 2", "", "P2")
	if err == nil {
		t.Error("creating duplicate task should return error")
	}
}

func TestCreateTaskInvalidSlug(t *testing.T) {
	v := testVault(t)
	err := v.CreateTask("proj", "BAD SLUG", "Title", "", "P1")
	if err == nil {
		t.Error("CreateTask with invalid slug should return error")
	}
}

func TestGetTaskNotFound(t *testing.T) {
	v := testVault(t)
	_, _, err := v.GetTask("proj", "nonexistent")
	if err == nil {
		t.Error("GetTask for nonexistent task should return error")
	}
}

func TestListTasks(t *testing.T) {
	v := testVault(t)
	v.CreateTask("proj", "task-a", "Task A", "", "P1")
	v.CreateTask("proj", "task-b", "Task B", "", "P2")

	got, err := v.ListTasks("proj", false)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListTasks returned %d, want 2", len(got))
	}
}

func TestListTasksEmpty(t *testing.T) {
	v := testVault(t)
	got, err := v.ListTasks("proj", false)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(got))
	}
}

func TestListTasksIncludeDone(t *testing.T) {
	v := testVault(t)
	v.CreateTask("proj", "active", "Active", "", "P1")
	v.CreateTask("proj", "to-retire", "To Retire", "", "P1")
	v.CreateTask("proj", "to-cancel", "To Cancel", "", "P1")

	v.RetireTask("proj", "to-retire")
	v.CancelTask("proj", "to-cancel")

	active, err := v.ListTasks("proj", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Errorf("active tasks = %d, want 1", len(active))
	}

	all, err := v.ListTasks("proj", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("all tasks = %d, want 3", len(all))
	}
}

func TestUpdateTaskStatus(t *testing.T) {
	v := testVault(t)
	v.CreateTask("proj", "my-task", "Title", "", "P1")

	if err := v.UpdateTaskStatus("proj", "my-task", "in_progress"); err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}

	meta, _, err := v.GetTask("proj", "my-task")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != "in_progress" {
		t.Errorf("Status = %q, want %q", meta.Status, "in_progress")
	}
}

func TestUpdateTaskStatusInvalid(t *testing.T) {
	v := testVault(t)
	v.CreateTask("proj", "my-task", "Title", "", "P1")

	err := v.UpdateTaskStatus("proj", "my-task", "invalid_status")
	if err == nil {
		t.Error("UpdateTaskStatus with invalid status should return error")
	}
}

func TestRetireTask(t *testing.T) {
	v := testVault(t)
	v.CreateTask("proj", "my-task", "Title", "", "P1")

	if err := v.RetireTask("proj", "my-task"); err != nil {
		t.Fatalf("RetireTask: %v", err)
	}

	// Should not be in active dir.
	activePath, _ := v.TaskFile("proj", "my-task")
	if _, err := os.Stat(activePath); !os.IsNotExist(err) {
		t.Error("retired task should not exist in active dir")
	}

	// Should be findable via GetTask (in done/).
	meta, _, err := v.GetTask("proj", "my-task")
	if err != nil {
		t.Fatalf("GetTask after retire: %v", err)
	}
	if !meta.Done {
		t.Error("Done should be true for retired task")
	}
	if meta.Status != "retired" {
		t.Errorf("Status = %q, want %q", meta.Status, "retired")
	}

	// Should exist in done dir.
	doneDir, _ := v.TaskDoneDir("proj")
	donePath := filepath.Join(doneDir, "my-task.md")
	if _, err := os.Stat(donePath); err != nil {
		t.Errorf("retired task should exist in done dir: %v", err)
	}
}

func TestRetireTaskAlreadyRetired(t *testing.T) {
	v := testVault(t)
	v.CreateTask("proj", "my-task", "Title", "", "P1")
	v.RetireTask("proj", "my-task")

	err := v.RetireTask("proj", "my-task")
	if err == nil {
		t.Error("retiring already-retired task should return error")
	}
}

func TestCancelTask(t *testing.T) {
	v := testVault(t)
	v.CreateTask("proj", "my-task", "Title", "", "P1")

	if err := v.CancelTask("proj", "my-task"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	meta, _, err := v.GetTask("proj", "my-task")
	if err != nil {
		t.Fatalf("GetTask after cancel: %v", err)
	}
	if !meta.Done {
		t.Error("Done should be true for cancelled task")
	}
	if meta.Status != "cancelled" {
		t.Errorf("Status = %q, want %q", meta.Status, "cancelled")
	}
}

func TestCancelTaskAlreadyCancelled(t *testing.T) {
	v := testVault(t)
	v.CreateTask("proj", "my-task", "Title", "", "P1")
	v.CancelTask("proj", "my-task")

	err := v.CancelTask("proj", "my-task")
	if err == nil {
		t.Error("cancelling already-cancelled task should return error")
	}
}

func TestTaskFileContent(t *testing.T) {
	v := testVault(t)
	v.CreateTask("proj", "my-task", "My Title", "## Details\n\nSome details.", "P0")

	path, _ := v.TaskFile("proj", "my-task")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !contains(content, "# My Title") {
		t.Error("file should contain title heading")
	}
	if !contains(content, "**Status:** pending") {
		t.Error("file should contain status line")
	}
	if !contains(content, "**Priority:** P0") {
		t.Error("file should contain priority line")
	}
	if !contains(content, "## Details") {
		t.Error("file should contain custom content")
	}
}

// TestCreateTask_ConcurrentSameSlugExactlyOneWins is the acceptance test for
// CreateTask's TOCTOU. The existence check used to sit OUTSIDE the per-path
// vaultlock (os.Stat, then lockedWrite), so two concurrent creates of the same
// slug could both pass the stat and the later write would silently overwrite the
// earlier task — breaking the documented "hard error if the task already exists"
// contract and losing whatever the first creator wrote.
//
// N goroutines create the SAME slug with DISTINCT bodies. Exactly one must
// succeed; the other N-1 must fail with the already-exists error; and the file on
// disk must be byte-for-byte the winner's content (not a torn or overwritten mix).
// The race detector cannot see this — a file-level overwrite is not a memory data
// race — so the on-disk bytes are the only detector.
func TestCreateTask_ConcurrentSameSlugExactlyOneWins(t *testing.T) {
	const n = 32
	v := testVault(t)

	// Pre-create the tasks dir so every goroutine races on the task file only.
	if err := v.CreateTask("proj", "seed", "Seed", "", "medium"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	body := func(i int) string { return fmt.Sprintf("body from creator %03d", i) }
	title := func(i int) string { return fmt.Sprintf("Title %03d", i) }

	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			errs[i] = v.CreateTask("proj", "contended", title(i), body(i), "medium")
		})
	}
	wg.Wait()

	winners := []int{}
	for i, err := range errs {
		if err == nil {
			winners = append(winners, i)
			continue
		}
		if !strings.Contains(err.Error(), `task "contended" already exists in project "proj"`) {
			t.Errorf("creator %03d: unexpected error %v", i, err)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("expected exactly 1 successful CreateTask, got %d: %v", len(winners), winners)
	}

	path, err := v.TaskFile("proj", "contended")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read task: %v", err)
	}
	w := winners[0]
	want := fmt.Sprintf("# %s\n\n**Status:** pending\n**Priority:** medium\n\n%s\n", title(w), body(w))
	if string(data) != want {
		t.Errorf("task file is not the winner's content verbatim (torn or overwritten)\n got: %q\nwant: %q", data, want)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstr(s, substr)
}

func searchSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
