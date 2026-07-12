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

// TestCreateTaskRejectsStatusLineInContent pins the door CreateTask now closes.
// Agent-written plan bodies idiomatically open with their own metadata block;
// CreateTask used to staple that under its OWN header verbatim, producing a file
// with two **Status:** lines. The writer then rewrote the first and the reader
// reported the last, so a task could read back "pending" forever.
func TestCreateTaskRejectsStatusLineInContent(t *testing.T) {
	v := testVault(t)
	content := "**Status:** in_progress\n**Priority:** high\n\n## Plan\n\nDo the thing."

	err := v.CreateTask("proj", "my-task", "My Task", content, "high")
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

	err := v.CreateTask("proj", "my-task", "My Task", content, "high")
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

	if err := v.CreateTask("proj", "my-task", "My Task", content, "high"); err != nil {
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
			if err := v.CreateTask("proj", "my-task", "My Task", tc.content, "high"); err != nil {
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

	if err := v.CreateTask("proj", "rig", "E2E Walkthrough Rig", content, "high"); err != nil {
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
	if err := v.CreateTask("proj", "my-task", "My Task", "## Plan\n\nDo it.", "high"); err != nil {
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
		if err := v.CreateTask("proj", "t", "T", "body", "high"); err != nil {
			t.Fatal(err)
		}
		if err := v.UpdateTaskStatus("proj", "t", terminal); err == nil {
			t.Errorf("UpdateTaskStatus(%q) should be refused: it is not in the write set", terminal)
		}
	}

	// retire → done/, status "retired".
	v := testVault(t)
	if err := v.CreateTask("proj", "r", "R", "body", "high"); err != nil {
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
	if err := v.CreateTask("proj", "c", "C", "body", "high"); err != nil {
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
