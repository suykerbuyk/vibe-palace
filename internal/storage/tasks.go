// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// TaskMeta is the lightweight metadata for task listing.
type TaskMeta struct {
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
	Done     bool   `json:"done"`
}

// validStatuses is the WRITE set for UpdateTaskStatus — and ONLY the write set.
//
// The terminal values ("completed", "retired", "cancelled") are deliberately
// absent. A task leaves the active set by MOVING (RetireTask → done/,
// CancelTask → cancelled/), and moveTask writes "retired"/"cancelled" straight
// through replaceStatusLine without ever consulting this map. Letting
// UpdateTaskStatus stamp a terminal status in place produced a task file that
// claimed to be finished while still sitting in the active directory — visible
// to nobody, moved by nothing. Removing them from the write set is friction on
// the shortest didn't-notice path to that state; it is not a claim that a task
// cannot be marked done by other means (vp_vault_edit and a text editor both
// still can).
//
// This is NOT a read whitelist and must never become one. Every archived file
// on disk carries "**Status:** retired" or "**Status:** cancelled" — 96 of them
// today. A read-side check built on this map would declare the entire archive
// invalid. parseTaskMeta reads whatever the file says, on purpose.
//
// Honest scope: 0 of the 114 task files on disk use "completed". This closes a
// door almost nobody walks through. It is worth closing, and it is not coverage.
var validStatuses = map[string]bool{
	"pending":     true,
	"in_progress": true,
	"blocked":     true,
}

// CreateTask creates a new task markdown file in the project's tasks directory.
// It is a hard error if the task already exists.
//
// The existence check runs INSIDE the per-path vaultlock: checking outside it is
// a TOCTOU — two concurrent creates of the same slug would both pass the stat and
// one would silently overwrite the other, breaking the already-exists contract.
//
// The write therefore goes through atomicfile.Write directly rather than
// lockedWrite: lockedWrite re-acquires this same lock, and vaultlock.Acquire is a
// blocking LOCK_EX with no LOCK_NB and no timeout, so the re-entry would be a
// permanent self-deadlock rather than an error. Same shape as
// (*Vault).WriteResume and UpdateTaskStatus.
func (v *Vault) CreateTask(project, slug, title, content, priority string) error {
	path, err := v.TaskFile(project, slug)
	if err != nil {
		return err
	}

	// Validate the caller-supplied body BEFORE any directory or file is
	// touched. This lives here, not in the MCP handler, as defense in depth:
	// the storage layer is the chokepoint every caller must pass through, so a
	// future second caller cannot reintroduce an unvalidated body by forgetting
	// to repeat the check. (vp_manage_task create is currently the only
	// production caller; vp_carried_promote_to_task was the second until it was
	// deleted.)
	if err := validateTaskBody(content); err != nil {
		return err
	}

	tasksDir, err := v.TasksDir(project)
	if err != nil {
		return err
	}
	if err := EnsureDir(tasksDir); err != nil {
		return fmt.Errorf("ensure tasks dir: %w", err)
	}

	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock task: %w", err)
	}
	defer release()

	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("task %q already exists in project %q", slug, project)
	}

	var buf strings.Builder
	fmt.Fprintf(&buf, "# %s\n\n", title)
	fmt.Fprintf(&buf, "**Status:** pending\n")
	fmt.Fprintf(&buf, "**Priority:** %s\n", priority)
	if content != "" {
		buf.WriteString("\n")
		buf.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			buf.WriteByte('\n')
		}
	}

	return atomicfile.Write(v.Root, path, []byte(buf.String()))
}

// GetTask reads a task file and returns its metadata and full content.
// It searches active, done, and cancelled directories.
func (v *Vault) GetTask(project, slug string) (TaskMeta, string, error) {
	if err := validateSlugs(project, slug); err != nil {
		return TaskMeta{}, "", err
	}

	type location struct {
		dir  func(string) (string, error)
		done bool
	}
	locations := []location{
		{v.TasksDir, false},
		{v.TaskDoneDir, true},
		{v.TaskCancelledDir, true},
	}

	for _, loc := range locations {
		dir, err := loc.dir(project)
		if err != nil {
			return TaskMeta{}, "", err
		}
		path := filepath.Join(dir, slug+".md")
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return TaskMeta{}, "", fmt.Errorf("read task: %w", err)
		}

		meta := parseTaskMeta(slug, string(data), loc.done)
		return meta, string(data), nil
	}

	return TaskMeta{}, "", fmt.Errorf("task %q not found in project %q", slug, project)
}

