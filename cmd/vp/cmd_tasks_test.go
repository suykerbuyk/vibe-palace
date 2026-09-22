// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// assumeTerminal makes cli.IsTerminal report true for the rest of the test.
// `vp tasks edit` and `vp tasks read` only start an editor when stdin and
// stdout are both terminals, and under `go test` they are not, so every test
// that drives the editor path has to say so. The no-terminal paths have their
// own tests, which pin VP_ASSUME_TTY off instead.
func assumeTerminal(t *testing.T) {
	t.Helper()
	t.Setenv("VP_ASSUME_TTY", "1")
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
	assumeTerminal(t)

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
	assumeTerminal(t)

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
	assumeTerminal(t)

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
	assumeTerminal(t)

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

// TestRunTasksEditRefusesAHeaderChange is the A7 regression test at the surface
// that had the gap.
//
// `vp tasks edit` hands the whole file to $EDITOR and writes back whatever comes
// out. Until the refusal moved onto storage.OverwriteTaskFile, nothing here
// compared the returned header against disk, so a hand-edited Status/Parent/
// Depends line saved cleanly through the CLI while the identical body was
// refused through MCP. This test edits header lines the way a human with an
// editor actually would — with sed, on the real file the command hands over.
//
// It deliberately asserts NO new code in cmd_tasks.go: the refusal arrives
// because runTasksEdit calls the common writer. If someone "fixes" a future gap
// by adding a second header diff here, this test still passes and the duplicate
// goes unnoticed — which is why the writer-level test is the primary one and
// this is the surface proof.
func TestRunTasksEditRefusesAHeaderChange(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sed        string
		wantField  string
		wantAction string
	}{
		{"status", `s/^\*\*Status:\*\* .*/**Status:** in_progress/`, "**Status:**", "update_status"},
		{"parent", `s/^\*\*Parent:\*\* .*/**Parent:** other-epic/`, "**Parent:**", "set_relations"},
		{"title", `s/^# .*/# Smuggled Title/`, "title", "set_meta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := testVault(t)
			mkTask(t, v, "test-proj", "hdr", "epic-a")
			_, before, err := v.GetTask("test-proj", "hdr")
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}

			dir := t.TempDir()
			stub := writeStubEditor(t, dir, "sed -i '"+tc.sed+"' \"$1\"\n")
			t.Setenv("VISUAL", "")
			t.Setenv("EDITOR", stub)
			assumeTerminal(t)

			var out, errOut bytes.Buffer
			if code := runTasksEdit(v, "test-proj", "hdr", &out, &errOut); code != cli.ExitUser {
				t.Fatalf("exit = %d, want ExitUser; stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			es := errOut.String()
			if !strings.Contains(es, tc.wantField) {
				t.Errorf("CLI refusal must name the field %q, got %q", tc.wantField, es)
			}
			if !strings.Contains(es, tc.wantAction) {
				t.Errorf("CLI refusal must name the owning action %q, got %q", tc.wantAction, es)
			}

			// The live file must be untouched by a rejected edit.
			_, after, err := v.GetTask("test-proj", "hdr")
			if err != nil {
				t.Fatalf("GetTask after refusal: %v", err)
			}
			if after != before {
				t.Errorf("a refused CLI edit modified the file:\n--- before ---\n%s\n--- after ---\n%s", before, after)
			}
		})
	}
}

// TestRunTasksEditStillWritesABodyOnlyChange proves the CLI did not become
// read-only: an edit that leaves the header alone still saves.
func TestRunTasksEditStillWritesABodyOnlyChange(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "bodyonly", "epic-a")

	dir := t.TempDir()
	stub := writeStubEditor(t, dir, "printf '\\nAppended prose.\\n' >> \"$1\"\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)
	assumeTerminal(t)

	var out, errOut bytes.Buffer
	if code := runTasksEdit(v, "test-proj", "bodyonly", &out, &errOut); code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", code, errOut.String())
	}
	_, content, err := v.GetTask("test-proj", "bodyonly")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Appended prose.") {
		t.Errorf("body-only CLI edit did not persist:\n%s", content)
	}
}

