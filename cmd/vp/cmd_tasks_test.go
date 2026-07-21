// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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

// mkTask creates a task and, when parent != "", points it at that parent.
func mkTask(t *testing.T, v *storage.Vault, proj, slug, parent string) {
	t.Helper()
	if err := v.CreateTask(proj, storage.TaskSpec{Slug: slug, Title: slug, Content: "body", Priority: "high"}); err != nil {
		t.Fatalf("create %s: %v", slug, err)
	}
	if parent != "" {
		if err := v.SetTaskRelations(proj, slug, storage.TaskRelations{Parent: &parent}); err != nil {
			t.Fatalf("relate %s->%s: %v", slug, parent, err)
		}
	}
}

func TestTasksFlagValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"epic+standalone", []string{"--epic", "x", "--standalone"}, "mutually exclusive"},
		{"epic+flat", []string{"--epic", "x", "--flat"}, "cannot be combined with --flat"},
		{"standalone+flat", []string{"--standalone", "--flat"}, "cannot be combined with --flat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			errs := captureStderr(t, func() { code = cmdTasks().Run(tc.args) })
			if code != cli.ExitUser {
				t.Errorf("exit = %d, want ExitUser", code)
			}
			if !strings.Contains(errs, tc.want) {
				t.Errorf("stderr = %q, want it to contain %q", errs, tc.want)
			}
		})
	}
}

func TestRunTasksStandalone(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "big-epic", "")
	mkTask(t, v, "test-proj", "child", "big-epic")
	mkTask(t, v, "test-proj", "lonely", "")

	var buf bytes.Buffer
	if code := runTasks(v, "test-proj", taskListOpts{standalone: true}, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "STANDALONE") || !strings.Contains(out, "lonely") {
		t.Errorf("standalone view missing its bucket:\n%s", out)
	}
	if strings.Contains(out, "big-epic") || strings.Contains(out, "child") {
		t.Errorf("standalone view leaked epic work:\n%s", out)
	}
}

func TestRunTasksEpicUnknownAndLeaf(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "big-epic", "")
	mkTask(t, v, "test-proj", "child", "big-epic")

	// Unknown slug.
	var buf bytes.Buffer
	errs := captureStderr(t, func() {
		if code := runTasks(v, "test-proj", taskListOpts{epic: "ghost"}, &buf); code != cli.ExitUser {
			t.Errorf("unknown epic exit = %d, want ExitUser", code)
		}
	})
	if !strings.Contains(errs, "no such task: ghost") {
		t.Errorf("unknown-epic error = %q", errs)
	}

	// A leaf task is not an epic/story.
	buf.Reset()
	errs = captureStderr(t, func() {
		if code := runTasks(v, "test-proj", taskListOpts{epic: "child"}, &buf); code != cli.ExitUser {
			t.Errorf("leaf epic exit = %d, want ExitUser", code)
		}
	})
	if !strings.Contains(errs, "leaf task") {
		t.Errorf("leaf-epic error = %q", errs)
	}
	// The hint must point ONLY at things that exist — never at a `vp tasks read`
	// command, which is deliberately not implemented.
	if strings.Contains(errs, "tasks read") {
		t.Errorf("hint references a nonexistent `vp tasks read` command: %q", errs)
	}
}

func TestRunTasksEpicSubtreeReRoots(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "top-epic", "")
	mkTask(t, v, "test-proj", "sub-story", "top-epic")
	mkTask(t, v, "test-proj", "deep-leaf", "sub-story")

	// --epic on the STORY: the subtree is re-rooted at sub-story, so deep-leaf —
	// a grandchild of the true root — indents relative to sub-story (indent 0),
	// not the over-indented depth-relative-to-true-root the review flagged.
	var buf bytes.Buffer
	if code := runTasks(v, "test-proj", taskListOpts{epic: "sub-story"}, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "STORY  sub-story") {
		t.Errorf("story tier label missing:\n%s", out)
	}
	// Re-rooted: deep-leaf sits flush under its subtree root (one space after the
	// branch glyph). The over-indented form would be "└─   deep-leaf".
	if !strings.Contains(out, "└─ deep-leaf") {
		t.Errorf("deep-leaf not re-rooted to indent 0:\n%s", out)
	}

	// --epic on the ROOT epic: tier is EPIC, and now deep-leaf IS a grandchild of
	// the group root, so it steps in one level (indent 1).
	buf.Reset()
	if code := runTasks(v, "test-proj", taskListOpts{epic: "top-epic"}, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out = buf.String()
	if !strings.Contains(out, "EPIC  top-epic") {
		t.Errorf("epic tier label missing:\n%s", out)
	}
	if !strings.Contains(out, "└─   deep-leaf") {
		t.Errorf("deep-leaf should step in one level under the root epic:\n%s", out)
	}
}