// ListTasks returns metadata for all tasks in a project. If includeDone is
// true, also includes tasks from the done/ and cancelled/ subdirectories.
func (v *Vault) ListTasks(project string, includeDone bool) ([]TaskMeta, error) {
	tasksDir, err := v.TasksDir(project)
	if err != nil {
		return nil, err
	}

	var result []TaskMeta

	// Active tasks.
	active, err := globTaskMeta(tasksDir, false)
	if err != nil {
		return nil, err
	}
	result = append(result, active...)

	if includeDone {
		doneDir, err := v.TaskDoneDir(project)
		if err != nil {
			return nil, err
		}
		done, err := globTaskMeta(doneDir, true)
		if err != nil {
			return nil, err
		}
		result = append(result, done...)

		cancelDir, err := v.TaskCancelledDir(project)
		if err != nil {
			return nil, err
		}
		cancelled, err := globTaskMeta(cancelDir, true)
		if err != nil {
			return nil, err
		}
		result = append(result, cancelled...)
	}

	return result, nil
}

// UpdateTaskStatus updates the status line of an ACTIVE task file in place.
//
// It accepts only the non-terminal statuses (see validStatuses). A task reaches
// a terminal state by being MOVED — RetireTask or CancelTask — never by being
// stamped in place, because a "completed" file left in tasks/ is a task that
// reads as finished and behaves as active.
func (v *Vault) UpdateTaskStatus(project, slug, status string) error {
	if !validStatuses[status] {
		return fmt.Errorf(
			"invalid status %q: UpdateTaskStatus writes only pending, in_progress, or blocked — "+
				"a task reaches a terminal state by being moved (RetireTask/CancelTask), not stamped in place",
			status)
	}

	path, err := v.TaskFile(project, slug)
	if err != nil {
		return err
	}

	// Hold the per-path lock across the read→rewrite (RMW): a concurrent
	// status update of the same task must not clobber this one.
	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock task: %w", err)
	}
	defer release()

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read task: %w", err)
	}

	updated := replaceStatusLine(string(data), status)
	return atomicfile.Write(v.Root, path, []byte(updated))
}

// RetireTask moves a task to the done/ directory with status "retired".
func (v *Vault) RetireTask(project, slug string) error {
	return v.moveTask(project, slug, v.TaskDoneDir, "retired")
}

// CancelTask moves a task to the cancelled/ directory with status "cancelled".
func (v *Vault) CancelTask(project, slug string) error {
	return v.moveTask(project, slug, v.TaskCancelledDir, "cancelled")
}

// moveTask updates a task's status and moves it to a destination directory.
func (v *Vault) moveTask(project, slug string, destFn func(string) (string, error), status string) error {
	srcPath, err := v.TaskFile(project, slug)
	if err != nil {
		return err
	}

	if _, err := os.Stat(srcPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("task %q not found (may already be %s)", slug, status)
		}
		return err
	}

	// Update status in file before moving.
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read task: %w", err)
	}
	updated := replaceStatusLine(string(data), status)

	destDir, err := destFn(project)
	if err != nil {
		return err
	}
	if err := EnsureDir(destDir); err != nil {
		return fmt.Errorf("ensure dest dir: %w", err)
	}

	destPath := filepath.Join(destDir, slug+".md")
	if err := v.lockedWrite(destPath, []byte(updated)); err != nil {
		return fmt.Errorf("write to dest: %w", err)
	}

	return os.Remove(srcPath)
}

// isStatusLine reports whether a line is a task **Status:** line.
//
// This is THE definition of a status line for the whole package: the parser
// (parseTaskMeta), the writer (replaceStatusLine) and the validator
// (validateTaskBody) all go through it, so they cannot drift apart and start
// disagreeing about which lines are metadata. Deliberately line-oriented and
// leading-whitespace tolerant — the same shape the markdown these files carry
// has always used. A "**Status:**" mid-sentence or inside a fenced code block
// is not a status line to anyone here, by construction.
func isStatusLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "**Status:**")
}

// isPriorityLine reports whether a line is a task **Priority:** line.
func isPriorityLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "**Priority:**")
}

// isH1Line reports whether a line is a markdown H1 ("# " at line start).
func isH1Line(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "# ")
}