// TestRunTasksFlatOutlierPriorityDoesNotWidenEveryRow is the second surface the
// same defect reaches, and the reason the cap lives in a helper both renderers
// call rather than in cmd_board.go. Measured on the live vault before the fix:
//
//	vp tasks --project vibe-palace --flat --done | head -1 | awk '{print index($0,"SLUG")-1}'   # 172
//	vp board --project vibe-palace | sed -n '/^HISTORY/,$p' | grep -m1 'retired  medium' \
//	  | awk '{i=index($0,"medium"); r=substr($0,i); print index(r,"completed")}'                # 173
//
// One task's free-text **Priority:** disfigured two commands. A fix only vp
// board honored would have left this one standing.
func TestRunTasksFlatOutlierPriorityDoesNotWidenEveryRow(t *testing.T) {
	v := testVault(t)
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "flat-short", Title: "Short", Content: "body", Priority: "high"})
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "flat-long", Title: "Long", Content: "body", Priority: livePriority})

	var buf bytes.Buffer
	if code := runTasks(v, "test-proj", taskListOpts{flat: true}, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	out := buf.String()
	header, _, _ := strings.Cut(out, "\n")

	// The PRIORITY column's width is the offset of the next header label.
	got := strings.Index(header, "SLUG")
	if got < 0 {
		t.Fatalf("no SLUG column in header: %q", header)
	}
	// len("PRIORITY") seeds the column at 8; the cap is maxMeasuredColWidth,
	// plus the two-space separator. Uncapped this was 172.
	if want := maxMeasuredColWidth + 2; got > want {
		t.Errorf("one free-text priority widened the PRIORITY column: SLUG@%d, want <= %d\nheader: %q", got, want, header)
	}
	// Capping a width never truncates a value.
	if !strings.Contains(out, livePriority) {
		t.Errorf("flat table must still print the long priority in full:\n%s", out)
	}
}

// TestRenderGroupsPriorityWidthOnlyWidens pins the required one-directional
// property of the %-8s -> %-*s conversion in renderGroups: with only ordinary
// priorities present the column must stay exactly as wide as the literal it
// replaced, never narrower.
func TestRenderGroupsPriorityWidthOnlyWidens(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "grp-epic", "")
	// "low" is 3 bytes — well under the old literal 8. If the conversion could
	// narrow, this is the fixture that would show it.
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "grp-kid", Title: "K", Content: "body", Priority: "low"})
	if err := v.SetTaskRelations("test-proj", "grp-kid", storage.TaskRelations{Parent: ptr("grp-epic")}); err != nil {
		t.Fatalf("relate: %v", err)
	}
	deps := []string{"grp-epic"}
	if err := v.SetTaskRelations("test-proj", "grp-kid", storage.TaskRelations{Depends: &deps}); err != nil {
		t.Fatalf("depends: %v", err)
	}

	var buf bytes.Buffer
	if code := runTasks(v, "test-proj", taskListOpts{}, &buf); code != cli.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	var row string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "grp-kid") && strings.Contains(line, "blocked by") {
			row = line
		}
	}
	if row == "" {
		t.Skipf("no blocked row rendered; padding is trimmed on unblocked rows:\n%s", buf.String())
	}
	// The blockers field is the only thing right of the priority column, so its
	// offset is the column's width. Seeded at groupsPriorityWidth, so "low"
	// must still occupy the full 8.
	i := strings.Index(row, "low")
	j := strings.Index(row, "[blocked by:")
	if got, want := j-i-2, groupsPriorityWidth; got != want {
		t.Errorf("renderGroups priority column = %d, want %d (the literal it replaced): %q", got, want, row)
	}
}

// ---------------------------------------------------------------------------
// tasks read
// ---------------------------------------------------------------------------

// readStub writes a stub editor that records the path it was handed into
// argvFile and then runs body (raw sh, with "$1" as the handed path).
func readStub(t *testing.T, dir, argvFile, body string) string {
	t.Helper()
	return writeStubEditor(t, dir, "printf '%s' \"$1\" > \""+argvFile+"\"\n"+body)
}

// handedPath returns the temp path the stub editor recorded, failing the test
// if the stub never ran.
func handedPath(t *testing.T, argvFile string) string {
	t.Helper()
	b, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("stub editor did not record argv (did it run?): %v", err)
	}
	return string(b)
}

// assertTempCopyClaimIsHonest pins the wording of the message that tells the
// reader where their typing went.
//
// 🔴 vp COMMITS TO NOTHING ABOUT THIS FILE'S LIFETIME, IN EITHER DIRECTION.
// The copy is a temp file under $TMPDIR. The first wording said "preserved",
// which promised durability a tmpfs never agreed to; the replacement said "may
// not survive a reboot", which still discussed a lifetime vp has no standing to
// discuss. The bar is now: state that the vault was not changed, state where
// the file is, and stop. Every word below has appeared in a real draft of this
// message, which is why the list is a list and not a principle.
func assertTempCopyClaimIsHonest(t *testing.T, stderr string) {
	t.Helper()
	for _, banned := range []string{
		"preserved", "preserve",
		"kept", "keep",
		"saved", "safe",
		"survive", "durable",
		"recover", "backup",
	} {
		if strings.Contains(strings.ToLower(stderr), banned) {
			t.Errorf("message says %q — vp makes no claim about a temp file's lifetime, in either direction:\n%s", banned, stderr)
		}
	}
	if !strings.Contains(stderr, "neither manages nor tracks") {
		t.Errorf("the message must disclaim the file rather than characterise its lifetime, got %q", stderr)
	}
}