func TestRunTasksEpicsTransitiveCounts(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "root-epic", "")
	mkTask(t, v, "test-proj", "mid-story", "root-epic")
	mkTask(t, v, "test-proj", "leaf-a", "mid-story")
	mkTask(t, v, "test-proj", "leaf-b", "mid-story")
	if err := v.RetireTask("test-proj", "leaf-b"); err != nil {
		t.Fatalf("retire: %v", err)
	}

	var buf bytes.Buffer
	if code := runTasksEpics(v, "test-proj", false, false, true, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	var rows []epicSummary
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	// Only the ROOT epic is listed — mid-story is a nested story.
	if len(rows) != 1 || rows[0].Slug != "root-epic" {
		t.Fatalf("want one row for root-epic, got %+v", rows)
	}
	// Direct children of root-epic = 1 (mid-story). The counts must be TRANSITIVE:
	// Total = mid-story + leaf-a + leaf-b (archived) = 3; Open drops leaf-b = 2.
	if rows[0].Total != 3 {
		t.Errorf("Total = %d, want 3 (transitive, incl. archive)", rows[0].Total)
	}
	if rows[0].Open != 2 {
		t.Errorf("Open = %d, want 2 (transitive, excl. archive)", rows[0].Open)
	}
}

func TestRunTasksEpicsText(t *testing.T) {
	v := testVault(t)
	// No epics yet.
	var buf bytes.Buffer
	if code := runTasksEpics(v, "test-proj", false, false, false, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(buf.String(), "No epics") {
		t.Errorf("empty roll-up = %q", buf.String())
	}

	mkTask(t, v, "test-proj", "root-epic", "")
	mkTask(t, v, "test-proj", "child", "root-epic")

	buf.Reset()
	if code := runTasksEpics(v, "test-proj", false, false, false, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "OPEN/TOTAL") {
		t.Errorf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "root-epic") || !strings.Contains(out, "1/1") {
		t.Errorf("expected root-epic with 1/1 counts:\n%s", out)
	}
}

// writeStubEditor writes an executable shell script to dir and returns its path.
func writeStubEditor(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "stub-editor.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write stub editor: %v", err)
	}
	return p
}

func TestResolveEditor(t *testing.T) {
	t.Run("VISUAL wins over EDITOR", func(t *testing.T) {
		t.Setenv("VISUAL", "vis")
		t.Setenv("EDITOR", "ed")
		bin, args, err := resolveEditor()
		if err != nil {
			t.Fatal(err)
		}
		if bin != "vis" || len(args) != 0 {
			t.Errorf("bin=%q args=%v, want vis with no args", bin, args)
		}
	})
	t.Run("multi-word EDITOR splits", func(t *testing.T) {
		t.Setenv("VISUAL", "")
		t.Setenv("EDITOR", "foo -a -b")
		bin, args, err := resolveEditor()
		if err != nil {
			t.Fatal(err)
		}
		if bin != "foo" || strings.Join(args, ",") != "-a,-b" {
			t.Errorf("bin=%q args=%v, want foo [-a -b]", bin, args)
		}
	})
	t.Run("neither set errors", func(t *testing.T) {
		t.Setenv("VISUAL", "")
		t.Setenv("EDITOR", "")
		if _, _, err := resolveEditor(); err == nil {
			t.Error("expected an error when neither $VISUAL nor $EDITOR is set")
		}
	})
}

