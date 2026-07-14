// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
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
	want := fmt.Sprintf("# %s\n\n**Status:** pending\n**Priority:** medium\n\n%s\n", title(w), body(w))
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
	if err := v.AmendTask("proj", "t", "Decision (205)", "Option B. The re-key is unjustified."); err != nil {
		t.Fatalf("AmendTask: %v", err)
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
	for range 3 {
		if err := v.AmendTask("proj", "t", "Decision", "Ship it."); err != nil {
			t.Fatalf("AmendTask: %v", err)
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
	if err := v.AmendTask("proj", "t", "Diagnosis", "REVERSED: the premise was false."); err != nil {
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
	if err := v.AmendTask("proj", "t", "Decision", "Recorded."); err != nil {
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

	if err := v.AmendTask("proj", "t", "Decision", "The real decision."); err != nil {
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
	if err := v.AmendTask("proj", "t", "Plan", "### Phase 1\n\nnew one\n\n### Phase 2\n\nnew two"); err != nil {
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
			err := v.AmendTask("proj", "t", "Decision", tc.body)
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
	if err := v.AmendTask("proj", "t", "Decision", body); err != nil {
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
			err := v.AmendTask("proj", "t", tc.section, tc.body)
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
	err := v.AmendTask("proj", "nope", "Decision", "x")
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
			errs[i] = v.AmendTask("proj", "t", s, fmt.Sprintf("body of %s", s))
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
	if err := v.AmendTask("proj", "t", "Decision", "Recorded."); err != nil {
		t.Fatalf("AmendTask: %v", err)
	}
	if err := v.SetTaskRelations("proj", "t", TaskRelations{Depends: &[]string{"a", "b"}}); err != nil {
		t.Fatalf("SetTaskRelations after amend: %v", err)
	}
	if err := v.AmendTask("proj", "t", "Decision", "Re-recorded."); err != nil {
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