func TestShortSlugForTempName(t *testing.T) {
	long := strings.Repeat("a", readTempSlugBudget+20)
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"short is unchanged", "fix-login-bug", "fix-login-bug"},
		{"exactly the budget is unchanged", strings.Repeat("b", readTempSlugBudget), strings.Repeat("b", readTempSlugBudget)},
		{"over the budget keeps the leading bytes", long, strings.Repeat("a", readTempSlugBudget)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shortSlugForTempName(tc.in); got != tc.want {
				t.Errorf("shortSlugForTempName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// assertReadOpens is the shared body of the state-coverage tests: whatever
// directory the task resolved from, the editor is handed a READ-ONLY-named .md
// temp file holding the task's exact bytes.
func assertReadOpens(t *testing.T, v *storage.Vault, proj, slug string) {
	t.Helper()
	_, want, err := v.GetTask(proj, slug)
	if err != nil {
		t.Fatalf("GetTask %s: %v", slug, err)
	}

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	// Copy the handed file aside so its contents survive the command's cleanup.
	copyFile := filepath.Join(dir, "handed-copy.md")
	stub := readStub(t, dir, argvFile, "cat \"$1\" > \""+copyFile+"\"\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)
	assumeTerminal(t)

	var errOut bytes.Buffer
	if code := runTasksRead(v, proj, slug, io.Discard, &errOut); code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", code, errOut.String())
	}

	handed := handedPath(t, argvFile)
	if !strings.HasSuffix(handed, ".md") {
		t.Errorf("editor was handed %q, want a .md temp path", handed)
	}
	if !strings.Contains(filepath.Base(handed), "READONLY") {
		t.Errorf("temp basename %q must carry READONLY — it is the half of the warning that survives an alternate-screen editor", filepath.Base(handed))
	}
	got, err := os.ReadFile(copyFile)
	if err != nil {
		t.Fatalf("stub did not copy the handed file: %v", err)
	}
	if string(got) != want {
		t.Errorf("editor was handed different bytes than the vault holds:\n--- handed ---\n%s\n--- vault ---\n%s", got, want)
	}
}

func TestRunTasksReadOpensAnActiveTask(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "still-open", "")
	assertReadOpens(t, v, "test-proj", "still-open")
}

func TestRunTasksReadOpensADoneTask(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "finished", "")
	if err := v.RetireTask("test-proj", "finished"); err != nil {
		t.Fatalf("retire: %v", err)
	}
	assertReadOpens(t, v, "test-proj", "finished")
}

func TestRunTasksReadOpensACancelledTask(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "abandoned", "")
	if err := v.CancelTask("test-proj", "abandoned", ""); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	assertReadOpens(t, v, "test-proj", "abandoned")
}

func TestRunTasksReadOpensAnEpic(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "epic-a", "")
	// An epic is a task something names as its parent, so the child must exist
	// for epic-a to BE one.
	mkTask(t, v, "test-proj", "child-of-epic", "epic-a")
	assertReadOpens(t, v, "test-proj", "epic-a")
}

func TestRunTasksReadLeavesAModifiedTempInPlace(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "scribbled", "")

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	// The `:w!` route: a forcing editor chmods the file and writes it.
	stub := readStub(t, dir, argvFile, "chmod u+w \"$1\"\nprintf '\\nReader notes.\\n' >> \"$1\"\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)
	assumeTerminal(t)

	var errOut bytes.Buffer
	if code := runTasksRead(v, "test-proj", "scribbled", io.Discard, &errOut); code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", code, errOut.String())
	}
	es := errOut.String()
	if !strings.Contains(es, "NOT written") {
		t.Errorf("stderr must say plainly that the vault was not changed, got %q", es)
	}
	assertTempCopyClaimIsHonest(t, es)

	kept := handedPath(t, argvFile)
	body, err := os.ReadFile(kept)
	if err != nil {
		t.Fatalf("modified copy was deleted: %v", err)
	}
	if !strings.Contains(string(body), "Reader notes.") {
		t.Errorf("the kept copy lost the reader's edit:\n%s", body)
	}
	if !strings.Contains(es, kept) {
		t.Errorf("stderr must name the path that was kept (%q), got %q", kept, es)
	}
	_ = os.Remove(kept)
}

func TestRunTasksReadRemovesAnUnmodifiedTemp(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "untouched-read", "")

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	stub := readStub(t, dir, argvFile, "exit 0\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)
	assumeTerminal(t)

	var errOut bytes.Buffer
	if code := runTasksRead(v, "test-proj", "untouched-read", io.Discard, &errOut); code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", code, errOut.String())
	}
	gone := handedPath(t, argvFile)
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		_ = os.Remove(gone)
		t.Errorf("an untouched copy must be removed silently; os.Stat(%q) err = %v", gone, err)
	}
	if strings.Contains(errOut.String(), gone) {
		t.Errorf("nothing was kept, so no temp path should be printed, got %q", errOut.String())
	}
}

