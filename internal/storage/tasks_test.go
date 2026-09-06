// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

func TestCreateAndGetTask(t *testing.T) {
	v := testVault(t)
	err := v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "My Task Title", Content: "Some content here.", Priority: "P1"})
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
	if err := v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "Title", Content: "", Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	err := v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "Title 2", Content: "", Priority: "P2"})
	if err == nil {
		t.Error("creating duplicate task should return error")
	}
}

// A retired task is still a task: creating a slug that already lives in done/
// must be refused, not silently duplicated into the active dir (which would then
// clobber the historical record on the next retire).
func TestCreateTaskOverRetiredSlug(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "Title", Content: "", Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	if err := v.RetireTask("proj", "my-task"); err != nil {
		t.Fatalf("RetireTask: %v", err)
	}

	err := v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "Title 2", Content: "", Priority: "P2"})
	if err == nil {
		t.Fatal("creating over a retired slug should be refused")
	}
	if !strings.Contains(err.Error(), "done/") {
		t.Errorf("error should name the done/ state, got: %v", err)
	}

	// No duplicate should have been written to the active dir.
	activePath, _ := v.TaskFile("proj", "my-task")
	if _, statErr := os.Stat(activePath); !os.IsNotExist(statErr) {
		t.Error("refused create must not leave a duplicate in the active dir")
	}
}

// Same guard for the cancelled/ directory.
func TestCreateTaskOverCancelledSlug(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "Title", Content: "", Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	if err := v.CancelTask("proj", "my-task"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	err := v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "Title 2", Content: "", Priority: "P2"})
	if err == nil {
		t.Fatal("creating over a cancelled slug should be refused")
	}
	if !strings.Contains(err.Error(), "cancelled/") {
		t.Errorf("error should name the cancelled/ state, got: %v", err)
	}
}

// A novel slug that collides with nothing in any of the three dirs still creates.
func TestCreateTaskNovelSlugAfterRetire(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{Slug: "old-task", Title: "Title", Content: "", Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	if err := v.RetireTask("proj", "old-task"); err != nil {
		t.Fatalf("RetireTask: %v", err)
	}
	if err := v.CreateTask("proj", TaskSpec{Slug: "new-task", Title: "Title", Content: "", Priority: "P1"}); err != nil {
		t.Fatalf("novel slug should still create: %v", err)
	}
}

// The actual data-loss mechanism: a re-retire whose destination already holds a
// historical done/ record must error loudly and leave the prior record intact,
// never overwrite it.
func TestRetireDoesNotClobberExistingDoneRecord(t *testing.T) {
	v := testVault(t)

	// Plant a historical done/ record with distinctive bytes.
	doneDir, _ := v.TaskDoneDir("proj")
	if err := EnsureDir(doneDir); err != nil {
		t.Fatal(err)
	}
	donePath := filepath.Join(doneDir, "my-task.md")
	historical := []byte("# My Task\n\n**Status:** retired\n\nHISTORICAL RECORD iter-209\n")
	if err := os.WriteFile(donePath, historical, 0o644); err != nil {
		t.Fatal(err)
	}

	// An active duplicate of the same slug (as could arise from a create that
	// slipped past the guard on an older binary).
	activePath, _ := v.TaskFile("proj", "my-task")
	if err := EnsureDir(filepath.Dir(activePath)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(activePath, []byte("# My Task\n\n**Status:** pending\n\nDUPLICATE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := v.RetireTask("proj", "my-task")
	if err == nil {
		t.Fatal("re-retire over an existing done/ record should be refused")
	}
	if !strings.Contains(err.Error(), "overwrite") {
		t.Errorf("error should explain it refuses to overwrite, got: %v", err)
	}

	// The historical record must be byte-for-byte intact.
	got, readErr := os.ReadFile(donePath)
	if readErr != nil {
		t.Fatalf("read done record: %v", readErr)
	}
	if string(got) != string(historical) {
		t.Errorf("historical done/ record was clobbered:\ngot:  %q\nwant: %q", got, historical)
	}
	// And the active duplicate must not have been removed (the move failed).
	if _, statErr := os.Stat(activePath); statErr != nil {
		t.Errorf("failed move should leave the source in place: %v", statErr)
	}
}

func TestCreateTaskInvalidSlug(t *testing.T) {
	v := testVault(t)
	err := v.CreateTask("proj", TaskSpec{Slug: "BAD SLUG", Title: "Title", Content: "", Priority: "P1"})
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
	v.CreateTask("proj", TaskSpec{Slug: "task-a", Title: "Task A", Content: "", Priority: "P1"})
	v.CreateTask("proj", TaskSpec{Slug: "task-b", Title: "Task B", Content: "", Priority: "P2"})

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
	v.CreateTask("proj", TaskSpec{Slug: "active", Title: "Active", Content: "", Priority: "P1"})
	v.CreateTask("proj", TaskSpec{Slug: "to-retire", Title: "To Retire", Content: "", Priority: "P1"})
	v.CreateTask("proj", TaskSpec{Slug: "to-cancel", Title: "To Cancel", Content: "", Priority: "P1"})

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
	v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "Title", Content: "", Priority: "P1"})

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
	v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "Title", Content: "", Priority: "P1"})

	err := v.UpdateTaskStatus("proj", "my-task", "invalid_status")
	if err == nil {
		t.Error("UpdateTaskStatus with invalid status should return error")
	}
}

func TestRetireTask(t *testing.T) {
	v := testVault(t)
	v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "Title", Content: "", Priority: "P1"})

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
	v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "Title", Content: "", Priority: "P1"})
	v.RetireTask("proj", "my-task")

	err := v.RetireTask("proj", "my-task")
	if err == nil {
		t.Error("retiring already-retired task should return error")
	}
}

func TestCancelTask(t *testing.T) {
	v := testVault(t)
	v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "Title", Content: "", Priority: "P1"})

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
	v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "Title", Content: "", Priority: "P1"})
	v.CancelTask("proj", "my-task")

	err := v.CancelTask("proj", "my-task")
	if err == nil {
		t.Error("cancelling already-cancelled task should return error")
	}
}

func TestTaskFileContent(t *testing.T) {
	v := testVault(t)
	v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "My Title", Content: "## Details\n\nSome details.", Priority: "P0"})

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
	if err := v.CreateTask("proj", TaskSpec{Slug: "seed", Title: "Seed", Content: "", Priority: "medium"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	body := func(i int) string { return fmt.Sprintf("body from creator %03d", i) }
	title := func(i int) string { return fmt.Sprintf("Title %03d", i) }

	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			errs[i] = v.CreateTask("proj", TaskSpec{Slug: "contended", Title: title(i), Content: body(i), Priority: "medium"})
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
	// The conventional first H2 is part of what CreateTask writes; build the
	// expectation from the same constant the writer uses so this test pins the
	// no-torn-write property rather than the heading's spelling.
	want := fmt.Sprintf("# %s\n\n**Status:** pending\n**Priority:** medium\n\n## %s\n\n%s\n",
		title(w), ConventionalFirstHeading, body(w))
	if string(data) != want {
		t.Errorf("task file is not the winner's content verbatim (torn or overwritten)\n got: %q\nwant: %q", data, want)
	}
}

// TestCreateTaskRejectsStatusLineInContent pins the door CreateTask now closes.
// Agent-written plan bodies idiomatically open with their own metadata block;
// CreateTask used to staple that under its OWN header verbatim, producing a file
// with two **Status:** lines. The writer then rewrote the first and the reader
// reported the last, so a task could read back "pending" forever.
func TestCreateTaskRejectsStatusLineInContent(t *testing.T) {
	v := testVault(t)
	content := "**Status:** in_progress\n**Priority:** high\n\n## Plan\n\nDo the thing."

	err := v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "My Task", Content: content, Priority: "high"})
	if err == nil {
		t.Fatal("CreateTask with a **Status:** line in content should return error")
	}
	if !strings.Contains(err.Error(), "status line") {
		t.Errorf("error should name the offending construct, got: %v", err)
	}
	if !strings.Contains(err.Error(), "strip the leading metadata block") {
		t.Errorf("error should be actionable, got: %v", err)
	}

	// Rejection must not leave a partial task behind.
	if _, _, err := v.GetTask("proj", "my-task"); err == nil {
		t.Error("rejected CreateTask should not have written a task file")
	}
}

func TestCreateTaskRejectsH1InContent(t *testing.T) {
	v := testVault(t)
	content := "# My Task\n\nSome body text."

	err := v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "My Task", Content: content, Priority: "high"})
	if err == nil {
		t.Fatal("CreateTask with an H1 in content should return error")
	}
	if !strings.Contains(err.Error(), "H1 heading") {
		t.Errorf("error should name the offending construct, got: %v", err)
	}
	if !strings.Contains(err.Error(), "strip the leading metadata block") {
		t.Errorf("error should be actionable, got: %v", err)
	}
}

// TestCreateTaskCleanBodyHasSingleHeader is the positive half: a body with no
// metadata of its own is accepted and yields EXACTLY one H1 and one status line.
func TestCreateTaskCleanBodyHasSingleHeader(t *testing.T) {
	v := testVault(t)
	content := "## Plan\n\nStep one.\n\n### Notes\n\nA `**Status:**` inline mention is not a status line."

	if err := v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "My Task", Content: content, Priority: "high"}); err != nil {
		t.Fatalf("CreateTask with a clean body: %v", err)
	}

	_, body, err := v.GetTask("proj", "my-task")
	if err != nil {
		t.Fatal(err)
	}
	var h1s, statuses int
	for line := range strings.SplitSeq(body, "\n") {
		if isH1Line(line) {
			h1s++
		}
		if isStatusLine(line) {
			statuses++
		}
	}
	if h1s != 1 {
		t.Errorf("H1 lines = %d, want 1\n%s", h1s, body)
	}
	if statuses != 1 {
		t.Errorf("**Status:** lines = %d, want 1\n%s", statuses, body)
	}
}

// TestCreateTaskAcceptsMetadataShapesInsideCodeFences pins the OTHER half of the
// validator's contract, and the one that matters most in practice: task plan
// bodies routinely carry shell/TOML/Python snippets, where "# Usage: ..." is a
// COMMENT, not a heading. A line-shape-only validator false-positives on those
// and hard-fails create on a body shape this project writes constantly. Inside a
// fence, nothing is metadata.
func TestCreateTaskAcceptsMetadataShapesInsideCodeFences(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "H1-shaped shell comments inside a backtick fence",
			content: "Run the rig:\n\n```bash\n# Verify pure Go\ngo build ./...\n# Usage: vp tasks list\n```\n\nDone.",
		},
		{
			name:    "H1-shaped comments inside a tilde fence",
			content: "Config:\n\n~~~toml\n# adaptive room classification\nthreshold = 0.5\n~~~\n\nDone.",
		},
		{
			name:    "a **Status:** line inside a fence is not a metadata block",
			content: "The task file looks like:\n\n```markdown\n**Status:** pending\n**Priority:** high\n```\n\nThat is the shape we parse.",
		},
		{
			name:    "unterminated fence does not error — rest of body is treated as fenced",
			content: "Snippet:\n\n```bash\n# Usage: python3 migrate.py\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := testVault(t)
			if err := v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "My Task", Content: tc.content, Priority: "high"}); err != nil {
				t.Fatalf("CreateTask should ACCEPT this body, got: %v", err)
			}
		})
	}
}

// TestCreateTaskAcceptsRealCorpusBodyShape is the regression guard, modeled on
// the real 114-file task corpus: prose, a ```bash fence carrying H1-shaped shell
// comments, then more prose. Ten files in the live corpus have exactly this
// shape and ZERO genuine duplicate H1s. It must be accepted, and the created
// file must still carry exactly ONE real H1 and ONE real status line — the ones
// CreateTask itself wrote.
func TestCreateTaskAcceptsRealCorpusBodyShape(t *testing.T) {
	v := testVault(t)
	content := "## Approach\n\n" +
		"Build the walkthrough rig, then measure it.\n\n" +
		"```bash\n" +
		"# Usage: python3 scripts/measure.py --rig e2e\n" +
		"# Verify pure Go\n" +
		"CGO_ENABLED=0 go build ./...\n" +
		"```\n\n" +
		"## Verification\n\n" +
		"The rig must report zero regressions.\n"

	if err := v.CreateTask("proj", TaskSpec{Slug: "rig", Title: "E2E Walkthrough Rig", Content: content, Priority: "high"}); err != nil {
		t.Fatalf("CreateTask should ACCEPT a real-corpus body shape, got: %v", err)
	}

	meta, body, err := v.GetTask("proj", "rig")
	if err != nil {
		t.Fatal(err)
	}
	var h1s, statuses int
	for line := range strings.SplitSeq(body, "\n") {
		if isH1Line(line) {
			h1s++
		}
		if isStatusLine(line) {
			statuses++
		}
	}
	// The fenced "# Usage:" / "# Verify" comments ARE H1-shaped lines to the
	// (deliberately fence-unaware) parsers — which is exactly why the parsers
	// must be first-wins, and why the real header sits at the top of the file.
	if h1s != 3 {
		t.Errorf("H1-shaped lines = %d, want 3 (1 real + 2 fenced comments)\n%s", h1s, body)
	}
	if statuses != 1 {
		t.Errorf("**Status:** lines = %d, want 1\n%s", statuses, body)
	}
	// What matters: the reader still reports the REAL header, not a comment.
	if meta.Title != "E2E Walkthrough Rig" {
		t.Errorf("Title = %q, want %q (first-wins must find the real H1)", meta.Title, "E2E Walkthrough Rig")
	}
	if meta.Status != "pending" {
		t.Errorf("Status = %q, want %q", meta.Status, "pending")
	}
}

// TestParseTaskMetaFirstStatusWins is the regression test for the exact live
// corruption: a task file that already carries a duplicated header on disk. The
// writer rewrites the FIRST status line, so the reader must report the FIRST
// one too — reporting the last is how "in_progress" showed up as "pending".
func TestParseTaskMetaFirstStatusWins(t *testing.T) {
	content := "# Real Title\n\n" +
		"**Status:** in_progress\n" +
		"**Priority:** high\n\n" +
		"# Agent's Own Title\n\n" +
		"**Status:** pending\n" +
		"**Priority:** low\n"

	meta := parseTaskMeta("dup", content, false)
	if meta.Status != "in_progress" {
		t.Errorf("Status = %q, want %q (first status line wins, matching the writer)", meta.Status, "in_progress")
	}
	if meta.Priority != "high" {
		t.Errorf("Priority = %q, want %q (first priority line wins)", meta.Priority, "high")
	}
	if meta.Title != "Real Title" {
		t.Errorf("Title = %q, want %q (first H1 wins)", meta.Title, "Real Title")
	}
}

// TestReplaceStatusLineAppendsWhenMissing pins the other half of the writer/
// reader contract: replaceStatusLine used to be a SILENT NO-OP on content with
// no status line, so a status update against such a file was simply lost.
func TestReplaceStatusLineAppendsWhenMissing(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "after H1",
			content: "# Title\n\nBody text.\n",
			want:    "# Title\n**Status:** completed\n\nBody text.\n",
		},
		{
			name:    "top of file when no H1",
			content: "Body text only.\n",
			want:    "**Status:** completed\nBody text only.\n",
		},
		{
			name:    "no trailing newline is preserved",
			content: "# Title\n\nBody",
			want:    "# Title\n**Status:** completed\n\nBody",
		},
		{
			name:    "empty content",
			content: "",
			want:    "**Status:** completed\n",
		},
		{
			name:    "existing status line is replaced, not appended",
			content: "# Title\n\n**Status:** pending\n**Priority:** high\n",
			want:    "# Title\n\n**Status:** completed\n**Priority:** high\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := replaceStatusLine(tc.content, "completed")
			if got != tc.want {
				t.Errorf("replaceStatusLine:\n got: %q\nwant: %q", got, tc.want)
			}
			// And it must round-trip: what the writer wrote, the reader reads.
			if meta := parseTaskMeta("s", got, false); meta.Status != "completed" {
				t.Errorf("round-trip: parseTaskMeta Status = %q, want %q", meta.Status, "completed")
			}
		})
	}
}

// TestUpdateTaskStatusReaderWriterAgree is the end-to-end statement of the whole
// bug: whatever UpdateTaskStatus writes, GetTask must read back. Every valid
// status, through the real file, through the real parser.
//
// The cases are driven from validStatuses itself rather than a hardcoded list,
// so the write set and this test cannot drift apart: a status added to (or
// removed from) the write set is automatically covered here. The terminal
// statuses are deliberately NOT in that set — a task reaches a terminal state by
// moving (RetireTask/CancelTask), never by being stamped in place.
func TestUpdateTaskStatusReaderWriterAgree(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{Slug: "my-task", Title: "My Task", Content: "## Plan\n\nDo it.", Priority: "high"}); err != nil {
		t.Fatal(err)
	}

	for status := range validStatuses {
		if err := v.UpdateTaskStatus("proj", "my-task", status); err != nil {
			t.Fatalf("UpdateTaskStatus(%q): %v", status, err)
		}
		meta, body, err := v.GetTask("proj", "my-task")
		if err != nil {
			t.Fatal(err)
		}
		if meta.Status != status {
			t.Errorf("wrote status %q, GetTask read back %q\n%s", status, meta.Status, body)
		}
		if meta.Priority != "high" {
			t.Errorf("status update clobbered Priority: %q", meta.Priority)
		}
		if meta.Title != "My Task" {
			t.Errorf("status update clobbered Title: %q", meta.Title)
		}
	}
}