func TestRunTasksEditWritesBack(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "editme", "")

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	// Record the path it was handed, then append a valid body line to it.
	stub := writeStubEditor(t, dir, "printf '%s' \"$1\" > \""+argvFile+"\"\nprintf '\\nEdited by stub.\\n' >> \"$1\"\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)

	var out, errOut bytes.Buffer
	if code := runTasksEdit(v, "test-proj", "editme", &out, &errOut); code != cli.ExitOK {
		t.Fatalf("exit = %d, stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "updated editme") {
		t.Errorf("expected confirmation, got %q", out.String())
	}
	// The editor was handed a real .md temp path.
	handed, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("stub did not record argv: %v", err)
	}
	if !strings.HasSuffix(string(handed), ".md") {
		t.Errorf("editor was handed %q, want a .md temp path", string(handed))
	}
	// The changed body was written back through the validating overwrite.
	_, content, err := v.GetTask("test-proj", "editme")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Edited by stub.") {
		t.Errorf("edit not persisted:\n%s", content)
	}
}

func TestRunTasksEditNoChanges(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "untouched", "")

	dir := t.TempDir()
	stub := writeStubEditor(t, dir, "exit 0\n") // touches nothing
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)

	var out, errOut bytes.Buffer
	if code := runTasksEdit(v, "test-proj", "untouched", &out, &errOut); code != cli.ExitOK {
		t.Fatalf("exit = %d, stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "no changes") {
		t.Errorf("expected 'no changes', got %q", out.String())
	}
}

func TestRunTasksEditInvalidPreservesTemp(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "breakme", "")
	_, orig, _ := v.GetTask("test-proj", "breakme")

	dir := t.TempDir()
	// Clobber the file with content that fails whole-file validation (no header).
	stub := writeStubEditor(t, dir, "printf 'just prose, no header at all\\n' > \"$1\"\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)

	var out, errOut bytes.Buffer
	if code := runTasksEdit(v, "test-proj", "breakme", &out, &errOut); code != cli.ExitUser {
		t.Fatalf("exit = %d, want ExitUser", code)
	}
	es := errOut.String()
	if !strings.Contains(es, "preserved at:") {
		t.Errorf("expected the temp path to be preserved and printed, got %q", es)
	}
	// The live file must be untouched by a rejected edit.
	_, after, _ := v.GetTask("test-proj", "breakme")
	if after != orig {
		t.Errorf("live file changed despite validation failure")
	}
	// Clean up the preserved temp file.
	for line := range strings.SplitSeq(es, "\n") {
		if _, path, found := strings.Cut(line, "preserved at: "); found {
			_ = os.Remove(strings.TrimSpace(path))
		}
	}
}

func TestRunTasksEditEditorAbort(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "keepme", "")
	_, orig, _ := v.GetTask("test-proj", "keepme")

	dir := t.TempDir()
	stub := writeStubEditor(t, dir, "exit 1\n") // editor aborts
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)

	var out, errOut bytes.Buffer
	if code := runTasksEdit(v, "test-proj", "keepme", &out, &errOut); code != cli.ExitUser {
		t.Fatalf("exit = %d, want ExitUser", code)
	}
	if !strings.Contains(errOut.String(), "editor exited abnormally") {
		t.Errorf("expected editor-abort message, got %q", errOut.String())
	}
	_, after, _ := v.GetTask("test-proj", "keepme")
	if after != orig {
		t.Errorf("live file changed after an aborted editor")
	}
}

func TestRunTasksEditArchivedGuard(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "gone", "")
	if err := v.RetireTask("test-proj", "gone"); err != nil {
		t.Fatalf("retire: %v", err)
	}

	var out, errOut bytes.Buffer
	// No editor is set: the guard must fire BEFORE resolveEditor is ever reached.
	code := runTasksEdit(v, "test-proj", "gone", &out, &errOut)
	if code != cli.ExitUser {
		t.Fatalf("exit = %d, want ExitUser", code)
	}
	if !strings.Contains(errOut.String(), "archived") {
		t.Errorf("expected archived-guard refusal, got %q", errOut.String())
	}
}

func TestRunTasksEditNoSuchTask(t *testing.T) {
	v := testVault(t)
	var out, errOut bytes.Buffer
	code := runTasksEdit(v, "test-proj", "nope", &out, &errOut)
	if code != cli.ExitUser {
		t.Fatalf("exit = %d, want ExitUser", code)
	}
	if !strings.Contains(errOut.String(), "no such task: nope") {
		t.Errorf("expected no-such-task error, got %q", errOut.String())
	}
}