// TestRunTasksReadAbortLeavesAModifiedTempInPlace pins the deliberate divergence
// from `vp tasks edit`, whose abort path deletes the temp unconditionally.
//
// Do not "restore symmetry" by making this path delete. In `edit` a non-zero
// exit means "cancel my save" and dropping the copy honours it; in `read` there
// is no save to cancel, so a non-zero exit says nothing about the reader's
// typing — and that typing is the only artifact this command can lose.
func TestRunTasksReadAbortLeavesAModifiedTempInPlace(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "aborted-scribble", "")

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	// The write-and-rename route: the editor replaces the PATH with a fresh
	// file at default permissions and never touches the read-only original.
	stub := readStub(t, dir, argvFile, "cat \"$1\" > \"$1.new\"\nprintf '\\nNotes before the crash.\\n' >> \"$1.new\"\nmv \"$1.new\" \"$1\"\nexit 1\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)
	assumeTerminal(t)

	var errOut bytes.Buffer
	if code := runTasksRead(v, "test-proj", "aborted-scribble", io.Discard, &errOut); code != cli.ExitUser {
		t.Fatalf("exit = %d, want ExitUser; stderr=%q", code, errOut.String())
	}
	es := errOut.String()
	if !strings.Contains(es, "editor exited abnormally") {
		t.Errorf("expected the abort to be reported, got %q", es)
	}
	kept := handedPath(t, argvFile)
	if !strings.Contains(es, kept) {
		t.Errorf("an aborted editor must NOT cost the reader their notes; stderr did not name %q: %s", kept, es)
	}
	assertTempCopyClaimIsHonest(t, es)
	body, err := os.ReadFile(kept)
	if err != nil {
		t.Fatalf("modified copy was deleted on the abort path: %v", err)
	}
	if !strings.Contains(string(body), "Notes before the crash.") {
		t.Errorf("the kept copy lost the reader's edit:\n%s", body)
	}
	_ = os.Remove(kept)
}

func TestRunTasksReadAbortRemovesAnUnmodifiedTemp(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "aborted-clean", "")

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	stub := readStub(t, dir, argvFile, "exit 1\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)
	assumeTerminal(t)

	var errOut bytes.Buffer
	if code := runTasksRead(v, "test-proj", "aborted-clean", io.Discard, &errOut); code != cli.ExitUser {
		t.Fatalf("exit = %d, want ExitUser; stderr=%q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "editor exited abnormally") {
		t.Errorf("expected the abort to be reported, got %q", errOut.String())
	}
	gone := handedPath(t, argvFile)
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		_ = os.Remove(gone)
		t.Errorf("an untouched copy must be removed even on the abort path; os.Stat(%q) err = %v", gone, err)
	}
}

// TestRunTasksReadNoEditorSet asserts the precondition at THIS surface, not
// only in TestResolveEditor, because the ordering is the point: `vp tasks edit`
// refuses an archived task BEFORE it resolves an editor, and the read path must
// have no such guard left to fire.
func TestRunTasksReadNoEditorSet(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "no-editor", "")
	if err := v.RetireTask("test-proj", "no-editor"); err != nil {
		t.Fatalf("retire: %v", err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	assumeTerminal(t)

	var errOut bytes.Buffer
	if code := runTasksRead(v, "test-proj", "no-editor", io.Discard, &errOut); code != cli.ExitUser {
		t.Fatalf("exit = %d, want ExitUser", code)
	}
	es := errOut.String()
	if !strings.Contains(es, "VISUAL") || !strings.Contains(es, "EDITOR") {
		t.Errorf("refusal must name $VISUAL and $EDITOR, got %q", es)
	}
	if strings.Contains(es, "refusing to edit") {
		t.Errorf("an archived task must NOT be refused by vp tasks read, got %q", es)
	}
}

// TestRunTasksReadDoesNotSendAnArchivedReaderAtARefusal pins the other half of
// the notice: `vp tasks edit` guards on meta.Done, so naming it to someone
// reading a done or cancelled task hands them a command that will refuse.
func TestRunTasksReadDoesNotSendAnArchivedReaderAtARefusal(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "archived-reader", "")
	if err := v.RetireTask("test-proj", "archived-reader"); err != nil {
		t.Fatalf("retire: %v", err)
	}

	dir := t.TempDir()
	stub := writeStubEditor(t, dir, "exit 0\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)
	assumeTerminal(t)

	var errOut bytes.Buffer
	if code := runTasksRead(v, "test-proj", "archived-reader", io.Discard, &errOut); code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", code, errOut.String())
	}
	es := errOut.String()
	if strings.Contains(es, "vp tasks edit") {
		t.Errorf("an archived reader must not be pointed at `vp tasks edit`, which refuses them: %q", es)
	}
	if !strings.Contains(es, "is discarded") {
		t.Errorf("the discard notice must still be given, got %q", es)
	}
	if !strings.Contains(es, "archived") {
		t.Errorf("the notice should say why there is no edit command to offer, got %q", es)
	}
}