// TestUpdateTaskStatusRejectsTerminalButMoveStillWritesThem pins the exact
// boundary of the terminal-status change, in BOTH directions — the second half
// matters more than the first.
//
// UpdateTaskStatus refuses the terminal values: stamping "completed" onto a file
// that stays in tasks/ produces a task that reads as finished and behaves as
// active. But retire and cancel MUST still reach those values, because moveTask
// writes them straight through replaceStatusLine and never consults
// validStatuses. If someone ever "tidies up" by routing moveTask through the
// write-set check, retire and cancel break — and this test is what says so.
func TestUpdateTaskStatusRejectsTerminalButMoveStillWritesThem(t *testing.T) {
	for _, terminal := range []string{"completed", "retired", "cancelled"} {
		v := testVault(t)
		if err := v.CreateTask("proj", TaskSpec{Slug: "t", Title: "T", Content: "body", Priority: "high"}); err != nil {
			t.Fatal(err)
		}
		if err := v.UpdateTaskStatus("proj", "t", terminal); err == nil {
			t.Errorf("UpdateTaskStatus(%q) should be refused: it is not in the write set", terminal)
		}
	}

	// retire → done/, status "retired".
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{Slug: "r", Title: "R", Content: "body", Priority: "high"}); err != nil {
		t.Fatal(err)
	}
	if err := v.RetireTask("proj", "r"); err != nil {
		t.Fatalf("RetireTask must still work: %v", err)
	}
	meta, _, err := v.GetTask("proj", "r")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != "retired" || !meta.Done {
		t.Errorf("retired task: status=%q done=%v, want %q/true", meta.Status, meta.Done, "retired")
	}

	// cancel → cancelled/, status "cancelled".
	if err := v.CreateTask("proj", TaskSpec{Slug: "c", Title: "C", Content: "body", Priority: "high"}); err != nil {
		t.Fatal(err)
	}
	if err := v.CancelTask("proj", "c"); err != nil {
		t.Fatalf("CancelTask must still work: %v", err)
	}
	meta, _, err = v.GetTask("proj", "c")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != "cancelled" || !meta.Done {
		t.Errorf("cancelled task: status=%q done=%v, want %q/true", meta.Status, meta.Done, "cancelled")
	}
}