// validateTaskBody rejects a caller-supplied task body that carries its own
// metadata header.
//
// CreateTask writes "# Title", "**Status:**" and "**Priority:**" itself. A body
// that repeats them yields a file with two H1s and two status lines — and since
// the writer (replaceStatusLine) rewrites the FIRST status line, the file then
// reads back with a status nobody ever wrote. Rejecting the duplicate at the
// door is the only way the two can stay in agreement.
//
// This is the ONE place that must be fence-aware. Task plan bodies routinely
// carry shell/TOML/Python snippets, where "# Usage: ..." is a comment, not a
// heading — rejecting those would hard-fail create on a very common and
// entirely legitimate body shape. Inside a fence, nothing is metadata. Outside
// one, an H1 or a status line can only be a duplicated header block.
//
// Fence detection lives in internal/mdfence and MUST NOT be reimplemented here.
// It was, once, as "the trimmed line starts with ``` or ~~~" — and that rule
// FAILED OPEN. A body line whose first non-space characters are an inline code
// run (```bash tutorial``` ...) was misread as an opening fence, so everything
// after it was treated as fenced and skipped, and a duplicate "**Status:**"
// walked straight past the check this function exists to perform — silently
// reinstating the corruption iteration 184 closed. Proven by test before the
// fix, not argued. See the mdfence package doc.
//
// An unterminated fence simply means the rest of the body is treated as fenced.
// A task body is not required to be well-formed markdown, and over-rejecting is
// precisely the failure mode being avoided here.
//
// parseTaskMeta and replaceStatusLine stay deliberately fence-UNAWARE: CreateTask
// writes the real header at the top of the file, the body follows it, and both
// are first-wins — so the genuine header is always found before any fence in the
// body can be reached.
func validateTaskBody(content string) error {
	const remedy = "strip the leading metadata block from content: create supplies the \"# Title\" heading, \"**Status:**\" and \"**Priority:**\" lines itself"
	for _, l := range mdfence.OutsideFences(content) {
		trimmed := strings.TrimSpace(l.Text)
		switch {
		case isStatusLine(trimmed):
			return fmt.Errorf("task content line %d is a status line (%q): %s", l.Num, trimmed, remedy)
		case isH1Line(trimmed):
			return fmt.Errorf("task content line %d is an H1 heading (%q): %s", l.Num, trimmed, remedy)
		}
	}
	return nil
}

// parseTaskMeta extracts metadata from task markdown content.
//
// Every field is FIRST-wins, matching what CreateTask writes and what
// replaceStatusLine rewrites. Last-wins here is what let a file with a
// duplicated header report line 8's status while the writer kept updating
// line 3's.
func parseTaskMeta(slug, content string, done bool) TaskMeta {
	meta := TaskMeta{Slug: slug, Done: done}
	var haveStatus, havePriority bool
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if isH1Line(trimmed) && meta.Title == "" {
			meta.Title = strings.TrimPrefix(trimmed, "# ")
		}
		if !haveStatus && isStatusLine(trimmed) {
			meta.Status = strings.TrimSpace(strings.TrimPrefix(trimmed, "**Status:**"))
			haveStatus = true
		}
		if !havePriority && isPriorityLine(trimmed) {
			meta.Priority = strings.TrimSpace(strings.TrimPrefix(trimmed, "**Priority:**"))
			havePriority = true
		}
	}
	return meta
}

// replaceStatusLine replaces the value on the first **Status:** line in task
// markdown. If the content has NO status line, one is inserted — immediately
// after the H1 title if there is one, otherwise at the top of the file.
// Silently dropping the update (the old behaviour) left the task file claiming
// a status the caller had explicitly changed away from.
func replaceStatusLine(content, newStatus string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if isStatusLine(line) {
			lines[i] = "**Status:** " + newStatus
			return strings.Join(lines, "\n")
		}
	}

	// No status line: insert one. Splicing into the line slice (rather than
	// concatenating strings) preserves the file's trailing-newline shape
	// exactly — a trailing "" element stays the trailing "" element.
	at := 0
	for i, line := range lines {
		if isH1Line(line) {
			at = i + 1
			break
		}
	}
	lines = slices.Insert(lines, at, "**Status:** "+newStatus)
	return strings.Join(lines, "\n")
}

// globTaskMeta reads all .md files from a directory and returns their metadata.
func globTaskMeta(dir string, done bool) ([]TaskMeta, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("glob tasks: %w", err)
	}

	var result []TaskMeta
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("read task %s: %w", m, err)
		}
		slug := strings.TrimSuffix(filepath.Base(m), ".md")
		meta := parseTaskMeta(slug, string(data), done)
		result = append(result, meta)
	}
	return result, nil
}