// noTerminalDeadline bounds the no-terminal tests. Both paths return without
// starting a process, so a regression that reaches the (blocking) stub editor
// fails here instead of hanging the suite.
const noTerminalDeadline = 5 * time.Second

// withoutATerminal makes the test's stdin /dev/null and its stdout a regular
// file, and pins VP_ASSUME_TTY off, so cli.IsTerminal reports false for both
// however `go test` itself was started. It also points TMPDIR at an empty
// directory the caller can inspect for a leaked temp copy, and installs a stub
// editor that records that it ran and then blocks — the editor a no-terminal
// run must never reach. It returns the TMPDIR and the stub's marker path.
func withoutATerminal(t *testing.T) (tmpDir, ranMarker string) {
	t.Helper()
	t.Setenv("VP_ASSUME_TTY", "")

	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	dir := t.TempDir()
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatalf("create stdout file: %v", err)
	}
	origIn, origOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = stdin, stdout
	t.Cleanup(func() {
		os.Stdin, os.Stdout = origIn, origOut
		stdin.Close()
		stdout.Close()
	})

	tmpDir = t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	ranMarker = filepath.Join(dir, "editor-ran")
	// The stub drops the test binary's stdio before blocking: otherwise, on a
	// regression, the orphaned sleep holds `go test`'s output pipe open and the
	// package run waits the full 30s after the deadline has already failed it.
	t.Setenv("VISUAL", writeStubEditor(t, dir, "touch \""+ranMarker+"\"\nexec </dev/null >/dev/null 2>&1\nsleep 30\n"))
	t.Setenv("EDITOR", "")
	return tmpDir, ranMarker
}

// runWithDeadline runs fn and fails the test if it has not returned within
// noTerminalDeadline.
func runWithDeadline(t *testing.T, fn func() int) int {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- fn() }()
	select {
	case code := <-done:
		return code
	case <-time.After(noTerminalDeadline):
		t.Fatalf("did not return within %v: the command is waiting on the editor", noTerminalDeadline)
		return 0
	}
}

// TestRunTasksReadWithoutATerminalPrintsTheBody pins the fix for `vp tasks read
// X | grep` hanging silently: with no terminal, an editor never exits, so the
// body must go straight to stdout with no editor and no temp copy.
func TestRunTasksReadWithoutATerminalPrintsTheBody(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "piped-read", "")
	_, want, err := v.GetTask("test-proj", "piped-read")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	tmpDir, ranMarker := withoutATerminal(t)

	var out, errOut bytes.Buffer
	code := runWithDeadline(t, func() int { return runTasksRead(v, "test-proj", "piped-read", &out, &errOut) })
	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", code, errOut.String())
	}
	if out.String() != want {
		t.Errorf("stdout is not the task's exact bytes:\n--- got ---\n%s\n--- want ---\n%s", out.String(), want)
	}
	if errOut.Len() != 0 {
		t.Errorf("nothing belongs on stderr when printing the body, got %q", errOut.String())
	}
	if _, err := os.Stat(ranMarker); !os.IsNotExist(err) {
		t.Errorf("the editor was started without a terminal (marker stat err = %v)", err)
	}
	if left, _ := os.ReadDir(tmpDir); len(left) != 0 {
		t.Errorf("no temp copy may be made on the print path, found %v", left)
	}
}

// TestRunTasksEditWithoutATerminalRefuses: `vp tasks edit` has no print
// fallback — its contract is to change the task — so with no terminal it must
// refuse promptly, name why, and point at the non-interactive route.
func TestRunTasksEditWithoutATerminalRefuses(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "piped-edit", "")
	_, before, err := v.GetTask("test-proj", "piped-edit")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	_, ranMarker := withoutATerminal(t)

	var out, errOut bytes.Buffer
	code := runWithDeadline(t, func() int { return runTasksEdit(v, "test-proj", "piped-edit", &out, &errOut) })
	if code == cli.ExitOK {
		t.Fatalf("exit = ExitOK, want a refusal; stdout=%q stderr=%q", out.String(), errOut.String())
	}
	es := errOut.String()
	for _, need := range []string{"stdin and stdout are not a terminal", "vp_manage_task"} {
		if !strings.Contains(es, need) {
			t.Errorf("refusal must mention %q, got %q", need, es)
		}
	}
	if out.Len() != 0 {
		t.Errorf("a refusal must not print the body, got %q", out.String())
	}
	if _, err := os.Stat(ranMarker); !os.IsNotExist(err) {
		t.Errorf("the editor was started without a terminal (marker stat err = %v)", err)
	}
	if _, after, err := v.GetTask("test-proj", "piped-edit"); err != nil || after != before {
		t.Errorf("the task changed on a refused edit (err=%v)", err)
	}
}