// TestParseTaskMetaReadsArchivedTerminalStatuses pins that removing the terminal
// values from the WRITE set did not touch the READ path. Every archived file on
// disk carries "**Status:** retired" or "**Status:** cancelled" — 96 of them.
// validStatuses is not, and must never become, a read whitelist: a read-side
// check built on it would declare the entire archive invalid.
func TestParseTaskMetaReadsArchivedTerminalStatuses(t *testing.T) {
	for _, status := range []string{"retired", "cancelled", "completed"} {
		meta := parseTaskMeta("archived", "# Old Task\n\n**Status:** "+status+"\n**Priority:** low\n", true)
		if meta.Status != status {
			t.Errorf("archived file with status %q read back as %q — the read path must not filter on the write set",
				status, meta.Status)
		}
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

// TestValidateTaskBodyDoesNotFailOpenOnInlineCodeRun pins the bug that a naive
// fence check ("the trimmed line starts with ```") introduced here.
//
// A body line whose first non-space characters are an INLINE code run — opened
// and closed on the same line — is prose, not an opening fence. The naive rule
// read it as a lone OPEN, inverted its fence state, and skipped the rest of the
// body as "fenced". A duplicate "**Status:**" then sailed past the very check
// this function exists to perform, silently reinstating the status-line
// corruption iteration 184 closed.
//
// The literal below is iterations.md:698, byte-exact — the line that actually
// occurs in this vault, not a paraphrase of it.
func TestValidateTaskBodyDoesNotFailOpenOnInlineCodeRun(t *testing.T) {
	const inlineRun = "  ```bash tutorial``` extraction from `doc/TUTORIAL.md` — deferred"

	t.Run("control: a duplicate status line is rejected", func(t *testing.T) {
		if err := validateTaskBody("some plan\n\n**Status:** Draft\n"); err == nil {
			t.Fatal("control broken: duplicate status line was not rejected")
		}
	})

	t.Run("an inline code run must not disable validation for the rest of the body", func(t *testing.T) {
		if err := validateTaskBody(inlineRun + "\n\n**Status:** Draft\n"); err == nil {
			t.Error("FAILS OPEN: the duplicate **Status:** was not rejected — an inline " +
				"code run was misread as an opening fence, skipping the rest of the body")
		}
	})

	t.Run("the same applies to a smuggled H1", func(t *testing.T) {
		if err := validateTaskBody(inlineRun + "\n\n# Smuggled Title\n"); err == nil {
			t.Error("FAILS OPEN: the duplicate H1 was not rejected")
		}
	})

	t.Run("a REAL fence still suppresses metadata-shaped lines", func(t *testing.T) {
		// The legitimate case 184 protected: "# Usage" inside a shell snippet is
		// a comment, not a heading. This must still be accepted.
		body := "a plan\n\n```bash\n# Usage: vp init\n**Status:** not really\n```\n"
		if err := validateTaskBody(body); err != nil {
			t.Errorf("over-rejected a legitimate fenced snippet: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Relations: parent / depends
// ---------------------------------------------------------------------------

// 🔴 THE BUG THIS FEATURE COULD HAVE SHIPPED WITH.
//
// parseTaskMeta scans the whole file, first-wins, fence-unaware — safe for
// Status and Priority only because CreateTask ALWAYS writes them in the header,
// so the header match is always reached first. Parent and Depends are OPTIONAL:
// a task that never declared a relation has no header line to win the race, so a
// whole-file scan would read its BODY as metadata and invent a relationship
// nobody wrote. Header-block scoping is what prevents it.
//
// This is not hypothetical. The task file that specified this feature quotes the
// exact header shape in its body, in prose and in a fence.
func TestBodyProseIsNeverReadAsARelation(t *testing.T) {
	v := testVault(t)

	body := "## Design\n\n" +
		"The header will look like this:\n\n" +
		"```\n" +
		"**Parent:** fenced-epic\n" +
		"**Depends:** fenced-a, fenced-b\n" +
		"```\n\n" +
		"An un-fenced section can discuss it too, and this is the harder case " +
		"because fence-awareness alone would not save us here.\n"

	if err := v.CreateTask("proj", TaskSpec{
		Slug: "spec", Title: "Spec", Content: body, Priority: "high",
	}); err != nil {
		t.Fatalf("a body that merely DISCUSSES relations must be legal: %v", err)
	}

	meta, _, err := v.GetTask("proj", "spec")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Parent != "" {
		t.Fatalf("phantom parent %q read from the body", meta.Parent)
	}
	if len(meta.Depends) != 0 {
		t.Fatalf("phantom depends %v read from the body", meta.Depends)
	}
}

// The other half of the defence: a body-borne relation line OUTSIDE a fence is
// rejected at the door, so it cannot reach disk in the first place.
func TestCreateRejectsBodyBorneRelationLines(t *testing.T) {
	v := testVault(t)
	for _, tc := range []struct{ name, body string }{
		{"parent", "## Plan\n\n**Parent:** sneaky-epic\n\nmore text\n"},
		{"depends", "## Plan\n\n**Depends:** sneaky-a, sneaky-b\n\nmore text\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := v.CreateTask("proj", TaskSpec{
				Slug: "t-" + tc.name, Title: "T", Content: tc.body, Priority: "high",
			})
			if err == nil {
				t.Fatal("a body-borne relation line outside a fence must be rejected")
			}
		})
	}
}

func TestCreateAndReadRelations(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{
		Slug: "child", Title: "Child", Content: "body", Priority: "high",
		Parent: "epic", Depends: []string{"a", "b", "a"}, // dupe is dropped
	}); err != nil {
		t.Fatal(err)
	}

	meta, _, err := v.GetTask("proj", "child")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Parent != "epic" {
		t.Errorf("parent = %q, want epic", meta.Parent)
	}
	if !slices.Equal(meta.Depends, []string{"a", "b"}) {
		t.Errorf("depends = %v, want [a b] (deduped, file order)", meta.Depends)
	}
	// Status and Priority must still parse — the header block grew, and the
	// original whole-file scan must not have been disturbed.
	if meta.Status != "pending" || meta.Priority != "high" {
		t.Errorf("status/priority regressed: %q/%q", meta.Status, meta.Priority)
	}
}

func TestSetTaskRelationsIsTriState(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{
		Slug: "t", Title: "T", Content: "body", Priority: "high",
		Parent: "epic", Depends: []string{"a"},
	}); err != nil {
		t.Fatal(err)
	}

	// Setting ONLY depends must leave the parent alone. A non-pointer field
	// could not express this, and the task would have been silently unparented.
	deps := []string{"x", "y"}
	if err := v.SetTaskRelations("proj", "t", TaskRelations{Depends: &deps}); err != nil {
		t.Fatal(err)
	}
	meta, _, _ := v.GetTask("proj", "t")
	if meta.Parent != "epic" {
		t.Fatalf("parent was clobbered by a depends-only update: %q", meta.Parent)
	}
	if !slices.Equal(meta.Depends, []string{"x", "y"}) {
		t.Fatalf("depends = %v", meta.Depends)
	}

	// A non-nil EMPTY value clears.
	empty := ""
	none := []string{}
	if err := v.SetTaskRelations("proj", "t", TaskRelations{Parent: &empty, Depends: &none}); err != nil {
		t.Fatal(err)
	}
	meta, content, _ := v.GetTask("proj", "t")
	if meta.Parent != "" || len(meta.Depends) != 0 {
		t.Fatalf("clear failed: parent=%q depends=%v", meta.Parent, meta.Depends)
	}
	if strings.Contains(content, "**Parent:**") || strings.Contains(content, "**Depends:**") {
		t.Fatalf("cleared relations must leave no line behind:\n%s", content)
	}
}

func TestSetTaskRelationsRejectsSelfReference(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{Slug: "t", Title: "T", Content: "body", Priority: "high"}); err != nil {
		t.Fatal(err)
	}
	self := "t"
	if err := v.SetTaskRelations("proj", "t", TaskRelations{Parent: &self}); err == nil {
		t.Error("a task must not be its own parent")
	}
	selfDep := []string{"t"}
	if err := v.SetTaskRelations("proj", "t", TaskRelations{Depends: &selfDep}); err == nil {
		t.Error("a task must not depend on itself")
	}
}

// Relations must survive the retire MOVE. moveTask rewrites the status line and
// copies everything else — this pins that, and guards the plausible future
// refactor that "tidies" the header on the way out.
func TestRetirePreservesRelations(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{
		Slug: "t", Title: "T", Content: "body", Priority: "high",
		Parent: "epic", Depends: []string{"a", "b"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := v.RetireTask("proj", "t"); err != nil {
		t.Fatal(err)
	}

	meta, _, err := v.GetTask("proj", "t")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != "retired" {
		t.Fatalf("status = %q", meta.Status)
	}
	if meta.Parent != "epic" || !slices.Equal(meta.Depends, []string{"a", "b"}) {
		t.Fatalf("relations lost in the move: parent=%q depends=%v", meta.Parent, meta.Depends)
	}
}

// A relation may name a task that does not exist YET. Enforcing existence at
// write time would force a creation order — a child could never be written
// before its epic — and would turn a typo into a hard failure instead of a
// finding the graph reports and works around.
func TestRelationsToNonexistentTasksAreAllowed(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{
		Slug: "child", Title: "C", Content: "body", Priority: "high",
		Parent: "epic-not-created-yet", Depends: []string{"also-missing"},
	}); err != nil {
		t.Fatalf("a forward reference must be legal: %v", err)
	}
}

func TestIceboxIsAValidNonTerminalStatus(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{Slug: "t", Title: "T", Content: "body", Priority: "low"}); err != nil {
		t.Fatal(err)
	}
	if err := v.UpdateTaskStatus("proj", "t", StatusIcebox); err != nil {
		t.Fatalf("icebox must be settable in place: %v", err)
	}
	meta, _, _ := v.GetTask("proj", "t")
	if meta.Status != StatusIcebox {
		t.Fatalf("status = %q", meta.Status)
	}
	if meta.Done {
		t.Fatal("icebox is NOT done — the file stays in the active directory")
	}
}

// An OLD binary (pre-relations) has no body-borne-relation validation, so it can
// write a task whose body carries a line starting with "**Parent:**" OUTSIDE any
// fence — validateTaskBody did not exist to stop it. The NEW parser must still
// not read it.
//
// This is THE cross-version hazard, and it is why the parser is bounded to the
// header block rather than merely being fence-aware: the line here is in no
// fence at all, so fence-awareness would not have saved us. Verified against a
// binary built from the pre-change commit, which reads and rewrites these files
// happily and preserves the header lines through a retire.
func TestOldBinaryBodyBorneRelationIsStillNotRead(t *testing.T) {
	raw := "# Title\n\n**Status:** pending\n**Priority:** high\n\n" +
		"## Notes\n\n**Parent:** sneaky-epic\n**Depends:** sneaky-a\n\nmore prose\n"

	meta := parseTaskMeta("t", raw, false)
	if meta.Parent != "" || len(meta.Depends) != 0 {
		t.Fatalf("a body-borne relation written by an OLD binary was read as real: parent=%q depends=%v",
			meta.Parent, meta.Depends)
	}
	if meta.Status != "pending" || meta.Priority != "high" {
		t.Fatalf("header regressed: %q/%q", meta.Status, meta.Priority)
	}
}

// ---------------------------------------------------------------------------
// AmendTask
// ---------------------------------------------------------------------------

// amendFixture creates a task with a two-section body.
func amendFixture(t *testing.T) *Vault {
	t.Helper()
	v := testVault(t)
	body := "## Diagnosis\n\nThe cache is never evicted.\n\n## Verification\n\nDrive the real tool.\n"
	if err := v.CreateTask("proj", TaskSpec{
		Slug: "t", Title: "T", Content: body, Priority: "high",
		Parent: "epic", Depends: []string{"other"},
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return v
}

func TestAmendTaskAppendsWhenSectionAbsent(t *testing.T) {
	v := amendFixture(t)
	op, err := v.AmendTask("proj", "t", "Decision (205)", "Option B. The re-key is unjustified.")
	if err != nil {
		t.Fatalf("AmendTask: %v", err)
	}
	if op != AmendAppended {
		t.Fatalf("op = %q, want %q", op, AmendAppended)
	}
	_, body, err := v.GetTask("proj", "t")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !strings.Contains(body, "## Decision (205)\n\nOption B. The re-key is unjustified.\n") {
		t.Errorf("appended section missing or malformed:\n%s", body)
	}
	// The pre-existing sections must survive.
	for _, want := range []string{"## Diagnosis", "## Verification", "The cache is never evicted."} {
		if !strings.Contains(body, want) {
			t.Errorf("amend destroyed existing content %q:\n%s", want, body)
		}
	}
}

// TestAmendTaskIsIdempotent is the whole reason amend is section-keyed rather
// than append-only. A retried amend must CONVERGE, not accumulate.
func TestAmendTaskIsIdempotent(t *testing.T) {
	v := amendFixture(t)
	for i := range 3 {
		op, err := v.AmendTask("proj", "t", "Decision", "Ship it.")
		if err != nil {
			t.Fatalf("AmendTask: %v", err)
		}
		want := AmendReplaced
		if i == 0 {
			want = AmendAppended
		}
		if op != want {
			t.Fatalf("amend %d op = %q, want %q", i, op, want)
		}
	}
	_, body, err := v.GetTask("proj", "t")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if n := strings.Count(body, "## Decision"); n != 1 {
		t.Errorf("section count = %d, want 1 — a repeated amend duplicated instead of replacing:\n%s", n, body)
	}
	if n := strings.Count(body, "Ship it."); n != 1 {
		t.Errorf("body count = %d, want 1:\n%s", n, body)
	}
}

func TestAmendTaskReplacesInPlaceAndKeepsNeighbours(t *testing.T) {
	v := amendFixture(t)
	if _, err := v.AmendTask("proj", "t", "Diagnosis", "REVERSED: the premise was false."); err != nil {
		t.Fatalf("AmendTask: %v", err)
	}
	_, body, err := v.GetTask("proj", "t")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if strings.Contains(body, "The cache is never evicted.") {
		t.Errorf("old section body survived the replace:\n%s", body)
	}
	if !strings.Contains(body, "REVERSED: the premise was false.") {
		t.Errorf("new body missing:\n%s", body)
	}
	// The section AFTER the replaced one must be intact, and still be a section.
	if !strings.Contains(body, "## Verification\n\nDrive the real tool.") {
		t.Errorf("following section was damaged by the splice:\n%s", body)
	}
	// Order must be preserved: Diagnosis still precedes Verification.
	if strings.Index(body, "## Diagnosis") > strings.Index(body, "## Verification") {
		t.Errorf("replace reordered the sections:\n%s", body)
	}
}

// TestAmendTaskNeverTouchesHeaderBlock pins the invariant that makes amend safe
// to expose at all: the reader and the writer of a task's status/edges must not
// be able to disagree because someone amended a body.
func TestAmendTaskNeverTouchesHeaderBlock(t *testing.T) {
	v := amendFixture(t)
	if err := v.UpdateTaskStatus("proj", "t", "in_progress"); err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}
	if _, err := v.AmendTask("proj", "t", "Decision", "Recorded."); err != nil {
		t.Fatalf("AmendTask: %v", err)
	}
	meta, body, err := v.GetTask("proj", "t")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if meta.Status != "in_progress" {
		t.Errorf("Status = %q, want in_progress — amend clobbered the status", meta.Status)
	}
	if meta.Priority != "high" {
		t.Errorf("Priority = %q, want high", meta.Priority)
	}
	if meta.Parent != "epic" {
		t.Errorf("Parent = %q, want epic — amend clobbered the parent edge", meta.Parent)
	}
	if !slices.Equal(meta.Depends, []string{"other"}) {
		t.Errorf("Depends = %v, want [other] — amend clobbered the dependency edge", meta.Depends)
	}
	if n := strings.Count(body, "**Status:**"); n != 1 {
		t.Errorf("status line count = %d, want exactly 1 — reader and writer would disagree:\n%s", n, body)
	}
	if !strings.HasPrefix(body, "# T\n") {
		t.Errorf("H1 title was disturbed:\n%s", body)
	}
}

// TestAmendTaskIsFenceAware is the 191/204 bug class, and it is not
// hypothetical: the task file that SPECIFIED this feature quotes "## Decision"
// inside a fence as an example. A naive scan would find that quoted heading
// first and splice the replacement into the middle of a code block.
func TestAmendTaskIsFenceAware(t *testing.T) {
	v := testVault(t)
	body := "## Schema\n\nAn amend looks like:\n\n```markdown\n## Decision\n\nsample text\n```\n\nThat fenced block is an example, not a section.\n"
	if err := v.CreateTask("proj", TaskSpec{Slug: "t", Title: "T", Content: body, Priority: "high"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if _, err := v.AmendTask("proj", "t", "Decision", "The real decision."); err != nil {
		t.Fatalf("AmendTask: %v", err)
	}

	_, got, err := v.GetTask("proj", "t")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	// The fenced sample must be untouched, byte for byte.
	if !strings.Contains(got, "```markdown\n## Decision\n\nsample text\n```") {
		t.Errorf("amend spliced into a fenced code block — the fence was not respected:\n%s", got)
	}
	// The real section must have been APPENDED at the end, not matched inside the fence.
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "## Decision\n\nThe real decision.") {
		t.Errorf("amend did not append a real section after the fenced example:\n%s", got)
	}
	if !strings.Contains(got, "That fenced block is an example, not a section.") {
		t.Errorf("amend destroyed prose following the fence:\n%s", got)
	}
}

// TestAmendTaskH3DoesNotTerminateSection: sub-headings belong to their section.
func TestAmendTaskH3DoesNotTerminateSection(t *testing.T) {
	v := testVault(t)
	body := "## Plan\n\n### Phase 1\n\nold one\n\n### Phase 2\n\nold two\n\n## Risks\n\nA risk.\n"
	if err := v.CreateTask("proj", TaskSpec{Slug: "t", Title: "T", Content: body, Priority: "high"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := v.AmendTask("proj", "t", "Plan", "### Phase 1\n\nnew one\n\n### Phase 2\n\nnew two"); err != nil {
		t.Fatalf("AmendTask: %v", err)
	}
	_, got, err := v.GetTask("proj", "t")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	// Replacing "## Plan" must have swallowed BOTH H3 subsections, not just up to the first.
	for _, gone := range []string{"old one", "old two"} {
		if strings.Contains(got, gone) {
			t.Errorf("H3 terminated the section early — %q survived a whole-section replace:\n%s", gone, got)
		}
	}
	for _, want := range []string{"new one", "new two", "## Risks", "A risk."} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q after amend:\n%s", want, got)
		}
	}
}

func TestAmendTaskRejectsMetadataAndH2InBody(t *testing.T) {
	cases := []struct {
		name, body, wantErr string
	}{
		{"status line", "**Status:** done", "status line"},
		{"parent line", "**Parent:** other-epic", "parent line"},
		{"depends line", "**Depends:** a, b", "depends line"},
		{"h1 heading", "# A New Title", "H1 heading"},
		{"h2 heading", "Some prose.\n\n## Sneaky Second Section\n\nmore", "H2 heading"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := amendFixture(t)
			_, err := v.AmendTask("proj", "t", "Decision", tc.body)
			if err == nil {
				t.Fatalf("AmendTask(%q) succeeded, want rejection", tc.body)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// A fenced metadata shape is sample text, not metadata. Same rule as create.
func TestAmendTaskAcceptsMetadataShapesInsideFences(t *testing.T) {
	v := amendFixture(t)
	body := "The header syntax is:\n\n```markdown\n**Status:** pending\n**Parent:** epic\n## Example\n```\n\nQuoted, not applied."
	if _, err := v.AmendTask("proj", "t", "Decision", body); err != nil {
		t.Fatalf("AmendTask rejected a fenced example: %v", err)
	}
	meta, got, err := v.GetTask("proj", "t")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if meta.Status != "pending" || meta.Parent != "epic" {
		t.Errorf("fenced sample text was read as metadata: status=%q parent=%q", meta.Status, meta.Parent)
	}
	if n := strings.Count(got, "**Status:**"); n != 2 {
		// One real header line + one quoted inside the fence.
		t.Errorf("status line count = %d, want 2 (one real, one fenced sample):\n%s", n, got)
	}
}

func TestAmendTaskRejectsBadSection(t *testing.T) {
	cases := []struct{ name, section, body, wantErr string }{
		{"empty section", "", "x", "section is required"},
		{"markup in section", "## Decision", "x", "heading TEXT, not the markup"},
		{"multiline section", "Decision\nExtra", "x", "single line"},
		{"empty body", "Decision", "   ", "body is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := amendFixture(t)
			_, err := v.AmendTask("proj", "t", tc.section, tc.body)
			if err == nil {
				t.Fatal("AmendTask succeeded, want rejection")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestAmendTaskNotFound(t *testing.T) {
	v := testVault(t)
	_, err := v.AmendTask("proj", "nope", "Decision", "x")
	if err == nil {
		t.Fatal("AmendTask on a missing task succeeded, want error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want it to say not found", err)
	}
}

// Concurrent amends of DIFFERENT sections must both survive: the per-path lock
// spans the read→rewrite, so neither can clobber the other's section.
func TestAmendTaskConcurrentDifferentSectionsBothSurvive(t *testing.T) {
	v := amendFixture(t)
	sections := []string{"Alpha", "Beta", "Gamma", "Delta", "Epsilon"}

	var wg sync.WaitGroup
	errs := make([]error, len(sections))
	for i, s := range sections {
		wg.Go(func() {
			_, errs[i] = v.AmendTask("proj", "t", s, fmt.Sprintf("body of %s", s))
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("AmendTask(%s): %v", sections[i], err)
		}
	}

	_, got, err := v.GetTask("proj", "t")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	for _, s := range sections {
		if !strings.Contains(got, "## "+s+"\n\nbody of "+s) {
			t.Errorf("concurrent amend lost section %q — a write clobbered another:\n%s", s, got)
		}
	}
	// And the original body is still there.
	if !strings.Contains(got, "## Diagnosis") || !strings.Contains(got, "## Verification") {
		t.Errorf("concurrent amends destroyed the original sections:\n%s", got)
	}
}

// The file must stay parseable by the thing that reads it — a round-trip through
// GetTask after an amend must yield the same metadata the writers set.
func TestAmendTaskRoundTripsThroughParser(t *testing.T) {
	v := amendFixture(t)
	if _, err := v.AmendTask("proj", "t", "Decision", "Recorded."); err != nil {
		t.Fatalf("AmendTask: %v", err)
	}
	if err := v.SetTaskRelations("proj", "t", TaskRelations{Depends: &[]string{"a", "b"}}); err != nil {
		t.Fatalf("SetTaskRelations after amend: %v", err)
	}
	if _, err := v.AmendTask("proj", "t", "Decision", "Re-recorded."); err != nil {
		t.Fatalf("second AmendTask: %v", err)
	}
	meta, got, err := v.GetTask("proj", "t")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !slices.Equal(meta.Depends, []string{"a", "b"}) {
		t.Errorf("Depends = %v, want [a b] — amend and set_relations disagree", meta.Depends)
	}
	if meta.Parent != "epic" {
		t.Errorf("Parent = %q, want epic", meta.Parent)
	}
	if strings.Contains(got, "Recorded.\n") && !strings.Contains(got, "Re-recorded.") {
		t.Errorf("second amend did not replace the first:\n%s", got)
	}
	if n := strings.Count(got, "## Decision"); n != 1 {
		t.Errorf("Decision section count = %d, want 1:\n%s", n, got)
	}
}

// ---------------------------------------------------------------------------
// SetTaskMeta
// ---------------------------------------------------------------------------

func TestSetTaskMetaRetitleAndReprioritize(t *testing.T) {
	v := amendFixture(t)

	newTitle := "REVERSED: the key is already a content hash"
	newPri := "critical"
	if err := v.SetTaskMeta("proj", "t", TaskMetaEdit{Title: &newTitle, Priority: &newPri}); err != nil {
		t.Fatalf("SetTaskMeta: %v", err)
	}

	meta, body, err := v.GetTask("proj", "t")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if meta.Title != newTitle {
		t.Errorf("Title = %q, want %q", meta.Title, newTitle)
	}
	if meta.Priority != "critical" {
		t.Errorf("Priority = %q, want critical", meta.Priority)
	}
	// Everything else must be untouched.
	if meta.Status != "pending" || meta.Parent != "epic" || !slices.Equal(meta.Depends, []string{"other"}) {
		t.Errorf("set_meta disturbed a field it does not own: %+v", meta)
	}
	if n := strings.Count(body, "# "+newTitle); n != 1 {
		t.Errorf("title line count = %d, want 1:\n%s", n, body)
	}
	if !strings.Contains(body, "## Diagnosis") {
		t.Errorf("set_meta destroyed the body:\n%s", body)
	}
}

// Tri-state: setting only one field must leave the other alone.
func TestSetTaskMetaLeavesTheUnspecifiedFieldAlone(t *testing.T) {
	v := amendFixture(t)

	pri := "low"
	if err := v.SetTaskMeta("proj", "t", TaskMetaEdit{Priority: &pri}); err != nil {
		t.Fatalf("SetTaskMeta: %v", err)
	}
	meta, _, err := v.GetTask("proj", "t")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "T" {
		t.Errorf("Title = %q, want T — set_meta clobbered a title it was not given", meta.Title)
	}
	if meta.Priority != "low" {
		t.Errorf("Priority = %q, want low", meta.Priority)
	}
}

func TestSetTaskMetaRejections(t *testing.T) {
	empty, blank, bad, ok := "", "   ", "urgent", "high"
	multi := "line one\nline two"
	cases := []struct {
		name    string
		edit    TaskMetaEdit
		wantErr string
	}{
		{"nothing to set", TaskMetaEdit{}, "nothing to set"},
		{"empty title", TaskMetaEdit{Title: &empty}, "title cannot be empty"},
		{"blank title", TaskMetaEdit{Title: &blank}, "title cannot be empty"},
		{"multiline title", TaskMetaEdit{Title: &multi}, "single line"},
		{"invalid priority", TaskMetaEdit{Priority: &bad}, "invalid priority"},
		{"empty priority", TaskMetaEdit{Priority: &empty}, "invalid priority"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := amendFixture(t)
			err := v.SetTaskMeta("proj", "t", tc.edit)
			if err == nil {
				t.Fatal("SetTaskMeta succeeded, want rejection")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
	// Sanity: the valid one is accepted.
	v := amendFixture(t)
	if err := v.SetTaskMeta("proj", "t", TaskMetaEdit{Priority: &ok}); err != nil {
		t.Errorf("SetTaskMeta with a valid priority failed: %v", err)
	}
}

// set_meta must not be a second way to write a status or an edge. Three writers,
// disjoint fields — that is what keeps the reader and the writer agreeing.
func TestSetTaskMetaCannotReachStatusOrEdges(t *testing.T) {
	v := amendFixture(t)
	if err := v.UpdateTaskStatus("proj", "t", "in_progress"); err != nil {
		t.Fatal(err)
	}

	// A title that LOOKS like a status line must be written as a title, not
	// interpreted as one.
	sneaky := "**Status:** done"
	if err := v.SetTaskMeta("proj", "t", TaskMetaEdit{Title: &sneaky}); err != nil {
		t.Fatalf("SetTaskMeta: %v", err)
	}
	meta, body, err := v.GetTask("proj", "t")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != "in_progress" {
		t.Errorf("Status = %q, want in_progress — a title was read as a status line", meta.Status)
	}
	if n := strings.Count(body, "\n**Status:**"); n != 1 {
		t.Errorf("real status line count = %d, want 1:\n%s", n, body)
	}
	if meta.Parent != "epic" {
		t.Errorf("Parent = %q, want epic", meta.Parent)
	}
}

func TestSetTaskMetaNotFound(t *testing.T) {
	v := testVault(t)
	title := "x"
	err := v.SetTaskMeta("proj", "nope", TaskMetaEdit{Title: &title})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want not found", err)
	}
}

// --- Phase B: resolveTaskFile / validateWholeTaskFile / OverwriteTaskFile ---

// TestResolveTaskFileResolvesActive proves resolveTaskFile resolves a live task
// to its tasks/ path and reports it as not archived.
func TestResolveTaskFileResolvesActive(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{Slug: "alpha", Title: "Alpha", Priority: "P1"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, done, err := v.resolveTaskFile("proj", "alpha")
	if err != nil {
		t.Fatalf("resolveTaskFile: %v", err)
	}
	if done {
		t.Errorf("done = true, want false for an active task")
	}
	want := filepath.Join(v.Root, "Projects", "proj", "tasks", "alpha.md")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

// TestResolveTaskFileResolvesDone proves resolveTaskFile searches the done/
// archive — where TaskFile (active-only) would not look — and flags it archived.
func TestResolveTaskFileResolvesDone(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{Slug: "beta", Title: "Beta", Priority: "P2"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := v.RetireTask("proj", "beta"); err != nil {
		t.Fatalf("RetireTask: %v", err)
	}

	got, done, err := v.resolveTaskFile("proj", "beta")
	if err != nil {
		t.Fatalf("resolveTaskFile: %v", err)
	}
	if !done {
		t.Errorf("done = false, want true for a retired task")
	}
	want := filepath.Join(v.Root, "Projects", "proj", "tasks", "done", "beta.md")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

// TestResolveTaskFileResolvesCancelled proves resolveTaskFile searches the
// cancelled/ archive and flags it archived.
func TestResolveTaskFileResolvesCancelled(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{Slug: "gamma", Title: "Gamma", Priority: "P3"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := v.CancelTask("proj", "gamma"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	got, done, err := v.resolveTaskFile("proj", "gamma")
	if err != nil {
		t.Fatalf("resolveTaskFile: %v", err)
	}
	if !done {
		t.Errorf("done = false, want true for a cancelled task")
	}
	want := filepath.Join(v.Root, "Projects", "proj", "tasks", "cancelled", "gamma.md")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

// TestResolveTaskFileNotFound proves an unknown slug is an error, not an active
// path pointing at a file that does not exist.
func TestResolveTaskFileNotFound(t *testing.T) {
	v := testVault(t)
	if _, _, err := v.resolveTaskFile("proj", "ghost"); err == nil {
		t.Fatal("resolveTaskFile for a nonexistent slug should error")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want not found", err)
	}
}

// validTaskFile is the canonical shape CreateTask writes: one title, one Status,
// one Priority, contiguous, then the conventional first H2 and a body under it.
//
// 🔴 The H2 is not decoration. CreateTask emits "## Context"
// (ConventionalFirstHeading) unconditionally, and validateWholeTaskFile refuses a
// file with no H2 at all — so a fixture without one is not "the shape CreateTask
// writes", it is a shape no writer can produce and the validator rejects.
const validTaskFile = "# Task Title\n\n" +
	"**Status:** pending\n" +
	"**Priority:** P1\n\n" +
	"## " + ConventionalFirstHeading + "\n\n" +
	"Body prose goes here.\n"

// TestValidateWholeTaskFileValid proves a normal well-formed task passes.
func TestValidateWholeTaskFileValid(t *testing.T) {
	if err := validateWholeTaskFile(validTaskFile); err != nil {
		t.Fatalf("valid task rejected: %v", err)
	}
}

// TestValidateWholeTaskFileTwoStatus proves a duplicated Status line is rejected
// by name.
func TestValidateWholeTaskFileTwoStatus(t *testing.T) {
	content := "# Task Title\n\n" +
		"**Status:** pending\n" +
		"**Status:** blocked\n" +
		"**Priority:** P1\n\n## Context\n\nBody.\n"
	err := validateWholeTaskFile(content)
	if err == nil || !strings.Contains(err.Error(), "two Status lines") {
		t.Fatalf("error = %v, want 'two Status lines'", err)
	}
}

// TestValidateWholeTaskFileTwoTitles proves a second H1 is rejected by name.
func TestValidateWholeTaskFileTwoTitles(t *testing.T) {
	content := "# Task Title\n# Second Title\n\n" +
		"**Status:** pending\n**Priority:** P1\n\n## Context\n\nBody.\n"
	err := validateWholeTaskFile(content)
	if err == nil || !strings.Contains(err.Error(), "two title lines") {
		t.Fatalf("error = %v, want 'two title lines'", err)
	}
}

// TestValidateWholeTaskFileTwoPriority proves a duplicated Priority line is
// rejected by name.
func TestValidateWholeTaskFileTwoPriority(t *testing.T) {
	content := "# Task Title\n\n" +
		"**Status:** pending\n" +
		"**Priority:** P1\n" +
		"**Priority:** P2\n\n## Context\n\nBody.\n"
	err := validateWholeTaskFile(content)
	if err == nil || !strings.Contains(err.Error(), "two Priority lines") {
		t.Fatalf("error = %v, want 'two Priority lines'", err)
	}
}

// TestValidateWholeTaskFileMissingField proves a file missing a required header
// field is rejected and names the field.
func TestValidateWholeTaskFileMissingField(t *testing.T) {
	content := "# Task Title\n\n**Status:** pending\n\n## Context\n\nBody, no priority.\n"
	err := validateWholeTaskFile(content)
	if err == nil || !strings.Contains(err.Error(), "missing Priority") {
		t.Fatalf("error = %v, want 'missing Priority'", err)
	}
}

// TestValidateWholeTaskFileMissingTitle proves a headerless file is rejected.
func TestValidateWholeTaskFileMissingTitle(t *testing.T) {
	content := "**Status:** pending\n**Priority:** P1\n\n## Context\n\nNo title here.\n"
	err := validateWholeTaskFile(content)
	if err == nil || !strings.Contains(err.Error(), "missing title") {
		t.Fatalf("error = %v, want 'missing title'", err)
	}
}

// TestValidateWholeTaskFileUnterminatedFence proves an open code fence is
// rejected by name.
func TestValidateWholeTaskFileUnterminatedFence(t *testing.T) {
	content := "# Task Title\n\n" +
		"**Status:** pending\n**Priority:** P1\n\n" +
		"## Context\n\n" +
		"```go\nnever closed\n"
	err := validateWholeTaskFile(content)
	if err == nil || !strings.Contains(err.Error(), "unterminated code fence") {
		t.Fatalf("error = %v, want 'unterminated code fence'", err)
	}
}

// TestValidateWholeTaskFileMalformedHeaderBlock proves a header field marooned in
// the body — not part of the contiguous run after the title — is rejected.
func TestValidateWholeTaskFileMalformedHeaderBlock(t *testing.T) {
	content := "# Task Title\n\n" +
		"**Status:** pending\n\n" + // blank line breaks the contiguous run
		"**Priority:** P1\n\n## Context\n\nBody.\n"
	err := validateWholeTaskFile(content)
	if err == nil || !strings.Contains(err.Error(), "malformed header block") {
		t.Fatalf("error = %v, want 'malformed header block'", err)
	}
}

// TestValidateWholeTaskFileFenceAware proves the validator does NOT count header
// lines that live INSIDE a fenced code block — the whole reason it drives
// mdfence. A closed fence containing a second "# H1" and a second "**Status:**"
// must not be read as duplicate header material.
//
// The fixture carries its own UNFENCED "## Context" so this test still fails for
// its own reason: without it the file would be refused for having no H2, and a
// green result would prove nothing about fence awareness. The mirror case — an
// H2 that exists ONLY inside a fence — is
// TestValidateWholeTaskFileFencedH2DoesNotCount.
func TestValidateWholeTaskFileFenceAware(t *testing.T) {
	content := "# Task Title\n\n" +
		"**Status:** pending\n**Priority:** P1\n\n" +
		"## Context\n\n" +
		"Example task markdown:\n\n" +
		"```md\n" +
		"# Not A Real Title\n" +
		"**Status:** fake\n" +
		"**Priority:** fake\n" +
		"```\n\n" +
		"Done.\n"
	if err := validateWholeTaskFile(content); err != nil {
		t.Fatalf("fenced header lines tripped the validator: %v", err)
	}
}

// TestValidateWholeTaskFileNoH2 proves the refusal this rule exists for: a file
// whose header is perfectly well formed but whose entire body sits above the
// first heading, because there is no heading. Every byte of that prose is
// unreachable to amend, which matches on an exact "## " heading line.
func TestValidateWholeTaskFileNoH2(t *testing.T) {
	content := "# Task Title\n\n" +
		"**Status:** pending\n**Priority:** P1\n\n" +
		"All of this prose is unaddressable.\n\nSo is this.\n"
	err := validateWholeTaskFile(content)
	if err == nil || !strings.Contains(err.Error(), "missing section") {
		t.Fatalf("error = %v, want 'missing section'", err)
	}
}

// TestValidateWholeTaskFileFencedH2DoesNotCount proves the H2 requirement is
// fence-aware like every other rule in the function. A task body that quotes a
// task's own markdown inside a fence carries a "## " line that amend can never
// match, so it must not satisfy the requirement.
//
// MUTATION anchor: count H2s over the raw content instead of
// mdfence.OutsideFences and this reds.
func TestValidateWholeTaskFileFencedH2DoesNotCount(t *testing.T) {
	content := "# Task Title\n\n" +
		"**Status:** pending\n**Priority:** P1\n\n" +
		"Example task markdown:\n\n" +
		"```md\n" +
		"## Context\n" +
		"body under it\n" +
		"```\n"
	err := validateWholeTaskFile(content)
	if err == nil || !strings.Contains(err.Error(), "missing section") {
		t.Fatalf("error = %v, want 'missing section'", err)
	}
}

// TestValidateWholeTaskFileManyH2 proves there is NO upper bound on H2s. Only
// zero is the defect — a task with many sections is the normal shape, and a rule
// that demanded exactly one would refuse nearly every real task file.
func TestValidateWholeTaskFileManyH2(t *testing.T) {
	content := "# Task Title\n\n" +
		"**Status:** pending\n**Priority:** P1\n\n" +
		"## Context\n\nWhy.\n\n" +
		"## Decision\n\nWhat.\n\n" +
		"## Notes\n\nHow.\n"
	if err := validateWholeTaskFile(content); err != nil {
		t.Fatalf("multiple H2 sections rejected: %v", err)
	}
}

// TestCreateTaskOutputPassesWholeFileValidation ties the new rule to its reason
// at BOTH ends: CreateTask establishes the H2 guarantee at birth
// (ConventionalFirstHeading, emitted unconditionally) and validateWholeTaskFile
// is what stops a later whole-file overwrite from undoing it. If the two ever
// disagree — a CreateTask that stopped emitting the heading, or a validator that
// demanded a shape CreateTask does not write — this reds.
func TestCreateTaskOutputPassesWholeFileValidation(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"empty body", ""},
		{"body with no heading of its own", "Just prose.\n"},
		{"body that already opens with an H2", "## Their Own Heading\n\nSome prose.\n"},
		{"body quoting a fenced H2", "Sample:\n\n```md\n## Fenced\n```\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := testVault(t)
			if err := v.CreateTask("proj", TaskSpec{
				Slug: "task", Title: "T", Priority: "P1", Content: tc.content,
			}); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
			_, body, err := v.GetTask("proj", "task")
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if err := validateWholeTaskFile(body); err != nil {
				t.Fatalf("CreateTask wrote a file validateWholeTaskFile refuses: %v\nfile:\n%s", err, body)
			}
		})
	}
}

// TestOverwriteTaskFileWritesValid proves a valid whole-file overwrite persists
// and re-reads verbatim, and that the surface stamp lands (v.Root was passed).
func TestOverwriteTaskFileWritesValid(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{Slug: "task", Title: "Old", Priority: "P1"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// The header must MATCH the task on disk: OverwriteTaskFile refuses a body
	// that moves a header field, and this test is about the write mechanic, not
	// that rule (TestOverwriteTaskFileRefusesAHeaderChange owns it). CreateTask
	// wrote title "Old" and priority "P1" with the default status, so the body
	// below restates them verbatim and changes only prose.
	newContent := "# Old\n\n" +
		"**Status:** pending\n**Priority:** P1\n\n## Context\n\nRewritten body.\n"
	if err := v.OverwriteTaskFile("proj", "task", newContent); err != nil {
		t.Fatalf("OverwriteTaskFile: %v", err)
	}

	_, body, err := v.GetTask("proj", "task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if body != newContent {
		t.Errorf("re-read body = %q, want %q", body, newContent)
	}

	stamp := filepath.Join(v.Root, "Projects", "proj", ".surface")
	if _, err := os.Stat(stamp); err != nil {
		t.Errorf(".surface stamp missing after overwrite (v.Root not passed?): %v", err)
	}
}

// TestOverwriteTaskFileRejectsInvalid proves invalid content is refused and the
// on-disk file is left byte-for-byte unchanged.
func TestOverwriteTaskFileRejectsInvalid(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{Slug: "task", Title: "Keep", Priority: "P1"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	path := filepath.Join(v.Root, "Projects", "proj", "tasks", "task.md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	bad := "# T\n\n**Status:** a\n**Status:** b\n**Priority:** P1\n\n## Context\n\nBody.\n"
	if err := v.OverwriteTaskFile("proj", "task", bad); err == nil {
		t.Fatal("OverwriteTaskFile accepted invalid content")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("file changed after rejected write:\nbefore=%q\nafter=%q", before, after)
	}
}

// TestOverwriteTaskFileIdenticalNoOp proves writing identical content returns nil
// and leaves the file intact.
func TestOverwriteTaskFileIdenticalNoOp(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{Slug: "task", Title: "T", Priority: "P1"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	_, body, err := v.GetTask("proj", "task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	if err := v.OverwriteTaskFile("proj", "task", body); err != nil {
		t.Fatalf("identical overwrite should be a no-op, got: %v", err)
	}

	_, after, err := v.GetTask("proj", "task")
	if err != nil {
		t.Fatalf("GetTask after: %v", err)
	}
	if after != body {
		t.Errorf("content changed after no-op overwrite:\nwant=%q\ngot=%q", body, after)
	}
}

// TestOverwriteTaskFileNotFound proves a nonexistent slug errors before any
// write.
func TestOverwriteTaskFileNotFound(t *testing.T) {
	v := testVault(t)
	err := v.OverwriteTaskFile("proj", "ghost", validTaskFile)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want not found", err)
	}
}

// TestArchiveMakesNoProgressWhileSourceLockHeld proves that an externally held
// lock on a task's ACTIVE path serializes the WHOLE archive operation — not
// merely its final step.
//
// It replaces TestMoveTask_UnlinkHoldsSourceLock, deleted 2026-09-01 when
// moveTask became rewrite-then-rename. That test was structurally obsolete, not
// wrong: it waited for the DESTINATION file to appear and only then asserted the
// source survived, which required an observable window where the destination
// EXISTS and the source STILL EXISTS. The old write-dest-then-unlink-source
// ordering produced exactly that window; rewrite-then-rename never can, because
// the destination comes into being at the rename, which is the last instruction.
// Coverage was not dropped — it was strengthened, and this is where it moved.
//
// The property is now STRONGER than the one the old test could observe. Under
// the old ordering the destination write happened OUTSIDE the source lock and
// only the unlink was serialized, so a concurrent holder of that lock could not
// stop the archive from writing. Now the destination stat, the read, the stamp
// and the rename all run under the single source lock, so a holder blocks the
// entire operation. The unstamped-source assertion below is the new half: it
// proves the STAMP itself is serialized, which the old ordering could not claim.
//
// Mutation check: move the vaultlock.Acquire in moveTask below the
// atomicfile.Write and this test goes red at "source body was stamped while its
// lock was held" — the stamp escapes the critical section while the rename
// stays inside it.
func TestArchiveMakesNoProgressWhileSourceLockHeld(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{Slug: "locked-task", Title: "Title", Content: "Body.", Priority: "P1"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	srcPath, err := v.TaskFile("proj", "locked-task")
	if err != nil {
		t.Fatalf("TaskFile: %v", err)
	}
	doneDir, err := v.TaskDoneDir("proj")
	if err != nil {
		t.Fatalf("TaskDoneDir: %v", err)
	}
	destPath := filepath.Join(doneDir, "locked-task.md")

	before, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read source before: %v", err)
	}

	release, err := vaultlock.Acquire(v.Root, srcPath)
	if err != nil {
		t.Fatalf("test Acquire: %v", err)
	}
	released := false
	unlock := func() {
		if !released {
			released = true
			_ = release()
		}
	}
	defer unlock()

	done := make(chan error, 1)
	go func() { done <- v.RetireTask("proj", "locked-task") }()

	// Hold the lock and assert NO observable progress for the whole window,
	// polling rather than sleeping once: a violation that appears transiently
	// and is then overwritten would slip past a single end-of-window check.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			unlock()
			t.Fatalf("retire completed (err=%v) while the source lock was held — the archive took no lock", err)
		default:
		}
		if _, statErr := os.Stat(destPath); statErr == nil {
			unlock()
			t.Fatalf("destination %s appeared while the source lock was held", destPath)
		}
		// The new half: the stamp is inside the critical section too, so the
		// body must still carry its non-terminal status.
		now, readErr := os.ReadFile(srcPath)
		if readErr != nil {
			unlock()
			t.Fatalf("source %s vanished while its lock was held: %v", srcPath, readErr)
		}
		if string(now) != string(before) {
			unlock()
			t.Fatalf("source body was stamped while its lock was held:\ngot:  %q\nwant: %q", now, before)
		}
		if meta := parseTaskMeta("locked-task", string(now), false); meta.Status == "retired" {
			unlock()
			t.Fatal("source carries a terminal status while its lock was held — the stamp escaped the critical section")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Releasing must let the whole blocked archive proceed.
	released = true
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RetireTask after release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("retire did not complete after the source lock was released")
	}

	// And the final state is the ordinary one: moved, stamped, source gone.
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Errorf("source should be gone after retire, stat err = %v", err)
	}
	archived, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("destination should exist after retire: %v", err)
	}
	if meta := parseTaskMeta("locked-task", string(archived), true); meta.Status != "retired" {
		t.Errorf("archived body status = %q, want %q", meta.Status, "retired")
	}
}

// ---------------------------------------------------------------------------
// Legacy header classification.
//
// The specimens below are the SHAPES measured on the live corpus, not invented
// ones. Every fixture in cmd_migrate_task_status_test.go was "# T", blank,
// "**Status:**" — the shape CreateTask writes — which is exactly why a live
// --apply failed 7 of 7 against shapes no fixture carried.

// legacyBoth is the BOTH class: an un-bolded legacy line holding the TRUE value,
// above a bolded field holding a stale one.
const legacyBoth = "# Task 3.5: Portable Command Execution\n" +
	"Status: Done\n" +
	"\n" +
	"**Status:** pending\n" +
	"**Priority:** high\n\n" +
	"## Summary\n\nBody.\n"

// TestScanLegacyHeaderClassifiesTheMeasuredShapes drives one table over every
// shape the corpus actually carries, including the ones a naive detector gets
// wrong in each direction.
func TestScanLegacyHeaderClassifiesTheMeasuredShapes(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		want     LegacyHeaderClass
		wantBare int // 1-indexed; 0 = no legacy line found
	}{
		{
			name:     "both, legacy line directly under the title",
			content:  legacyBoth,
			want:     LegacyHeaderBoth,
			wantBare: 2,
		},
		{
			name: "both, a blank line between the title and the legacy line",
			content: "# T\n\nStatus: Done\n\n**Status:** In Progress\n" +
				"**Priority:** medium\n\n## Context\n\nBody.\n",
			want:     LegacyHeaderBoth,
			wantBare: 3,
		},
		{
			name: "bare-only, no bolded field anywhere",
			content: "# T\n\nStatus: Planned (reviewed, revised)\n\n" +
				"## Context\n\nBody.\n",
			want:     LegacyHeaderBareOnly,
			wantBare: 3,
		},
		{
			name: "title offset by a YAML block still finds the legacy line",
			content: "---\ntype: task\npriority: high\n---\n\n" +
				"# Plan: Phase E\nStatus: Cutover IN PROGRESS\n\n" +
				"**Status:** pending\n**Priority:** high\n\n## Context\n\nBody.\n",
			want:     LegacyHeaderBoth,
			wantBare: 7,
		},
		{
			name:    "clean, the shape CreateTask writes",
			content: validTaskFile,
			want:    LegacyHeaderClean,
		},
		{
			name: "body prose beginning with the word Status is NOT a header line",
			content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n" +
				"## Implementation plan\n\n" +
				"Status: **design only — not implemented.** Stop until the chair accepts.\n",
			want: LegacyHeaderClean,
		},
		{
			name: "a Rust path expression is NOT a header line",
			content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n## Wiring\n\n" +
				"Wiring: replace `SubCheck::new(\"field-names\",\n" +
				"Status::Skipped, \"deferred\")` with check_field_names().\n",
			want: LegacyHeaderClean,
		},
		{
			name: "multi-title wins over a bolded field plus a legacy line",
			content: "# Salvage HNSW recall harness\n\n" +
				"**Status:** retired\n**Priority:** medium\n\n" +
				"# Plan: Salvage HNSW Recall Harness\n\n" +
				"Status: Planned, architecture-reviewed.\n\n## Context\n\nBody.\n",
			want: LegacyHeaderMultiTitle,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScanLegacyHeader(tc.content)
			if got.Class != tc.want {
				t.Errorf("class = %s, want %s (scan %+v)", got.Class, tc.want, got)
			}
			if tc.wantBare != 0 && got.BareLine != tc.wantBare {
				t.Errorf("BareLine = %d, want %d", got.BareLine, tc.wantBare)
			}
		})
	}
}

// TestScanLegacyHeaderIgnoresFencedSpecimens is the self-inflicted-wound test.
// The two live task files that DOCUMENT this defect quote its specimens inside
// code fences. A fence-blind scan classifies those tasks as instances of the bug
// and a fence-blind repair rewrites the very files describing it.
func TestScanLegacyHeaderIgnoresFencedSpecimens(t *testing.T) {
	content := "# One shared classifier for legacy task headers\n\n" +
		"**Status:** pending\n" +
		"**Priority:** medium\n\n" +
		"## The two shapes, measured\n\n" +
		"```\n" + legacyBoth + "```\n\n" +
		"Prose after the fence.\n"

	got := ScanLegacyHeader(content)
	if got.Class != LegacyHeaderClean {
		t.Fatalf("class = %s, want %s — a fenced specimen was read as structure (scan %+v)",
			got.Class, LegacyHeaderClean, got)
	}
	if got.BareLine != 0 {
		t.Errorf("BareLine = %d, want 0: the fenced legacy line is sample text", got.BareLine)
	}
}

// TestRepairLegacyBothHeaderCarriesTheTrueValueInOneWrite proves the repair
// drops the bare line AND carries its value onto the bolded field. A result that
// did only the first half would leave the file asserting the falsehood alone.
func TestRepairLegacyBothHeaderCarriesTheTrueValueInOneWrite(t *testing.T) {
	got, err := RepairLegacyBothHeader(legacyBoth)
	if err != nil {
		t.Fatalf("RepairLegacyBothHeader: %v", err)
	}

	if strings.Contains(got, "\nStatus: Done") {
		t.Errorf("the bare legacy line survived:\n%s", got)
	}
	if strings.Contains(got, "**Status:** pending") {
		t.Errorf("the STALE value survived — dropping the bare line alone leaves "+
			"the file asserting only the falsehood:\n%s", got)
	}
	if !strings.Contains(got, "**Status:** Done") {
		t.Errorf("the true value was not carried onto the bolded field:\n%s", got)
	}

	// The oracle: assert the validator's verdict, never a byte comparison.
	if err := validateWholeTaskFile(got); err != nil {
		t.Fatalf("repaired file is one validateWholeTaskFile refuses: %v\nfile:\n%s", err, got)
	}
}

// TestRepairLegacyBothHeaderRefusesEveryOtherClass is the guard that keeps this
// unit inside the scope the operator split it to. BareOnly needs PROMOTION —
// deleting its only status declaration leaves the file refused at the "missing
// Status" arm rather than repaired — and MultiTitle needs a per-file judgment.
func TestRepairLegacyBothHeaderRefusesEveryOtherClass(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    LegacyHeaderClass
	}{
		{"bare-only", "# T\n\nStatus: Planned\n\n## Context\n\nBody.\n", LegacyHeaderBareOnly},
		{"clean", validTaskFile, LegacyHeaderClean},
		{
			name: "multi-title",
			content: "# First\n\n**Status:** retired\n**Priority:** medium\n\n" +
				"# Second\n\nStatus: Planned\n\n## Context\n\nBody.\n",
			want: LegacyHeaderMultiTitle,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RepairLegacyBothHeader(tc.content)
			if err == nil {
				t.Fatalf("repair accepted a %s file; it must refuse every class but %s",
					tc.want, LegacyHeaderBoth)
			}
			if !strings.Contains(err.Error(), tc.want.String()) {
				t.Errorf("error should name the class it refused (%s), got: %v", tc.want, err)
			}
		})
	}
}

// TestRepairLegacyBothHeaderIsIdempotent proves a second pass over a repaired
// file finds nothing to do. The repaired file is Clean, so the repair refuses it
// — which is how the command reports "nothing to do" rather than rewriting.
func TestRepairLegacyBothHeaderIsIdempotent(t *testing.T) {
	once, err := RepairLegacyBothHeader(legacyBoth)
	if err != nil {
		t.Fatalf("first repair: %v", err)
	}
	if got := ScanLegacyHeader(once).Class; got != LegacyHeaderClean {
		t.Fatalf("repaired file classifies as %s, want %s", got, LegacyHeaderClean)
	}
	if _, err := RepairLegacyBothHeader(once); err == nil {
		t.Fatal("a second repair succeeded; a repaired file must have nothing left to repair")
	}
}

// TestBareOnlyDeletionWouldNotRepairPinsTheReasonForTheSplit records WHY
// BARE-ONLY is a different repair rather than the same one. Deleting the bare
// line — the disposition the original plan proposed for the whole population —
// leaves a file the validator still refuses, now at a different arm. The test
// performs that deletion by hand precisely because no shipped code may do it.
func TestBareOnlyDeletionWouldNotRepairPinsTheReasonForTheSplit(t *testing.T) {
	content := "# T\n\nStatus: Planned (reviewed, revised)\n\n## Context\n\nBody.\n"
	if got := ScanLegacyHeader(content).Class; got != LegacyHeaderBareOnly {
		t.Fatalf("fixture classifies as %s, want %s", got, LegacyHeaderBareOnly)
	}

	lines := strings.Split(content, "\n")
	deleted := strings.Join(slices.Delete(lines, 2, 3), "\n")

	err := validateWholeTaskFile(deleted)
	if err == nil {
		t.Fatal("deleting the only status line produced a file the validator ACCEPTS; " +
			"if this ever passes, the split between BOTH and BARE-ONLY needs revisiting")
	}
	if !strings.Contains(err.Error(), "missing Status") {
		t.Errorf("want the 'missing Status' arm, got: %v", err)
	}
}

// legacyInverted is the shape that breaks the both-class premise: the BOLDED
// value is a correct terminal status and the bare legacy line is prose.
//
// 🔴 The **Priority:** line is the whole point of this fixture, not filler. The
// live specimen that exposed this class (done/phase-d-parallel-operation) has no
// priority field, so validateWholeTaskFile refuses its repaired bytes at the
// missing-Priority arm — an UNRELATED guard that happens to point the same way.
// A fixture without a priority line would therefore pass for the wrong reason
// and would keep passing if the class were deleted. This one carries a valid
// priority field so nothing but the class itself can stop the repair.
const legacyInverted = "# Plan: Phase D — Parallel Operation\n" +
	"Status: Closed — operator accepted retrospective 2026-06-06; advancing to Phase E\n" +
	"**Status:** retired\n" +
	"**Priority:** medium\n\n" +
	"## Context\n\nBody.\n"

// TestScanLegacyHeaderSeparatesTheInvertedShape pins the discriminator: a bolded
// value that is already TERMINAL means the repair's premise — bare is true,
// bolded is stale — is unproven for that file, so it is not a repair candidate.
//
// The premise held for all 17 files repaired in vault 7a8393741, which is exactly
// the kind of run that turns an assumption into an unexamined one. It is derived
// here rather than assumed.
func TestScanLegacyHeaderSeparatesTheInvertedShape(t *testing.T) {
	if got := ScanLegacyHeader(legacyInverted).Class; got != LegacyHeaderInverted {
		t.Fatalf("inverted file classified %s, want %s", got, LegacyHeaderInverted)
	}
	// The ordinary both shape must NOT be swept into the new class: its bolded
	// value is non-terminal, so the premise still holds and it stays repairable.
	if got := ScanLegacyHeader(legacyBoth).Class; got != LegacyHeaderBoth {
		t.Fatalf("ordinary both file classified %s, want %s", got, LegacyHeaderBoth)
	}
}

// TestRepairLegacyBothHeaderRefusesTheInvertedShape is the defect test.
//
// 🔴 It must RED against 6fdf569, and it does: there the file classifies Both,
// the repair carries the bare PROSE onto the bolded field, and the shape oracle
// accepts the result because a sentence is a structurally valid field value. The
// file then sits in done/ asserting a status IsTerminalStatus rejects — the exact
// finding task-status-directory rule 1 exists to report, manufactured by the tool
// meant to help clear it.
func TestRepairLegacyBothHeaderRefusesTheInvertedShape(t *testing.T) {
	repaired, err := RepairLegacyBothHeader(legacyInverted)
	if err == nil {
		t.Fatalf("repair accepted an inverted file and produced:\n%s", repaired)
	}
	if !strings.Contains(err.Error(), LegacyHeaderInverted.String()) {
		t.Errorf("error should name the class it refused (%s), got: %v", LegacyHeaderInverted, err)
	}
}

// TestInvertedRepairWouldReplaceATerminalStatus records WHY the class exists, in
// the terms the failure would actually take. It asserts the property directly —
// a terminal status must never come out non-terminal — so it keeps meaning
// something if the class boundary is later moved or widened.
func TestInvertedRepairWouldReplaceATerminalStatus(t *testing.T) {
	scan := ScanLegacyHeader(legacyInverted)
	if !IsTerminalStatus(scan.BoldValue) {
		t.Fatalf("fixture is not inverted: bolded value %q is not terminal", scan.BoldValue)
	}
	if IsTerminalStatus(scan.BareValue) {
		t.Fatalf("fixture is not inverted: bare value %q is terminal too", scan.BareValue)
	}
	// Whatever the repair does with this file, it must not be "emit the bare
	// value as the status" — that is the write this class exists to prevent.
	if repaired, err := RepairLegacyBothHeader(legacyInverted); err == nil {
		got, ok := TaskStatusValue(statusLineOf(t, repaired))
		if ok && !IsTerminalStatus(got) {
			t.Fatalf("repair replaced terminal %q with non-terminal %q", scan.BoldValue, got)
		}
	}
}

// statusLineOf returns the first bolded status line of a task file.
func statusLineOf(t *testing.T, content string) string {
	t.Helper()
	for line := range strings.SplitSeq(content, "\n") {
		if _, ok := TaskStatusValue(line); ok {
			return line
		}
	}
	return ""
}

// The bare-only fixtures are the LIVE shapes, copied from the corpus rather than
// invented. Every one of the five layouts below is a real file: a one-line run,
// a run with a bare Priority, a wrapped value with the Priority underneath it,
// a YAML-frontmatter file whose priority exists only there, and the run whose
// value CONTAINS "Key:"-shaped lines. The reason a live --apply once failed 7 of
// 7 is that every fixture in the tree was the shape the current writer produces.
const (
	// boMinimal — the dominant layout: one blank line after the title, a
	// one-line status, and no priority anywhere in the file.
	boMinimal = "# Documentation accuracy pass — tutorial\n\n" +
		"Status: Complete\n\n" +
		"## Context\n\nBody.\n"

	// boGapZeroWithPriority — the status abuts the title with no blank line, and
	// the run carries a bare Priority. Both layouts exist on disk; neither is
	// rare.
	boGapZeroWithPriority = "# Phase 10: Help and manpages\n" +
		"Status: Complete\n" +
		"Priority: high\n\n" +
		"## Context\n\nBody.\n"

	// boWrappedThenPriority — friction-analytics-port. The status value wraps
	// over three lines and the bare Priority sits UNDER the wrap, so a repair
	// that stopped at the status line's end would miss it.
	boWrappedThenPriority = "# Port friction analytics\n\n" +
		"Status: In Progress — refined 2026-06-21; two architecture reviews folded into the\n" +
		"numbered Phases 2026-06-23 (review #2 below + Grok cross-review); operator-approved.\n" +
		"Ready to implement.\n" +
		"Priority: Medium\n\n" +
		"## Context\n\nBody.\n"

	// boFrontmatter — the only file in the class with YAML frontmatter, and the
	// only one whose priority exists solely there. Its frontmatter `status`
	// DISAGREES with its bare Status line, which is why the repair reads only
	// `priority` from the block and leaves the rest alone.
	boFrontmatter = "---\ntype: task\nstatus: planned-awaiting-approval\npriority: high\n" +
		"created: 2026-06-06\n---\n\n" +
		"# Plan: Phase E — Plugin/Marketplace Build\n" +
		"Status: Cutover IN PROGRESS (2026-06-06). DONE: PR #3 merged to main.\n\n" +
		"## Context\n\nBody.\n"

	// boKeyShapedProse — vpc-slash-command-shims, the shape that refutes a
	// "value ends at the next Key: line" rule. "Design decision:" and
	// "Depends on:" are line-initial and Key:-shaped, and both are mid-sentence
	// prose inside ONE status value — the following line continues each of them.
	boKeyShapedProse = "# Slash-command shims\n\n" +
		"Status: Phases 1–3 complete (Phase 3 landed 2026-04-13, iteration 56).\n" +
		"Phase 1 shipped `internal/shims/`; 31 unit tests, 82.7% coverage.\n" +
		"Design decision: no `commandsUpgradeOpts`-style opts struct for init\n" +
		"— existing XDG-isolation test harness suffices. Phases 4–5 remain.\n" +
		"Depends on: existing `vp init`, `vp commands upgrade`, agent-file wiring,\n" +
		"and the embedded command template set.\n\n" +
		"## Context\n\nBody.\n"

	// boScopeKey — vp-absorb-legacy-claude-md. Here a Key:-shaped line IS a real
	// second legacy field, and its own value wraps. Same line shape as
	// boKeyShapedProse, opposite meaning: this pair is why the repair never
	// decides between them.
	boScopeKey = "# Absorb legacy CLAUDE.md\n" +
		"Status: Not started. Test case: `/home/johns/code/checkers01/CLAUDE.md`.\n" +
		"Scope: CLAUDE.md first, then AGENTS.md, .cursorrules, .rules, copilot\n" +
		"instructions — all in the same command.\n\n" +
		"## Context\n\nBody.\n"
)

// TestRepairLegacyBareOnlyHeaderConstructsAHeaderTheValidatorAccepts is the
// acceptance test for the whole unit, and it asserts the ORACLE's verdict rather
// than bytes.
//
// 🔴 The no-priority case is the one that refutes the plan this task was filed
// with. Promoting Status alone — what "promotion, not deletion" was read to mean
// — leaves the file refused at the missing-PRIORITY arm, because every file in
// this class carries zero bolded fields of any kind. The deliverable is header
// CONSTRUCTION, and this row is what proves it.
func TestRepairLegacyBareOnlyHeaderConstructsAHeaderTheValidatorAccepts(t *testing.T) {
	cases := []struct {
		name         string
		content      string
		wantStatus   string
		wantPriority string
		wantSource   LegacyPrioritySource
	}{
		{
			name:         "no priority in any form: the supplied default",
			content:      boMinimal,
			wantStatus:   "Complete",
			wantPriority: LegacyPriorityDefault,
			wantSource:   PriorityFromDefault,
		},
		{
			name:         "bare Priority in the run, status abutting the title",
			content:      boGapZeroWithPriority,
			wantStatus:   "Complete",
			wantPriority: "high",
			wantSource:   PriorityFromRun,
		},
		{
			name:         "wrapped status with the Priority underneath the wrap",
			content:      boWrappedThenPriority,
			wantStatus:   "In Progress — refined 2026-06-21; two architecture reviews folded into the",
			wantPriority: "Medium",
			wantSource:   PriorityFromRun,
		},
		{
			name:         "priority only in YAML frontmatter",
			content:      boFrontmatter,
			wantStatus:   "Cutover IN PROGRESS (2026-06-06). DONE: PR #3 merged to main.",
			wantPriority: "high",
			wantSource:   PriorityFromFrontmatter,
		},
		{
			name:         "a second legacy key in the run does not become a field",
			content:      boScopeKey,
			wantStatus:   "Not started. Test case: `/home/johns/code/checkers01/CLAUDE.md`.",
			wantPriority: LegacyPriorityDefault,
			wantSource:   PriorityFromDefault,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RepairLegacyBareOnlyHeader(tc.content)
			if err != nil {
				t.Fatalf("RepairLegacyBareOnlyHeader: %v", err)
			}
			if err := validateWholeTaskFile(got.Content); err != nil {
				t.Fatalf("constructed file is one validateWholeTaskFile refuses: %v\nfile:\n%s", err, got.Content)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.Priority != tc.wantPriority {
				t.Errorf("Priority = %q, want %q", got.Priority, tc.wantPriority)
			}
			if got.PrioritySource != tc.wantSource {
				t.Errorf("PrioritySource = %q, want %q", got.PrioritySource, tc.wantSource)
			}
			if !strings.Contains(got.Content, "**Status:** "+tc.wantStatus) {
				t.Errorf("the constructed Status field is missing:\n%s", got.Content)
			}
			if !strings.Contains(got.Content, "**Priority:** "+tc.wantPriority) {
				t.Errorf("the constructed Priority field is missing:\n%s", got.Content)
			}
			// The parser must read back exactly what was constructed. A header
			// the validator accepts but the reader parses differently would be
			// two definitions of the same block.
			meta := parseTaskMeta("t", got.Content, true)
			if meta.Status != tc.wantStatus || meta.Priority != tc.wantPriority {
				t.Errorf("parseTaskMeta read back {Status:%q Priority:%q}, want {%q %q}",
					meta.Status, meta.Priority, tc.wantStatus, tc.wantPriority)
			}
		})
	}
}

// TestRepairLegacyBareOnlyHeaderKeepsEveryByteOfAKeyShapedValue is the pin for
// the refutation recorded in RepairLegacyBareOnlyHeader's doc comment.
//
// The plan this was built from specified that a value "ends at the next line
// opening a new Key: field". Applied to this real file that rule truncates a
// multi-line status value at "Design decision:", which is mid-sentence prose —
// the NEXT line continues it with an em-dash. The rule is not implementable,
// because boScopeKey carries the identical line shape as a genuine second field.
// So the boundary is never decided: everything after the value's first line is
// relocated verbatim, and this test asserts not one byte was dropped.
func TestRepairLegacyBareOnlyHeaderKeepsEveryByteOfAKeyShapedValue(t *testing.T) {
	got, err := RepairLegacyBareOnlyHeader(boKeyShapedProse)
	if err != nil {
		t.Fatalf("RepairLegacyBareOnlyHeader: %v", err)
	}

	// Every continuation line of the original run, including the two Key:-shaped
	// ones, must still be present.
	for _, want := range []string{
		"Phase 1 shipped `internal/shims/`; 31 unit tests, 82.7% coverage.",
		"Design decision: no `commandsUpgradeOpts`-style opts struct for init",
		"— existing XDG-isolation test harness suffices. Phases 4–5 remain.",
		"Depends on: existing `vp init`, `vp commands upgrade`, agent-file wiring,",
		"and the embedded command template set.",
	} {
		if !strings.Contains(got.Content, want) {
			t.Errorf("a line of the legacy run was DROPPED: %q\nfile:\n%s", want, got.Content)
		}
	}

	// The Key:-shaped lines must NOT have become header fields. Depends in
	// particular has exactly one writer and it is set_relations.
	meta := parseTaskMeta("t", got.Content, true)
	if len(meta.Depends) != 0 {
		t.Errorf("Depends = %v — a prose line was read as a relation nobody wrote", meta.Depends)
	}
	if got.PrioritySource != PriorityFromDefault {
		t.Errorf("PrioritySource = %q; no Priority key is present in this run", got.PrioritySource)
	}

	// And the Status FIELD is the first line only — never the flattened value.
	if !strings.Contains(got.Content, "**Status:** Phases 1–3 complete (Phase 3 landed 2026-04-13, iteration 56).\n") {
		t.Errorf("the Status field is not the value's first line:\n%s", got.Content)
	}
	if strings.Contains(got.Content, "**Status:** Phases 1–3 complete (Phase 3 landed 2026-04-13, iteration 56). Phase 1") {
		t.Errorf("the wrapped value was FLATTENED onto the field; migrate task-status "+
			"replaces the whole value and would destroy it:\n%s", got.Content)
	}
}

// TestRepairLegacyBareOnlyHeaderRelocatesOverflowUnderAnAddressableHeading
// asserts the DESTINATION of the relocated prose, not merely that the file is
// valid. Prose parked above the first H2 is unreachable to amend forever, which
// is the same defect the task-preamble dimension exists to report — so a repair
// that "kept every byte" by leaving them in the preamble would be trading one
// unreachable region for another.
func TestRepairLegacyBareOnlyHeaderRelocatesOverflowUnderAnAddressableHeading(t *testing.T) {
	got, err := RepairLegacyBareOnlyHeader(boWrappedThenPriority)
	if err != nil {
		t.Fatalf("RepairLegacyBareOnlyHeader: %v", err)
	}
	if got.Relocated != 3 {
		t.Fatalf("Relocated = %d, want 3 (the Status line and its two continuation lines)", got.Relocated)
	}

	lines := strings.Split(got.Content, "\n")
	headingAt, proseAt, nextH2At := -1, -1, -1
	for i, l := range lines {
		switch {
		case strings.TrimSpace(l) == legacyHeaderSectionHeading:
			headingAt = i
		case strings.HasPrefix(l, "numbered Phases 2026-06-23"):
			proseAt = i
		case isH2Line(l) && headingAt >= 0 && i > headingAt && nextH2At < 0:
			nextH2At = i
		}
	}
	if headingAt < 0 {
		t.Fatalf("no %q section was written:\n%s", legacyHeaderSectionHeading, got.Content)
	}
	if proseAt < 0 {
		t.Fatalf("the relocated prose is missing entirely:\n%s", got.Content)
	}
	if proseAt < headingAt {
		t.Errorf("the continuation landed at line %d, ABOVE the heading at %d — that is the "+
			"preamble, which amend cannot reach:\n%s", proseAt, headingAt, got.Content)
	}
	if nextH2At >= 0 && proseAt > nextH2At {
		t.Errorf("the continuation landed under a LATER section (%d) than %q (%d)",
			proseAt, legacyHeaderSectionHeading, headingAt)
	}
	// The heading names a topic. A heading carrying a claim cannot be revised —
	// amend is keyed on its text — which is the rule the doctrine states.
	for _, claim := range []string{"TODO", "WIP", "BLOCKED", "not yet", "UNCOMMITTED"} {
		if strings.Contains(legacyHeaderSectionHeading, claim) {
			t.Errorf("the relocation heading asserts %q; a heading is written once and cannot be revised", claim)
		}
	}
}

// TestRepairLegacyBareOnlyHeaderRelocatesTheStatusLineItself is the correction
// to the plan's overflow-only boundary, and it is a LOSS test rather than a
// shape test.
//
// The plan relocated the value's second line onward because
// `vp migrate task-status` — the mandatory next command — replaces the WHOLE
// Status value with a terminal token. That reasoning is right and stops one line
// short: the FIRST line is handed to exactly that field, so the paired run
// destroys it. Measured on a copy of the live vault, one file's 236-byte
// upstream-PR provenance survived nowhere afterwards, and another's relocated
// block opened mid-sentence.
//
// So the assertion is: after simulating what task-status does to the field, the
// legacy value is still in the file.
func TestRepairLegacyBareOnlyHeaderRelocatesTheStatusLineItself(t *testing.T) {
	for _, tc := range []struct{ name, content, value string }{
		{"one-line status", boMinimal, "Complete"},
		{"status plus a bare Priority", boGapZeroWithPriority, "Complete"},
		{"wrapped status", boWrappedThenPriority, "In Progress — refined 2026-06-21; two architecture reviews folded into the"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RepairLegacyBareOnlyHeader(tc.content)
			if err != nil {
				t.Fatalf("RepairLegacyBareOnlyHeader: %v", err)
			}
			if got.Relocated == 0 {
				t.Fatalf("nothing was relocated, so the legacy value exists only in the field "+
					"that the paired command overwrites:\n%s", got.Content)
			}

			// What migrate task-status does next, through the same writer.
			stamped := replaceStatusLine(got.Content, StatusRetired)
			if !strings.Contains(stamped, "**Status:** "+StatusRetired) {
				t.Fatalf("the simulation did not stamp the field:\n%s", stamped)
			}
			if !strings.Contains(stamped, tc.value) {
				t.Errorf("the legacy status value %q did not survive the paired command — "+
					"relocating only the OVERFLOW hands the value's first line to the very "+
					"field that command replaces:\n%s", tc.value, stamped)
			}
			// And it survives somewhere amend can reach.
			if !strings.Contains(stamped, legacyHeaderSectionHeading) {
				t.Errorf("no %q section carries it:\n%s", legacyHeaderSectionHeading, stamped)
			}
		})
	}
}

// TestRepairLegacyBareOnlyHeaderRefusesEveryOtherClass is the scope guard. Each
// other class has its own disposition and its own task; a repair that widened
// quietly would be writing files nobody reviewed.
func TestRepairLegacyBareOnlyHeaderRefusesEveryOtherClass(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    LegacyHeaderClass
	}{
		{"both", legacyBoth, LegacyHeaderBoth},
		{"clean", validTaskFile, LegacyHeaderClean},
		{"inverted", legacyInverted, LegacyHeaderInverted},
		{
			name: "multi-title",
			content: "# First\n\n**Status:** retired\n**Priority:** medium\n\n" +
				"# Second\n\nStatus: Planned\n\n## Context\n\nBody.\n",
			want: LegacyHeaderMultiTitle,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RepairLegacyBareOnlyHeader(tc.content)
			if err == nil {
				t.Fatalf("the bare-only repair accepted a %s file", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want.String()) {
				t.Errorf("error should name the class it refused (%s), got: %v", tc.want, err)
			}
		})
	}
}

// TestRepairLegacyBareOnlyHeaderIsIdempotent proves a repaired file has left the
// population. The relocated prose still contains a line beginning "Status:", and
// the classifier must not read it as a legacy header line — its structural guard
// requires the nearest preceding non-blank line to be the title, and after the
// repair it is an H2.
func TestRepairLegacyBareOnlyHeaderIsIdempotent(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"minimal", boMinimal},
		{"wrapped", boWrappedThenPriority},
		{"key-shaped prose", boKeyShapedProse},
		{"frontmatter", boFrontmatter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			once, err := RepairLegacyBareOnlyHeader(tc.content)
			if err != nil {
				t.Fatalf("first repair: %v", err)
			}
			if got := ScanLegacyHeader(once.Content).Class; got != LegacyHeaderClean {
				t.Fatalf("repaired file classifies as %s, want %s:\n%s", got, LegacyHeaderClean, once.Content)
			}
			if _, err := RepairLegacyBareOnlyHeader(once.Content); err == nil {
				t.Error("a second repair succeeded; a repaired file must have nothing left to repair")
			}
		})
	}
}

// TestRepairLegacyBareOnlyHeaderIgnoresFencedSpecimens is the self-inflicted
// wound. The task files that DOCUMENT this defect quote its specimens inside
// code fences — this one included — and a fence-blind repair rewrites the very
// files describing the bug.
func TestRepairLegacyBareOnlyHeaderIgnoresFencedSpecimens(t *testing.T) {
	content := "# Promote a bare legacy Status line\n\n" +
		"**Status:** pending\n**Priority:** medium\n\n" +
		"## The shape, quoted\n\n" +
		"```\n" + boMinimal + "```\n\nProse after the fence.\n"

	if got := ScanLegacyHeader(content).Class; got != LegacyHeaderClean {
		t.Fatalf("class = %s, want %s — a fenced specimen was read as structure", got, LegacyHeaderClean)
	}
	if _, err := RepairLegacyBareOnlyHeader(content); err == nil {
		t.Fatal("the repair rewrote a file whose only legacy header is a fenced quotation")
	}
}

// TestRepairLegacyBareOnlyHeaderRefusesARelocationHeadingCollision keeps the
// repair from stranding a section that already exists. A second H2 by the same
// name is unreachable to amend, which takes the first match — the defect this
// project already has filed against CreateTask's unconditional "## Context".
func TestRepairLegacyBareOnlyHeaderRefusesARelocationHeadingCollision(t *testing.T) {
	content := "# Wrapped status beside an existing section\n\n" +
		"Status: In Progress — this value wraps onto\n" +
		"a second line, so prose must be relocated.\n\n" +
		legacyHeaderSectionHeading + "\n\nSomething a human already wrote here.\n"

	if got := ScanLegacyHeader(content).Class; got != LegacyHeaderBareOnly {
		t.Fatalf("fixture classifies as %s, want %s", got, LegacyHeaderBareOnly)
	}
	_, err := RepairLegacyBareOnlyHeader(content)
	if err == nil {
		t.Fatal("the repair added a second section by a name the file already uses")
	}
	if !strings.Contains(err.Error(), legacyHeaderSectionHeading) {
		t.Errorf("the refusal should name the colliding heading, got: %v", err)
	}
}

// TestFrontmatterValueReadsOnlyATerminatedOpeningBlock pins the delimiter policy
// this function owns. The list is not claimed to be closed — an earlier version
// of this comment said "the two ways this reader could be wrong" and a review
// promptly found three more, which is the same defect shape as a heading that
// asserts a claim instead of naming a topic.
//
// The nested-key row is the one that matters most: it is the guarantee a
// hand-rolled copy of the key match had silently dropped, and it belongs to
// frontmatterField now rather than to this function.
func TestFrontmatterValueReadsOnlyATerminatedOpeningBlock(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
		wantOK  bool
	}{
		{"the live shape", boFrontmatter, "high", true},
		{"a --- rule in the body is not frontmatter",
			"# T\n\n---\npriority: high\n---\n", "", false},
		{"a key below the closing delimiter is body content",
			"---\ntype: task\n---\n\npriority: high\n", "", false},
		{"no frontmatter at all", boMinimal, "", false},
		{"an UNTERMINATED block does not run to EOF",
			"---\ntype: task\n\n# T\n\npriority: critical\n", "", false},
		{"a NESTED key is not top-level",
			"---\ntype: task\nmeta:\n  priority: critical\n---\n\n# T\n", "", false},
		{"the key match is case-sensitive, like every other in this package",
			"---\nPriority: HIGH\n---\n\n# T\n", "", false},
		{"a quoted scalar is unquoted",
			"---\npriority: \"high\"\n---\n\n# T\n", "high", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := frontmatterValue(tc.content, "priority")
			if v != tc.want || ok != tc.wantOK {
				t.Errorf("frontmatterValue = (%q, %v), want (%q, %v)", v, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestFrontmatterReadersShareOneKeyDefinition is the anti-drift pin. Two readers
// of one file format existed in this package and one was a looser copy of the
// other; they now share frontmatterField, and this drives BOTH over the same
// inputs so a future edit to either cannot quietly re-open the gap.
func TestFrontmatterReadersShareOneKeyDefinition(t *testing.T) {
	cases := []struct{ name, front, want string }{
		{"top-level", "type: task\npriority: high", "high"},
		{"nested is not top-level", "meta:\n  priority: critical", ""},
		{"case-sensitive", "Priority: HIGH", ""},
		{"quoted", "priority: 'high'", "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			direct := frontmatterField(tc.front, "priority")
			if direct != tc.want {
				t.Errorf("frontmatterField = %q, want %q", direct, tc.want)
			}
			// The task-file reader must agree, wrapped in its own delimiters.
			viaTask, _ := frontmatterValue("---\n"+tc.front+"\n---\n\n# T\n", "priority")
			if viaTask != direct {
				t.Errorf("frontmatterValue = %q but frontmatterField = %q — the two readers "+
					"disagree about what a key is, which is the drift the shared definition exists to stop",
					viaTask, direct)
			}
			// ...and so must the path-based reader, over the same bytes.
			dir := t.TempDir()
			p := filepath.Join(dir, "n.md")
			if err := os.WriteFile(p, []byte("---\n"+tc.front+"\n---\n\nbody\n"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			if viaHead := frontmatterFieldFromHead(p, "priority"); viaHead != direct {
				t.Errorf("frontmatterFieldFromHead = %q but frontmatterField = %q", viaHead, direct)
			}
		})
	}
}

// The multi-title fixtures are the LIVE shapes. The canonical one is 24 of the
// 33 files in the class: a five-line modern header, then a rival titled block
// carrying its own bolded fields.
const (
	// mtCanonical — H1 / blank / Status / Priority / blank / rival H1 with its
	// own fields. The dominant shape on disk.
	mtCanonical = "# Refresh per-project skill shims on upgrade\n\n" +
		"**Status:** retired\n**Priority:** medium\n\n" +
		"# Refresh per-project skill shims on upgrade (not only vp init)\n\n" +
		"**Status:** pending — investigated 2026-06-07\n" +
		"**Priority:** medium\n\n" +
		"## Problem\n\nBody.\n"

	// mtYAMLWedge — a stranded YAML block between the two titles, carrying a
	// THIRD status syntax. It is not frontmatter: it does not start at line 1.
	// Four live files have this.
	mtYAMLWedge = "# Add vp_list_learnings / vp_get_learning\n\n" +
		"**Status:** retired\n**Priority:** medium\n\n" +
		"---\ntype: task\nstatus: reviewed-ready-to-implement\npriority: medium\n---\n\n" +
		"# Plan: Add cross-project \"learnings\" support\n\n" +
		"**Status:** Reviewed twice — 2026-06-20\n**Priority:** medium\n\n" +
		"## Context\n\nBody.\n"

	// mtBareLegacySecond — the second title's block uses the BARE legacy syntax,
	// not the bolded one, so there is nothing under it to relabel.
	mtBareLegacySecond = "# Salvage HNSW constitution recall harness\n\n" +
		"**Status:** retired\n**Priority:** medium\n\n" +
		"# Plan: Salvage HNSW Recall Harness into vibe-palace\n\n" +
		"Status: Planned, architecture-reviewed.\n" +
		"Not started. Spun out of `hnsw-library-bug-fixes`.\n\n" +
		"## Background\n\nBody.\n"

	// mtSectionHeadings — H1 used as ORDINARY SECTION HEADINGS of one document.
	// The live specimen opens H2 sections at line 18 and its first "rival" H1 is
	// at line 57, reading "PHASE 1 — MOVED OUT"; later ones read "Open
	// Questions" and "Definition of done".
	mtSectionHeadings = "# ADR-006 umbrella — CLOSED 2026-08-16\n\n" +
		"**Status:** retired\n**Priority:** high\n**Parent:** honest-instruments\n\n" +
		"## The thesis\n\nBody.\n\n" +
		"# PHASE 2 — DERIVE WHAT IS ACTUALLY DERIVABLE\n\nPhase body.\n\n" +
		"# Open Questions\n\nQuestions.\n\n" +
		"# Definition of done\n\nDone when.\n"

	// mtSectionsTwoTitlesOnly — trips ONLY the prepended-over rule: exactly two
	// H1s, but an H2 opened the body before the second one arrives. Without this
	// fixture the prepend rule would be untested, because the live specimen trips
	// the title-count rule as well.
	mtSectionsTwoTitlesOnly = "# One document with a mis-levelled section\n\n" +
		"**Status:** retired\n**Priority:** medium\n\n" +
		"## Context\n\nBody.\n\n" +
		"# Definition of done\n\nDone when.\n"

	// mtThreeTitles — trips ONLY the title-count rule: three rival titles, none
	// preceded by an H2. Without this fixture the count rule would be untested
	// for the same reason.
	mtThreeTitles = "# First\n\n**Status:** retired\n**Priority:** medium\n\n" +
		"# Second\n\n**Status:** pending\n**Priority:** high\n\n" +
		"# Third\n\n**Status:** blocked\n**Priority:** low\n\n" +
		"## Body\n\nBody.\n"

	// mtCorruptModernHeader — 🔴 THE TRAP. Its MODERN header is non-contiguous:
	// free prose sits between its own **Status:** and **Priority:** lines. After
	// the transform this file classifies CLEAN and the validator still REFUSES
	// it, so a repair keyed on the classifier writes nothing for it and says
	// nothing about it.
	mtCorruptModernHeader = "# Vault whole-file writes have no lock across read-modify-write\n\n" +
		"**Status:** retired\n" +
		"Plan-reviewed 2026-06-06; design decisions below are locked.\n" +
		"No code written.\n" +
		"**Priority:** medium\n\n" +
		"# Vault whole-file writes lack RMW serialization (lost-update hole)\n\n" +
		"## Problem\n\nBody.\n"
)

// TestRepairLegacyMultiTitleDemotesAndRelabels is the acceptance test, and it
// asserts the ORACLE's verdict rather than bytes.
func TestRepairLegacyMultiTitleDemotesAndRelabels(t *testing.T) {
	cases := []struct {
		name         string
		content      string
		wantDemoted  string
		wantStatus   int
		wantPriority int
	}{
		{"canonical rival block", mtCanonical,
			"## Refresh per-project skill shims on upgrade (not only vp init)", 1, 1},
		{"stranded YAML wedge between the titles", mtYAMLWedge,
			"## Plan: Add cross-project \"learnings\" support", 1, 1},
		{"second block uses the bare legacy syntax", mtBareLegacySecond,
			"## Plan: Salvage HNSW Recall Harness into vibe-palace", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScanLegacyHeader(tc.content).Class; got != LegacyHeaderMultiTitle {
				t.Fatalf("fixture classifies as %s, want %s", got, LegacyHeaderMultiTitle)
			}
			got, err := RepairLegacyMultiTitleHeader(tc.content)
			if err != nil {
				t.Fatalf("RepairLegacyMultiTitleHeader: %v", err)
			}
			if err := validateWholeTaskFile(got.Content); err != nil {
				t.Fatalf("transformed file is one validateWholeTaskFile refuses: %v\nfile:\n%s", err, got.Content)
			}
			if got.DemotedTitle != tc.wantDemoted {
				t.Errorf("DemotedTitle = %q, want %q", got.DemotedTitle, tc.wantDemoted)
			}
			if got.RelabelledStatus != tc.wantStatus || got.RelabelledPriority != tc.wantPriority {
				t.Errorf("relabelled {status:%d priority:%d}, want {%d %d}",
					got.RelabelledStatus, got.RelabelledPriority, tc.wantStatus, tc.wantPriority)
			}
			// The legacy values survive, verbatim, as prose.
			for _, l := range strings.Split(tc.content, "\n") {
				trimmed := strings.TrimSpace(l)
				if !isStatusLine(trimmed) && !isPriorityLine(trimmed) {
					continue
				}
				value := trimmed[strings.Index(trimmed, ":**")+3:]
				if !strings.Contains(got.Content, strings.TrimSpace(value)) {
					t.Errorf("a header value was lost: %q\nfile:\n%s", value, got.Content)
				}
			}
		})
	}
}

// TestRepairLegacyMultiTitleChangesNoReadersAnswer is the argument the whole
// design rests on, made a measurement.
//
// The filed plan refused automation because "the two headers disagree, and
// choosing between them is a judgment call per file". The premise is true; the
// conclusion does not follow, because this transform DOES NOT CHOOSE. parseTaskMeta
// is first-wins and the modern block is first, so every reader already returns the
// modern values and the legacy ones are invisible. If that ever stops being true,
// this test fails and the design's justification is gone with it.
func TestRepairLegacyMultiTitleChangesNoReadersAnswer(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"canonical", mtCanonical},
		{"yaml wedge", mtYAMLWedge},
		{"bare legacy second block", mtBareLegacySecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RepairLegacyMultiTitleHeader(tc.content)
			if err != nil {
				t.Fatalf("RepairLegacyMultiTitleHeader: %v", err)
			}
			before := parseTaskMeta("s", tc.content, true)
			after := parseTaskMeta("s", got.Content, true)
			if before.Status != after.Status || before.Priority != after.Priority ||
				before.Title != after.Title || before.Parent != after.Parent ||
				strings.Join(before.Depends, ",") != strings.Join(after.Depends, ",") {
				t.Errorf("a reader's answer CHANGED — the transform chose a value, which is the one "+
					"thing it must not do:\n before {%q %q %q}\n after  {%q %q %q}",
					before.Status, before.Priority, before.Title,
					after.Status, after.Priority, after.Title)
			}
		})
	}
}

// TestRepairLegacyMultiTitleRefusesWhatIsNotAPrependedHeader pins the two
// preconditions SEPARATELY. The one live specimen trips both, so a single
// fixture would leave one rule permanently untested and free to rot.
func TestRepairLegacyMultiTitleRefusesWhatIsNotAPrependedHeader(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{"H1 used as section headings, live shape", mtSectionHeadings, "already opened this document's body"},
		{"exactly two titles, but a section opened the body first",
			mtSectionsTwoTitlesOnly, "already opened this document's body"},
		{"three rival titles, no section before the second", mtThreeTitles, "unfenced H1 lines"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScanLegacyHeader(tc.content).Class; got != LegacyHeaderMultiTitle {
				t.Fatalf("fixture classifies as %s, want %s", got, LegacyHeaderMultiTitle)
			}
			_, err := RepairLegacyMultiTitleHeader(tc.content)
			if err == nil {
				t.Fatal("the repair restructured a document that is not a prepended header")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should say %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

// TestRepairLegacyMultiTitleTrapClassifierSaysCleanValidatorRefuses is the
// single most important test in this unit.
//
// 🔴 After the transform, ScanLegacyHeader returns `clean` for EVERY file it
// touches — including this one, whose modern header is corrupted independently of
// its two titles. The classifier is honest (it counts titles, and one title is one
// title), but a repair keyed on it would write nothing here AND report nothing,
// leaving a file no tool can write and no tool mentions.
//
// The assertion is the trap itself: classifier clean, validator refusing, repair
// refusing with the validator's own words.
func TestRepairLegacyMultiTitleTrapClassifierSaysCleanValidatorRefuses(t *testing.T) {
	if got := ScanLegacyHeader(mtCorruptModernHeader).Class; got != LegacyHeaderMultiTitle {
		t.Fatalf("fixture classifies as %s, want %s", got, LegacyHeaderMultiTitle)
	}

	// What a classifier-keyed repair would have believed.
	demotedOnly := demoteH1Everywhere(mtCorruptModernHeader)
	if got := ScanLegacyHeader(demotedOnly).Class; got != LegacyHeaderClean {
		t.Fatalf("the trap does not reproduce: classifier says %s after the transform, want %s",
			got, LegacyHeaderClean)
	}
	if err := validateWholeTaskFile(demotedOnly); err == nil {
		t.Fatal("the trap does not reproduce: the validator ACCEPTS the transformed file, so " +
			"classifier and validator agree and there is nothing to guard against")
	}

	// What this repair actually does: refuse, in the validator's words.
	_, err := RepairLegacyMultiTitleHeader(mtCorruptModernHeader)
	if err == nil {
		t.Fatal("the repair wrote a file the validator refuses")
	}
	if !strings.Contains(err.Error(), "contiguous header block") {
		t.Errorf("the refusal must carry the VALIDATOR's reason so an operator can act on it, got: %v", err)
	}
}

// demoteH1Everywhere is the classifier-satisfying transform WITHOUT the
// validator gate — the thing this unit must not be. It exists only so the trap
// test can demonstrate what such a repair would have concluded.
func demoteH1Everywhere(content string) string {
	lines := strings.Split(content, "\n")
	seen := 0
	for _, l := range mdfence.OutsideFences(content) {
		if !isH1Line(l.Text) {
			continue
		}
		seen++
		if seen > 1 {
			lines[l.Num-1] = demoteH1(lines[l.Num-1])
		}
	}
	return strings.Join(lines, "\n")
}

// TestRepairLegacyMultiTitleRefusesEveryOtherClass is the scope guard.
func TestRepairLegacyMultiTitleRefusesEveryOtherClass(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    LegacyHeaderClass
	}{
		{"both", legacyBoth, LegacyHeaderBoth},
		{"clean", validTaskFile, LegacyHeaderClean},
		{"inverted", legacyInverted, LegacyHeaderInverted},
		{"bare-only", boMinimal, LegacyHeaderBareOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RepairLegacyMultiTitleHeader(tc.content)
			if err == nil {
				t.Fatalf("the multi-title repair accepted a %s file", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want.String()) {
				t.Errorf("error should name the class it refused (%s), got: %v", tc.want, err)
			}
		})
	}
}

// TestRepairLegacyMultiTitleIsIdempotent proves a transformed file has left the
// population: one title remains, so the classifier no longer reports it and the
// repair refuses a second pass.
func TestRepairLegacyMultiTitleIsIdempotent(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"canonical", mtCanonical},
		{"yaml wedge", mtYAMLWedge},
		{"bare legacy second block", mtBareLegacySecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			once, err := RepairLegacyMultiTitleHeader(tc.content)
			if err != nil {
				t.Fatalf("first repair: %v", err)
			}
			if got := ScanLegacyHeader(once.Content).Class; got != LegacyHeaderClean {
				t.Fatalf("transformed file classifies as %s, want %s:\n%s", got, LegacyHeaderClean, once.Content)
			}
			if _, err := RepairLegacyMultiTitleHeader(once.Content); err == nil {
				t.Error("a second repair succeeded; a transformed file must have nothing left to repair")
			}
		})
	}
}

// TestRepairLegacyMultiTitleIgnoresFencedSpecimens is the self-inflicted wound:
// the task files documenting this defect quote two-H1 specimens inside fences.
func TestRepairLegacyMultiTitleIgnoresFencedSpecimens(t *testing.T) {
	content := "# A per-file proposal for multi-title task files\n\n" +
		"**Status:** pending\n**Priority:** medium\n\n" +
		"## The shape, quoted\n\n" +
		"```\n" + mtCanonical + "```\n\nProse after the fence.\n"

	if got := ScanLegacyHeader(content).Class; got != LegacyHeaderClean {
		t.Fatalf("class = %s, want %s — a fenced specimen was read as structure", got, LegacyHeaderClean)
	}
	if _, err := RepairLegacyMultiTitleHeader(content); err == nil {
		t.Fatal("the repair rewrote a file whose only second title is a fenced quotation")
	}
}

// TestRepairLegacyMultiTitleRelabelIsInvisibleToEveryFieldReader pins the one
// invented thing. "**Legacy status:**" must not be readable as a field by any
// predicate in this package — otherwise the relabel would create the duplicate it
// exists to remove.
func TestRepairLegacyMultiTitleRelabelIsInvisibleToEveryFieldReader(t *testing.T) {
	line := legacyStatusRelabel + " In Progress — investigated 2026-06-07"
	if isStatusLine(line) {
		t.Errorf("%q reads as a **Status:** line", line)
	}
	if isHeaderFieldLine(line) {
		t.Errorf("%q reads as a header field line", line)
	}
	if _, ok := legacyStatusValue(line); ok {
		t.Errorf("%q reads as a BARE legacy status line", line)
	}
	if _, ok := TaskStatusValue(line); ok {
		t.Errorf("%q is visible to the exported status reader the audit uses", line)
	}
	pline := legacyPriorityRelabel + " medium"
	if isPriorityLine(pline) || isHeaderFieldLine(pline) {
		t.Errorf("%q reads as a **Priority:** line", pline)
	}
}

// TestDemoteH1PreservesIndentation guards the one-character edit. isH1Line trims
// before testing, so an indented "  # Title" is an H1; prepending a hash at the
// START of the line would produce "# # Title", which is still an H1 and would
// leave the file with two titles after a repair that reported success.
func TestDemoteH1PreservesIndentation(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"# Title", "## Title"},
		{"  # Title", "  ## Title"},
		{"\t# Title", "\t## Title"},
	} {
		if got := demoteH1(tc.in); got != tc.want {
			t.Errorf("demoteH1(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if isH1Line(demoteH1(tc.in)) {
			t.Errorf("demoteH1(%q) is still an H1", tc.in)
		}
	}
}

const (
	// mtSectionH1NoH2Anywhere — 🔴 FIX 1's shape. H1 used as a SECTION heading in
	// a document that has no H2 at all, so nothing gives it away structurally.
	// This is the derive-dont-ask harm in a file that merely lacks the H2 that
	// exposed it. No live file has this shape; the command runs against vaults
	// this measurement never saw.
	mtSectionH1NoH2Anywhere = "# Port the surface handshake\n\n" +
		"**Status:** retired\n**Priority:** medium\n\n" +
		"# Background\n\n" +
		"Prose about the background, written by an author who used H1 for sections.\n"

	// mtH2InModernPreamble — 🔴 FIX 2's shape. A GENUINE prepended-over file whose
	// MODERN half carries a section of its own before the legacy title arrives.
	// The legacy title owns its own header block, which is what shows it opens a
	// document; refusing this file would be an over-refusal.
	mtH2InModernPreamble = "# Modern title\n\n" +
		"**Status:** retired\n**Priority:** medium\n\n" +
		"## Review notes\n\nA section belonging to the modern half.\n\n" +
		"# Legacy document title\n\n" +
		"**Status:** In Progress\n**Priority:** high\n\n" +
		"## Problem\n\nBody.\n"
)

// TestRepairLegacyMultiTitleAsksOneQuestionOfTheSecondTitle is the coherent form
// of the shape rule, and it replaces two patches that agreed by luck.
//
// THE QUESTION IS SINGULAR: can this H1 be shown to OPEN A DOCUMENT OF ITS OWN?
// A task document starts one of two ways, and either is enough:
//
//	A. it owns header material — a bolded field line or a bare legacy "Status:"
//	   line — before the next heading of any level; or
//	B. it is followed by the file's FIRST H2, meaning no section of the first
//	   document precedes it.
//
// Neither signal alone is sufficient, and the corpus proves both directions:
// A alone wrongly refuses two live files whose legacy half carries no header
// block (mcp-execute-plan-no-truncation, vault-write-concurrency); B alone
// wrongly refuses a prepended-over file whose modern half has a section. Measured
// over the live class: A holds for 30, B for 32, A-or-B for 32, and NEITHER for
// exactly one — the document that uses H1 for its section headings.
func TestRepairLegacyMultiTitleAsksOneQuestionOfTheSecondTitle(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantRepair bool
		wantErr    string
	}{
		{
			name:       "evidence A only: a section precedes the legacy title, which owns a header block",
			content:    mtH2InModernPreamble,
			wantRepair: true,
		},
		{
			name:       "evidence B only: no header block under the legacy title, but no section precedes it",
			content:    mtBareLegacySecond,
			wantRepair: true,
		},
		{
			name:    "NEITHER: sections already opened, and the later H1 owns no header",
			content: mtSectionHeadings,
			wantErr: "cannot be shown to open a document",
		},
		{
			name:    "NEITHER: no H2 anywhere, so the later H1 opens no section either",
			content: mtSectionH1NoH2Anywhere,
			wantErr: "cannot be shown to open a document",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScanLegacyHeader(tc.content).Class; got != LegacyHeaderMultiTitle {
				t.Fatalf("fixture classifies as %s, want %s", got, LegacyHeaderMultiTitle)
			}
			got, err := RepairLegacyMultiTitleHeader(tc.content)
			if tc.wantRepair {
				if err != nil {
					t.Fatalf("a genuine prepended-over file was refused: %v", err)
				}
				if verr := validateWholeTaskFile(got.Content); verr != nil {
					t.Fatalf("transformed file is one the validator refuses: %v", verr)
				}
				return
			}
			if err == nil {
				t.Fatal("the repair restructured a document whose later H1 is a section heading")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should say %q, got: %v", tc.wantErr, err)
			}
			if got.Refusal != LegacyRefusedShape {
				t.Errorf("Refusal = %v, want %v — the caller must be able to tell a SHAPE refusal "+
					"from a validator one, because the validator never saw these bytes",
					got.Refusal, LegacyRefusedShape)
			}
		})
	}
}

// TestRepairLegacyMultiTitleReportsWhichRefusalItMade pins the distinction the
// operator-facing text depends on.
//
// A SHAPE refusal happens BEFORE the transform runs, so the validator never sees
// any bytes and there is nothing for it to have refused. A VALIDATOR refusal
// happens after. Reporting both as "the validator refuses it" asserts the
// design's central claim about a row where it does not hold.
func TestRepairLegacyMultiTitleReportsWhichRefusalItMade(t *testing.T) {
	shape, err := RepairLegacyMultiTitleHeader(mtSectionHeadings)
	if err == nil {
		t.Fatal("expected a shape refusal")
	}
	if shape.Refusal != LegacyRefusedShape {
		t.Errorf("Refusal = %v, want %v", shape.Refusal, LegacyRefusedShape)
	}

	val, err := RepairLegacyMultiTitleHeader(mtCorruptModernHeader)
	if err == nil {
		t.Fatal("expected a validator refusal")
	}
	if val.Refusal != LegacyRefusedValidator {
		t.Errorf("Refusal = %v, want %v", val.Refusal, LegacyRefusedValidator)
	}

	ok, err := RepairLegacyMultiTitleHeader(mtCanonical)
	if err != nil {
		t.Fatalf("RepairLegacyMultiTitleHeader: %v", err)
	}
	if ok.Refusal != LegacyRefusedNone {
		t.Errorf("Refusal = %v on success, want %v", ok.Refusal, LegacyRefusedNone)
	}
}

// TestCreateTaskRefusesABodyRepeatingTheConventionalHeading is THE discriminating
// test for the option that shipped, and it is written to be RED under the two
// options that were not taken.
//
// 🔴 IT ASSERTS AN ERROR *AND* THAT NO FILE EXISTS. Both halves are load-bearing.
// The alternatives considered were "detect the collision and skip the emit" and
// "splice the author's section under the emitted heading"; both CREATE THE TASK
// successfully and both produce a file with exactly one conventional heading. So
// every assertion about the resulting file's content is GREEN under all three
// options and discriminates nothing — the only observable that separates refusal
// from adaptation is that refusal leaves no task behind.
//
// The body shapes cover the widened predicate: the heading may sit anywhere, not
// only at the top.
func TestCreateTaskRefusesABodyRepeatingTheConventionalHeading(t *testing.T) {
	heading := "## " + ConventionalFirstHeading
	for _, tc := range []struct {
		name, content string
	}{
		{"body opens with it", heading + "\n\nFiled 2026-09-05.\n"},
		{"body carries it after another section", "## Design\n\nPlan.\n\n" + heading + "\n\nFiled.\n"},
		{"body carries it after prose", "Framing.\n\n" + heading + "\n\nFiled.\n"},
		{"indented, since the check trims", "  " + heading + "\n\nFiled.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := testVault(t)
			err := v.CreateTask("proj", TaskSpec{
				Slug: "task", Title: "T", Priority: "medium", Content: tc.content,
			})
			if err == nil {
				t.Fatal("CreateTask accepted a body repeating the conventional heading; the file it " +
					"wrote carries two sections under one amend key")
			}
			if !strings.Contains(err.Error(), ConventionalFirstHeading) {
				t.Errorf("error = %v, want it to name %q so the author can act on it", err, ConventionalFirstHeading)
			}
			// The half that options 2 and 4 fail: no task was created.
			if _, _, gerr := v.GetTask("proj", "task"); gerr == nil {
				t.Error("a refused create left a task file behind")
			}
		})
	}
}

// TestCreateTaskAcceptsAFencedConventionalHeading proves the rule is fence-aware,
// and the specimen is not hypothetical.
//
// A fence-blind form of this rule would have refused the creation of the very
// task that filed this defect: its reproduction section quotes a `## Context`
// pair inside a `~~~md` fence. Measured over the live vault at the time of this
// change, a fence-blind duplicate scan reports two more files than a fence-aware
// one, and one of those two is an ACTIVE file — so fence-blindness overstates the
// active population by 100%. Inside a fence nothing is structure, which is the
// same rule the four metadata arms already obey.
func TestCreateTaskAcceptsAFencedConventionalHeading(t *testing.T) {
	heading := "## " + ConventionalFirstHeading
	for _, tc := range []struct {
		name, content string
	}{
		{"backtick fence", "The collision looks like:\n\n```md\n" + heading + "\n\n" + heading + "\n```\n\nQuoted, not applied.\n"},
		{"tilde fence", "Reproduction:\n\n~~~md\n" + heading + "\n~~~\n\nProse.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := testVault(t)
			if err := v.CreateTask("proj", TaskSpec{
				Slug: "task", Title: "T", Priority: "medium", Content: tc.content,
			}); err != nil {
				t.Fatalf("CreateTask refused a FENCED sample heading: %v", err)
			}
			_, body, err := v.GetTask("proj", "task")
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if err := validateWholeTaskFile(body); err != nil {
				t.Fatalf("the accepted file does not validate: %v\n%s", err, body)
			}
		})
	}
}

// TestConventionalHeadingRefusalMatchesSectionBoundsEquality is the anti-second-
// definition pin, and it is why the refusal matches EXACTLY rather than folding
// case the way refuseLegacySectionCollision does.
//
// A refusal has to be keyed on the same notion of "same heading" as the resolver
// it protects. sectionBounds compares `"## " + section` with `==`, so a body
// heading that differs only in case is a DIFFERENT amend key and strands nothing;
// refusing it would reject a body that produces the documented, accepted outcome.
// Two definitions that agree only on today's inputs are still two definitions, so
// this drives both over the same set rather than asserting the property in prose.
func TestConventionalHeadingRefusalMatchesSectionBoundsEquality(t *testing.T) {
	for _, heading := range []string{
		"## " + ConventionalFirstHeading,
		"## " + strings.ToLower(ConventionalFirstHeading),
		"## " + strings.ToUpper(ConventionalFirstHeading),
		"## " + ConventionalFirstHeading + " and more",
		"### " + ConventionalFirstHeading,
	} {
		t.Run(heading, func(t *testing.T) {
			refused := isConventionalFirstHeadingLine(heading)

			// Does this heading actually collide? Build the file create WOULD
			// have written, then ask the RESOLVER twice: locate the first
			// section by the conventional name, and re-run over everything after
			// it. A second hit means two sections share one key, so the second is
			// unreachable by every section name for the life of the file — which
			// is the defect itself, measured through sectionBounds rather than by
			// restating its equality rule here.
			file := "# T\n\n**Status:** pending\n**Priority:** medium\n\n" +
				"## " + ConventionalFirstHeading + "\n\n" + heading + "\n\nAuthor prose.\n"
			_, end, found := sectionBounds(file, ConventionalFirstHeading)
			if !found {
				t.Fatalf("sectionBounds cannot find the emitted heading at all in:\n%s", file)
			}
			lines := strings.Split(file, "\n")
			var strands bool
			if end < len(lines) {
				_, _, strands = sectionBounds(strings.Join(lines[end:], "\n"), ConventionalFirstHeading)
			}

			if refused != strands {
				t.Errorf("isConventionalFirstHeadingLine(%q) = %v but sectionBounds says it strands = %v — "+
					"the refusal and the resolver disagree about which headings are the same heading",
					heading, refused, strands)
			}
		})
	}
}

// TestAmendBodyWithTheConventionalHeadingIsStillRefused records what the fifth arm
// does to the OTHER caller of validateTaskBody.
//
// AmendTask calls validateTaskBody before validateAmendBody, and validateAmendBody
// already refuses EVERY H2 in an amend body. So this arm changes no amend outcome,
// only the message for one particular H2 — which is why its remedy text also names
// the "###" fix. Pinned so a later reader does not read the reordering as free.
func TestAmendBodyWithTheConventionalHeadingIsStillRefused(t *testing.T) {
	v := amendFixture(t)
	body := "Prose.\n\n## " + ConventionalFirstHeading + "\n\nmore\n"
	_, err := v.AmendTask("proj", "t", "Decision", body)
	if err == nil {
		t.Fatal("an amend body carrying an H2 must be refused")
	}
	if !strings.Contains(err.Error(), "###") {
		t.Errorf("the refusal must point an amend caller at the sub-heading fix, got: %v", err)
	}
}

// TestOverwriteTaskFileRefusesAHeaderChange is the A7 guard: the header-change
// refusal lives on the WRITER, so every caller inherits it.
//
// It used to live in the MCP handler alone, and that was the gap — `vp tasks
// edit` reached this same writer with no header diff, so a hand-edited Status
// line saved cleanly through the CLI while MCP refused it. Two surfaces, one
// rule, and only one of them enforcing it. Testing it HERE, at the writer, is
// the point: a test against either surface would pass again if the rule were
// re-copied into that surface only.
func TestOverwriteTaskFileRefusesAHeaderChange(t *testing.T) {
	for _, tc := range []struct {
		name       string
		old, new   string
		wantField  string
		wantAction string
	}{
		{"status", "**Status:** pending", "**Status:** in_progress", "**Status:**", "update_status"},
		{"priority", "**Priority:** high", "**Priority:** low", "**Priority:**", "set_meta"},
		{"title", "# Original", "# Smuggled", "title", "set_meta"},
		{"parent", "**Parent:** epic-a", "**Parent:** epic-b", "**Parent:**", "set_relations"},
		{"depends", "**Depends:** dep-one, dep-two", "**Depends:** dep-one", "**Depends:**", "set_relations"},
		// A reorder is a change to the field like any other: Depends is written
		// and read back as an ordered list, so set_relations owns a reshuffle.
		{"depends reorder", "**Depends:** dep-one, dep-two", "**Depends:** dep-two, dep-one", "**Depends:**", "set_relations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := testVault(t)
			parent := "epic-a"
			if err := v.CreateTask("proj", TaskSpec{
				Slug: "task", Title: "Original", Priority: "high",
				Parent: parent, Depends: []string{"dep-one", "dep-two"},
				Content: "Original body.\n",
			}); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
			_, before, err := v.GetTask("proj", "task")
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}

			smuggled := strings.Replace(before, tc.old, tc.new, 1)
			if smuggled == before {
				t.Fatalf("test bug: %q not present in the fixture:\n%s", tc.old, before)
			}

			err = v.OverwriteTaskFile("proj", "task", smuggled)
			if err == nil {
				t.Fatalf("expected a refusal for a body that changes %s", tc.wantField)
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Errorf("refusal must name the field %q, got: %v", tc.wantField, err)
			}
			// A refusal that only says no gets worked around. It has to point at
			// the action that DOES own the field.
			if !strings.Contains(err.Error(), tc.wantAction) {
				t.Errorf("refusal must name the owning action %q, got: %v", tc.wantAction, err)
			}

			// Typed, so a surface can classify it without parsing prose.
			var hce *HeaderChangeError
			if !errors.As(err, &hce) {
				t.Errorf("refusal must be a *HeaderChangeError, got %T", err)
			}
			// Marked at its source: a guard rejecting bad input is the caller's
			// fault, and must not be counted against system health.
			if !apperr.IsCaller(err) {
				t.Errorf("refusal must be classified apperr.Caller, got %v", err)
			}

			// Refused WITHOUT touching the file.
			_, after, err2 := v.GetTask("proj", "task")
			if err2 != nil {
				t.Fatalf("GetTask after refusal: %v", err2)
			}
			if after != before {
				t.Errorf("a refused overwrite modified the file on disk:\n--- before ---\n%s\n--- after ---\n%s", before, after)
			}
		})
	}
}

// TestOverwriteTaskFileAcceptsABodyOnlyChange is the other half: the refusal must
// not have made the writer useless. Rewriting prose beneath an unchanged header
// is exactly what overwrite is FOR — the preamble and an H2's own wording are
// what amend structurally cannot reach.
func TestOverwriteTaskFileAcceptsABodyOnlyChange(t *testing.T) {
	v := testVault(t)
	parent := "epic-a"
	if err := v.CreateTask("proj", TaskSpec{
		Slug: "task", Title: "Original", Priority: "high",
		Parent: parent, Depends: []string{"dep-one", "dep-two"},
		Content: "Original body.\n",
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	_, before, err := v.GetTask("proj", "task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	rewritten := strings.Replace(before, "Original body.", "Rewritten body, same header.", 1)
	if rewritten == before {
		t.Fatal("test bug: body marker not found in the fixture")
	}
	if err := v.OverwriteTaskFile("proj", "task", rewritten); err != nil {
		t.Fatalf("body-only overwrite refused: %v", err)
	}

	_, after, err := v.GetTask("proj", "task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if after != rewritten {
		t.Errorf("body-only overwrite did not persist verbatim:\n%s", after)
	}
}

// TestOverwriteTaskFileRewritingHeaderIsTheMigrationOptOut pins the escape hatch
// the `vp migrate task-*` commands need, and pins that it is an OPT-IN.
//
// Three migrations exist to repair a malformed header block, so "the header must
// not move" is the wrong rule for exactly those callers. The opt-out is a
// separate, awkwardly-named method rather than a boolean argument so that adding
// a fourth is a deliberate act a reviewer can find with one grep.
func TestOverwriteTaskFileRewritingHeaderIsTheMigrationOptOut(t *testing.T) {
	v := testVault(t)
	if err := v.CreateTask("proj", TaskSpec{
		Slug: "task", Title: "Original", Priority: "high",
		Content: "Body.\n",
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	_, before, err := v.GetTask("proj", "task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	repaired := strings.Replace(before, "**Status:** pending", "**Status:** in_progress", 1)
	if repaired == before {
		t.Fatal("test bug: status line not found in the fixture")
	}

	// The strict writer refuses it...
	if err := v.OverwriteTaskFile("proj", "task", repaired); err == nil {
		t.Fatal("the strict writer accepted a header change")
	}
	// ...and the migration writer is the one door that takes it.
	if err := v.OverwriteTaskFileRewritingHeader("proj", "task", repaired); err != nil {
		t.Fatalf("migration writer refused a header repair: %v", err)
	}

	_, after, err := v.GetTask("proj", "task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !strings.Contains(after, "**Status:** in_progress") {
		t.Errorf("header repair did not persist:\n%s", after)
	}
}

// TestLegacyHeaderRepairsMoveOnlyTheFieldsTheyClaim is the ratchet under
// OverwriteTaskFileRewritingHeader's opt-out.
//
// That opt-out is FILE-WIDE: it lifts the header-change refusal for every field,
// not just the one the calling migration repairs. That is safe today only
// because of what these repair functions actually construct — a review found
// that a future edit which also normalized, say, Title would sail through with
// nothing failing. This is that nothing.
//
// It does NOT widen the writer's design; it pins the assumption the widened
// permission rests on. If a repair starts moving a field it never claimed, this
// reddens and the opt-out gets revisited rather than silently covering it.
func TestLegacyHeaderRepairsMoveOnlyTheFieldsTheyClaim(t *testing.T) {
	// A legacy-Both file that also carries Parent and Depends, so a repair that
	// disturbed either would be visible. The bare "Status: Done" line above the
	// modern block is the legacy defect being repaired.
	const withEdges = "# Task 3.5: Portable Command Execution\n" +
		"Status: Done\n" +
		"\n" +
		"**Status:** pending\n" +
		"**Priority:** high\n" +
		"**Parent:** epic-a\n" +
		"**Depends:** dep-one, dep-two\n\n" +
		"## Summary\n\nBody.\n"

	// NB: on a legacy-Both file the bare "Status: Done" line terminates the
	// header block, so Parent/Depends do not PARSE before the repair — which is
	// exactly why this asserts on the TEXT. The claim under test is that the
	// repair does not disturb lines it never claimed, and the text is where that
	// is visible; a parse-vs-parse comparison would read empty-to-empty and pass
	// while the lines were being dropped.
	for _, line := range []string{
		"# Task 3.5: Portable Command Execution",
		"**Priority:** high",
		"**Parent:** epic-a",
		"**Depends:** dep-one, dep-two",
	} {
		if !strings.Contains(withEdges, line) {
			t.Fatalf("test bug: fixture lacks %q", line)
		}
	}

	repaired, err := RepairLegacyBothHeader(withEdges)
	if err != nil {
		t.Fatalf("RepairLegacyBothHeader: %v", err)
	}

	// The field it EXISTS to move: the true value was the bare line's.
	if !strings.Contains(repaired, "**Status:** Done") {
		t.Errorf("repair did not carry the true status into the modern field:\n%s", repaired)
	}
	// Every line it never claimed must survive byte-for-byte.
	for _, line := range []string{
		"# Task 3.5: Portable Command Execution",
		"**Priority:** high",
		"**Parent:** epic-a",
		"**Depends:** dep-one, dep-two",
	} {
		if !strings.Contains(repaired, line) {
			t.Errorf("repair moved or dropped a line it does not own: %q\n%s", line, repaired)
		}
	}
}
