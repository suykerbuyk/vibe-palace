// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
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

// carriedSlug/taskSlug zero-pad their index: an unpadded "item-1" is a substring
// of "item-10".."item-19", which would make both the removal and the containment
// assertions below silently wrong.
func carriedSlug(i int) string { return fmt.Sprintf("item-%03d", i) }
func taskSlug(i int) string    { return fmt.Sprintf("promoted-%03d", i) }

// seedCarriedBullets writes a resume fixture with n carried bullets plus some
// unrelated content, and returns its absolute path.
func seedCarriedBullets(t *testing.T, vault *storage.Vault, project string, n int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("# Resume\n\n## Open Threads\n\n### Carried forward\n\n")
	for i := range n {
		fmt.Fprintf(&b, "- **%s** — carried item %03d\n", carriedSlug(i), i)
	}
	b.WriteString("\n### alpha\n\nalpha body\n\n## Project History\n\nhistory\n")
	return writeResumeFixture(t, vault, project, b.String())
}

// TestCarriedPromoteToTask_ConcurrentPromotesAllSurvive is the acceptance test
// for vp_carried_promote_to_task's read-modify-write of resume.md. The handler
// used to read resume.md, create the task, and then write back the STALE content
// it had read — through a lock-free atomicfile.Write — so two concurrent promotes
// would each drop the other's bullet removal (and any concurrent edit) on the
// floor. It now creates the task first (task lock taken and released inside
// CreateTask) and then removes the bullet under the resume lock via EditResume:
// two sequential, never-nested locks.
//
// N goroutines each promote a DISTINCT carried bullet. Every promote that
// returned nil MUST have created its task file AND removed its bullet from the
// final on-disk resume, and the unrelated resume content must survive. The race
// detector cannot see this — a file-level lost update is not a memory data race —
// so the final file content is the only detector. If this test hangs, a lock has
// been nested or re-entered (vaultlock.Acquire is a blocking LOCK_EX with no
// timeout).
func TestCarriedPromoteToTask_ConcurrentPromotesAllSurvive(t *testing.T) {
	const n = 32
	vault := newVaultRoot(t)
	abs := seedCarriedBullets(t, vault, "proj", n)

	tool := CarriedPromoteToTaskTool(vault)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			p, _ := json.Marshal(map[string]any{
				"project":       "proj",
				"slug":          carriedSlug(i),
				"new_task_slug": taskSlug(i),
			})
			_, errs[i] = tool.Handler(context.Background(), p)
		})
	}
	wg.Wait()

	final := readFile(t, abs)
	lost := 0
	for i, err := range errs {
		if err != nil {
			t.Errorf("promote %s failed: %v", carriedSlug(i), err)
			continue
		}
		if _, _, err := vault.GetTask("proj", taskSlug(i)); err != nil {
			t.Errorf("promote %s reported success but task %s is missing: %v", carriedSlug(i), taskSlug(i), err)
		}
		if strings.Contains(final, "**"+carriedSlug(i)+"**") {
			lost++
			t.Errorf("lost update: %s reported success but its bullet is still in the final resume", carriedSlug(i))
		}
	}
	if lost > 0 {
		t.Errorf("%d of %d concurrent promotes were lost", lost, n)
	}
	if !strings.Contains(final, "### alpha") || !strings.Contains(final, "history") {
		t.Error("pre-existing content clobbered")
	}
}

// TestCarriedPromoteToTask_ConcurrentWithThreadInsert races promotes against
// vp_thread_insert on the SAME resume.md. Both editors take the same per-path
// lock through EditResume, so every insert and every bullet removal must survive.
func TestCarriedPromoteToTask_ConcurrentWithThreadInsert(t *testing.T) {
	const n = 16
	vault := newVaultRoot(t)
	abs := seedCarriedBullets(t, vault, "proj", n)

	promote := CarriedPromoteToTaskTool(vault)
	insert := ThreadInsertTool(vault)
	threadSlug := func(i int) string { return fmt.Sprintf("thread-%03d", i) }

	promoteErrs := make([]error, n)
	insertErrs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			p, _ := json.Marshal(map[string]any{
				"project":       "proj",
				"slug":          carriedSlug(i),
				"new_task_slug": taskSlug(i),
			})
			_, promoteErrs[i] = promote.Handler(context.Background(), p)
		})
		wg.Go(func() {
			p, _ := json.Marshal(map[string]any{
				"project":  "proj",
				"position": map[string]any{"mode": "bottom"},
				"slug":     threadSlug(i),
				"body":     "body " + threadSlug(i),
			})
			_, insertErrs[i] = insert.Handler(context.Background(), p)
		})
	}
	wg.Wait()

	final := readFile(t, abs)
	for i := range n {
		if err := promoteErrs[i]; err != nil {
			t.Errorf("promote %s failed: %v", carriedSlug(i), err)
		} else if strings.Contains(final, "**"+carriedSlug(i)+"**") {
			t.Errorf("lost update: promoted bullet %s is still in the final resume", carriedSlug(i))
		}
		if err := insertErrs[i]; err != nil {
			t.Errorf("insert %s failed: %v", threadSlug(i), err)
			continue
		}
		if got := strings.Count(final, "### "+threadSlug(i)); got != 1 {
			t.Errorf("insert %s appears %d times in the final resume, want 1", threadSlug(i), got)
		}
	}
	if !strings.Contains(final, "### alpha") || !strings.Contains(final, "history") {
		t.Error("pre-existing content clobbered")
	}
}