func TestRunTasksReadNoSuchTask(t *testing.T) {
	v := testVault(t)
	var errOut bytes.Buffer
	if code := runTasksRead(v, "test-proj", "nope", io.Discard, &errOut); code != cli.ExitUser {
		t.Fatalf("exit = %d, want ExitUser", code)
	}
	if !strings.Contains(errOut.String(), "no such task: nope") {
		t.Errorf("expected no-such-task error, got %q", errOut.String())
	}
}

func TestRunTasksReadCannotDetectProject(t *testing.T) {
	// Detection is hard to defeat: project.DetectProject falls back to the
	// directory BASENAME, so almost any cwd yields something. A basename with no
	// alphanumerics slugifies to the empty string, which is one of the few ways
	// to reach the refusal at all — worth knowing, because it means this branch
	// is rare rather than dead.
	dir := filepath.Join(t.TempDir(), "!!!")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Chdir(dir)
	var code int
	errs := captureStderr(t, func() { code = cmdTasksRead().Run([]string{"some-slug"}) })
	if code != cli.ExitUser {
		t.Errorf("exit = %d, want ExitUser", code)
	}
	if !strings.Contains(errs, "--project") {
		t.Errorf("refusal must name --project, got %q", errs)
	}
}

// TestRunTasksReadAnnouncesDiscardBeforeTheEditorRuns proves the ORDER, not
// merely that the notice exists.
//
// Both the notice and the stub editor's own marker are routed to the process
// stderr, so they land in one captured stream and their relative position is
// observable. Printing the notice after the editor exits would be worthless —
// by then the reader has already typed into a file they believed was live.
func TestRunTasksReadAnnouncesDiscardBeforeTheEditorRuns(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "announced", "")

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	stub := readStub(t, dir, argvFile, "printf 'STUB-EDITOR-RAN\\n' >&2\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)
	assumeTerminal(t)

	var code int
	stream := captureStderr(t, func() { code = runTasksRead(v, "test-proj", "announced", io.Discard, os.Stderr) })
	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", code, stream)
	}

	notice := strings.Index(stream, "READ-ONLY")
	discard := strings.Index(stream, "is discarded")
	ran := strings.Index(stream, "STUB-EDITOR-RAN")
	if notice < 0 || discard < 0 {
		t.Fatalf("the read-only notice is missing from stderr:\n%s", stream)
	}
	if ran < 0 {
		t.Fatalf("the stub editor never ran:\n%s", stream)
	}
	if notice > ran || discard > ran {
		t.Errorf("the discard notice must be printed BEFORE the editor starts:\n%s", stream)
	}

	handed := handedPath(t, argvFile)
	if !strings.Contains(filepath.Base(handed), "READONLY") {
		t.Errorf("temp basename %q must carry the warning too — a printed line is wiped by any alternate-screen editor", filepath.Base(handed))
	}
}

// TestRunTasksReadBoundsAPathologicalSlug pins the temp-name budget.
//
// slug.Validate caps a slug at 64 bytes today, so this is not currently a
// route to ENAMETOOLONG — that cap lives in another package and is exactly the
// kind of premise that expires silently. The bound here holds regardless, and
// the reason it exists at all is legibility: the slug is in the filename to be
// readable in an editor's status line, which a 64-byte slug defeats.
func TestRunTasksReadBoundsAPathologicalSlug(t *testing.T) {
	const prefix = "pathological-read-slug-"
	slug := prefix + strings.Repeat("x", 64-len(prefix)) // the longest slug storage will accept
	if len(slug) != 64 {
		t.Fatalf("fixture slug is %d bytes, want 64", len(slug))
	}

	v := testVault(t)
	mkTask(t, v, "test-proj", slug, "")

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	stub := readStub(t, dir, argvFile, "exit 0\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)
	assumeTerminal(t)

	var errOut bytes.Buffer
	if code := runTasksRead(v, "test-proj", slug, io.Discard, &errOut); code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", code, errOut.String())
	}

	base := filepath.Base(handedPath(t, argvFile))
	if !strings.Contains(base, "READONLY") {
		t.Errorf("temp basename %q lost the READONLY marker", base)
	}
	if !strings.Contains(base, slug[:readTempSlugBudget]) {
		t.Errorf("temp basename %q must keep the slug's leading %d bytes so it stays recognisable", base, readTempSlugBudget)
	}
	if strings.Contains(base, slug) {
		t.Errorf("temp basename %q embeds the whole 64-byte slug; it was supposed to be bounded", base)
	}
	if len(base) > 80 {
		t.Errorf("temp basename %q is %d bytes; the budget is supposed to keep it well under NAME_MAX", base, len(base))
	}
}

// vaultDigest returns relpath -> sha256 of every regular file under root.
//
// 🔴 THE DIGEST IS OVER BYTES, VIA sha256 — never over len(). A byte-diff
// procedure that records character counts reports clean while measuring
// something else, which has bitten this project more than once.
func vaultDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("walk vault: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("vault digest is empty; the snapshot examined nothing")
	}
	return out
}

// TestRunTasksReadLeavesTheVaultBytesUnchanged is the containment proof: the
// ABSENCE of write-back is demonstrated on bytes, not assumed from the absence
// of a call.
//
// It snapshots the WHOLE vault tree rather than re-reading the one task,
// because a write to a different file — the active copy, a sibling, a lock, an
// index — is precisely the failure a single-file assertion reports as clean.
//
// The stub rewrites the temp file wholesale, INCLUDING the `# Title` and
// `**Status:**` header lines, so a write routed through the header-rewriting
// escape hatch (OverwriteTaskFileRewritingHeader) would be caught too, not only
// a plain OverwriteTaskFile.
func TestRunTasksReadLeavesTheVaultBytesUnchanged(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "epic-root", "")
	mkTask(t, v, "test-proj", "active-one", "epic-root")
	mkTask(t, v, "test-proj", "done-one", "")
	mkTask(t, v, "test-proj", "cancelled-one", "")
	if err := v.RetireTask("test-proj", "done-one"); err != nil {
		t.Fatalf("retire: %v", err)
	}
	if err := v.CancelTask("test-proj", "cancelled-one", ""); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	before := vaultDigest(t, v.Root)

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	stub := readStub(t, dir, argvFile, `chmod u+w "$1"
cat > "$1" <<'EOF'
# Smuggled Title

**Status:** in_progress
**Priority:** critical

## Smuggled Section

The reader rewrote every byte, header block included.
EOF
`)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)
	assumeTerminal(t)

	var errOut bytes.Buffer
	if code := runTasksRead(v, "test-proj", "done-one", io.Discard, &errOut); code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK; stderr=%q", code, errOut.String())
	}

	after := vaultDigest(t, v.Root)

	for rel, sum := range before {
		got, ok := after[rel]
		if !ok {
			t.Errorf("vp tasks read REMOVED a vault file: %s", rel)
			continue
		}
		if got != sum {
			t.Errorf("vp tasks read CHANGED vault bytes at %s\n  before sha256 %s\n  after  sha256 %s", rel, sum, got)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			t.Errorf("vp tasks read ADDED a vault file: %s", rel)
		}
	}

	// The rewritten copy is kept, per the disposition rule; clean it up.
	_ = os.Remove(handedPath(t, argvFile))
}

// TestCreateReadOnlyTempCopyClearsTheWriteBit asserts the guard the operator
// asked for, and the cleanup property that guard could have broken, on the
// real artifact rather than on a stand-in.
//
// 🔴 IT ASSERTS THE WRITE BIT IS CLEAR, NOT THAT THE MODE IS 0400.
// Windows Stat reports 0444 for a read-only file and 0666 otherwise
// (os/types_windows.go), so `mode == 0400` would pass here and fail there —
// and this repo ships Windows build tags, so "here" is not the whole story.
func TestCreateReadOnlyTempCopyClearsTheWriteBit(t *testing.T) {
	const body = "# A Task\n\nSome body bytes.\n"
	path, err := createReadOnlyTempCopy("some-slug", body)
	if err != nil {
		t.Fatalf("createReadOnlyTempCopy: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o200 != 0 {
		t.Errorf("temp copy mode is %v; the owner write bit must be clear so editors surface it as read-only", info.Mode().Perm())
	}
	if !strings.Contains(filepath.Base(path), "READONLY") {
		t.Errorf("the filename must keep its READONLY marker too, got %q", filepath.Base(path))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read-only must still mean READABLE: %v", err)
	}
	if string(got) != body {
		t.Errorf("temp copy holds %q, want %q", got, body)
	}
}

// TestReadOnlyTempCopyIsStillRemovable is the cross-platform hazard, pinned.
//
// The worry was that making the copy read-only would break the cleanup and turn
// a warning into a leak on every successful read. It does not, and the reason
// differs per platform — which is why this is one test rather than a Linux-only
// assertion that would no-op where the risk actually lives:
//
//   - POSIX: os.Remove needs write permission on the DIRECTORY, not on the
//     file, so the mode is simply not consulted.
//   - Windows: os.Chmod without S_IWRITE sets FILE_ATTRIBUTE_READONLY and
//     DeleteFile refuses it — but os.Remove re-reads the attributes, clears
//     that bit with SetFileAttributes and retries (os/file_windows.go).
//
// Either way this must pass. If it ever fails on some platform, that platform
// needs an explicit chmod before the remove, and this test is where that gets
// discovered rather than in a $TMPDIR filling up with task bodies.
func TestReadOnlyTempCopyIsStillRemovable(t *testing.T) {
	path, err := createReadOnlyTempCopy("removable", "# A Task\n\nbody\n")
	if err != nil {
		t.Fatalf("createReadOnlyTempCopy: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("a read-only temp copy must still be removable, else every clean read leaks a file: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("after os.Remove, os.Stat(%q) err = %v, want IsNotExist", path, err)
	}
}

// TestRunTasksReadReadOnlyCopyRefusesAPlainEditorWrite is the guard working
// end to end: a stub that writes in place WITHOUT forcing gets EACCES, so the
// bytes come back unchanged and the copy is removed like any untouched read.
//
// The two tests above it cover the routes that DO get through — `:w!` chmods,
// and write-and-rename replaces the path — which is why the disposition branch
// is still load-bearing and was not deleted when the mode was added.
func TestRunTasksReadReadOnlyCopyRefusesAPlainEditorWrite(t *testing.T) {
	v := testVault(t)
	mkTask(t, v, "test-proj", "mode-guarded", "")
	_, before, err := v.GetTask("test-proj", "mode-guarded")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	// No chmod, no rename: exactly what a naive editor does on save.
	stub := readStub(t, dir, argvFile, "printf 'clobbered\\n' >> \"$1\"\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", stub)
	assumeTerminal(t)

	var errOut bytes.Buffer
	code := runTasksRead(v, "test-proj", "mode-guarded", io.Discard, &errOut)

	handed := handedPath(t, argvFile)
	if _, err := os.Stat(handed); err == nil {
		_ = os.Remove(handed)
		t.Errorf("the write was refused, so the copy was unchanged and should have been removed: %s", handed)
	}
	// The shell reports the failed redirect, so this lands on the abort path.
	if code != cli.ExitUser {
		t.Errorf("exit = %d, want ExitUser (the stub's write failed); stderr=%q", code, errOut.String())
	}
	_, after, err := v.GetTask("test-proj", "mode-guarded")
	if err != nil {
		t.Fatalf("GetTask after: %v", err)
	}
	if after != before {
		t.Errorf("the vault changed during a read")
	}
}

// TestTaskIsAnExactAliasOfTasks drives `vp task read <slug>` through the real
// command table (registerAll + Dispatch), not through the handler: an alias is
// a dispatch property, so only dispatch can prove it. The singular spelling
// must reach the same subcommand and hand the editor the same bytes as the
// plural one.
func TestTaskIsAnExactAliasOfTasks(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	projDir := t.TempDir()
	marker := filepath.Join(projDir, ".vibe-palace.toml")
	body := "vault_path = \"" + vaultDir + "\"\n\n[project]\nname = \"test-proj\"\n"
	if err := os.WriteFile(marker, []byte(body), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Chdir(projDir)

	v := storage.NewVault(vaultDir)
	mkTask(t, v, "test-proj", "alias-target", "")
	_, want, err := v.GetTask("test-proj", "alias-target")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	for _, word := range []string{"tasks", "task"} {
		t.Run(word, func(t *testing.T) {
			dir := t.TempDir()
			argvFile := filepath.Join(dir, "argv.txt")
			copyFile := filepath.Join(dir, "handed-copy.md")
			t.Setenv("VISUAL", "")
			t.Setenv("EDITOR", readStub(t, dir, argvFile, "cat \"$1\" > \""+copyFile+"\"\n"))
			assumeTerminal(t)

			reg, _, errOut := testRegistry()
			if code := reg.Dispatch([]string{word, "read", "alias-target"}); code != cli.ExitOK {
				t.Fatalf("vp %s read: exit = %d, want ExitOK; stderr=%q", word, code, errOut.String())
			}
			handedPath(t, argvFile)
			got, err := os.ReadFile(copyFile)
			if err != nil {
				t.Fatalf("stub did not copy the handed file: %v", err)
			}
			if string(got) != want {
				t.Errorf("vp %s read handed different bytes than the vault holds:\n%s", word, got)
			}
		})
	}

	// Help and typo detection resolve through the alias too.
	reg, out, _ := testRegistry()
	if code := reg.Dispatch([]string{"help", "task", "read"}); code != cli.ExitOK {
		t.Fatalf("vp help task read: exit = %d", code)
	}
	if !strings.Contains(out.String(), "Usage: vp tasks read") {
		t.Errorf("vp help task read rendered %q, want the tasks read help", out.String())
	}
	reg, _, errOut := testRegistry()
	if code := reg.Dispatch([]string{"task", "bogus"}); code != cli.ExitUser {
		t.Errorf("vp task bogus: exit = %d, want ExitUser", code)
	}
	if !strings.Contains(errOut.String(), `vp tasks: unknown subcommand "bogus"`) {
		t.Errorf("vp task bogus stderr = %q", errOut.String())
	}
}
